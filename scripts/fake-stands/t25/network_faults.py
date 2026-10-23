#!/usr/bin/env python3
"""What clio and creatio-mcp-go answer when the stand cannot be reached (T25).

A local fake stand on 127.0.0.1 that the probe switches between behaviours while it calls one MCP server
(clio's `mcp-server` or this server), one tool after another. Each server gets a temporary CLIO_HOME with one
environment, `netfail`, pointing at the fake stand; the real clio settings are never read. No DNS is involved.

Scenarios, per case:
  fresh-down    nothing listens on the port: the login is refused
  fresh-reset   every connection is reset (SO_LINGER 0) after its request was read: the login is reset
  fresh-hang    every connection is accepted and never answered: the login gets no answer
  cached-down   the stand answers the login (the tool is called twice, so a session is held), then stops
                listening: the tool is called again and that answer is recorded
  cached-reset  as cached-down, but the stand keeps answering logins and resets every other request

The two calls before a cached scenario's recorded one also count the logins the stand received, which shows
whether the server logged in again for the second call ("logins" in the output: first, second, recorded).

From the repository root:

  python3 scripts/fake-stands/t25/network_faults.py probe clio <clio.dll> cached-down clio-cached-down.json
  python3 scripts/fake-stands/t25/network_faults.py probe go ./creatio-mcp-go cached-down go-cached-down.json
  python3 scripts/fake-stands/t25/network_faults.py compare clio-cached-down.json go-cached-down.json

`--cases` picks the case file (default: cases.json next to this script, 114 tools), `--tools` a subset. Probe
several scenarios or sides in parallel: each probe takes a free port of its own. fresh-hang waits the full
login timeout per tool (100 s on both servers), so run it on a few tools (`--tools`).
"""
import argparse, importlib.util, json, os, pathlib, queue, shutil, socket, struct, subprocess, sys, tempfile
import threading, time

HERE = pathlib.Path(__file__).resolve().parent
REPO = HERE.parent.parent.parent
ENVIRONMENT = "netfail"
LOGIN_PATH = "/ServiceModel/AuthService.svc/Login"


def compare_mcp():
    spec = importlib.util.spec_from_file_location("compare_mcp", REPO / "scripts/compare-mcp.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class FakeStand:
    """ok: the login answers Code 0 with session cookies, SelectQuery an empty success, anything else HTTP 500.
    reset / reset-login / hang as described in the module docstring; stop() closes the listener."""

    def __init__(self, port):
        self.port, self.mode, self.sock, self.held = port, "ok", None, []
        self.requests, self.lock = [], threading.Lock()

    def logins(self):
        with self.lock:
            return sum(1 for path in self.requests if path.endswith(LOGIN_PATH))

    def start(self, mode="ok"):
        self.mode = mode
        if self.sock is not None:
            return
        listener = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        listener.bind(("127.0.0.1", self.port))
        listener.listen(64)
        self.sock = listener
        threading.Thread(target=self._serve, args=(listener,), daemon=True).start()

    def stop(self):
        listener, self.sock = self.sock, None
        if listener is not None:
            listener.close()
        for conn in self.held:
            conn.close()
        self.held = []
        time.sleep(0.2)

    def _serve(self, listener):
        while True:
            try:
                conn, _ = listener.accept()
            except OSError:
                return
            threading.Thread(target=self._handle, args=(conn,), daemon=True).start()

    @staticmethod
    def _read(conn):
        data = b""
        while b"\r\n\r\n" not in data:
            chunk = conn.recv(65536)
            if not chunk:
                return None
            data += chunk
        head, _, body = data.partition(b"\r\n\r\n")
        lines = head.decode("latin-1").split("\r\n")
        length = 0
        for line in lines[1:]:
            name, _, value = line.partition(":")
            if name.strip().lower() == "content-length":
                length = int(value.strip() or 0)
        while len(body) < length:
            chunk = conn.recv(65536)
            if not chunk:
                break
            body += chunk
        return lines[0].split(" ")[1].split("?")[0]

    @staticmethod
    def _reply(conn, status, body, cookies=False):
        data = json.dumps(body).encode()
        head = [f"HTTP/1.1 {status} X", "Content-Type: application/json; charset=utf-8", f"Content-Length: {len(data)}",
                "Connection: close"]
        if cookies:
            head += ["Set-Cookie: .ASPXAUTH=fake; path=/", "Set-Cookie: BPMCSRF=fakecsrf; path=/"]
        conn.sendall(("\r\n".join(head) + "\r\n\r\n").encode() + data)
        conn.close()

    def _handle(self, conn):
        try:
            path = self._read(conn)
            if path is None:
                conn.close()
                return
            with self.lock:
                self.requests.append(path)
            login = path.endswith(LOGIN_PATH)
            if self.mode == "hang":
                self.held.append(conn)
            elif self.mode == "reset" or (self.mode == "reset-login" and not login):
                conn.setsockopt(socket.SOL_SOCKET, socket.SO_LINGER, struct.pack("ii", 1, 0))
                conn.close()
            elif login:
                self._reply(conn, 200, {"Code": 0, "Message": ""}, cookies=True)
            elif path.endswith("/SelectQuery"):
                self._reply(conn, 200, {"success": True, "rows": [], "rowsAffected": 0})
            else:
                self._reply(conn, 500, {"Message": "fake stand"})
        except OSError:
            pass


class Server:
    """One MCP server over stdio, with a reader thread so that a call can time out."""

    def __init__(self, side, binary, home):
        env = {key: value for key, value in os.environ.items() if not key.startswith("CREATIO_")}
        env["CLIO_HOME"] = home
        command = ["dotnet", binary, "mcp-server"] if side == "clio" else [binary]
        self.process = subprocess.Popen(command, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                        stderr=subprocess.DEVNULL, env=env)
        self.messages, self.next_id = queue.Queue(), 0
        threading.Thread(target=self._read, daemon=True).start()
        self.request("initialize", {"protocolVersion": "2025-06-18", "capabilities": {},
                                    "clientInfo": {"name": "t25-network-faults", "version": "1"}}, 120)
        self._send({"jsonrpc": "2.0", "method": "notifications/initialized", "params": {}})

    def _read(self):
        for line in self.process.stdout:
            try:
                self.messages.put(json.loads(line))
            except json.JSONDecodeError:
                pass
        self.messages.put(None)

    def _send(self, message):
        self.process.stdin.write((json.dumps(message) + "\n").encode())
        self.process.stdin.flush()

    def request(self, method, params, limit):
        self.next_id += 1
        self._send({"jsonrpc": "2.0", "id": self.next_id, "method": method, "params": params})
        deadline = time.monotonic() + limit
        while True:
            try:
                message = self.messages.get(timeout=max(0.0, deadline - time.monotonic()))
            except queue.Empty:
                raise TimeoutError(method)
            if message is None:
                raise RuntimeError("server exited")
            if message.get("id") == self.next_id:
                return message

    def close(self):
        self.process.kill()
        self.process.wait()


def free_port():
    with socket.socket() as probe:
        probe.bind(("127.0.0.1", 0))
        return probe.getsockname()[1]


def settings_home(port):
    home = tempfile.mkdtemp(prefix="t25-clio-home-")
    settings = {
        "ActiveEnvironmentKey": ENVIRONMENT,
        "autoupdate": {"clio": {"enabled": False}, "knowledge": {"enabled": False}, "toolkit": {"enabled": False}},
        "Environments": {ENVIRONMENT: {"Uri": f"http://127.0.0.1:{port}", "Login": "example-user", "Password": "replace-me",
                                       "IsNetCore": False}},
    }
    pathlib.Path(home, "appsettings.json").write_text(json.dumps(settings, indent=2), encoding="utf-8")
    return home


def tool_call(side, case):
    # The case's own arguments win: a case that passes a blank environment checks validation, not the network.
    args = {case.get("env-key", "environment-name"): ENVIRONMENT, **case.get("args", {})}
    if case.get("clio-run"):
        return {"name": "clio-run", "arguments": {"command": case["tool"], "args": args}}
    if side == "clio" or case.get("go-wrapped"):
        return {"name": case["tool"], "arguments": {"args": args}}
    return {"name": case["tool"], "arguments": args}


def probe(options):
    cases = json.loads(pathlib.Path(options.cases).read_text(encoding="utf-8"))
    if options.tools:
        cases = [case for case in cases if case["tool"] in options.tools]
    if options.scenario.startswith("cached-"):
        # A tool marked fresh-only starts an operation its server keeps tracking (a restart): the calls that
        # establish the session would leave it running, and the recorded call would only report that.
        cases = [case for case in cases if not case.get("fresh-only")]
    port = free_port()
    home = settings_home(port)
    stand = FakeStand(port)
    state = {"server": Server(options.side, options.binary, home)}
    results = []

    def once(call, limit):
        started = time.monotonic()
        try:
            response = state["server"].request("tools/call", call, limit)
        except (TimeoutError, RuntimeError) as exc:
            response = {"exception": repr(exc)}
            state["server"].close()
            state["server"] = Server(options.side, options.binary, home)
        return response, round(time.monotonic() - started, 2)

    try:
        for case in cases:
            call, entry = tool_call(options.side, case), {"tool": case["tool"]}
            if options.scenario.startswith("cached-"):
                stand.start("ok")
                counted = [stand.logins()]
                once(call, 90)
                counted.append(stand.logins())
                once(call, 90)
                counted.append(stand.logins())
                if options.scenario == "cached-down":
                    stand.stop()
                else:
                    stand.mode = "reset-login"
                entry["response"], entry["seconds"] = once(call, options.timeout)
                entry["logins"] = [counted[1] - counted[0], counted[2] - counted[1], stand.logins() - counted[2]]
            else:
                mode = options.scenario.split("-", 1)[1]
                if mode != "down":
                    stand.start(mode)
                entry["response"], entry["seconds"] = once(call, options.timeout)
            stand.stop()
            entry["response"] = json.loads(json.dumps(entry["response"]).replace(f"127.0.0.1:{port}", "127.0.0.1:PORT"))
            results.append(entry)
            print(case["tool"], entry.get("logins", ""), entry["seconds"], flush=True)
    finally:
        state["server"].close()
        shutil.rmtree(home, ignore_errors=True)
    pathlib.Path(options.out).write_text(json.dumps({"scenario": options.scenario, "side": options.side,
                                                     "results": results}, indent=1, ensure_ascii=False), encoding="utf-8")


def answers(path):
    cm = compare_mcp()
    recorded = json.loads(pathlib.Path(path).read_text(encoding="utf-8"))
    out = {}
    for entry in recorded["results"]:
        response = entry["response"]
        out[entry["tool"]] = response if "exception" in response else cm.normalize(cm.payload(response))
    return out, recorded


def compare(options):
    cm = compare_mcp()
    reference, _ = answers(options.reference)
    candidate, _ = answers(options.candidate)
    same = [tool for tool in reference if tool in candidate and not cm.differences(reference[tool], candidate[tool], "$", [], limit=5)]
    differ = [tool for tool in reference if tool in candidate and tool not in same]
    print(f"identical: {len(same)} differ: {len(differ)}")
    for tool in differ:
        print(f"== {tool}\n   reference: {json.dumps(reference[tool], ensure_ascii=False)[:600]}\n"
              f"   candidate: {json.dumps(candidate[tool], ensure_ascii=False)[:600]}")


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    commands = parser.add_subparsers(dest="command", required=True)
    run = commands.add_parser("probe", help="call every case on one server and record the answers")
    run.add_argument("side", choices=("clio", "go"))
    run.add_argument("binary", help="clio.dll for clio, the built server for go")
    run.add_argument("scenario", choices=("fresh-down", "fresh-reset", "fresh-hang", "cached-down", "cached-reset"))
    run.add_argument("out")
    run.add_argument("--cases", default=str(HERE / "cases.json"))
    run.add_argument("--tools", nargs="*")
    run.add_argument("--timeout", type=float, default=150, help="seconds to wait for the recorded call")
    diff = commands.add_parser("compare", help="compare two recorded probes after compare-mcp's normalization")
    diff.add_argument("reference")
    diff.add_argument("candidate")
    options = parser.parse_args()
    probe(options) if options.command == "probe" else compare(options)


if __name__ == "__main__":
    sys.exit(main())
