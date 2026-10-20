"""A stateful canned Creatio for comparing clio and creatio-mcp-go on the T19 tools (get-object-rights,
set-object-rights, set-default-record-rights, apply-default-record-rights, get-entity-connections,
update-entity-connections) without a stand: a save is kept, so the read-back after it sees the change.

Each server gets its own copy of the state (told apart by the user agent: Go's is "Go-http-client"), so
both see the same sequence of reads and writes. Every request is logged; `compare` checks that the two
servers sent the same requests.

From the repository root:

  python3 scripts/fake-stands/t19/fake_creatio.py serve 18919 <dir> &      # writes <dir>/appsettings.json
  python3 scripts/compare-mcp.py --clio-dll <clio.dll> --go-bin <creatio-mcp-go> --clio-env t19fake \
      --clio-settings <dir>/appsettings.json --cases scripts/fake-stands/t19/cases.json --evidence <file>
  python3 scripts/fake-stands/t19/fake_creatio.py compare <dir>/requests.jsonl

compare-mcp counts two refusals worded differently as "both-failed"; the cases here are meant to match
exactly, so read the differences it prints for those too.
"""
import collections
import copy
import json
import pathlib
import sys
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

ALL_EMPLOYEES = "a29a3ba5-4b0d-de11-9a51-005056c00008"
SALES = "11111111-1111-1111-1111-111111111111"
SUPPORT = "22222222-2222-2222-2222-222222222222"
UNITS = {ALL_EMPLOYEES: "All employees", SALES: "Sales managers", SUPPORT: "Support"}
PACKAGE = "44444444-4444-4444-4444-444444444444"
ACCOUNT = "55555555-5555-5555-5555-555555555555"
COLUMNS = {"Owner": "66666666-0000-0000-0000-000000000001", "PrimaryContact": "66666666-0000-0000-0000-000000000002",
           "Type": "66666666-0000-0000-0000-000000000003", "Name": "66666666-0000-0000-0000-000000000004"}
PROCESS = "77777777-7777-7777-7777-777777777777"
SLOW_PROCESS = "77777777-7777-7777-7777-777777777778"
RUNNING = "ed2ae277-b6e2-df11-971b-001d60e938c6"
CONTACT = "55555555-5555-5555-5555-555555555556"
LEAD = "55555555-5555-5555-5555-555555555557"
STRANGER = "66666666-0000-0000-0000-0000000000ff"
ENTITIES = {"Account": ACCOUNT, "Contact": CONTACT, "Lead": LEAD}
COMPLETED = "815c9586-b6e2-df11-971b-001d60e938c6"


def row(unit, position, read=True, append=True, edit=True, delete=True):
    return {"sysAdminUnit": {"id": unit, "name": UNITS.get(unit)}, "position": position,
            "canRead": read, "canAppend": append, "canEdit": edit, "canDelete": delete}


def rule(author, grantee, read=1, edit=0, delete=0, manager=False):
    return {"authorSysAdminUnit": {"id": author, "name": UNITS.get(author)},
            "granteeSysAdminUnit": {"id": grantee, "name": UNITS.get(grantee)},
            "readRightLevel": read, "editRightLevel": edit, "deleteRightLevel": delete, "doNotApplyForManager": manager}


def objects():
    """name -> (candidate UIds, the object the base candidate answers with, record count)."""
    def node(uid, name, caption, operations=False, records=False, rows=None, rules=None):
        return {"uId": uid, "name": name, "caption": caption, "administratedByOperations": operations,
                "administratedByColumns": False, "administratedByRecords": records,
                "entitySchemaOperationsRights": rows or [], "entitySchemaRecordDefRights": rules or [],
                "entitySchemaColumnsRights": [], "entityOperationGrantees": []}
    found = {
        "UsrOrder": (["aaaaaaaa-0000-0000-0000-000000000001"],
                     node("aaaaaaaa-0000-0000-0000-000000000001", "UsrOrder", "Orders"), 12),
        "UsrInvoice": (["aaaaaaaa-0000-0000-0000-000000000002"],
                       node("aaaaaaaa-0000-0000-0000-000000000002", "UsrInvoice", "Invoices", operations=True, records=True,
                            rows=[row(SUPPORT, 0, True, False, False, False), row(ALL_EMPLOYEES, 1)],
                            rules=[rule(ALL_EMPLOYEES, SALES, 1, 2, 0, True)]), 3),
        "UsrStored": (["aaaaaaaa-0000-0000-0000-000000000003"],
                      node("aaaaaaaa-0000-0000-0000-000000000003", "UsrStored", "UsrStored", rows=[row(SALES, 0, True, False, False, False)],
                           rules=[rule(SALES, SUPPORT)]), 0),
        "UsrLayered": (["bbbbbbbb-0000-0000-0000-00000000000f", "aaaaaaaa-0000-0000-0000-000000000004"],
                       node("aaaaaaaa-0000-0000-0000-000000000004", "UsrLayered", "Layered", operations=True,
                            rows=[row(SALES, 0), row(SALES, 3, True, False, False, False)]), 1),
        "UsrBroken": (["bbbbbbbb-0000-0000-0000-00000000000e", "bbbbbbbb-0000-0000-0000-00000000000d"], None, 0),
        "Feature": (["aaaaaaaa-0000-0000-0000-000000000005"],
                    node("aaaaaaaa-0000-0000-0000-000000000005", "Feature", "Creatio functionality", operations=True,
                         rows=[row(ALL_EMPLOYEES, 0)]), 40),
        "UsrDupA": (["aaaaaaaa-0000-0000-0000-000000000006"], node("aaaaaaaa-0000-0000-0000-000000000006", "UsrDupA", "Shared title"), 0),
        "UsrDupB": (["aaaaaaaa-0000-0000-0000-000000000007"], node("aaaaaaaa-0000-0000-0000-000000000007", "UsrDupB", "Shared title"), 0),
        "UsrRecords": (["aaaaaaaa-0000-0000-0000-000000000008"],
                       node("aaaaaaaa-0000-0000-0000-000000000008", "UsrRecords", "Records", records=True,
                            rules=[rule(SALES, SUPPORT, 1, 1, 0), rule(SALES, SUPPORT, 2, 0, 0)]), 5),
        "UsrBadLevel": (["aaaaaaaa-0000-0000-0000-000000000009"],
                        node("aaaaaaaa-0000-0000-0000-000000000009", "UsrBadLevel", "Bad level", records=True,
                             rules=[rule(SALES, SUPPORT, 7, 0, 0)]), 0),
    }
    for index, name in enumerate(["UsrSaveFails", "UsrSaveLies", "UsrSaveError", "UsrApplyRefused", "UsrApplyQueued",
                                  "UsrApplySlow", "UsrApplyGarbage"]):
        uid = f"dddddddd-0000-0000-0000-{index + 1:012d}"
        found[name] = ([uid], node(uid, name, name, operations=True, records=True, rows=[row(ALL_EMPLOYEES, 0)],
                                   rules=[rule(ALL_EMPLOYEES, SALES)]), 2)
    return found


class State:
    def __init__(self):
        self.objects = objects()
        self.slow_started = False
        self.addons = {ACCOUNT: json.dumps({"items": [{"UId": COLUMNS["PrimaryContact"], "ColumnUId": COLUMNS["PrimaryContact"],
                                                         "Position": 0}], "removed": [], "caption": "kept"}),
                       CONTACT: "", LEAD: json.dumps({"items": [], "removed": [{"UId": COLUMNS["Owner"], "ColumnUId": COLUMNS["Owner"]}],
                                                      "Removed2": 1})}
        self.tables = {ACCOUNT: [COLUMNS["Owner"]], CONTACT: [COLUMNS["Owner"], COLUMNS["Type"]],
                       LEAD: [COLUMNS["Owner"], STRANGER]}

    def by_uid(self, uid):
        for name, (uids, node, count) in self.objects.items():
            if uid in uids:
                return name, uids.index(uid), node
        return None, -1, None

    def administrated(self, node):
        answer = copy.deepcopy(node)
        if not answer["administratedByOperations"] and not answer["entitySchemaOperationsRights"]:
            answer["entitySchemaOperationsRights"] = [row(ALL_EMPLOYEES, 0)]
        return answer

    def save(self, payload):
        name, _, node = self.by_uid(payload.get("uId"))
        if node is None:
            return {"success": False, "errorInfo": {"message": "Unknown object"}}
        if name == "UsrSaveFails":
            return {"success": False, "errorInfo": {"message": "Save denied:\nno rights"}}
        if name == "UsrSaveLies":
            return {"success": True}
        answer = {"success": True} if name != "UsrSaveError" else {"success": False, "errorInfo": {"message": ""}}
        node["administratedByOperations"] = payload.get("administratedByOperations", node["administratedByOperations"])
        node["administratedByRecords"] = payload.get("administratedByRecords", node["administratedByRecords"])
        if payload.get("entitySchemaOperationsRights") is not None:
            node["entitySchemaOperationsRights"] = [self.named(item, "sysAdminUnit") for item in payload["entitySchemaOperationsRights"]]
        if payload.get("entitySchemaRecordDefRights") is not None:
            node["entitySchemaRecordDefRights"] = [self.named(self.named(item, "authorSysAdminUnit"), "granteeSysAdminUnit")
                                                   for item in payload["entitySchemaRecordDefRights"]]
        return answer

    @staticmethod
    def named(item, key):
        item = copy.deepcopy(item)
        unit = item.get(key) or {}
        unit["name"] = UNITS.get(unit.get("id"), unit.get("name"))
        item[key] = unit
        return item

    def connections(self, entity):
        if entity == LEAD:
            return list(self.tables[LEAD])
        meta = json.loads(self.addons[entity] or "{}")
        removed = {entry["ColumnUId"] for entry in meta.get("removed", [])}
        items = [entry["ColumnUId"] for entry in sorted(meta.get("items", []), key=lambda e: e.get("Position", 0))]
        merged = [uid for uid in items if uid not in removed]
        merged += [uid for uid in self.tables[entity] if uid not in removed and uid not in merged]
        return merged


def filters_of(body):
    values = {}
    for item in (body.get("filters") or {}).get("items", {}).values():
        column = item.get("leftExpression", {}).get("columnPath")
        values[column] = item.get("rightExpression", {}).get("parameter", {}).get("value")
    return values


def select(state, body):
    root = body.get("rootSchemaName")
    values = filters_of(body)
    columns = (body.get("columns") or {}).get("items", {})
    if root == "SysSchema" and "ExtendParent" in columns:
        entry = state.objects.get(values.get("Name"))
        return {"success": True, "rows": [{"ExtendParent": i > 0, "UId": uid} for i, uid in enumerate(entry[0])] if entry else []}
    if root == "SysSchema" and "Caption" in columns:
        rows = [{"Name": name, "Caption": node["caption"]} for name, (_, node, _) in sorted(state.objects.items())
                if node and node["caption"] == values.get("Caption")]
        return {"success": True, "rows": rows}
    if root == "SysAdminUnit":
        name = UNITS.get(values.get("Id"))
        return {"success": True, "rows": [{"Name": name}] if name else []}
    if root == "SysProcessLog" and "Status" in columns and "Id" in values:
        status = (RUNNING, "Running") if values["Id"] == SLOW_PROCESS else (COMPLETED, "Completed")
        return {"success": True, "rows": [{"Status": {"value": status[0], "displayValue": status[1]}}]}
    if root == "SysProcessLog":
        return {"success": True, "rows": [{"Id": SLOW_PROCESS, "StartDate": "2026-10-20T10:00:00"}] if state.slow_started else []}
    if root == "SysPackage":
        return {"success": True, "rows": [{"UId": PACKAGE}] if values.get("Name") == "Custom" else []}
    if root == "EntityConnection":
        table = state.tables.get(values.get("SysEntitySchemaUId"), [])
        return {"success": True, "rows": [{"ColumnUId": uid, "Position": i, "Id": f"{i}"} for i, uid in enumerate(table)]}
    if root in state.objects and "Count" in columns:
        return {"success": True, "rows": [{"Count": state.objects[root][2]}]}
    return {"success": False, "errorInfo": {"message": f"Unexpected query on {root}"}}


def design_item(body):
    entity = ENTITIES.get(body.get("name"))
    if entity is None:
        return {"success": True, "schema": None}
    def column(name, data_type, reference=None, caption=None):
        value = {"uId": COLUMNS[name], "name": name, "type": data_type, "caption": [{"cultureName": "en-US", "value": caption or name}]}
        if reference:
            value["referenceSchema"] = {"name": reference}
        return value
    return {"success": True, "schema": {"uId": entity, "name": body.get("name"), "parentSchema": {"uId": "88888888-8888-8888-8888-888888888888"},
                                         "columns": [column("Owner", 10, "Contact", "Owner"), column("Name", 1)],
                                         "inheritedColumns": [column("PrimaryContact", 10, "Contact", "Primary contact"),
                                                              column("Type", 10, "AccountType")]}}


def answer(state, path, body):
    if path.endswith("/RightManagementService.svc/GetAdministratedObject"):
        uid = body.get("schemaUId")
        name, index, node = state.by_uid(uid)
        if node is not None and node["uId"] == uid:
            return 200, {"success": True, "administratedObject": state.administrated(node)}
        if name == "UsrBroken" and index == 0:
            return 200, {"success": False, "errorInfo": {"message": "Access denied to\nthe object"}}
        # A replacing layer's UId (or an unknown one) answers with the platform's error page.
        return 400, "<html><body>Request Error</body></html>"
    if path.endswith("/RightManagementService.svc/SaveAdministratedObject"):
        return 200, state.save(body.get("administratedObject") or {})
    if path.endswith("/DataService/json/SyncReply/SelectQuery"):
        return 200, select(state, body)
    if path.endswith("/DataService/json/SyncReply/RuntimeEntitySchemaRequest"):
        if body.get("Name") != "UsrInvoice":
            return 200, {"success": False, "errorInfo": {"message": f"Schema {body.get('Name')} not found"}}
        def column(uid, name, reference, inherited=False):
            return {"uId": uid, "name": name, "dataValueType": 10, "isInherited": inherited, "referenceSchemaName": reference,
                    "caption": name}
        items = {"c1": column("cccccccc-0000-0000-0000-000000000001", "Order", "UsrOrder"),
                 "c2": column("cccccccc-0000-0000-0000-000000000002", "Owner", "SysAdminUnit"),
                 "c3": column("cccccccc-0000-0000-0000-000000000003", "Broken", "UsrBroken"),
                 "c4": column("cccccccc-0000-0000-0000-000000000004", "CreatedBy", "Contact", inherited=True),
                 "c5": column("cccccccc-0000-0000-0000-000000000005", "Again", "usrorder"),
                 "c6": column("cccccccc-0000-0000-0000-000000000006", "Self", "UsrInvoice")}
        return 200, {"success": True, "schema": {"uId": "aaaaaaaa-0000-0000-0000-000000000002", "name": "UsrInvoice",
                                                  "caption": "Invoices", "columns": {"items": items},
                                                  "primaryColumnUId": "cccccccc-0000-0000-0000-000000000001"}}
    if path.endswith("/ProcessEngineService.svc/RunProcess"):
        uid = ((body.get("parameterValues") or [{}])[0]).get("value")
        name, _, _ = state.by_uid(uid)
        if name == "UsrApplyRefused":
            return 200, {"processId": "00000000-0000-0000-0000-000000000000", "processStatus": 0, "success": False,
                         "errorInfo": {"errorCode": "SecurityException", "message": "No rights to run it"}}
        if name == "UsrApplyQueued":
            return 200, {"processId": "00000000-0000-0000-0000-000000000000", "processStatus": 0, "success": True}
        if name == "UsrApplyGarbage":
            return 502, "<html>Bad gateway</html>"
        if name == "UsrApplySlow":
            state.slow_started = True
            return 200, {"processId": SLOW_PROCESS, "processStatus": 1, "resultParameterValues": None, "success": True}
        return 200, {"processId": PROCESS, "processStatus": 1, "resultParameterValues": None, "success": True}
    if path.endswith("/EntitySchemaDesignerService.svc/GetSchemaDesignItem"):
        return 200, design_item(body)
    if path.endswith("/AddonSchemaDesignerService.svc/GetSchema"):
        entity = body.get("targetSchemaUId")
        return 200, {"success": True, "schema": {"uId": "99999999-9999-9999-9999-999999999999", "name": "EntityConnections",
                                                  "targetSchemaUId": entity, "metaData": state.addons.get(entity, ""), "resources": []}}
    if path.endswith("/AddonSchemaDesignerService.svc/SaveSchema"):
        state.addons[body.get("targetSchemaUId")] = body.get("metaData") or ""
        return 200, {"success": True, "value": True}
    if path.endswith("/rest/EntityConnectionsService/GetConnections"):
        entity = body.get("entitySchemaUId")
        if entity == CONTACT:
            return 404, "<html><body>Not found</body></html>"
        return 200, {"success": True, "connections": [{"columnUId": uid} for uid in state.connections(entity)]}
    return 200, {"success": True}


def serve(port, directory):
    directory = pathlib.Path(directory)
    directory.mkdir(parents=True, exist_ok=True)
    settings = {"Environments": {"t19fake": {"Uri": f"http://127.0.0.1:{port}", "Login": "example-user",
                                             "Password": "replace-me", "IsNetCore": False, "Safe": False}},
                "ActiveEnvironmentKey": "t19fake"}
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
                status, payload = answer(states[side], path, body if isinstance(body, dict) else {})
            self.reply(status, payload)

        do_GET = handle_any
        do_POST = handle_any

    ThreadingHTTPServer(("127.0.0.1", port), Handler).serve_forever()


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
