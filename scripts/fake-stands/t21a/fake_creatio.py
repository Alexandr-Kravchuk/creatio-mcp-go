"""A stateful canned Creatio for comparing clio 8.1.0.142 and creatio-mcp-go on the T21a tools (create-package,
place-section-in-workplace, localize-page, trigger-smoke) without a stand: writes are kept, so the read-backs
after them see the change.

Each server gets its own copy of the state (told apart by the user agent: Go's is "Go-http-client"), so both
see the same sequence of reads and writes. Every request is logged; `compare` checks that the two servers sent
the same requests (bodies compared with fresh GUIDs and timestamps masked).

From the repository root:

  python3 scripts/fake-stands/t21a/fake_creatio.py serve 18921 <dir> &      # writes <dir>/appsettings.json
  python3 scripts/compare-mcp.py --clio-dll <clio.dll> --go-bin <creatio-mcp-go> --clio-env t21afake \
      --clio-settings <dir>/appsettings.json --cases scripts/fake-stands/t21a/cases.json --evidence <file>
  python3 scripts/fake-stands/t21a/fake_creatio.py compare <dir>/requests.jsonl
"""
import collections
import copy
import json
import pathlib
import re
import socket
import sys
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

CRT_BASE = "11111111-0000-0000-0000-000000000001"
CRT_UI = "11111111-0000-0000-0000-000000000002"
USR_EXISTING = "11111111-0000-0000-0000-000000000003"
APP_ID = "22222222-0000-0000-0000-000000000001"


def package(uid, name, description="", maintainer="Creatio", install_type=1, deps=None):
    return {"uId": uid, "name": name, "description": description, "maintainer": maintainer,
            "installType": install_type, "version": "8.0.0", "type": 0, "dependsOnPackages": deps or []}


class State:
    def __init__(self):
        self.packages = {CRT_BASE: package(CRT_BASE, "CrtBase"), CRT_UI: package(CRT_UI, "CrtUIv2"),
                         USR_EXISTING: package(USR_EXISTING, "UsrExisting", maintainer="Customer", install_type=0)}
        self.app_packages = {}

    def by_name(self, name):
        return next((p for p in self.packages.values() if p["name"].lower() == (name or "").lower()), None)

    def create(self, dto, app_id=None):
        name = dto.get("name", "")
        if name.endswith("RefuseMe"):
            return {"success": False, "errorInfo": {"message": "Name is not valid"}}
        if name.endswith("NoDetails"):
            return {"success": False}
        stored = package(dto.get("uId"), name, dto.get("description", ""), "Customer", 0)
        if name.endswith("NullDescription"):
            stored["description"] = None
        if name.endswith("NoInstallType"):
            del stored["installType"]
        self.packages[dto.get("uId")] = stored
        if app_id:
            self.app_packages[dto.get("uId")] = app_id
        return {"success": True}

    def properties(self, uid):
        stored = self.packages.get(uid)
        if stored is None:
            return {"success": False, "errorInfo": {"message": f"Package with UId {uid} not found"}}
        if stored["name"].endswith("ReadbackFail"):
            return {"success": False, "errorInfo": {"message": "Readback denied"}}
        return {"success": True, "package": copy.deepcopy(stored)}

    def save_properties(self, dto):
        stored = self.packages.get(dto.get("uId"))
        if stored is None:
            return {"success": False, "errorInfo": {"message": "Unknown package"}}
        if stored["name"].endswith("DepsFail"):
            return {"success": False, "errorInfo": {"message": "Dependency cycle"}}
        stored["dependsOnPackages"] = [{"uId": d.get("uId"), "name": d.get("name"), "version": d.get("version")}
                                       for d in dto.get("dependsOnPackages") or []]
        return {"success": True, "compilationRequired": False}


def filters_of(body):
    values = {}
    for item in (body.get("filters") or {}).get("items", {}).values():
        column = item.get("leftExpression", {}).get("columnPath")
        values[column] = item.get("rightExpression", {}).get("parameter", {}).get("value")
    return values


def select(state, body):
    root = body.get("rootSchemaName")
    values = filters_of(body)
    if root == "SysPackage":
        return {"success": True, "rows": [{"Name": p["name"], "UId": p["uId"], "Maintainer": p["maintainer"],
                                           "Version": p["version"]} for p in state.packages.values()]}
    if root == "SysInstalledApp":
        rows = [{"Id": APP_ID, "Code": "UsrApp", "Name": "My app", "Version": "1.0.0"}] if values.get("Code") == "UsrApp" else []
        return {"success": True, "rows": rows}
    if root == "SysSettingsValue":
        return {"success": True, "rows": [{"TextValue": "Usr", "GuidValue": None, "ValueTypeName": "ShortText"}]}
    return {"success": False, "errorInfo": {"message": f"Unexpected query on {root}"}}


def answer(state, path, body):
    if path.endswith("/DataService/json/SyncReply/QuerySysSettings"):
        # clio's ATF provider writes SysSettingsNameCollection, this server sysSettingsNameCollection; Creatio
        # reads both.
        names = body.get("sysSettingsNameCollection") or body.get("SysSettingsNameCollection") or []
        return 200, {"success": True, "values": {name: {"dataValueType": 1, "value": "Usr"} for name in names
                                                 if name == "SchemaNamePrefix"}}
    if path.endswith("/DataService/json/SyncReply/SelectQuery"):
        return 200, select(state, body)
    if path.endswith("/PackageService.svc/CreatePackage"):
        return 200, state.create(body)
    if path.endswith("/ApplicationPackagesService.svc/CreatePackageInApp"):
        return 200, state.create(body.get("package") or {}, body.get("appId"))
    if path.endswith("/PackageService.svc/GetPackageProperties"):
        return 200, state.properties(body if isinstance(body, str) else "")
    if path.endswith("/PackageService.svc/SavePackageProperties"):
        return 200, state.save_properties(body)
    return 404, {"success": False, "errorInfo": {"message": f"Unexpected route {path}"}}


def serve(port, directory):
    directory = pathlib.Path(directory)
    directory.mkdir(parents=True, exist_ok=True)
    settings = {"Environments": {"t21afake": {"Uri": f"http://127.0.0.1:{port}", "Login": "example-user",
                                              "Password": "replace-me", "IsNetCore": False, "Safe": False}},
                "ActiveEnvironmentKey": "t21afake"}
    (directory / "appsettings.json").write_text(json.dumps(settings, indent=2), encoding="utf-8")
    log = open(directory / "requests.jsonl", "a", buffering=1, encoding="utf-8")
    lock = threading.Lock()
    states = collections.defaultdict(State)

    class Handler(BaseHTTPRequestHandler):
        def log_message(self, *args):
            pass

        def reply(self, status, body, cookies=False):
            data = (body if isinstance(body, str) else json.dumps(body)).encode("utf-8")
            self.send_response(status)
            self.send_header("Content-Type", "text/html" if isinstance(body, str) else "application/json; charset=utf-8")
            self.send_header("Content-Length", str(len(data)))
            if cookies:
                for cookie in (".ASPXAUTH=fake; path=/", "BPMCSRF=fakecsrf; path=/", "BPMLOADER=fake; path=/"):
                    self.send_header("Set-Cookie", cookie)
            self.end_headers()
            self.wfile.write(data)

        def drop(self):
            """Close the connection without an answer: the client sees a transport failure."""
            try:
                self.connection.shutdown(socket.SHUT_RDWR)
            except OSError:
                pass
            self.close_connection = True

        def handle_any(self):
            length = int(self.headers.get("Content-Length") or 0)
            raw = self.rfile.read(length).decode("utf-8", "replace") if length else ""
            path = self.path.split("?")[0]
            if path.startswith("/0/"):
                path = path[2:]
            side = "go" if "Go-http-client" in (self.headers.get("User-Agent") or "") else "clio"
            with lock:
                log.write(json.dumps({"side": side, "method": self.command, "path": path, "body": raw}) + "\n")
                if path.endswith("/AuthService.svc/Login"):
                    return self.reply(200, {"Code": 0, "Message": ""}, cookies=True)
                try:
                    body = json.loads(raw) if raw else {}
                except ValueError:
                    body = {}
                drop = False
                if path.endswith("/CreatePackage") and isinstance(body, dict) and str(body.get("name", "")).startswith("UsrDrop"):
                    # UsrDropStored is stored before the connection drops; UsrDropLost is not.
                    drop = True
                    if body["name"] == "UsrDropStored":
                        states[side].create(body)
                    status, payload = 0, None
                else:
                    status, payload = answer(states[side], path, body)
            if drop:
                return self.drop()
            try:
                self.reply(status, payload)
            except (BrokenPipeError, ConnectionResetError):
                pass

        do_GET = handle_any
        do_POST = handle_any

    ThreadingHTTPServer(("127.0.0.1", port), Handler).serve_forever()


GUID = re.compile(r"[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}")
KNOWN_GUIDS = {CRT_BASE, CRT_UI, USR_EXISTING, APP_ID}


def canonical(value):
    if isinstance(value, dict):
        return {k: canonical(v) for k, v in value.items()}
    if isinstance(value, list):
        return [canonical(item) for item in value]
    if isinstance(value, str) and value[:1] in "{[" and value[-1:] in "}]":
        try:
            return canonical(json.loads(value))
        except ValueError:
            return value
    if isinstance(value, str):
        # A GUID the server generated (a new package UId) differs on every run; a fixture GUID does not.
        return GUID.sub(lambda m: m.group(0) if m.group(0).lower() in KNOWN_GUIDS else "<new-guid>", value)
    return value


def compare(log_path):
    sent = collections.defaultdict(lambda: collections.defaultdict(list))
    for line in open(log_path, encoding="utf-8"):
        entry = json.loads(line)
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
        for item in sorted(set(clio) ^ set(go))[:6]:
            print("   ", "clio" if item in clio else "go  ", item[:400])
    print(json.dumps({"routes": len(set(sent["clio"]) | set(sent["go"])), "different": differing}))
    return 1 if differing else 0


if __name__ == "__main__":
    if len(sys.argv) == 4 and sys.argv[1] == "serve":
        serve(int(sys.argv[2]), sys.argv[3])
    elif len(sys.argv) == 3 and sys.argv[1] == "compare":
        sys.exit(compare(sys.argv[2]))
    else:
        sys.exit(__doc__)
