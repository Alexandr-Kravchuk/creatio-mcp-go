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
USR_PKG = "11111111-0000-0000-0000-000000000004"
CRT_OTHER = "11111111-0000-0000-0000-000000000005"
USR_LOCKED = "11111111-0000-0000-0000-000000000006"
APP_ID = "22222222-0000-0000-0000-000000000001"

# place-section-in-workplace fixtures
WEB = "195785b4-f55a-4e72-ace3-6480b54c8fa5"
MOBILE = "be8ab9ee-344e-4a56-8c97-41c84f22bd88"
ALL_EMPLOYEES = "a29a3ba5-4b0d-de11-9a51-005056c00008"
SALES_ROLE = "33333333-0000-0000-0000-000000000001"
DUP_ROLE_1 = "33333333-0000-0000-0000-000000000002"
DUP_ROLE_2 = "33333333-0000-0000-0000-000000000003"
WP_TYPE = "44444444-0000-0000-0000-000000000001"
WP_LOADER = "44444444-0000-0000-0000-000000000002"
MY_APPS = "55555555-0000-0000-0000-000000000001"
SALES_WP = "55555555-0000-0000-0000-000000000002"
FOREIGN_WP = "55555555-0000-0000-0000-000000000003"
MOBILE_MY_APPS = "55555555-0000-0000-0000-000000000004"
M_ORDERS = "66666666-0000-0000-0000-000000000001"
M_DUP_PLACED = "66666666-0000-0000-0000-000000000002"
M_DUP_FREE = "66666666-0000-0000-0000-000000000003"
M_LONELY = "66666666-0000-0000-0000-000000000004"
M_FOREIGN = "66666666-0000-0000-0000-000000000005"
M_TWICE = "66666666-0000-0000-0000-000000000006"
P_ORDERS = "77777777-0000-0000-0000-000000000001"
P_DUP = "77777777-0000-0000-0000-000000000002"
P_FOREIGN = "77777777-0000-0000-0000-000000000005"
P_TWICE_1 = "77777777-0000-0000-0000-000000000006"
P_TWICE_2 = "77777777-0000-0000-0000-000000000007"
G_ALL = "88888888-0000-0000-0000-000000000001"
B_FOREIGN_WP = "99999999-0000-0000-0000-000000000001"
B_FOREIGN_P = "99999999-0000-0000-0000-000000000002"
ID_COLUMN = "ae0e45ca-c495-4fe7-a39d-3ab7278e1617"
LOOKUPS = {"SysApplicationClientType", "Type", "SysWorkplace", "SysModule", "SysAdminUnit"}
SCHEMAS = {
    "SysWorkplace": [("Id", ID_COLUMN, 0), ("Name", "a0000000-0000-0000-0000-000000000001", 1),
                     ("Position", "a0000000-0000-0000-0000-000000000002", 4),
                     ("SysApplicationClientType", "a0000000-0000-0000-0000-000000000003", 10),
                     ("Type", "a0000000-0000-0000-0000-000000000004", 10), ("LoaderId", "a0000000-0000-0000-0000-000000000005", 0)],
    "SysModuleInWorkplace": [("Id", ID_COLUMN, 0), ("SysWorkplace", "a0000000-0000-0000-0000-000000000011", 10),
                             ("SysModule", "a0000000-0000-0000-0000-000000000012", 10),
                             ("Position", "a0000000-0000-0000-0000-000000000013", 4)],
    "SysAdminUnitInWorkplace": [("Id", ID_COLUMN, 0), ("SysWorkplace", "a0000000-0000-0000-0000-000000000021", 10),
                                ("SysAdminUnit", "a0000000-0000-0000-0000-000000000022", 10)],
}
SCHEMA_UIDS = {"SysWorkplace": "b0000000-0000-0000-0000-000000000001", "SysModuleInWorkplace": "b0000000-0000-0000-0000-000000000002",
               "SysAdminUnitInWorkplace": "b0000000-0000-0000-0000-000000000003"}


def package(uid, name, description="", maintainer="Creatio", install_type=1, deps=None):
    return {"uId": uid, "name": name, "description": description, "maintainer": maintainer,
            "installType": install_type, "version": "8.0.0", "type": 0, "dependsOnPackages": deps or []}


def workplace(uid, name, client, created, position=1):
    return {"Id": uid, "Name": name, "Position": position, "SysApplicationClientType.Id": client, "Type.Id": WP_TYPE,
            "LoaderId": WP_LOADER, "CreatedOn": created}


def binding(uid, name, package_uid, schema, ids):
    return {"UId": uid, "Name": name, "SysPackage.UId": package_uid, "SysSchema.Name": schema, "ids": list(ids)}


class Tables:
    """A tiny table store answering the SelectQuery, InsertQuery and UpdateQuery shapes the workplace tools send."""

    def __init__(self, packages):
        self.packages = packages
        self.clock = 0
        self.tables = {
            "SysApplicationClientType": [{"Id": WEB, "Name": "Web"}, {"Id": MOBILE, "Name": "Mobile"}],
            "SysAdminUnit": [{"Id": ALL_EMPLOYEES, "Name": "All employees"}, {"Id": SALES_ROLE, "Name": "Sales"},
                             {"Id": DUP_ROLE_1, "Name": "Dup"}, {"Id": DUP_ROLE_2, "Name": "Dup"}],
            "SysModule": [{"Id": M_ORDERS, "Code": "UsrOrders"}, {"Id": M_DUP_PLACED, "Code": "UsrDup"},
                          {"Id": M_DUP_FREE, "Code": "UsrDup"}, {"Id": M_LONELY, "Code": "UsrLonely"},
                          {"Id": M_FOREIGN, "Code": "UsrForeign"}, {"Id": M_TWICE, "Code": "UsrTwice"}],
            "SysWorkplace": [workplace(MY_APPS, "My applications", WEB, 1), workplace(SALES_WP, "Sales", WEB, 2, 2),
                             workplace(FOREIGN_WP, "Foreign WP", WEB, 3, 3),
                             workplace(MOBILE_MY_APPS, "My applications", MOBILE, 4)],
            "SysModuleInWorkplace": [
                {"Id": P_ORDERS, "SysWorkplace.Id": MY_APPS, "SysModule.Id": M_ORDERS},
                {"Id": P_DUP, "SysWorkplace.Id": MY_APPS, "SysModule.Id": M_DUP_PLACED},
                {"Id": P_FOREIGN, "SysWorkplace.Id": MY_APPS, "SysModule.Id": M_FOREIGN},
                {"Id": P_TWICE_1, "SysWorkplace.Id": MY_APPS, "SysModule.Id": M_TWICE},
                {"Id": P_TWICE_2, "SysWorkplace.Id": MY_APPS, "SysModule.Id": M_TWICE}],
            "SysAdminUnitInWorkplace": [{"Id": G_ALL, "SysWorkplace.Id": MY_APPS, "SysAdminUnit.Id": ALL_EMPLOYEES}],
            "SysPackageSchemaData": [binding(B_FOREIGN_WP, "SysWorkplace_ForeignWP", CRT_OTHER, "SysWorkplace", [FOREIGN_WP]),
                                     binding(B_FOREIGN_P, "SysModuleInWorkplace_UsrForeign", CRT_OTHER, "SysModuleInWorkplace",
                                             [P_FOREIGN])],
        }

    def owns(self, root):
        return root in self.tables

    def row_by_id(self, table, uid):
        return next((r for r in self.tables.get(table, []) if str(r.get("Id", "")).lower() == str(uid).lower()), None)

    def resolve(self, root, row, path):
        if root == "SysPackageSchemaData":
            if path == "SysPackage.Name":
                pkg = self.packages.get(row["SysPackage.UId"])
                return pkg["name"] if pkg else None
            if path == "Data":
                return json.dumps({"PackageData": [{"Row": [{"SchemaColumnUId": ID_COLUMN, "Value": i}]} for i in row["ids"]]})
        if path in row:
            return row[path]
        if "." in path:
            head, tail = path.split(".", 1)
            target = self.row_by_id(head, row.get(head + ".Id"))
            return target.get(tail) if target else None
        return None

    def select(self, body):
        root = body.get("rootSchemaName")
        columns = (body.get("columns") or {}).get("items", {})
        rows = []
        for row in self.tables[root]:
            ok = True
            for item in (body.get("filters") or {}).get("items", {}).values():
                path = item.get("leftExpression", {}).get("columnPath")
                value = item.get("rightExpression", {}).get("parameter", {}).get("value")
                if str(self.resolve(root, row, path) or "").lower() != str(value or "").lower():
                    ok = False
            if ok:
                rows.append(row)
        order = [c["expression"]["columnPath"] for c in columns.values()
                 if c.get("orderPosition", -1) is not None and c.get("orderPosition", -1) >= 0]
        for path in order:
            rows = sorted(rows, key=lambda r: self.resolve(root, r, path) or 0)
        return {"success": True, "rows": [{alias: self.resolve(root, r, c["expression"]["columnPath"]) for alias, c in columns.items()}
                                          for r in rows]}

    @staticmethod
    def values_of(body):
        result = {}
        for name, item in ((body.get("columnValues") or {}).get("items") or {}).items():
            value = (item.get("parameter") or {}).get("value")
            result[name + ".Id" if name in LOOKUPS else name] = value
        return result

    def insert(self, body):
        root = body.get("rootSchemaName")
        values = self.values_of(body)
        if self.row_by_id(root, values.get("Id")):
            return {"success": False, "errorInfo": {"message": "Duplicate key"}}
        if root == "SysWorkplace":
            self.clock += 1
            row = {"Position": len(self.tables[root]) + 1, "Type.Id": WP_TYPE, "LoaderId": WP_LOADER, "CreatedOn": 100 + self.clock}
            row.update(values)
            row["Position"] = int(row["Position"])
            values = row
        self.tables[root].append(values)
        return {"success": True, "rowsAffected": 1}

    def update(self, body):
        root = body.get("rootSchemaName")
        items = ((body.get("filters") or {}).get("items") or {})
        uid = next(iter(items.values()), {}).get("rightExpression", {}).get("parameter", {}).get("value")
        row = self.row_by_id(root, uid)
        if row is None:
            return {"success": True, "rowsAffected": 0}
        for key, value in self.values_of(body).items():
            row[key] = int(value) if key == "Position" else value
        return {"success": True, "rowsAffected": 1}

    def runtime_schema(self, name):
        columns = SCHEMAS.get(name)
        if columns is None:
            return {"success": False, "errorInfo": {"message": f"Schema {name} not found"}}
        return {"success": True, "schema": {"uId": SCHEMA_UIDS[name], "name": name, "primaryColumnUId": ID_COLUMN,
                                            "columns": {"items": {uid: {"uId": uid, "name": col, "dataValueType": t}
                                                                  for col, uid, t in columns}}}}

    def save_binding(self, body):
        name = body.get("name", "")
        if "FailHub" in name and body.get("entitySchemaName") == "SysAdminUnitInWorkplace":
            return {"success": False, "errorInfo": {"message": "Binding save denied"}}
        rows = self.tables["SysPackageSchemaData"]
        existing = next((r for r in rows if r["UId"] == body.get("uId")), None)
        ids = list(body.get("boundRecordIds") or [])
        if existing:
            existing["ids"] = ids
        else:
            rows.append(binding(body.get("uId"), name, (body.get("package") or {}).get("uId"), body.get("entitySchemaName"), ids))
        return {"success": True}

    def bound_data(self, body):
        found = next((r for r in self.tables["SysPackageSchemaData"] if r["UId"] == body.get("uId")), None)
        items = [{"Id": i} for i in (found["ids"] if found else [])]
        return {"success": True, "items": items}


PAGE_TEMPLATE = "c0000000-0000-0000-0000-000000000001"
PAGE_OWN = "c0000000-0000-0000-0000-000000000002"
PAGE_FOREIGN = "c0000000-0000-0000-0000-000000000003"
PAGE_REPLACED_BASE = "c0000000-0000-0000-0000-000000000004"
PAGE_REPLACED = "c0000000-0000-0000-0000-000000000005"
PAGE_SAVE_FAILS = "c0000000-0000-0000-0000-000000000006"
PAGE_SAVE_LIES = "c0000000-0000-0000-0000-000000000007"
PAGE_INHERITED = "c0000000-0000-0000-0000-000000000008"
PAGE_SAVE_DROPS = "c0000000-0000-0000-0000-000000000009"
PAGE_NO_STRINGS = "c0000000-0000-0000-0000-00000000000a"
PAGE_BROKEN = "c0000000-0000-0000-0000-00000000000b"
BODY = ('define("UsrPage_FormPage", [], function() { return { viewConfigDiff: /**SCHEMA_VIEW_CONFIG_DIFF*/[{"operation": "insert", '
        '"name": "UsrBound", "values": {"caption": "$Resources.Strings.UsrBound"}}]/**SCHEMA_VIEW_CONFIG_DIFF*/, '
        'viewModelConfigDiff: /**SCHEMA_VIEW_MODEL_CONFIG_DIFF*/[{"operation": "merge", "path": [], "values": {"attributes": '
        '{"UsrBound": {"modelConfig": {"path": "PDS.UsrBound"}}}}}]/**SCHEMA_VIEW_MODEL_CONFIG_DIFF*/ }; });')


def culture_values(**values):
    return [{"cultureName": name.replace("_", "-"), "value": value} for name, value in values.items()]


def strings(*entries):
    return [{"uId": f"d0000000-0000-0000-0000-{index:012d}", "name": name, "values": culture_values(**values)}
            for index, (name, values) in enumerate(entries, start=1)]


def page(uid, name, package_uid, package_name, localizable, caption, parent_caption=None, body=BODY):
    return {"uId": uid, "name": name, "package": {"uId": package_uid, "name": package_name}, "body": body,
            "localizableStrings": localizable, "caption": caption,
            "parent": {"uId": PAGE_TEMPLATE, "caption": parent_caption or culture_values(en_US="Blank page", es_ES="Página en blanco")},
            "schemaType": 9, "extendParent": False}


class Pages:
    """ClientUnitSchemaDesignerService and SysSchema for localize-page: GetSchema answers what SaveSchema stored."""

    def __init__(self):
        own_strings = strings(("UsrTitle_caption", {"en_US": "Title"}), ("UsrName_caption", {"en_US": "Name", "es_ES": "Nombre"}),
                              ("UsrSame", {"en_US": "OK", "es_ES": "OK"}), ("UsrEmpty", {"en_US": "Empty", "es_ES": ""}))
        template = {"uId": PAGE_TEMPLATE, "name": "PageWithTabsFreedomTemplate", "package": {"uId": CRT_UI, "name": "CrtUIv2"},
                    "localizableStrings": strings(("ParentKey", {"en_US": "Parent", "es_ES": "Padre"}),
                                                  ("UsrTitle_caption", {"en_US": "Template title", "de_DE": "Vorlage"})),
                    "caption": culture_values(en_US="Blank page", es_ES="Página en blanco")}
        self.template = template
        self.schemas = {
            PAGE_OWN: page(PAGE_OWN, "UsrPage_FormPage", USR_PKG, "UsrPkg", own_strings, culture_values(en_US="My page")),
            PAGE_FOREIGN: page(PAGE_FOREIGN, "UsrForeign_FormPage", CRT_OTHER, "CrtOther", strings(("A", {"en_US": "A"})),
                               culture_values(en_US="Foreign")),
            PAGE_REPLACED_BASE: page(PAGE_REPLACED_BASE, "UsrReplaced_FormPage", CRT_OTHER, "CrtOther", strings(("A", {"en_US": "A"})),
                                     culture_values(en_US="Base")),
            PAGE_REPLACED: page(PAGE_REPLACED, "UsrReplaced_FormPage", USR_PKG, "UsrPkg", strings(("A", {"en_US": "A"})),
                                culture_values(en_US="Replaced")),
            PAGE_SAVE_FAILS: page(PAGE_SAVE_FAILS, "UsrSaveFails_FormPage", USR_PKG, "UsrPkg", strings(("A", {"en_US": "A"})),
                                  culture_values(en_US="Fails")),
            PAGE_SAVE_LIES: page(PAGE_SAVE_LIES, "UsrSaveLies_FormPage", USR_PKG, "UsrPkg", strings(("A", {"en_US": "A"})),
                                 culture_values(en_US="Lies")),
            PAGE_SAVE_DROPS: page(PAGE_SAVE_DROPS, "UsrSaveDrops_FormPage", USR_PKG, "UsrPkg", strings(("A", {"en_US": "A"})),
                                  culture_values(en_US="Drops")),
            PAGE_INHERITED: page(PAGE_INHERITED, "UsrInherited_FormPage", USR_PKG, "UsrPkg", strings(("A", {"en_US": "A"})),
                                 culture_values(en_US="Renamed page", es_ES="Página en blanco")),
            PAGE_NO_STRINGS: {"uId": PAGE_NO_STRINGS, "name": "UsrNoStrings_FormPage", "package": {"uId": USR_PKG, "name": "UsrPkg"},
                              "body": "", "schemaType": 9},
            PAGE_BROKEN: page(PAGE_BROKEN, "UsrBroken_FormPage", USR_PKG, "UsrPkg", strings(("A", {"en_US": "A"})),
                              culture_values(en_US="Broken")),
        }
        self.checksums = {uid: f"checksum-{uid[-2:]}-0" for uid in self.schemas}
        self.design = {PAGE_OWN: USR_PKG, PAGE_FOREIGN: USR_PKG, PAGE_REPLACED_BASE: USR_PKG, PAGE_SAVE_FAILS: USR_PKG,
                       PAGE_SAVE_LIES: USR_PKG, PAGE_INHERITED: USR_PKG, PAGE_SAVE_DROPS: USR_PKG, PAGE_NO_STRINGS: USR_PKG,
                       PAGE_BROKEN: USR_PKG}

    def base_uid(self, name):
        """SysSchema by name: the base schema (the first one created under that name)."""
        return next((uid for uid, schema in self.schemas.items() if schema["name"] == name), None)

    def select(self, body):
        values = filters_of(body)
        columns = (body.get("columns") or {}).get("items", {})
        if "SysPackage.UId" in values:
            uid = next((u for u, s in self.schemas.items() if s["name"] == values.get("Name")
                        and s["package"]["uId"] == str(values["SysPackage.UId"]).lower()), None)
            return {"success": True, "rows": [{"UId": uid}] if uid else []}
        if "UId" in values:
            uid = str(values["UId"]).lower()
            if uid not in self.schemas:
                return {"success": True, "rows": []}
            row = {"UId": uid, "Checksum": self.checksums[uid], "ModifiedOn": "2026-10-20T10:00:00"}
            return {"success": True, "rows": [{alias: row.get(alias) for alias in columns}]}
        uid = self.base_uid(values.get("Name"))
        return {"success": True, "rows": [{"UId": uid}] if uid else []}

    def design_package(self, body):
        uid = body.get("schemaUId")
        if uid not in self.design:
            return {"success": False, "errorInfo": {"message": "Unknown schema"}}
        return {"success": True, "uId": self.design[uid]}

    def parents(self, body):
        uid = body.get("schemaUId")
        if uid == PAGE_BROKEN:
            return {"success": False, "errorInfo": {"message": "Hierarchy denied"}}
        head = self.schemas[uid]
        if uid == PAGE_REPLACED_BASE:
            head = self.schemas[PAGE_REPLACED_BASE]
        values = [copy.deepcopy(head), copy.deepcopy(self.template)]
        return {"success": True, "values": values}

    def get(self, body):
        uid = body.get("schemaUId")
        if uid not in self.schemas:
            return {"success": False, "errorInfo": {"message": f"Schema {uid} not found"}}
        return {"success": True, "schema": copy.deepcopy(self.schemas[uid])}

    def save(self, body):
        uid = body.get("uId")
        if uid == PAGE_SAVE_FAILS:
            return {"success": False, "errorInfo": {"message": "Save denied: no rights"}}
        if uid == PAGE_SAVE_DROPS:
            return None
        if uid != PAGE_SAVE_LIES and uid in self.schemas:
            self.schemas[uid] = body
            number = int(self.checksums[uid].rsplit("-", 1)[1]) + 1
            self.checksums[uid] = f"{self.checksums[uid].rsplit('-', 1)[0]}-{number}"
        return {"success": True}


CULTURES = [{"Name": "en-US", "Active": True}, {"Name": "es-ES", "Active": True}, {"Name": "de-DE", "Active": False}]


class State:
    def __init__(self):
        self.pages = Pages()
        self.packages = {CRT_BASE: package(CRT_BASE, "CrtBase"), CRT_UI: package(CRT_UI, "CrtUIv2"),
                         USR_EXISTING: package(USR_EXISTING, "UsrExisting", maintainer="Customer", install_type=0),
                         USR_PKG: package(USR_PKG, "UsrPkg", install_type=0), CRT_OTHER: package(CRT_OTHER, "CrtOther"),
                         USR_LOCKED: package(USR_LOCKED, "UsrLocked")}
        self.app_packages = {}
        self.tables = Tables(self.packages)

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
    if state.tables.owns(root):
        return state.tables.select(body)
    if root == "SysCulture":
        return {"success": True, "rows": copy.deepcopy(CULTURES)}
    if root == "SysSchema":
        return state.pages.select(body)
    if root == "SysPackage":
        rows = [p for p in state.packages.values() if "UId" not in values or p["uId"] == str(values["UId"]).lower()]
        return {"success": True, "rows": [{"Name": p["name"], "UId": p["uId"], "Maintainer": p["maintainer"],
                                           "Version": p["version"], "InstallType": p.get("installType")} for p in rows]}
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
        settings = {"SchemaNamePrefix": "Usr", "CurrentPackageId": USR_PKG}
        return 200, {"success": True, "values": {name: {"dataValueType": 1, "value": settings[name]} for name in names
                                                 if name in settings}}
    if path.endswith("/DataService/json/SyncReply/SelectQuery"):
        return 200, select(state, body)
    if path.endswith("/DataService/json/SyncReply/InsertQuery"):
        return 200, state.tables.insert(body)
    if path.endswith("/DataService/json/SyncReply/UpdateQuery"):
        return 200, state.tables.update(body)
    if path.endswith("/DataService/json/SyncReply/RuntimeEntitySchemaRequest"):
        return 200, state.tables.runtime_schema(body.get("Name") or body.get("name"))
    if path.endswith("/SchemaDataDesignerService.svc/SaveSchema"):
        return 200, state.tables.save_binding(body)
    if path.endswith("/SchemaDataDesignerService.svc/GetBoundSchemaData"):
        return 200, state.tables.bound_data(body)
    if path.endswith("/ApplicationPackagesService.svc/GetDesignPackageUId"):
        return 200, state.pages.design_package(body)
    if path.endswith("/ClientUnitSchemaDesignerService.svc/GetParentSchemas"):
        return 200, state.pages.parents(body)
    if path.endswith("/ClientUnitSchemaDesignerService.svc/GetSchema"):
        return 200, state.pages.get(body)
    if path.endswith("/ClientUnitSchemaDesignerService.svc/SaveSchema"):
        saved = state.pages.save(body)
        return (200, saved) if saved is not None else (0, None)
    if path.endswith("/rest/WorkplaceService/ResetScriptCache"):
        return 200, {"success": True}
    if path.endswith("/WorkspaceExplorerService.svc/GetIsFileDesignMode"):
        return 200, {"success": True, "value": False}
    if path.endswith("/rest/ConfigurationDataService/GetData"):
        return 200, {"success": True}
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
                    drop = status == 0
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
KNOWN_GUIDS = {value for value in list(globals().values()) if isinstance(value, str) and GUID.fullmatch(value)}
KNOWN_GUIDS |= {uid for columns in SCHEMAS.values() for _, uid, _ in columns} | set(SCHEMA_UIDS.values())


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
