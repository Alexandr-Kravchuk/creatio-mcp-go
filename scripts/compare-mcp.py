#!/usr/bin/env python3
"""Call the same MCP tools on clio's MCP server and on this Go server, then compare the answers.

Both servers run over stdio against ONE Creatio environment. clio receives it per call as
`environment-name`; the Go server receives it through CREATIO_* variables, which --clio-env copies
from clio's appsettings.json into the Go child process only.

Printed and persisted: verdicts, timings, and the JSON paths that differ. Never values, URLs,
environment names or credentials, so the evidence file is safe to share.
"""
import argparse, json, os, pathlib, subprocess, sys, time

REPO = pathlib.Path(__file__).resolve().parent.parent


class Server:
    def __init__(self, command, env=None):
        self.process = subprocess.Popen(command, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                        stderr=subprocess.DEVNULL, env=env, text=True, bufsize=1,
                                        encoding="utf-8")
        self.next_id = 0
        started = time.monotonic()
        self.request("initialize", {"protocolVersion": "2025-06-18", "capabilities": {},
                                    "clientInfo": {"name": "compare-mcp", "version": "1"}})
        self.startup_seconds = time.monotonic() - started
        self.send({"jsonrpc": "2.0", "method": "notifications/initialized", "params": {}})

    def send(self, message):
        self.process.stdin.write(json.dumps(message) + "\n")
        self.process.stdin.flush()

    def request(self, method, params):
        self.next_id += 1
        self.send({"jsonrpc": "2.0", "id": self.next_id, "method": method, "params": params})
        while True:
            line = self.process.stdout.readline()
            if not line:
                raise RuntimeError(f"MCP server exited during {method}")
            try:
                message = json.loads(line)
            except json.JSONDecodeError:
                continue
            if message.get("id") == self.next_id:
                return message

    def call(self, name, arguments):
        started = time.monotonic()
        response = self.request("tools/call", {"name": name, "arguments": arguments})
        return response, time.monotonic() - started

    def close(self):
        self.process.kill()
        self.process.wait()


def payload(response):
    if "error" in response:
        return {"success": False, "error": response["error"].get("message", "")}
    result = response.get("result") or {}
    value = result.get("structuredContent")
    if value is None:
        text = next((c.get("text", "") for c in result.get("content", []) if c.get("type") == "text"), "")
        try:
            value = json.loads(text)
        except json.JSONDecodeError:
            value = {"success": False, "error": text}
    if result.get("isError") and isinstance(value, dict):
        value = {**value, "success": False}
    return value


# clio stamps a fresh random correlation-id on many answers so its own log can be searched; it never
# matches between two calls, and this server keeps no such log, so the key is left out of the comparison.
# get-page's files.fetchedAt is the time of the call itself, so it differs between any two calls too.
IGNORED_KEYS = {"correlationid", "fetchedat"}


def normalize(value):
    """Ignore serializer conventions: key case, '-'/'_' and absent-versus-empty values."""
    if isinstance(value, dict):
        normalized = {key.lower().replace("-", "").replace("_", ""): item for key, item in value.items()}
        return {key: normalize(item) for key, item in normalized.items()
                if item not in (None, "", []) and key not in IGNORED_KEYS}
    if isinstance(value, list):
        return [normalize(item) for item in value]
    return value


def differences(left, right, path="$", out=None, limit=20):
    out = [] if out is None else out
    if len(out) >= limit:
        return out
    if type(left) is not type(right):
        out.append(f"{path}: {type(left).__name__} in clio, {type(right).__name__} in go")
    elif isinstance(left, dict):
        for key in sorted(set(left) | set(right)):
            if key not in right:
                out.append(f"{path}.{key}: only in clio")
            elif key not in left:
                out.append(f"{path}.{key}: only in go")
            else:
                differences(left[key], right[key], f"{path}.{key}", out, limit)
    elif isinstance(left, list):
        if len(left) != len(right):
            out.append(f"{path}: {len(left)} items in clio, {len(right)} in go")
        for index, (a, b) in enumerate(zip(left, right)):
            differences(a, b, f"{path}[{index}]", out, limit)
    elif left != right:
        out.append(f"{path}: value differs")
    return out


def verdict(clio_value, go_value):
    found = differences(clio_value, go_value)
    if not found:
        return "match", found
    # A refusal is success:false, or a non-zero exit-code in clio's command-style envelope.
    failed = [isinstance(v, dict) and (v.get("success") is False or v.get("exitcode", 0) != 0)
              for v in (clio_value, go_value)]
    if all(failed) and all(p.endswith("error: value differs") or p.endswith("].value: value differs")
                           or ": only in " in p for p in found):
        # Both refused. The wording is expected to differ; the refusal itself is the contract.
        return "both-failed", found
    if all(p.endswith("error: value differs") for p in found):
        # Same data; only a nested diagnostic (for example projectError) is worded differently.
        return "error-text", found
    return "mismatch", found


def settings_candidates():
    home = pathlib.Path.home()
    local = os.environ.get("LOCALAPPDATA")
    paths = [home / "creatio/clio/appsettings.json", home / ".local/share/creatio/clio/appsettings.json"]
    if local:
        paths.append(pathlib.Path(local) / "creatio/clio/appsettings.json")
    return paths


def go_environment(clio_env, settings_path):
    env = dict(os.environ)
    if not clio_env:
        return env
    candidates = [pathlib.Path(settings_path)] if settings_path else settings_candidates()
    path = next((p for p in candidates if p.is_file()), None)
    if path is None:
        sys.exit("clio appsettings.json not found; pass --clio-settings")
    settings = json.loads(path.read_text(encoding="utf-8-sig")).get("Environments", {}).get(clio_env)
    if settings is None:
        sys.exit("the named clio environment is not registered")
    # Drop every inherited connection variable, including the CREATIO_MCP_* aliases, so the named
    # environment alone decides the target and the authentication mode.
    for key in [key for key in env if key.startswith("CREATIO_")]:
        env.pop(key)
    env["CREATIO_URL"] = settings["Uri"]
    oauth = {"CREATIO_CLIENT_ID": "ClientId", "CREATIO_CLIENT_SECRET": "ClientSecret", "CREATIO_AUTH_APP_URI": "AuthAppUri"}
    forms = {"CREATIO_LOGIN": "Login", "CREATIO_PASSWORD": "Password"}
    for variable, key in (oauth if settings.get("ClientId") else forms).items():
        env[variable] = settings.get(key) or ""
    env["CREATIO_IS_NET_CORE"] = "true" if settings.get("IsNetCore") else "false"
    return env


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--clio-dll", required=True, help="clio.dll that serves `mcp-server`")
    parser.add_argument("--go-bin", required=True, help="built creatio-mcp-go binary")
    parser.add_argument("--clio-env", required=True, help="registered clio environment name")
    parser.add_argument("--clio-settings", help="clio appsettings.json (default: the platform location)")
    parser.add_argument("--cases", nargs="+", default=[str(REPO / "scripts/mcp-parity-cases.json")]
                        + sorted(str(p) for p in (REPO / "scripts/parity-cases").glob("*.json")),
                        help="case files; default: mcp-parity-cases.json plus scripts/parity-cases/*.json")
    parser.add_argument("--evidence", default=str(REPO / "evidence/mcp-latest.json"))
    options = parser.parse_args()

    cases = [case for path in options.cases for case in json.loads(pathlib.Path(path).read_text(encoding="utf-8"))]
    clio = Server(["dotnet", options.clio_dll, "mcp-server"])
    go = Server([options.go_bin], env=go_environment(options.clio_env, options.clio_settings))
    results = []
    try:
        for case in cases:
            name, arguments = case["tool"], case.get("args", {})
            # A few clio tools name the environment argument differently (get-fsm-mode: environmentName).
            clio_arguments = {case.get("clio-environment-key", "environment-name"): options.clio_env, **arguments}
            if case.get("clio-run"):
                clio_response, clio_seconds = clio.call("clio-run", {"command": name, "args": clio_arguments})
            else:
                clio_response, clio_seconds = clio.call(name, {"args": clio_arguments})
            go_response, go_seconds = go.call(name, arguments)
            outcome, paths = verdict(normalize(payload(clio_response)), normalize(payload(go_response)))
            known = set(case.get("known-differences", []))
            if outcome == "mismatch" and paths and all(path.split(":")[0] in known for path in paths):
                # Listed in the case file with the reason; reported, but not counted as a regression.
                outcome = "known-diff"
            results.append({"tool": name, "case": case.get("label", name), "verdict": outcome,
                            "clio_seconds": round(clio_seconds, 2), "go_seconds": round(go_seconds, 2),
                            "differences": paths})
            print(f"{outcome:12} {case.get('label', name):45} clio {clio_seconds:5.2f}s  go {go_seconds:5.2f}s")
            for path in paths:
                print(f"{'':12} {path}")
    finally:
        clio.close()
        go.close()

    summary = {"schema": 1, "clio_startup_seconds": round(clio.startup_seconds, 2),
               "go_startup_seconds": round(go.startup_seconds, 2),
               "counts": {v: sum(r["verdict"] == v for r in results) for v in ("match", "both-failed", "error-text", "known-diff", "mismatch")},
               "cases": results}
    evidence = pathlib.Path(options.evidence)
    evidence.parent.mkdir(parents=True, exist_ok=True)
    evidence.write_text(json.dumps(summary, indent=2) + "\n", encoding="utf-8")
    print(json.dumps(summary["counts"]))
    if results and summary["counts"]["both-failed"] == len(results):
        print("every case failed on both servers: the environment is unreachable or rejects the credentials",
              file=sys.stderr)
        return 2
    return 1 if summary["counts"]["mismatch"] else 0


if __name__ == "__main__":
    sys.exit(main())
