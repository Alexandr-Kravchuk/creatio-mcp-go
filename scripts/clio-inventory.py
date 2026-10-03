#!/usr/bin/env python3
"""Record exactly what clio's MCP server offers: tools, prompts, resources and every tool contract.

Asks a running `clio mcp-server` for tools/list, prompts/list, resources/list and
resources/templates/list (following nextCursor), the get-tool-contract index, and the full
get-tool-contract answer of every tool in that index, one tool per call (the same call an agent makes).

Writes:
- docs/clio-inventory.json: the raw answers, the source of truth for this server's contract data
  (`go generate ./cmd/creatio-mcp-go` copies the part it serves into the binary);
- docs/clio-inventory.md: a summary, one row per tool, with the task that ports it;
- the "Appendix" section of docs/migration-plan.md, regenerated from the same data.

--go-bin lets the summary mark the tools this server already serves (asked through its own
get-tool-contract index). --compare-dll records how a second clio build (for example a build of clio
master) differs from the main one, by tool name, resident flag, annotations and input schema.

The output holds clio's own texts only: no environment names, URLs or credentials are asked for.
"""
import argparse, datetime, importlib.util, json, os, pathlib, sys

REPO = pathlib.Path(__file__).resolve().parent.parent

_spec = importlib.util.spec_from_file_location("compare_mcp", REPO / "scripts/compare-mcp.py")
compare_mcp = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(compare_mcp)

# Which migration task ports each clio tool (docs/migration-plan.md, "Tasks and dependencies").
# A tool this server already serves is reported as done whatever its row here says; a clio tool that is
# not listed lands in the "unassigned" bucket, so a new clio tool is never silently dropped. Rows may name
# tools the inventoried clio does not answer: newer ones (clio master's *-to-file variants, create-package,
# localize-page, ...) and ones behind clio's deploy-identity feature toggle (deploy-identity,
# uninstall-identity, create-oauth-technical-user, watch-compilation).
TASKS = {
    "T4 shared infrastructure": ["clio-run-destructive"],
    "T6 guidance and knowledge": [
        "add-knowledge-source", "configure-knowledge-feedback-policy", "delete-knowledge", "delete-toolkit",
        "disable-knowledge-source", "enable-knowledge-source", "experimental", "export-component-registry",
        "get-component-info", "get-component-info-to-file", "get-guidance", "get-request-info-to-file", "get-knowledge-feedback-policy", "get-mobile-page-conversion-guide",
        "get-request-info", "get-telemetry-consent", "info-knowledge", "install-knowledge", "install-toolkit",
        "list-knowledge-examples", "list-knowledge-sources", "merge-creatio-artifact", "remove-knowledge-source",
        "send-telemetry", "update-knowledge", "update-toolkit", "withdraw-telemetry-consent",
    ],
    "T7 applications": ["create-app", "create-app-section", "delete-app", "delete-app-section",
                        "install-application", "update-app-section"],
    "T8 schemas": [
        "create-entity-schema", "create-lookup", "create-schema", "create-sql-schema", "delete-schema",
        "export-schema", "import-schema", "install-sql-schema", "list-entity-client-schemas-to-file",
        "modify-entity-schema-column",
        "set-entity-schema-properties", "sync-schemas", "update-entity-schema", "update-schema",
        "update-sql-schema",
    ],
    "T9 pages": ["create-client-unit-schema", "create-page", "create-related-page-addon", "create-user-task-page",
                 "localize-page", "sync-pages", "update-client-unit-schema", "update-page"],
    "T10 business rules": ["create-entity-business-rules", "update-entity-business-rules",
                           "delete-entity-business-rules", "create-page-business-rules",
                           "update-page-business-rules", "delete-page-business-rules"],
    "T11 data": [
        "add-data-binding-row", "create-data-binding", "create-data-binding-db", "execute-dataservice-batch",
        "execute-esq-to-file", "execute-sql-script", "odata-create", "odata-read-to-file", "odata-delete", "odata-update", "remove-data-binding-row",
        "remove-data-binding-row-db", "run-process", "upsert-data-binding-row-db",
    ],
    "T12 settings, users, access": [
        "create-oauth-technical-user", "create-server-to-server-oauth-app", "create-sys-setting",
        "download-sys-setting-file", "get-identity-assertion", "get-identity-public-jwk", "manage-access",
        "manage-license", "manage-role", "manage-user", "regenerate-identity-signing-key", "set-fsm-mode",
        "set-record-rights", "update-sys-setting",
    ],
    "T13 processes": [
        "create-business-process", "create-user-task", "enroll-sequence-participants", "generate-process-model",
        "install-process-builder", "modify-business-process", "modify-business-process-as-new-version",
        "modify-user-task-parameters", "register-process-element", "set-active-business-process-version",
        "validate-process-graph",
    ],
    "T14 themes, branding, email": [
        "advise-theme-palette", "build-theme", "clear-themes-cache", "create-theme", "delete-theme",
        "set-background-image", "set-logo", "set-user-theme", "update-email-template", "update-theme",
        "upload-image",
    ],
    "T15 packages, compile, restart": [
        "add-custom-logging", "add-package-dependency", "compile-creatio", "create-package",
        "dataforge-initialize", "dataforge-update", "download-configuration-by-build",
        "download-configuration-by-environment", "finish-hotfix", "install-dashboards-migrator", "install-gate",
        "pkg-to-db", "pkg-to-file-system", "remove-package-dependency", "restart-by-credentials",
        "restart-by-environment-name", "unlock-for-hotfix", "watch-compilation",
    ],
    "T16 local, infrastructure, workspace": [
        "StopAllCreatio", "add-item-model", "add-package", "assert-infrastructure", "check-auth-code-flow",
        "check-settings-health", "clear-browser-session", "clear-redis-db-by-credentials",
        "clear-redis-db-by-environment", "create-workspace", "deploy-creatio", "deploy-identity",
        "generate-source-code", "get-browser-session", "get-identity-service-config",
        "link-from-repository-by-env-package-path", "link-from-repository-by-environment",
        "link-from-repository-unlocked", "list-creatio-builds", "list-db-templates",
        "new-integration-test-project", "new-test-project", "new-ui-project", "prune-db-templates",
        "push-workspace", "reg-web-app", "restore-db-by-credentials", "restore-db-by-environment",
        "restore-db-to-local-server", "restore-workspace", "show-passing-infrastructure", "stop-all-creatio",
        "stop-creatio", "uninstall-creatio", "uninstall-identity",
    ],
}
UNASSIGNED = "unassigned (no task yet; decide before T17)"
DONE = "done (served by creatio-mcp-go)"
APPENDIX_HEADING = "## Appendix: every clio tool, by task"


class InventoryServer(compare_mcp.Server):
    """compare-mcp's stdio client, keeping the initialize answer (clio's version)."""

    def request(self, method, params):
        response = super().request(method, params)
        if method == "initialize":
            self.initialize_result = response.get("result", {})
        return response


def result_of(response, method):
    if "error" in response:
        raise RuntimeError(f"{method} failed: {response['error'].get('message', '')}")
    return response.get("result") or {}


def list_all(server, method, key):
    """Every item of a paginated MCP list, following nextCursor."""
    items, cursor = [], None
    while True:
        result = result_of(server.request(method, {"cursor": cursor} if cursor else {}), method)
        items.extend(result.get(key, []))
        cursor = result.get("nextCursor")
        if not cursor:
            return items


def tool_text(response, method):
    result = result_of(response, method)
    text = next((c.get("text", "") for c in result.get("content", []) if c.get("type") == "text"), "")
    if result.get("isError"):
        raise RuntimeError(f"{method} failed: {text}")
    return json.loads(text)


def read_clio(dll, label=None):
    server = InventoryServer(["dotnet", dll, "mcp-server"])
    try:
        info = getattr(server, "initialize_result", {}).get("serverInfo", {})
        inventory = {
            "schema": 1,
            "clio": {"name": info.get("name", ""), "version": label or info.get("version", "")},
            "generatedOn": datetime.date.today().isoformat(),
            "toolsList": list_all(server, "tools/list", "tools"),
            "prompts": list_all(server, "prompts/list", "prompts"),
            "resources": list_all(server, "resources/list", "resources"),
            "resourceTemplates": list_all(server, "resources/templates/list", "resourceTemplates"),
        }
        index = tool_text(server.request("tools/call", {"name": "get-tool-contract", "arguments": {}}),
                          "get-tool-contract")
        if index.get("success") is not True or not isinstance(index.get("index"), list):
            raise RuntimeError("get-tool-contract returned no index")
        inventory["index"] = index["index"]
        # detail=full answers with the curated contracts only, a narrower set than the index.
        full = tool_text(server.request("tools/call", {
            "name": "get-tool-contract", "arguments": {"args": {"detail": "full"}}}), "get-tool-contract")
        inventory["fullDetail"] = [tool["name"] for tool in full.get("tools") or []]
        # The index leaves get-tool-contract itself out, but its contract can be asked for by name.
        names = [entry["name"] for entry in index["index"]]
        names += [tool["name"] for tool in inventory["toolsList"] if tool["name"] not in names]
        contracts = {}
        for name in names:
            answer = tool_text(server.request("tools/call", {
                "name": "get-tool-contract", "arguments": {"args": {"tool-names": [name]}}}), "get-tool-contract")
            tools = answer.get("tools") or []
            if answer.get("success") is not True or len(tools) != 1 or tools[0].get("name") != name:
                raise RuntimeError(f"get-tool-contract returned no single contract for {name}")
            contracts[name] = tools[0]
        inventory["contracts"] = contracts
        return inventory
    finally:
        server.close()


def go_tool_names(go_bin):
    """Tools this server serves: its get-tool-contract index plus its tools/list."""
    env = {key: value for key, value in os.environ.items() if not key.startswith("CREATIO_")}
    server = compare_mcp.Server([go_bin], env=env)
    try:
        listed = {tool["name"] for tool in list_all(server, "tools/list", "tools")}
        index = tool_text(server.request("tools/call", {"name": "get-tool-contract", "arguments": {}}),
                          "get-tool-contract")
    finally:
        server.close()
    # Builds before T5 answered {"tools": [names]} instead of clio's {"index": [...]}.
    names = [entry["name"] for entry in index.get("index", [])] + list(index.get("tools") or [])
    return listed | {name for name in names if isinstance(name, str)}


def task_of(name, served):
    if name in served:
        return DONE
    return next((task for task, names in TASKS.items() if name in names), UNASSIGNED)


def master_gap(main, other):
    """How `other` (for example a clio master build) differs from `main`, per tool."""
    lines = []
    main_index = {e["name"]: e for e in main["index"]}
    other_index = {e["name"]: e for e in other["index"]}
    for name in sorted(set(other_index) - set(main_index)):
        lines.append(f"- `{name}`: only in {other['clio']['version']}")
    for name in sorted(set(main_index) - set(other_index)):
        lines.append(f"- `{name}`: only in {main['clio']['version']}")
    for name in sorted(set(main_index) & set(other_index)):
        changed = [key for key in ("resident", "destructive", "purpose")
                   if main_index[name].get(key) != other_index[name].get(key)]
        a, b = main["contracts"].get(name, {}), other["contracts"].get(name, {})
        changed += [key for key in sorted(set(a) | set(b)) if a.get(key) != b.get(key)]
        if changed:
            lines.append(f"- `{name}`: {', '.join(changed)} differ")
    main_list = {t["name"]: t for t in main["toolsList"]}
    other_list = {t["name"]: t for t in other["toolsList"]}
    if list(main_list) != list(other_list):
        lines.append("- tools/list: the resident set or its order differs")
    for name in sorted(set(main_list) & set(other_list)):
        changed = [key for key in ("description", "inputSchema", "annotations")
                   if main_list[name].get(key) != other_list[name].get(key)]
        if changed:
            lines.append(f"- tools/list `{name}`: {', '.join(changed)} differ")
    return lines


def buckets(inventory, served):
    grouped = {}
    for entry in inventory["index"]:
        grouped.setdefault(task_of(entry["name"], served), []).append(entry["name"])
    order = [DONE] + list(TASKS) + [UNASSIGNED]
    return [(task, sorted(grouped[task])) for task in order if task in grouped]


def appendix(inventory, served):
    count = len(inventory["index"])
    lines = [
        APPENDIX_HEADING, "",
        f"{count} clio tools in the get-tool-contract index of clio {inventory['clio']['version']} "
        f"(plus `get-tool-contract` itself, which the index leaves out), {len(inventory['toolsList'])} of them "
        "resident in tools/list. Generated by `scripts/clio-inventory.py` from clio's live answers "
        "(`docs/clio-inventory.json`, summary in [clio-inventory.md](clio-inventory.md)); do not edit by hand, "
        "change `TASKS` in the script and rerun it.", "",
        "| Task | Count | Tools |", "|---|---|---|",
    ]
    for task, names in buckets(inventory, served):
        lines.append(f"| {task} | {len(names)} | {', '.join(f'`{n}`' for n in names)} |")
    return "\n".join(lines) + "\n"


def summary(inventory, served, gap, other_version):
    clio = inventory["clio"]
    resident = [tool["name"] for tool in inventory["toolsList"]]
    lines = [
        "# clio MCP inventory", "",
        f"Generated by `scripts/clio-inventory.py` from clio {clio['version']} on {inventory['generatedOn']}. "
        "Raw answers: [clio-inventory.json](clio-inventory.json).", "",
        "| | Count |", "|---|---|",
        f"| Tools in the get-tool-contract index | {len(inventory['index'])} |",
        f"| Resident tools (tools/list) | {len(resident)} |",
        f"| Prompts | {len(inventory['prompts'])} |",
        f"| Resources | {len(inventory['resources'])} |",
        f"| Resource templates | {len(inventory['resourceTemplates'])} |",
        f"| Served by creatio-mcp-go | {len([n for n in served if n in inventory['contracts']])} |", "",
        "Resident tools in clio's tools/list order: " + ", ".join(f"`{n}`" for n in resident) + ".", "",
        "## Tools", "",
        "| Tool | Resident | Destructive | Task | Purpose |", "|---|---|---|---|---|",
    ]
    for entry in inventory["index"]:
        purpose = entry.get("purpose", "").replace("|", "\\|").replace("\n", " ")
        lines.append(f"| `{entry['name']}` | {'yes' if entry.get('resident') else ''} | "
                     f"{'yes' if entry.get('destructive') else ''} | {task_of(entry['name'], served)} | {purpose} |")
    lines += ["", "## Prompts", "", ", ".join(f"`{p['name']}`" for p in inventory["prompts"]), "",
              "## Resource templates", ""]
    lines += [f"- `{t['uriTemplate']}`: {t.get('description', '')}" for t in inventory["resourceTemplates"]]
    if gap is not None:
        lines += ["", f"## Differences in clio {other_version}", ""]
        lines += gap or ["None."]
    return "\n".join(lines) + "\n"


def replace_appendix(plan_text, new_appendix):
    """Replace everything from the first '## Appendix' heading to the end of the file."""
    marker = plan_text.find("\n## Appendix")
    if marker < 0:
        return plan_text.rstrip("\n") + "\n\n" + new_appendix
    return plan_text[:marker + 1] + new_appendix


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--clio-dll", required=True, help="clio.dll that serves `mcp-server`")
    parser.add_argument("--go-bin", help="built creatio-mcp-go binary; marks the tools it already serves")
    parser.add_argument("--compare-dll", help="a second clio.dll (e.g. a clio master build) to record gaps against")
    parser.add_argument("--compare-label", help="how the summary names --compare-dll (a local build reports 0.0.0.0)")
    parser.add_argument("--json", default=str(REPO / "docs/clio-inventory.json"))
    parser.add_argument("--markdown", default=str(REPO / "docs/clio-inventory.md"))
    parser.add_argument("--plan", default=str(REPO / "docs/migration-plan.md"),
                        help="migration plan whose appendix is regenerated; pass '' to leave it")
    options = parser.parse_args()

    inventory = read_clio(options.clio_dll)
    served = go_tool_names(options.go_bin) if options.go_bin else set()
    inventory["tasks"] = {entry["name"]: task_of(entry["name"], served) for entry in inventory["index"]}
    gap, other_version = None, None
    if options.compare_dll:
        other = read_clio(options.compare_dll, options.compare_label)
        gap, other_version = master_gap(inventory, other), other["clio"]["version"]
    pathlib.Path(options.json).write_text(json.dumps(inventory, indent=1, ensure_ascii=False) + "\n", encoding="utf-8")
    pathlib.Path(options.markdown).write_text(summary(inventory, served, gap, other_version), encoding="utf-8")
    if options.plan:
        plan = pathlib.Path(options.plan)
        plan.write_text(replace_appendix(plan.read_text(encoding="utf-8"), appendix(inventory, served)),
                        encoding="utf-8")
    print(f"clio {inventory['clio']['version']}: {len(inventory['index'])} indexed tools, "
          f"{len(inventory['toolsList'])} resident, {len(inventory['prompts'])} prompts, "
          f"{len(inventory['resources'])} resources, {len(inventory['resourceTemplates'])} templates")
    return 0


if __name__ == "__main__":
    sys.exit(main())
