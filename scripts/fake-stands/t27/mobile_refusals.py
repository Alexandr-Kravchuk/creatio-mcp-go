#!/usr/bin/env python3
"""Captures get-mobile-page-conversion-guide's refusal paths (no stand needed) from an MCP server as RAW result text.

    python3 scripts/fake-stands/t27/mobile_refusals.py <out-dir> -- dotnet <clio.dll> mcp-server
    python3 scripts/fake-stands/t27/mobile_refusals.py <out-dir> -- <creatio-mcp-go binary>

The server runs with its own temp HOME and CLIO_HOME whose clio appsettings.json registers "closedport" (a port
nothing listens on) and "safe" (Safe: true, same port). Each case is called through clio-run and, being read-only,
by its raw name; <out-dir>/<label>.<path>.txt holds the result text with a correlation id masked.
"""
import json, os, pathlib, re, subprocess, sys, tempfile, time

TOOL = "get-mobile-page-conversion-guide"
CASES = [
    ("no-args", None),
    ("empty-args", {}),
    ("blank-schema", {"schema-name": "   ", "environment-name": "closedport"}),
    ("version-two-part", {"schema-name": "UsrX_FormPage", "environment-name": "closedport", "version": "8.3"}),
    ("version-network-path", {"schema-name": "UsrX_FormPage", "environment-name": "closedport", "version": "//host/x"}),
    ("version-text", {"schema-name": "UsrX_FormPage", "environment-name": "closedport", "version": "abc"}),
    ("unknown-env", {"schema-name": "UsrX_FormPage", "environment-name": "nope"}),
    ("safe-env", {"schema-name": "UsrX_FormPage", "environment-name": "safe"}),
    ("closed-port", {"schema-name": "UsrX_FormPage", "environment-name": "closedport"}),
    ("closed-port-target", {"schema-name": "UsrX_FormPage", "environment-name": "closedport", "target-schema-name": "UsrX_Mobile"}),
    ("no-env", {"schema-name": "UsrX_FormPage"}),
    ("wrong-type", {"schema-name": 5, "environment-name": "closedport"}),
    ("unknown-arg", {"schema-name": "UsrX_FormPage", "environment-name": "closedport", "bogus": 1}),
]


def main():
    out = pathlib.Path(sys.argv[1])
    command = sys.argv[sys.argv.index("--") + 1:]
    out.mkdir(parents=True, exist_ok=True)
    home = pathlib.Path(tempfile.mkdtemp(prefix="mobref-"))
    settings = {"Environments": {
        "closedport": {"Uri": "http://127.0.0.1:9", "Login": "example-user", "Password": "replace-me", "IsNetCore": False, "Safe": False},
        "safe": {"Uri": "http://127.0.0.1:9", "Login": "example-user", "Password": "replace-me", "IsNetCore": False, "Safe": True}},
        "ActiveEnvironmentKey": "closedport"}
    for relative in ("creatio/clio", ".local/share/creatio/clio", "clio-home"):
        (home / relative).mkdir(parents=True, exist_ok=True)
        (home / relative / "appsettings.json").write_text(json.dumps(settings, indent=2), encoding="utf-8")
    env = {**os.environ, "HOME": str(home), "CLIO_HOME": str(home / "clio-home"), "XDG_DATA_HOME": str(home / ".local/share")}
    process = subprocess.Popen(command, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
                               env=env, text=True, bufsize=1, encoding="utf-8")
    next_id = [0]

    def request(method, params):
        next_id[0] += 1
        process.stdin.write(json.dumps({"jsonrpc": "2.0", "id": next_id[0], "method": method, "params": params}) + "\n")
        process.stdin.flush()
        while True:
            line = process.stdout.readline()
            if not line:
                raise RuntimeError("server exited")
            try:
                message = json.loads(line)
            except json.JSONDecodeError:
                continue
            if message.get("id") == next_id[0]:
                return message

    request("initialize", {"protocolVersion": "2025-06-18", "capabilities": {}, "clientInfo": {"name": "refusals", "version": "1"}})
    process.stdin.write(json.dumps({"jsonrpc": "2.0", "method": "notifications/initialized", "params": {}}) + "\n")
    process.stdin.flush()
    try:
        for label, args in CASES:
            for path in ("clio-run", "raw"):
                if path == "clio-run":
                    inner = {"command": TOOL}
                    if args is not None:
                        inner["args"] = args
                    response = request("tools/call", {"name": "clio-run", "arguments": inner})
                else:
                    arguments = {} if args is None else {"args": args}
                    response = request("tools/call", {"name": TOOL, "arguments": arguments})
                if "error" in response:
                    text = "JSON-RPC error: " + json.dumps(response["error"], ensure_ascii=False)
                else:
                    result = response.get("result") or {}
                    texts = [c.get("text", "") for c in result.get("content", []) if c.get("type") == "text"]
                    text = ("isError " if result.get("isError") else "") + "\n".join(texts)
                text = re.sub(r'"correlation-?[iI]d"\s*:\s*"[^"]*"', '"correlationId":"<masked>"', text)
                (out / f"{label}.{path}.txt").write_text(text, encoding="utf-8")
                time.sleep(0.05)
    finally:
        process.kill()
        process.wait()


if __name__ == "__main__":
    main()
