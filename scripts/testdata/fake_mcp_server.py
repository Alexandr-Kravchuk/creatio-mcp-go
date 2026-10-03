#!/usr/bin/env python3
"""A tiny stdio MCP server for test_compare_mcp_writes.py: just enough of clio's or this server's
sys-setting and OData tools, backed by one JSON state file both fakes share (one fake "stand").

--flavor clio expects clio's wrapping ({"args": {..., "environment-name": ...}}) and dashed keys;
--flavor go expects flat arguments and answers with camelCase keys, like this server would.
--fault fail-create | mangle-read injects the failure a test needs.
"""
import argparse, json, os, pathlib, sys, uuid


def load(path):
    return json.loads(path.read_text(encoding="utf-8")) if path.is_file() else {}


def save(path, state):
    path.write_text(json.dumps(state), encoding="utf-8")


def handle(tool, args, options):
    state_path = pathlib.Path(options.state)
    state = load(state_path)
    calls = state.setdefault("_calls", [])
    calls.append(f"{options.flavor}:{tool}")
    settings = state.setdefault("settings", {})
    type_key = "value-type-name" if options.flavor == "clio" else "valueTypeName"
    try:
        if tool == "create-sys-setting":
            if "fail-create" in options.fault:
                return {"success": False, "error": "create refused by the fake"}
            code = args["code"]
            if code in settings:
                return {"success": False, "error": "already exists"}
            settings[code] = {"id": str(uuid.uuid4()), "type": args["value-type-name"], "value": args.get("value", "")}
            return {"success": True, "code": code, type_key: args["value-type-name"], "value": args.get("value", "")}
        if tool == "get-sys-setting":
            item = settings.get(args["code"])
            value = item["value"] if item else ""
            if "mangle-read" in options.fault:
                value += "-changed"
            return {"success": True, "code": args["code"], "value": value}
        if tool == "odata-read":
            condition = args["filters"]["all"][0]
            rows = [{"Id": item["id"]} for code, item in settings.items() if code == condition["value"]]
            return {"success": True, "count": len(rows), "value": rows}
        if tool == "odata-delete":
            if args.get("confirm") is not True:
                return {"success": False, "error": "confirm=true is required"}
            for code, item in list(settings.items()):
                if item["id"] == args["id"]:
                    del settings[code]
                    return {"success": True, "id": args["id"]}
            return {"success": False, "error": "not found"}
        return {"success": False, "error": f"unknown tool {tool}"}
    finally:
        save(state_path, state)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--flavor", choices=("clio", "go"), required=True)
    parser.add_argument("--state", required=True)
    parser.add_argument("--fault", action="append", default=[])
    options = parser.parse_args()
    for line in sys.stdin:
        if not line.strip():
            continue
        message = json.loads(line)
        if "id" not in message:
            continue
        if message["method"] == "initialize":
            result = {"protocolVersion": "2025-06-18", "capabilities": {"tools": {}},
                      "serverInfo": {"name": f"fake-{options.flavor}", "version": "1"}}
        elif message["method"] == "tools/call":
            params = message["params"]
            args = params.get("arguments", {})
            if options.flavor == "clio":
                args = args.get("args", {})
                if not args.pop("environment-name", None):
                    answer = {"success": False, "error": "environment-name is required"}
                else:
                    answer = handle(params["name"], args, options)
            else:
                # Like this server: CREATIO_* names one environment and calls carry no name; without them every
                # call names its environment.
                named = args.pop("environment-name", None)
                if bool(named) == bool(os.environ.get("CREATIO_URL")):
                    answer = {"success": False, "error": "environment-name does not match the server's environment mode"}
                else:
                    answer = handle(params["name"], args, options)
            result = {"content": [{"type": "text", "text": json.dumps(answer)}], "isError": not answer["success"]}
        else:
            result = {}
        sys.stdout.write(json.dumps({"jsonrpc": "2.0", "id": message["id"], "result": result}) + "\n")
        sys.stdout.flush()


if __name__ == "__main__":
    main()
