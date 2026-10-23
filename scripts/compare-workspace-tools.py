#!/usr/bin/env python3
"""Run clio 8.1.0.142's workspace and code-generation tools and this server's ports on the same inputs,
then compare the answers and every file they leave behind.

Each side runs in its own fresh temporary home (CLIO_HOME, HOME, TMPDIR inside --work/<side>/<scenario>),
seeded with the appsettings.json clio writes at its first start plus the scenario's environments. A scenario
is a list of steps (scripts/workspace-scenarios/*.json):

  {"tool": "create-workspace", "args": {...}}           a call through clio-run ("direct": true: by raw name)
  {"mkdir": "base"} / {"write": "path", "content": ".."} prepare the home

A scenario's "environment" object is merged into environment "fake" ({home} substituted), for example an
EnvironmentPath. A scenario with "stand": {"rules": [...]} gets a fake Creatio site per side, registered as environment "fake":
it answers AuthService.svc/Login with session cookies and every other request with the first rule whose
"route" ends the path and whose "method" and "contains" (texts in the body or query) match; a rule answers
"body" (text), "body_base64" or "body_file" (relative to scripts/workspace-scenarios/fixtures) with
"status" and "content_type", and may "set" a flag that later rules require with "if" or exclude with
"unless". Unmatched requests get {"success":true}.

In arguments {home} is the side's home and {ws} is {home}/base/ws. Compared per step: the answer, with
clio's correlation-id left out and the home path replaced by {home}. Compared per scenario: the tree under
{home}/base (paths, file modes, bytes), with GUIDs and Microsoft JSON dates replaced by placeholders numbered
in order of appearance, and the requests each side sent to a fake stand. Nothing reaches a real stand, and
neither this machine's clio settings nor its workspaces are read or written; create-workspace reads the
CreatioSDK versions from api.nuget.org on both sides, as clio does.

    python3 scripts/compare-workspace-tools.py --clio-dll <clio.dll> --go-bin <creatio-mcp-go> --work <dir> \
        [--scenario NAME ...] [--evidence evidence/t16c-workspace.json] [--show]
"""
import argparse, base64, glob, hashlib, http.server, json, os, pathlib, re, shutil, socket, stat, subprocess, sys
import threading, time, urllib.parse

REPO = pathlib.Path(__file__).resolve().parent.parent
SCENARIOS = REPO / "scripts" / "workspace-scenarios"
FIXTURES = SCENARIOS / "fixtures"
GUID = re.compile(r"[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}")
DATE = re.compile(r"/Date\(\d+\)/")
CORRELATION = re.compile(r',?"correlation-id":"[0-9a-f]+"')


class Server:
    def __init__(self, command, home, env_extra=None):
        env = dict(os.environ)
        for key in [key for key in env if key.startswith("CREATIO_")]:
            env.pop(key)
        env.update({"HOME": os.path.join(home, "home"), "USERPROFILE": os.path.join(home, "home"),
                    "CLIO_HOME": home, "CLIO_TELEMETRY_HOME": os.path.join(home, "telemetry"),
                    "TMPDIR": os.path.join(home, "tmp"), "DOTNET_CLI_TELEMETRY_OPTOUT": "1",
                    "CLIO_TELEMETRY_ENABLED": "false", "CLIO_KNOWLEDGE_CURATED_API_BASE_URL": "http://127.0.0.1:9/",
                    "CLIO_COMPONENT_REGISTRY_CDN_BASE_URL": "http://127.0.0.1:9/"})
        env.update(env_extra or {})
        for folder in ("home", "tmp"):
            os.makedirs(os.path.join(home, folder), exist_ok=True)
        self.process = subprocess.Popen(command, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                        stderr=open(os.path.join(home, "server-stderr.log"), "w"), env=env,
                                        text=True, bufsize=1, encoding="utf-8", cwd=home)
        self.next_id = 0
        self.request("initialize", {"protocolVersion": "2025-06-18", "capabilities": {},
                                    "clientInfo": {"name": "compare-workspace-tools", "version": "1"}})
        self.send({"jsonrpc": "2.0", "method": "notifications/initialized", "params": {}})

    def send(self, message):
        self.process.stdin.write(json.dumps(message) + "\n")
        self.process.stdin.flush()

    def request(self, method, params):
        self.next_id += 1
        self.send({"jsonrpc": "2.0", "id": self.next_id, "method": method, "params": params})
        progress = []
        while True:
            line = self.process.stdout.readline()
            if not line:
                raise RuntimeError(f"MCP server exited during {method}")
            try:
                message = json.loads(line)
            except json.JSONDecodeError:
                continue
            if message.get("id") == self.next_id:
                message["progress"] = progress
                return message
            if message.get("method") == "notifications/progress":
                progress.append(message["params"].get("message"))

    def close(self):
        try:
            self.process.stdin.close()
            self.process.wait(timeout=20)
        except Exception:
            self.process.kill()
            self.process.wait()


class Stand:
    """A fake Creatio site answering a scenario's rules; it records every request but the login."""

    def __init__(self, rules):
        self.rules, self.flags, self.requests, self.lock = rules, set(), [], threading.Lock()
        with socket.socket() as probe:
            probe.bind(("127.0.0.1", 0))
            self.port = probe.getsockname()[1]
        self.url = f"http://127.0.0.1:{self.port}"
        self.server = http.server.ThreadingHTTPServer(("127.0.0.1", self.port), self.handler())
        threading.Thread(target=self.server.serve_forever, daemon=True).start()

    def close(self):
        self.server.shutdown()
        self.server.server_close()

    def handler(self):
        stand = self

        class Handler(http.server.BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"

            def log_message(self, *args):
                pass

            def answer(self, status, data, content_type, cookies=False):
                self.send_response(status)
                self.send_header("Content-Type", content_type)
                self.send_header("Content-Length", str(len(data)))
                if cookies:
                    for cookie in (".ASPXAUTH=fake; path=/", "BPMCSRF=fakecsrf; path=/", "BPMLOADER=fake; path=/",
                                   "UserType=General; path=/"):
                        self.send_header("Set-Cookie", cookie)
                self.end_headers()
                self.wfile.write(data)

            def handle_any(self):
                length = int(self.headers.get("Content-Length") or 0)
                raw = self.rfile.read(length) if length else b""
                if self.headers.get("Transfer-Encoding", "").lower() == "chunked":
                    raw = self.read_chunked()
                path, _, query = self.path.partition("?")
                query = urllib.parse.unquote(query)
                if path.startswith("/0/"):
                    path = path[2:]
                if path.endswith("/AuthService.svc/Login"):
                    self.answer(200, b'{"Code":0,"Message":""}', "application/json; charset=utf-8", cookies=True)
                    return
                text = raw.decode("utf-8", "replace")
                with stand.lock:
                    stand.requests.append({"method": self.command, "path": path, "query": query, "body": raw})
                    rule = stand.match(self.command, path, text + "\n" + query)
                if rule is None:
                    self.answer(200, b'{"success":true}', "application/json; charset=utf-8")
                    return
                if "body_file" in rule:
                    data = (FIXTURES / rule["body_file"]).read_bytes()
                elif "body_base64" in rule:
                    data = base64.b64decode(rule["body_base64"])
                else:
                    data = rule.get("body", "").encode("utf-8")
                self.answer(rule.get("status", 200), data, rule.get("content_type", "application/json; charset=utf-8"))

            def read_chunked(self):
                data = b""
                while True:
                    size = int(self.rfile.readline().strip() or b"0", 16)
                    if size == 0:
                        self.rfile.readline()
                        return data
                    data += self.rfile.read(size)
                    self.rfile.readline()

            do_GET = do_POST = do_PUT = do_DELETE = do_PATCH = handle_any

        return Handler

    def match(self, method, path, seen):
        for rule in self.rules:
            if rule.get("method", method) != method or not path.endswith(rule["route"]):
                continue
            contains = rule.get("contains", [])
            contains = [contains] if isinstance(contains, str) else contains
            if not all(text in seen for text in contains) or any(text in seen for text in rule.get("excludes", [])):
                continue
            if rule.get("if") and rule["if"] not in self.flags or rule.get("unless") and rule["unless"] in self.flags:
                continue
            if rule.get("set"):
                self.flags.add(rule["set"])
            if rule.get("clear"):
                self.flags.discard(rule["clear"])
            return rule
        return None


def normalize_requests(requests, home, url):
    out = []
    guids, dates = {}, {}
    for request in requests:
        try:
            body = request["body"].decode("utf-8")
            for variant in sorted({home, os.path.realpath(home), json.dumps(home)[1:-1]}, key=len, reverse=True):
                body = body.replace(variant, "{home}")
            body = body.replace(url, "{stand}")
            body = GUID.sub(lambda m: guids.setdefault(m.group(0).lower(), f"{{guid{len(guids) + 1}}}"), body)
            body = DATE.sub(lambda m: dates.setdefault(m.group(0), f"{{date{len(dates) + 1}}}"), body)
        except UnicodeDecodeError:
            body = "sha256:" + hashlib.sha256(request["body"]).hexdigest()
        out.append(f'{request["method"]} {request["path"]}?{request["query"]} {body}')
    return out


def answer_text(response, home):
    """The answer as compared: the text content (or the protocol error), home path and correlation-id out."""
    if "error" in response:
        text = "protocol-error: " + response["error"].get("message", "")
    else:
        result = response.get("result") or {}
        text = "\n".join(c.get("text", "") for c in result.get("content", []) if c.get("type") == "text")
        if result.get("isError"):
            text = "isError: " + text
    text = CORRELATION.sub("", text)
    for variant in sorted({home, os.path.realpath(home), json.dumps(home)[1:-1], json.dumps(os.path.realpath(home))[1:-1]},
                          key=len, reverse=True):
        text = text.replace(variant, "{home}")
    return text


def tree(root):
    """path -> (mode, bytes) for every file under root, in sorted order."""
    files = {}
    for base, folders, names in os.walk(root):
        folders.sort()
        # os.walk lists a link to a folder among the folders and does not descend into it.
        for name in sorted(names + [f for f in folders if os.path.islink(os.path.join(base, f))]):
            full = os.path.join(base, name)
            relative = os.path.relpath(full, root).replace(os.sep, "/")
            if os.path.islink(full):
                files[relative] = ("link", os.readlink(full).encode())
                continue
            files[relative] = (oct(stat.S_IMODE(os.stat(full).st_mode)), open(full, "rb").read())
    return files


def normalize_tree(files, home):
    """GUIDs and dates replaced by numbered placeholders in order of appearance; the home path by {home}."""
    guids, dates = {}, {}

    def placeholder(table, prefix):
        def replace(match):
            key = match.group(0).lower()
            table.setdefault(key, f"{{{prefix}{len(table) + 1}}}")
            return table[key]
        return replace

    out = {}
    for path, (mode, data) in files.items():
        try:
            text = data.decode("utf-8")
        except UnicodeDecodeError:
            out[path] = (mode, hashlib.sha256(data).hexdigest())
            continue
        for variant in sorted({home, os.path.realpath(home)}, key=len, reverse=True):
            text = text.replace(variant, "{home}")
        text = GUID.sub(placeholder(guids, "guid"), text)
        text = DATE.sub(placeholder(dates, "date"), text)
        out[path] = (mode, text)
    return out


def seed_home(clio_dll, home, environments):
    """A fresh clio home: clio's own first-start appsettings.json plus the scenario's environments."""
    shutil.rmtree(home, ignore_errors=True)
    os.makedirs(home)
    Server(["dotnet", clio_dll, "mcp-server"], home).close()
    settings_path = os.path.join(home, "appsettings.json")
    settings = json.load(open(settings_path, encoding="utf-8-sig"))
    settings["Environments"] = environments
    if environments:
        settings["ActiveEnvironmentKey"] = next(iter(environments))
    json.dump(settings, open(settings_path, "w", encoding="utf-8"), indent=2)
    for leftover in ("mcp-workers", "server-stderr.log"):
        path = os.path.join(home, leftover)
        shutil.rmtree(path, ignore_errors=True) if os.path.isdir(path) else (os.path.exists(path) and os.remove(path))


def substitute(value, mapping):
    if isinstance(value, dict):
        return {key: substitute(item, mapping) for key, item in value.items()}
    if isinstance(value, list):
        return [substitute(item, mapping) for item in value]
    if isinstance(value, str):
        for key, replacement in mapping.items():
            value = value.replace(key, replacement)
    return value


def run_side(command, home, scenario, clio_dll):
    stand = Stand(scenario["stand"]["rules"]) if scenario.get("stand") else None
    try:
        answers, files = run_steps(command, home, scenario, clio_dll, stand.url if stand else None)
    finally:
        if stand:
            stand.close()
    requests = normalize_requests(stand.requests, home, stand.url) if stand else []
    return answers, files, requests


def run_steps(command, home, scenario, clio_dll, stand_url):
    environments = {}
    if scenario.get("environments", True):
        environments = {"fake": {"Uri": stand_url or "http://127.0.0.1:9", "Login": "example-user", "Password": "replace-me",
                                 "IsNetCore": True, "Safe": False, "Maintainer": scenario.get("maintainer", "Customer")}}
        environments["fake"].update(substitute(scenario.get("environment", {}), {"{home}": home}))
    seed_home(clio_dll, home, environments)
    os.makedirs(os.path.join(home, "base"), exist_ok=True)
    mapping = {"{ws}": os.path.join(home, "base", "ws"), "{home}": home}
    server = Server(command, home)
    answers = []
    try:
        for step in scenario["steps"]:
            if "mkdir" in step:
                os.makedirs(os.path.join(home, substitute(step["mkdir"], mapping)), exist_ok=True)
                continue
            if "write" in step:
                target = os.path.join(home, substitute(step["write"], mapping))
                os.makedirs(os.path.dirname(target), exist_ok=True)
                open(target, "w", encoding="utf-8", newline="").write(substitute(step["content"], mapping))
                continue
            args = substitute(step.get("args", {}), mapping)
            if step.get("direct"):
                response = server.request("tools/call", {"name": step["tool"], "arguments": args})
            else:
                response = server.request("tools/call", {"name": "clio-run", "arguments": {"command": step["tool"], "args": args}})
            text = answer_text(response, home)
            if stand_url:
                text = text.replace(stand_url, "{stand}")
            answers.append({"step": step.get("label", step["tool"]), "answer": text,
                            "progress": len(response["progress"]) > 0})
    finally:
        server.close()
    return answers, normalize_tree(tree(os.path.join(home, "base")), home)


def compare(clio_side, go_side):
    differences = []
    clio_answers, clio_tree, clio_requests = clio_side
    go_answers, go_tree, go_requests = go_side
    if clio_requests != go_requests:
        differences.append({"what": "requests", "clio": "\n".join(clio_requests), "go": "\n".join(go_requests)})
    for clio_answer, go_answer in zip(clio_answers, go_answers):
        if clio_answer["answer"] != go_answer["answer"]:
            differences.append({"step": clio_answer["step"], "what": "answer", "clio": clio_answer["answer"], "go": go_answer["answer"]})
    for path in sorted(set(clio_tree) | set(go_tree)):
        if path not in go_tree:
            differences.append({"path": path, "what": "missing in go"})
        elif path not in clio_tree:
            differences.append({"path": path, "what": "extra in go"})
        elif clio_tree[path][0] != go_tree[path][0]:
            differences.append({"path": path, "what": "mode", "clio": clio_tree[path][0], "go": go_tree[path][0]})
        elif clio_tree[path][1] != go_tree[path][1]:
            differences.append({"path": path, "what": "content", "clio": clio_tree[path][1], "go": go_tree[path][1]})
    return differences


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--clio-dll", required=True)
    parser.add_argument("--go-bin", required=True)
    parser.add_argument("--work", required=True)
    parser.add_argument("--scenario", nargs="*")
    parser.add_argument("--evidence", default=str(REPO / "evidence" / "t16c-workspace.json"))
    parser.add_argument("--show", action="store_true")
    options = parser.parse_args()
    work = os.path.realpath(options.work)
    scenarios = []
    for path in sorted(glob.glob(str(SCENARIOS / "*.json"))):
        scenarios += json.load(open(path, encoding="utf-8"))
    if options.scenario:
        scenarios = [s for s in scenarios if s["name"] in options.scenario]
    results = []
    for scenario in scenarios:
        sides = {}
        for side, command in (("clio", ["dotnet", options.clio_dll, "mcp-server"]), ("go", [os.path.realpath(options.go_bin)])):
            home = os.path.join(work, scenario["name"], side)
            sides[side] = run_side(command, home, scenario, options.clio_dll)
        differences = compare(sides["clio"], sides["go"])
        known = scenario.get("known-differences", [])
        unexplained = [d for d in differences if not any(k in json.dumps(d) for k in known)]
        verdict = "match" if not differences else ("known-diff" if not unexplained else "mismatch")
        files = len(sides["clio"][1])
        results.append({"scenario": scenario["name"], "verdict": verdict, "files": files,
                        "steps": len(sides["clio"][0]), "differences": [{k: v for k, v in d.items() if k in ("step", "path", "what")} for d in differences]})
        print(f"{verdict:10} {scenario['name']} ({len(sides['clio'][0])} calls, {files} files)")
        for difference in differences[:40]:
            print("    ", difference.get("step") or difference.get("path") or "", difference["what"])
            if options.show and "clio" in difference:
                print("       clio:", str(difference["clio"])[:3000])
                print("       go:  ", str(difference["go"])[:3000])
        if options.show and not differences:
            for answer in sides["clio"][0]:
                print("     ", answer["step"], answer["answer"][:400])
    counts = {v: sum(r["verdict"] == v for r in results) for v in ("match", "known-diff", "mismatch")}
    evidence = pathlib.Path(options.evidence)
    evidence.parent.mkdir(parents=True, exist_ok=True)
    evidence.write_text(json.dumps({"schema": 1, "counts": counts, "scenarios": results}, indent=2) + "\n", encoding="utf-8")
    print(json.dumps(counts))
    return 1 if counts["mismatch"] else 0


if __name__ == "__main__":
    sys.exit(main())
