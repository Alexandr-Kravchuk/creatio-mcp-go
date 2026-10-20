#!/usr/bin/env python3
"""Compare clio 8.1.0.142 and creatio-mcp-go on the five *-to-file tools (T21 stream b), files included.

Both servers are started with the same working directory, a fresh directory under the OS temp root, so a
relative output-file lands inside the workspace anchor on both sides. In a case's arguments `{side}`
becomes `clio<n>` or `go<n>` (n = the case's index), `{who}` becomes `clio` or `go` (the same name in every
case, to name one file from two cases) and `{tmp}` becomes that working directory, so each server writes its
own file.

For every case the answers are compared after the side's own path (the working directory, as given and
symlink-resolved, and the `{side}` segment) is replaced by a placeholder; then every file named by a
case's `files` list (argument keys) is compared byte for byte: both absent, or both present with the same
sha256. Each case is called through clio-run on both servers (the tools are not read-only).

  python3 scripts/fake-stands/t21b/fake_creatio.py serve 18971 <dir> &        # canned Creatio, see that file
  python3 scripts/fake-stands/t21b/run_cases.py --clio-dll <clio.dll> --go-bin <creatio-mcp-go> \
      --settings <dir>/appsettings.json --env fake --cases scripts/fake-stands/t21b/cases.json

validation-cases.json needs no fake: run it with a settings file that registers `dummy` at an unreachable
address (http://127.0.0.1:9) and --env dummy.

Printed and saved (--evidence): verdicts, differing JSON paths and file digests, never values.
"""
import argparse
import hashlib
import importlib.util
import json
import os
import pathlib
import sys
import tempfile

REPO = pathlib.Path(__file__).resolve().parents[3]
_spec = importlib.util.spec_from_file_location("compare_mcp", REPO / "scripts" / "compare-mcp.py")
compare_mcp = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(compare_mcp)


def substitute(value, mapping):
    if isinstance(value, dict):
        return {key: substitute(item, mapping) for key, item in value.items()}
    if isinstance(value, list):
        return [substitute(item, mapping) for item in value]
    if isinstance(value, str):
        for placeholder, text in mapping.items():
            value = value.replace(placeholder, text)
    return value


def unsubstitute(value, replacements):
    """Replace side-specific text in an answer by its placeholder, longest first."""
    if isinstance(value, dict):
        return {key: unsubstitute(item, replacements) for key, item in value.items()}
    if isinstance(value, list):
        return [unsubstitute(item, replacements) for item in value]
    if isinstance(value, str):
        for text, placeholder in replacements:
            value = value.replace(text, placeholder)
    return value


def digest(path):
    path = pathlib.Path(path)
    if not path.is_file():
        return None
    return hashlib.sha256(path.read_bytes()).hexdigest()


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--clio-dll", required=True)
    parser.add_argument("--go-bin", required=True)
    parser.add_argument("--settings", required=True, help="clio appsettings.json both servers read")
    parser.add_argument("--env", required=True, help="environment name passed as environment-name")
    parser.add_argument("--cases", nargs="+", required=True)
    parser.add_argument("--evidence", default=str(REPO / "evidence" / "t21b-latest.json"))
    parser.add_argument("--show", action="store_true", help="also print both answers (values) for each case")
    options = parser.parse_args()
    # Every path is resolved before the working directory changes to the fresh workspace.
    options.evidence = os.path.abspath(options.evidence)
    options.settings = os.path.abspath(options.settings)
    options.go_bin = os.path.abspath(options.go_bin)
    options.cases = [os.path.abspath(path) for path in options.cases]

    cases = [case for path in options.cases for case in json.loads(pathlib.Path(path).read_text(encoding="utf-8"))]
    workdir = pathlib.Path(tempfile.mkdtemp(prefix="t21b-"))
    real_workdir = os.path.realpath(workdir)
    os.chdir(workdir)
    clio_env = dict(os.environ)
    clio_env["CLIO_HOME"] = compare_mcp.clio_home(options.settings)
    clio = compare_mcp.Server(["dotnet", options.clio_dll, "mcp-server"], env=clio_env)
    go = compare_mcp.Server([options.go_bin], env=compare_mcp.go_environment(options.env, options.settings, "name"))
    results = []
    try:
        for index, case in enumerate(cases):
            label = case.get("label", case["tool"])
            answers, files = {}, {}
            for side, server in (("clio", clio), ("go", go)):
                mapping = {"{side}": f"{side}{index}", "{who}": side, "{tmp}": str(workdir)}
                arguments = substitute(case.get("args", {}), mapping)
                if case.get("environment", True):
                    arguments = {"environment-name": options.env, **arguments}
                response, _ = server.call("clio-run", {"command": case["tool"], "args": arguments})
                value = compare_mcp.payload(response)
                replacements = sorted({(real_workdir, "{tmp}"), (str(workdir), "{tmp}"),
                                       (f"{side}{index}", "{side}"), (f"shared-{side}", "shared-{who}")}, key=lambda item: -len(item[0]))
                answers[side] = unsubstitute(value, replacements)
                files[side] = {key: digest(arguments[key]) for key in case.get("files", []) if isinstance(arguments.get(key), str)}
            # Strict: a refusal worded differently is a mismatch here, unlike compare-mcp.py's both-failed.
            paths = compare_mcp.differences(compare_mcp.normalize(answers["clio"]), compare_mcp.normalize(answers["go"]))
            outcome = "mismatch" if paths else "match"
            known = set(case.get("known-differences", []))
            if paths and all(path.split(":")[0] in known for path in paths):
                outcome = "known-diff"
            file_verdict = "same" if files["clio"] == files["go"] else "DIFFERENT"
            if file_verdict == "DIFFERENT":
                outcome = "mismatch"
            written = sorted({key for key, value in files["clio"].items() if value})
            results.append({"tool": case["tool"], "case": label, "verdict": outcome, "differences": paths,
                            "files": file_verdict, "written": written,
                            "sha256": {side: files[side] for side in files}})
            print(f"{outcome:12} files:{file_verdict:9} {label}")
            for path in paths:
                print(f"{'':12} {path}")
            if options.show:
                print("   clio:", json.dumps(answers["clio"])[:2000])
                print("   go:  ", json.dumps(answers["go"])[:2000])
    finally:
        clio.close()
        go.close()
    counts = {v: sum(r["verdict"] == v for r in results) for v in ("match", "known-diff", "mismatch")}
    evidence = pathlib.Path(options.evidence)
    evidence.parent.mkdir(parents=True, exist_ok=True)
    evidence.write_text(json.dumps({"schema": 1, "counts": counts, "cases": results}, indent=2) + "\n", encoding="utf-8")
    print(json.dumps(counts))
    return 1 if counts["mismatch"] else 0


if __name__ == "__main__":
    sys.exit(main())
