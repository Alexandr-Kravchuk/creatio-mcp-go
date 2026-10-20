#!/usr/bin/env python3
"""Compare the tool contracts of clio's MCP server and this Go server.

Asks both servers, live, for tools/list and get-tool-contract (the default line index with its legend,
detail=index-entries, every served tool's contract by default, with detail=full and with detail=short,
detail=full without names, one unknown name and one two-tool batch) and diffs the answers. Neither server needs a Creatio environment:
the Go server starts without CREATIO_* variables and nothing here contacts Creatio.

Every difference is either explained by KNOWN_DIFFERENCES below (with the reason) or counted as
unexplained; the exit code is non-zero when any difference is unexplained. A clio tool this server does
not implement yet is the known difference "not-ported", labelled with the task that ports it (from the
"tasks" map of docs/clio-inventory.json).

Printed and saved (evidence/contracts-latest.json): tool names, verdicts and JSON paths, no values.
"""
import argparse, importlib.util, json, os, pathlib, sys

REPO = pathlib.Path(__file__).resolve().parent.parent

_spec = importlib.util.spec_from_file_location("compare_mcp", REPO / "scripts/compare-mcp.py")
compare_mcp = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(compare_mcp)

# Differences that are intended, by class, with the reason recorded next to the evidence.
KNOWN_DIFFERENCES = {
    "not-ported": "clio has the tool and this server does not implement it yet; the task column says which task ports it",
    "suggestions": "an unknown name's suggestions are ranked over the tools each server has, so they differ while tools are unported",
    "server-info": "serverInfo names each server; it is not part of the tool contract",
    "fit-multi-name": "clio fits a reply naming several tools to 18 KB, shortening the largest contracts first by "
                      "its own serialized size (which counts the nulls the wire omits); this server serves each "
                      "named tool in the form clio gives it alone, so a batch that clio has to shorten differs. "
                      "Single-name answers, detail=full and detail=short are compared exactly",
}

# MCP defines an absent readOnlyHint/idempotentHint/destructiveHint/openWorldHint by its default; the Go
# SDK omits a false bool hint, clio writes it. Compared after filling the defaults in.
ANNOTATION_DEFAULTS = {"readOnlyHint": False, "idempotentHint": False, "destructiveHint": True, "openWorldHint": True}


# A two-tool batch that clio fits by shortening both contracts.
BATCH_PROBE = ["get-page", "update-page"]


def annotations(tool):
    return {**ANNOTATION_DEFAULTS, **(tool.get("annotations") or {})}


def differences(left, right, path="$"):
    """Every JSON path at which two values differ (compare-mcp's walk, without its limit)."""
    return compare_mcp.differences(left, right, path, [], limit=10 ** 6)


def compare_tools_list(clio_tools, go_tools, tasks):
    """Rows for tools/list: membership, order and each common entry's description, schema and annotations."""
    rows = []
    clio_names = [tool["name"] for tool in clio_tools]
    go_names = [tool["name"] for tool in go_tools]
    for name in clio_names:
        if name not in go_names:
            rows.append(row("tools/list", name, "known:not-ported", [], tasks.get(name, "")))
    for name in go_names:
        if name not in clio_names:
            rows.append(row("tools/list", name, "unexplained", ["only in go"]))
    common = [name for name in clio_names if name in go_names]
    if [name for name in go_names if name in clio_names] != common:
        rows.append(row("tools/list", "(order)", "unexplained", ["the common tools are listed in another order"]))
    clio_by_name = {tool["name"]: tool for tool in clio_tools}
    go_by_name = {tool["name"]: tool for tool in go_tools}
    for name in common:
        a, b = clio_by_name[name], go_by_name[name]
        paths = []
        if a.get("description") != b.get("description"):
            paths.append("$.description: value differs")
        paths += differences(a.get("inputSchema"), b.get("inputSchema"), "$.inputSchema")
        paths += differences(annotations(a), annotations(b), "$.annotations")
        rows.append(row("tools/list", name, "unexplained" if paths else "match", paths))
    return rows


def compare_index(clio_index, go_index, tasks):
    rows = []
    clio_by_name = {entry["name"]: entry for entry in clio_index}
    go_by_name = {entry["name"]: entry for entry in go_index}
    for name in clio_by_name:
        if name not in go_by_name:
            rows.append(row("index", name, "known:not-ported", [], tasks.get(name, "")))
    for name in go_by_name:
        if name not in clio_by_name:
            rows.append(row("index", name, "unexplained", ["only in go"]))
        else:
            paths = differences(clio_by_name[name], go_by_name[name])
            rows.append(row("index", name, "unexplained" if paths else "match", paths))
    common = [name for name in clio_by_name if name in go_by_name]
    if [name for name in go_by_name if name in clio_by_name] != common:
        rows.append(row("index", "(order)", "unexplained", ["the common entries are in another order"]))
    return rows


def line_name(line):
    """The tool name a tool-index line starts with: `name (R)(D) [aka ...]: purpose`."""
    return line.split(":", 1)[0].split(" ", 1)[0]


def compare_line_index(clio_answer, go_answer, tasks):
    """The default no-names answer: the same keys, clio's legend, and clio's line for every served tool."""
    rows = []
    paths = differences(sorted(clio_answer), sorted(go_answer), "$.keys")
    if clio_answer.get("success") != go_answer.get("success"):
        paths.append("$.success: value differs")
    if clio_answer.get("legend") != go_answer.get("legend"):
        paths.append("$.legend: value differs")
    rows.append(row("tool-index", "(legend)", "unexplained" if paths else "match", paths))
    clio_lines = {line_name(line): line for line in clio_answer.get("tool-index") or []}
    go_lines = {line_name(line): line for line in go_answer.get("tool-index") or []}
    for name in clio_lines:
        if name not in go_lines:
            rows.append(row("tool-index", name, "known:not-ported", [], tasks.get(name, "")))
    for name, line in go_lines.items():
        if name not in clio_lines:
            rows.append(row("tool-index", name, "unexplained", ["only in go"]))
        else:
            same = line == clio_lines[name]
            rows.append(row("tool-index", name, "match" if same else "unexplained", [] if same else ["$: line differs"]))
    common = [name for name in clio_lines if name in go_lines]
    if [name for name in go_lines if name in clio_lines] != common:
        rows.append(row("tool-index", "(order)", "unexplained", ["the common lines are in another order"]))
    return rows


def compare_contract(name, clio_answer, go_answer, surface="contract"):
    paths = differences(clio_answer, go_answer)
    return row(surface, name, "unexplained" if paths else "match", paths)


def compare_batch(names, clio_answer, go_answer):
    """A request naming several tools. clio may shorten some contracts to fit its reply budget (known); every
    other difference, and a contract both answer in the same form that still differs, is unexplained."""
    paths = differences(clio_answer, go_answer)
    if not paths:
        return row("contract", "(batch " + ",".join(names) + ")", "match", [])
    clio_tools, go_tools = clio_answer.get("tools") or [], go_answer.get("tools") or []
    fitted = [a.get("detail") != b.get("detail") or "same-as" in json.dumps(a.get("error-contract"))
              for a, b in zip(clio_tools, go_tools)]
    explained = len(clio_tools) == len(go_tools) and any(fitted) and all(
        f or not differences(a, b) for f, a, b in zip(fitted, clio_tools, go_tools)) and not differences(
        {k: v for k, v in clio_answer.items() if k != "tools"}, {k: v for k, v in go_answer.items() if k != "tools"})
    return row("contract", "(batch " + ",".join(names) + ")", "known:fit-multi-name" if explained else "unexplained", paths)


def compare_not_found(clio_answer, go_answer):
    """An unknown name: the same envelope; the suggestions may differ (known)."""
    paths = differences(clio_answer, go_answer)
    unexplained = [p for p in paths if ".suggestions" not in p]
    if unexplained:
        return row("contract", "(unknown name)", "unexplained", paths)
    return row("contract", "(unknown name)", "known:suggestions" if paths else "match", paths)


def compare_full(clio_tools, go_tools, served):
    """detail=full: the curated contracts of the tools served here, in clio's order, each as clio's."""
    clio_served = [tool for tool in clio_tools if tool["name"] in served]
    paths = differences([t["name"] for t in clio_served], [t["name"] for t in go_tools], "$.names")
    if not paths:
        for a, b in zip(clio_served, go_tools):
            paths += differences(a, b, f"$.{a['name']}")
    return row("contract", "(detail=full)", "unexplained" if paths else "match", paths)


def row(surface, name, verdict, paths, task=""):
    return {"surface": surface, "tool": name, "verdict": verdict, "task": task, "differences": paths}


def contract_call(server, arguments):
    response = server.request("tools/call", {"name": "get-tool-contract", "arguments": arguments})
    result = response.get("result") or {}
    text = next((c.get("text", "") for c in result.get("content", []) if c.get("type") == "text"), "")
    if "error" in response or result.get("isError"):
        raise RuntimeError(f"get-tool-contract {arguments} failed: {text or response.get('error')}")
    return json.loads(text)


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--clio-dll", required=True, help="clio.dll that serves `mcp-server`")
    parser.add_argument("--go-bin", required=True, help="built creatio-mcp-go binary")
    parser.add_argument("--inventory", default=str(REPO / "docs/clio-inventory.json"),
                        help="clio inventory; its tasks map labels the not-ported tools")
    parser.add_argument("--evidence", default=str(REPO / "evidence/contracts-latest.json"))
    options = parser.parse_args()

    inventory_path = pathlib.Path(options.inventory)
    tasks = json.loads(inventory_path.read_text(encoding="utf-8")).get("tasks", {}) if inventory_path.is_file() else {}
    go_env = {key: value for key, value in os.environ.items() if not key.startswith("CREATIO_")}
    clio = compare_mcp.Server(["dotnet", options.clio_dll, "mcp-server"])
    go = compare_mcp.Server([options.go_bin], env=go_env)
    rows = []
    try:
        lists = [compare_mcp.Server.request(s, "tools/list", {})["result"]["tools"] for s in (clio, go)]
        rows += compare_tools_list(lists[0], lists[1], tasks)
        bare = [contract_call(s, {}) for s in (clio, go)]
        rows += compare_line_index(bare[0], bare[1], tasks)
        entries = {"args": {"detail": "index-entries"}}
        indexes = [contract_call(s, entries).get("index") or [] for s in (clio, go)]
        rows += compare_index(indexes[0], indexes[1], tasks)
        served = [entry["name"] for entry in indexes[1]] + ["get-tool-contract"]
        for name in served:
            arguments = {"args": {"tool-names": [name]}}
            rows.append(compare_contract(name, contract_call(clio, arguments), contract_call(go, arguments)))
            for detail in ("full", "short"):
                arguments = {"args": {"tool-names": [name], "detail": detail}}
                rows.append(compare_contract(name, contract_call(clio, arguments), contract_call(go, arguments),
                                             f"contract detail={detail}"))
        unknown = {"args": {"tool-names": ["get-pag"]}}
        rows.append(compare_not_found(contract_call(clio, unknown), contract_call(go, unknown)))
        full = {"args": {"detail": "full"}}
        rows.append(compare_full(contract_call(clio, full)["tools"], contract_call(go, full)["tools"], set(served)))
        # Two contracts clio 8.1.0.142 returns short together and complete alone: the reply budget at work.
        pair = [name for name in BATCH_PROBE if name in served]
        if len(pair) == len(BATCH_PROBE):
            arguments = {"args": {"tool-names": pair}}
            rows.append(compare_batch(pair, contract_call(clio, arguments), contract_call(go, arguments)))
    finally:
        clio.close()
        go.close()

    for item in rows:
        if item["verdict"] != "match" or item["differences"]:
            label = f"{item['surface']}:{item['tool']}"
            print(f"{item['verdict']:20} {label:55} {item['task']}")
            for path in item["differences"][:20]:
                print(f"{'':20} {path}")
    counts = {}
    for item in rows:
        counts[item["verdict"]] = counts.get(item["verdict"], 0) + 1
    summary = {"schema": 1, "known-differences": KNOWN_DIFFERENCES, "counts": counts, "rows": rows}
    evidence = pathlib.Path(options.evidence)
    evidence.parent.mkdir(parents=True, exist_ok=True)
    evidence.write_text(json.dumps(summary, indent=2) + "\n", encoding="utf-8")
    print(json.dumps(counts, sort_keys=True))
    return 1 if counts.get("unexplained") else 0


if __name__ == "__main__":
    sys.exit(main())
