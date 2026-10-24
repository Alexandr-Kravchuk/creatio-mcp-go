#!/usr/bin/env python3
"""Decide whether a new clio build changed what its MCP server offers, and write the PR body that says how.

Compares two inventories written by scripts/clio-inventory.py (the committed docs/clio-inventory.json and
a fresh one) on clio's own content: tools/list, the get-tool-contract legend, line index and index
entries, every contract (full and short), which contracts clio shortens by default, detail=full,
prompts, resources and resource templates.

Not counted as a change:
- generatedOn and clio.version (the version string is also masked wherever clio repeats it);
- the "tasks" map: its labels come from clio-inventory.py's TASKS table and its "done" marks from the
  --go-bin build, so they move with this repository, not with clio (listed as informational);
- resources under docs://knowledge/: clio downloads its curated knowledge library when it starts, and that
  library is released on its own (listed as informational);
- the order of resources and resource templates: clio lists them in a different order from run to run.

Exit code: 0 no change, 3 changed, 2 bad input. The Markdown body goes to stdout or --body-out.
"""
import argparse, json, pathlib, sys

VOLATILE = ("generatedOn", "tasks")
KNOWLEDGE_PREFIX = "docs://knowledge/"
VERSION_MASK = "<clio-version>"
CHANGED = 3


def mask_version(value, version):
    if not version:
        return value
    if isinstance(value, str):
        return value.replace(version, VERSION_MASK)
    if isinstance(value, list):
        return [mask_version(item, version) for item in value]
    if isinstance(value, dict):
        return {key: mask_version(item, version) for key, item in value.items()}
    return value


def normalize(inventory):
    """clio-sourced content only, with the version masked and the knowledge-library parts set apart."""
    version = (inventory.get("clio") or {}).get("version", "")
    data = {key: value for key, value in inventory.items() if key not in VOLATILE}
    data["clio"] = {key: value for key, value in (inventory.get("clio") or {}).items() if key != "version"}
    data = mask_version(data, version)
    resources = data.get("resources") or []
    data["resources"] = sorted((r for r in resources if not r.get("uri", "").startswith(KNOWLEDGE_PREFIX)),
                               key=lambda r: r.get("uri", ""))
    data["resourceTemplates"] = sorted(data.get("resourceTemplates") or [], key=lambda t: t.get("uriTemplate", ""))
    return data


def tool_names(inventory):
    names = [entry["name"] for entry in inventory.get("index") or []]
    names += [tool["name"] for tool in inventory.get("toolsList") or []]
    names += list(inventory.get("contracts") or {})
    return list(dict.fromkeys(names))


def schema_properties(schema, prefix=""):
    """Property path -> definition, for a JSON schema (properties is an object) or a clio contract
    input-schema (properties is a list of {name, ...})."""
    if not isinstance(schema, dict):
        return {}
    props = schema.get("properties")
    if isinstance(props, list):
        props = {p.get("name", ""): p for p in props if isinstance(p, dict)}
    if not isinstance(props, dict):
        return {}
    found = {}
    for name, definition in props.items():
        path = prefix + name
        found[path] = definition
        found.update(schema_properties(definition, path + "."))
        if isinstance(definition, dict):
            found.update(schema_properties(definition.get("items"), path + "[]."))
    return found


def schema_changes(old, new):
    """Changed property names of one schema: added, removed, changed, plus a changed required list."""
    a, b = schema_properties(old), schema_properties(new)
    parts = []
    for label, names in (("added", sorted(set(b) - set(a))), ("removed", sorted(set(a) - set(b))),
                         ("changed", sorted(n for n in set(a) & set(b) if a[n] != b[n]))):
        # A changed parent is reported through its changed children when it has any.
        if label == "changed":
            names = [n for n in names if not any(m.startswith(n + ".") or m.startswith(n + "[].") for m in names)]
        if names:
            parts.append(f"{label} {', '.join(f'`{n}`' for n in names)}")
    old_required = (old or {}).get("required") if isinstance(old, dict) else None
    new_required = (new or {}).get("required") if isinstance(new, dict) else None
    if old_required != new_required:
        parts.append(f"required {old_required or []} -> {new_required or []}")
    if not parts and old != new:
        parts.append("other schema keywords changed")
    return parts


def names_list(names, limit=20):
    shown = ", ".join(f"`{n}`" for n in names[:limit])
    return shown + (f" and {len(names) - limit} more" if len(names) > limit else "")


def by_name(items, key="name"):
    return {item.get(key): item for item in items or []}


def compare(old_inventory, new_inventory):
    """Everything the PR body reports. `changed` is the decision; the info lists never set it."""
    old, new = normalize(old_inventory), normalize(new_inventory)
    old_names, new_names = tool_names(old), tool_names(new)
    report = {
        "old_version": (old_inventory.get("clio") or {}).get("version", ""),
        "new_version": (new_inventory.get("clio") or {}).get("version", ""),
        "new_tools": [n for n in new_names if n not in old_names],
        "removed_tools": [n for n in old_names if n not in new_names],
        "schemas": [], "descriptions": [], "other": [], "info": [],
    }
    common = [n for n in new_names if n in old_names]
    old_list, new_list = by_name(old.get("toolsList")), by_name(new.get("toolsList"))
    old_index, new_index = by_name(old.get("index")), by_name(new.get("index"))
    old_contracts, new_contracts = old.get("contracts") or {}, new.get("contracts") or {}
    old_short, new_short = old.get("shortContracts") or {}, new.get("shortContracts") or {}
    for name in common:
        a, b = old_list.get(name), new_list.get(name)
        if a and b:
            parts = schema_changes(a.get("inputSchema"), b.get("inputSchema"))
            if parts:
                report["schemas"].append(f"`{name}` (tools/list): {'; '.join(parts)}")
            fields = [k for k in ("description", "annotations") if a.get(k) != b.get(k)]
            if fields:
                report["descriptions"].append(f"`{name}` (tools/list): {', '.join(fields)}")
        elif bool(a) != bool(b):
            report["other"].append(f"`{name}`: {'now' if b else 'no longer'} resident in tools/list")
        a, b = old_contracts.get(name), new_contracts.get(name)
        if a is not None and b is not None:
            parts = schema_changes(a.get("input-schema"), b.get("input-schema"))
            if parts:
                report["schemas"].append(f"`{name}` (contract): {'; '.join(parts)}")
            if a.get("description") != b.get("description"):
                report["descriptions"].append(f"`{name}` (contract): description")
            rest = sorted(k for k in set(a) | set(b) if k not in ("description", "input-schema") and a.get(k) != b.get(k))
            if rest:
                report["other"].append(f"`{name}` contract: {', '.join(rest)}")
        a, b = old_index.get(name), new_index.get(name)
        if a and b:
            fields = sorted(k for k in set(a) | set(b) if a.get(k) != b.get(k))
            if fields:
                report["descriptions"].append(f"`{name}` (index entry): {', '.join(fields)}")
        a, b = old_short.get(name), new_short.get(name)
        if a is not None and b is not None and a != b:
            fields = sorted(k for k in set(a) | set(b) if a.get(k) != b.get(k))
            report["other"].append(f"`{name}` short contract: {', '.join(fields)}")
    if [n for n in old_list if n in new_list] != [n for n in new_list if n in old_list]:
        report["other"].append("tools/list: the common tools are in another order")
    if [n for n in old_index if n in new_index] != [n for n in new_index if n in old_index]:
        report["other"].append("index: the common entries are in another order")
    if old.get("legend") != new.get("legend"):
        report["descriptions"].append("get-tool-contract legend")
    old_lines = dict(zip(old_index, old.get("toolIndex") or []))
    new_lines = dict(zip(new_index, new.get("toolIndex") or []))
    lines = [n for n in common if n in old_lines and n in new_lines and old_lines[n] != new_lines[n]]
    if lines:
        report["descriptions"].append("tool-index lines: " + ", ".join(f"`{n}`" for n in lines))
    for key, label in (("defaultShort", "short by default"), ("fullDetail", "answered by detail=full")):
        a, b = old.get(key) or [], new.get(key) or []
        if a != b:
            added, removed = [n for n in b if n not in a], [n for n in a if n not in b]
            report["other"].append(f"{label}: added {added or 'none'}, removed {removed or 'none'}"
                                   + ("" if added or removed else " (order only)"))
    for key, ident, label in (("prompts", "name", "prompts"), ("resources", "uri", "resources"),
                              ("resourceTemplates", "uriTemplate", "resource templates")):
        a, b = by_name(old.get(key), ident), by_name(new.get(key), ident)
        added, removed = [n for n in b if n not in a], [n for n in a if n not in b]
        edited = [n for n in b if n in a and a[n] != b[n]]
        for verb, names in (("added", added), ("removed", removed), ("changed", edited)):
            if names:
                report["other"].append(f"{label} {verb}: " + names_list(names, 50))
        if not (added or removed or edited) and old.get(key) != new.get(key):
            report["other"].append(f"{label}: order changed")
    rest = sorted(k for k in set(old) | set(new) if k not in (
        "clio", "toolsList", "index", "contracts", "shortContracts", "legend", "toolIndex", "defaultShort",
        "fullDetail", "prompts", "resources", "resourceTemplates") and old.get(k) != new.get(k))
    if old.get("clio") != new.get("clio"):
        rest.insert(0, "clio")
    if rest:
        report["other"].append("other inventory fields: " + ", ".join(rest))
    report["info"] = informational(old_inventory, new_inventory)
    report["changed"] = bool(report["new_tools"] or report["removed_tools"] or report["schemas"]
                             or report["descriptions"] or report["other"])
    return report


def informational(old_inventory, new_inventory):
    info = []
    old_tasks, new_tasks = old_inventory.get("tasks") or {}, new_inventory.get("tasks") or {}
    drift = sorted(n for n in set(old_tasks) & set(new_tasks) if old_tasks[n] != new_tasks[n])
    if drift:
        info.append("task labels or served marks differ (they follow this repository and the --go-bin "
                    f"build, not clio) for {len(drift)} tools: " + names_list(drift))

    def knowledge(inventory):
        return by_name([r for r in inventory.get("resources") or [] if r.get("uri", "").startswith(KNOWLEDGE_PREFIX)], "uri")

    a, b = knowledge(old_inventory), knowledge(new_inventory)
    added, removed = [u for u in b if u not in a], [u for u in a if u not in b]
    edited = [u for u in b if u in a and a[u] != b[u]]
    if added or removed or edited:
        info.append(f"knowledge library resources (clio downloads the library at start; it is released on its "
                    f"own): {len(added)} added, {len(removed)} removed, {len(edited)} changed"
                    + "".join(f"\n  - {verb}: " + names_list(names)
                              for verb, names in (("added", added), ("removed", removed), ("changed", edited)) if names))
    for key, ident, label in (("resources", "uri", "resources"), ("resourceTemplates", "uriTemplate", "resource templates")):
        a = [item.get(ident) for item in old_inventory.get(key) or []]
        b = [item.get(ident) for item in new_inventory.get(key) or []]
        common_a, common_b = [n for n in a if n in b], [n for n in b if n in a]
        if common_a != common_b:
            info.append(f"{label} are listed in another order (clio's order changes from run to run)")
    return info


def unexplained_rows(evidence):
    """The rows compare-contracts.py could not explain, from its evidence JSON."""
    return [row for row in (evidence or {}).get("rows") or [] if row.get("verdict") == "unexplained"]


def section(title, items):
    return [f"### {title}", ""] + ([f"- {item}" for item in items] if items else ["None."]) + [""]


def body(report, evidence=None, contracts_exit=None, contracts_note=""):
    lines = [f"clio {report['new_version']} compared with the committed inventory of clio {report['old_version']}.", "",
             "**Decision: " + ("changed" if report["changed"] else "no change") + "**", ""]
    lines += section("New tools", [f"`{n}`" for n in report["new_tools"]])
    lines += section("Removed tools", [f"`{n}`" for n in report["removed_tools"]])
    lines += section("Input schemas changed", report["schemas"])
    lines += section("Descriptions and annotations changed", report["descriptions"])
    lines += section("Other clio changes", report["other"])
    lines += section("Informational, not counted as a change", report["info"])
    if evidence is not None or contracts_exit is not None:
        rows = unexplained_rows(evidence)
        title = "compare-contracts: unexplained rows"
        if contracts_note:
            title += f" ({contracts_note})"
        lines += [f"### {title}", ""]
        if contracts_exit is not None:
            lines += [f"Exit code {contracts_exit}; counts {json.dumps((evidence or {}).get('counts', {}), sort_keys=True)}.", ""]
        for row in rows:
            paths = row.get("differences") or []
            shown = "; ".join(paths[:5]) + (f"; and {len(paths) - 5} more" if len(paths) > 5 else "")
            lines.append(f"- {row.get('surface')}: `{row.get('tool')}`" + (f": {shown}" if shown else ""))
        if not rows:
            lines.append("None.")
        lines.append("")
    return "\n".join(lines) + "\n"


def read_json(path):
    return json.loads(pathlib.Path(path).read_text(encoding="utf-8"))


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("old", help="the committed inventory (docs/clio-inventory.json)")
    parser.add_argument("new", help="the inventory just read from the new clio")
    parser.add_argument("--contracts", help="evidence JSON written by compare-contracts.py --evidence")
    parser.add_argument("--contracts-exit", type=int, help="compare-contracts.py's exit code")
    parser.add_argument("--contracts-note", default="", help="added to the compare-contracts heading")
    parser.add_argument("--body-out", help="write the Markdown body here instead of stdout")
    options = parser.parse_args(argv)
    try:
        report = compare(read_json(options.old), read_json(options.new))
        evidence = read_json(options.contracts) if options.contracts and pathlib.Path(options.contracts).is_file() else None
    except (OSError, ValueError, KeyError, TypeError) as error:
        print(f"clio_watch_diff: {error}", file=sys.stderr)
        return 2
    text = body(report, evidence, options.contracts_exit, options.contracts_note)
    if options.body_out:
        pathlib.Path(options.body_out).write_text(text, encoding="utf-8")
    else:
        sys.stdout.write(text)
    return CHANGED if report["changed"] else 0


if __name__ == "__main__":
    sys.exit(main())
