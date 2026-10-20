"""A canned Creatio for comparing clio 8.1.0.142 and creatio-mcp-go on the T21b tools: odata-read and
odata-read-to-file, execute-esq and execute-esq-to-file, list-entity-client-schemas(-to-file).

The T20 fake with one change: `contains` / `excludes` also see the URL-decoded query string, so an OData GET
can be matched (and is logged and counted) by its $filter / $select / $top, which proves both servers send
the same request. A rule's optional `content_type` replaces the JSON content type (an HTML error page).

Every request is answered by the first rule of rules.json whose method, route suffix, `contains` substrings
(all of them) and `excludes` substrings (none of them) match. Anything else gets {"success":true}. Each
request is logged with the caller (clio or go, told apart by the user agent) and the rule that answered it.

From the repository root (use a port nobody else uses; a stray caller's requests land in the log):

  python3 scripts/fake-stands/t21b/fake_creatio.py serve 18971 <dir> &     # writes <dir>/appsettings.json
  python3 scripts/fake-stands/t21b/run_cases.py --clio-dll <clio.dll> --go-bin <creatio-mcp-go> \
      --settings <dir>/appsettings.json --env fake --cases scripts/fake-stands/t21b/cases.json
  python3 scripts/fake-stands/t21b/fake_creatio.py count <dir>/requests.jsonl   # requests per route, query, rule
"""
import urllib.parse
import json
import pathlib
import sys
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


def serve(port, directory, rules_path):
    directory = pathlib.Path(directory)
    directory.mkdir(parents=True, exist_ok=True)
    settings = {"Environments": {"fake": {"Uri": f"http://127.0.0.1:{port}", "Login": "example-user",
                                          "Password": "replace-me", "IsNetCore": True, "Safe": False}},
                "ActiveEnvironmentKey": "fake"}
    (directory / "appsettings.json").write_text(json.dumps(settings, indent=2), encoding="utf-8")
    log = open(directory / "requests.jsonl", "a", buffering=1, encoding="utf-8")
    lock = threading.Lock()
    flags = set()

    class Handler(BaseHTTPRequestHandler):
        def log_message(self, *args):
            pass

        def answer(self, status, body, cookies=False, content_type="application/json; charset=utf-8"):
            data = body.encode("utf-8")
            self.send_response(status)
            if data:
                self.send_header("Content-Type", content_type)
            self.send_header("Content-Length", str(len(data)))
            if cookies:
                for cookie in (".ASPXAUTH=fake; path=/", "BPMCSRF=fakecsrf; path=/", "BPMLOADER=fake; path=/"):
                    self.send_header("Set-Cookie", cookie)
            self.end_headers()
            self.wfile.write(data)

        def handle_any(self):
            rules = json.loads(pathlib.Path(rules_path).read_text(encoding="utf-8"))
            length = int(self.headers.get("Content-Length") or 0)
            body = self.rfile.read(length).decode("utf-8", "replace") if length else ""
            path, _, query = self.path.partition("?")
            query = urllib.parse.unquote(query)
            seen = body + "\n" + query
            if path.startswith("/0/"):
                path = path[2:]
            side = "go" if "Go-http-client" in (self.headers.get("User-Agent") or "") else "clio"
            matched = None
            if path.endswith("/AuthService.svc/Login"):
                matched = "login"
                self.answer(200, '{"Code":0,"Message":""}', cookies=True)
            else:
                for index, rule in enumerate(rules):
                    if rule.get("method", self.command) != self.command:
                        continue
                    contains = rule.get("contains", [])
                    if isinstance(contains, str):
                        contains = [contains]
                    if rule.get("if") and rule["if"] not in flags:
                        continue
                    if rule.get("unless") and rule["unless"] in flags:
                        continue
                    if any(x in seen for x in rule.get("excludes", [])):
                        continue
                    if path.endswith(rule["route"]) and all(c in seen for c in contains):
                        matched = index
                        if rule.get("set"):
                            flags.add(rule["set"])
                        if rule.get("clear"):
                            flags.discard(rule["clear"])
                        self.answer(rule.get("status", 200), rule["body"],
                                    content_type=rule.get("content_type", "application/json; charset=utf-8"))
                        break
                else:
                    matched = "default"
                    self.answer(200, '{"success":true}')
            with lock:
                log.write(json.dumps({"side": side, "method": self.command, "path": path, "query": query,
                                      "rule": matched, "body": body}) + "\n")

        do_GET = handle_any
        do_POST = handle_any
        do_DELETE = handle_any
        do_PATCH = handle_any

    ThreadingHTTPServer(("127.0.0.1", port), Handler).serve_forever()


def count(log_path):
    """Requests per (method, route, query, rule) for each side; 1 when they differ."""
    import collections
    counts = collections.defaultdict(collections.Counter)
    for line in open(log_path, encoding="utf-8"):
        entry = json.loads(line)
        if entry["rule"] == "login":
            continue
        counts[entry["side"]][(entry["method"], entry["path"], entry.get("query", ""), str(entry["rule"]))] += 1
    differing = 0
    for key in sorted(set(counts["clio"]) | set(counts["go"])):
        clio, go = counts["clio"][key], counts["go"][key]
        differing += clio != go
        print(f"{'same' if clio == go else 'DIFFERENT':10} clio={clio} go={go} {' '.join(key)}")
    print(json.dumps({"keys": len(set(counts["clio"]) | set(counts["go"])), "different": differing}))
    return 1 if differing else 0


if __name__ == "__main__":
    if len(sys.argv) in (4, 5) and sys.argv[1] == "serve":
        rules = sys.argv[4] if len(sys.argv) == 5 else str(pathlib.Path(__file__).resolve().parent / "rules.json")
        serve(int(sys.argv[2]), sys.argv[3], rules)
    elif len(sys.argv) == 3 and sys.argv[1] == "count":
        sys.exit(count(sys.argv[2]))
    else:
        sys.exit(__doc__)
