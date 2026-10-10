"""A canned Creatio for comparing clio and creatio-mcp-go on the T13 process tools the write stand cannot
serve (it has no CrtProcessBuilder, and the user-task tools need a local workspace).

The server answers every route from rules.json (first match by path suffix and an optional request-body
substring; anything else gets {"success":true}), logs each request with the caller (clio or go, told apart
by the user agent) and can compare what the two servers sent.

From the repository root:

  python3 scripts/fake-stands/t13/fake_creatio.py serve 18913 <dir> &      # writes <dir>/appsettings.json
  python3 scripts/compare-mcp.py --clio-dll <clio.dll> --go-bin <creatio-mcp-go> --clio-env t13fake \
      --clio-settings <dir>/appsettings.json --cases scripts/fake-stands/t13/cases.json --evidence <file>
  python3 scripts/fake-stands/t13/fake_creatio.py compare <dir>/requests.jsonl

The workspace in this folder is the user-task fixture; the cases name it by a path relative to the
repository root, which both servers resolve against their working directory.
"""
import collections
import json
import pathlib
import sys
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

HERE = pathlib.Path(__file__).resolve().parent


def serve(port, directory):
    directory = pathlib.Path(directory)
    directory.mkdir(parents=True, exist_ok=True)
    settings = {"Environments": {"t13fake": {"Uri": f"http://127.0.0.1:{port}", "Login": "example-user",
                                             "Password": "replace-me", "IsNetCore": False, "Safe": False}},
                "ActiveEnvironmentKey": "t13fake"}
    (directory / "appsettings.json").write_text(json.dumps(settings, indent=2), encoding="utf-8")
    rules = json.loads((HERE / "rules.json").read_text(encoding="utf-8"))
    log = open(directory / "requests.jsonl", "a", buffering=1, encoding="utf-8")
    lock = threading.Lock()

    class Handler(BaseHTTPRequestHandler):
        def log_message(self, *args):
            pass

        def answer(self, status, body, cookies=False):
            data = body.encode("utf-8")
            self.send_response(status)
            self.send_header("Content-Type", "application/json; charset=utf-8")
            self.send_header("Content-Length", str(len(data)))
            if cookies:
                for cookie in (".ASPXAUTH=fake; path=/", "BPMCSRF=fakecsrf; path=/", "BPMLOADER=fake; path=/"):
                    self.send_header("Set-Cookie", cookie)
            self.end_headers()
            self.wfile.write(data)

        def handle_any(self):
            length = int(self.headers.get("Content-Length") or 0)
            body = self.rfile.read(length).decode("utf-8", "replace") if length else ""
            path = self.path.split("?")[0]
            if path.startswith("/0/"):
                path = path[2:]
            side = "go" if "Go-http-client" in (self.headers.get("User-Agent") or "") else "clio"
            with lock:
                log.write(json.dumps({"side": side, "method": self.command, "path": path, "body": body}) + "\n")
            if path.endswith("/AuthService.svc/Login"):
                return self.answer(200, '{"Code":0,"Message":""}', cookies=True)
            for rule in rules:
                if path.endswith(rule["route"]) and rule.get("contains", "") in body:
                    return self.answer(rule.get("status", 200), rule["body"])
            return self.answer(200, '{"success":true}')

        do_GET = handle_any
        do_POST = handle_any

    ThreadingHTTPServer(("127.0.0.1", port), Handler).serve_forever()


def canonical(value):
    """Parsed JSON with generated parameter uIds blanked (they differ per run) and JSON-in-string expanded."""
    if isinstance(value, dict):
        return {k: ("<generated>" if k == "uId" and isinstance(v, str) and len(v) == 36 else canonical(v)) for k, v in value.items()}
    if isinstance(value, list):
        return [canonical(item) for item in value]
    if isinstance(value, str) and value[:1] in "{[" and value[-1:] in "}]":
        try:
            return canonical(json.loads(value))
        except ValueError:
            return value
    return value


def compare(log_path):
    sent = collections.defaultdict(lambda: collections.defaultdict(list))
    for line in open(log_path, encoding="utf-8"):
        entry = json.loads(line)
        # Sign-in and clio's own connectivity probe are not part of any tool's contract.
        if entry["path"].endswith("/Login") or entry["path"] == "/ping":
            continue
        try:
            body = json.loads(entry["body"]) if entry["body"] else None
        except ValueError:
            body = entry["body"]
        sent[entry["side"]][entry["path"]].append(json.dumps(canonical(body), sort_keys=True))
    differing = 0
    for route in sorted(set(sent["clio"]) | set(sent["go"])):
        clio, go = sorted(sent["clio"].get(route, [])), sorted(sent["go"].get(route, []))
        if clio == go:
            print(f"same       {route} x{len(clio)}")
            continue
        differing += 1
        print(f"different  {route} clio x{len(clio)} go x{len(go)}")
    print(json.dumps({"routes": len(set(sent["clio"]) | set(sent["go"])), "different": differing}))
    return 1 if differing else 0


if __name__ == "__main__":
    if len(sys.argv) == 4 and sys.argv[1] == "serve":
        serve(int(sys.argv[2]), sys.argv[3])
    elif len(sys.argv) == 3 and sys.argv[1] == "compare":
        sys.exit(compare(sys.argv[2]))
    else:
        sys.exit(__doc__)
