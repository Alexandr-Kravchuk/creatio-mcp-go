#!/usr/bin/env python3
"""Run write scenarios on clio's MCP server and on this Go server, then compare what each wrote.

Writes cannot be compared by sending both servers the same arguments: they would fight over one object.
Every scenario therefore creates a separate object per server ({side} = clio or go, {run} = this run),
reads each object back through its own server, compares the read-backs after replacing each side's
names with the neutral template, and removes the objects again.

Safety:
  * the clio environment name must be listed in ~/.config/creatio-mcp-go/write-stands (one per line)
    AND --i-understand-this-writes must be passed; nothing is started otherwise;
  * the plan (steps and objects to create) is printed before anything runs;
  * destructive tools (delete-*, odata-delete, ...) run only against an object this run created;
  * every object is recorded in ~/.cache/creatio-mcp-go/write-ledger.jsonl before it is created, and
    --cleanup-ledger removes what a crashed run left behind.

Printed and persisted: verdicts, timings and the JSON paths that differ. Never values, URLs,
environment names, object names or credentials, so the evidence file is safe to share.
"""
import argparse, importlib.util, json, os, pathlib, re, shlex, sys, time

REPO = pathlib.Path(__file__).resolve().parent.parent
SIDES = ("clio", "go")

_spec = importlib.util.spec_from_file_location("compare_mcp", REPO / "scripts/compare-mcp.py")
compare_mcp = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(compare_mcp)
Server, payload, normalize, differences, verdict, go_environment, clio_home = (
    compare_mcp.Server, compare_mcp.payload, compare_mcp.normalize, compare_mcp.differences,
    compare_mcp.verdict, compare_mcp.go_environment, compare_mcp.clio_home)

STEP_KINDS = {"write", "read-back", "expect", "cleanup"}
DESTRUCTIVE_PREFIXES = ("delete-", "remove-", "uninstall-", "clear-", "prune-", "restore-db")
DESTRUCTIVE_TOOLS = {"odata-delete", "clio-run-destructive", "execute-sql-script"}
# Worst first: the scenario verdict is the worst verdict of its steps.
SEVERITY = ["mismatch", "known-diff", "error-text", "both-failed", "match", "skipped"]
PLACEHOLDER = re.compile(r"\{([A-Za-z][A-Za-z0-9_-]*)\}")


def allow_list_path():
    return pathlib.Path.home() / ".config/creatio-mcp-go/write-stands"


def ledger_path():
    return pathlib.Path.home() / ".cache/creatio-mcp-go/write-ledger.jsonl"


def new_run_id():
    """Alphanumeric only: Creatio codes and schema names reject '-' and '_' in several places."""
    number, digits, out = int(time.time()), "0123456789abcdefghijklmnopqrstuvwxyz", ""
    while number:
        number, rest = divmod(number, 36)
        out = digits[rest] + out
    return out


def is_destructive(step):
    tool = step.get("tool", "")
    return bool(step.get("destructive")) or tool in DESTRUCTIVE_TOOLS or tool.startswith(DESTRUCTIVE_PREFIXES)


def substitute(value, variables):
    """Replace {name} placeholders whose name is known; leave others (for example JSON braces) alone."""
    if isinstance(value, dict):
        return {key: substitute(item, variables) for key, item in value.items()}
    if isinstance(value, list):
        return [substitute(item, variables) for item in value]
    if isinstance(value, str):
        return PLACEHOLDER.sub(lambda m: str(variables[m.group(1)]) if m.group(1) in variables else m.group(0), value)
    return value


def placeholders(value):
    text = json.dumps(value)
    return set(PLACEHOLDER.findall(text))


def neutralize(value, replacements):
    """Replace each side's concrete names and captured ids with their template, case-insensitively,
    in keys and string values, so clio's and Go's objects compare equal."""
    if not replacements:
        return value
    pattern = re.compile("|".join(re.escape(concrete) for concrete in sorted(replacements, key=len, reverse=True)),
                         re.IGNORECASE)
    lookup = {concrete.lower(): template for concrete, template in replacements.items()}

    def text(item):
        return pattern.sub(lambda m: lookup[m.group(0).lower()], item)

    def walk(item):
        if isinstance(item, dict):
            return {text(key): walk(child) for key, child in item.items()}
        if isinstance(item, list):
            return [walk(child) for child in item]
        return text(item) if isinstance(item, str) else item

    return walk(value)


def resolve_path(value, path):
    """Resolve '$.a.b[0].c' on a normalized payload. Returns (found, value)."""
    if not path.startswith("$"):
        return False, None
    for name, index in re.findall(r"\.([^.\[\]]+)|\[(\d+)\]", path[1:]):
        if name:
            if not isinstance(value, dict) or name.lower().replace("-", "").replace("_", "") not in value:
                return False, None
            value = value[name.lower().replace("-", "").replace("_", "")]
        else:
            if not isinstance(value, list) or int(index) >= len(value):
                return False, None
            value = value[int(index)]
    return True, value


def failed(value):
    return isinstance(value, dict) and (value.get("success") is False or value.get("exitcode", 0) != 0)


def worst(verdicts):
    present = [v for v in verdicts if v in SEVERITY]
    return min(present, key=SEVERITY.index) if present else "match"


# ---------------------------------------------------------------------------------------------- loading


def load_scenarios(paths):
    scenarios = []
    for path in paths:
        for scenario in json.loads(pathlib.Path(path).read_text(encoding="utf-8")):
            validate(scenario, path)
            scenarios.append(scenario)
    return scenarios


def validate(scenario, path):
    label = scenario.get("label")
    if not label or not isinstance(scenario.get("steps"), list):
        raise SystemExit(f"{path}: every scenario needs a label and a steps list")
    creates = set()
    for step in scenario["steps"]:
        kind = step.get("kind")
        if kind not in STEP_KINDS:
            raise SystemExit(f"{path}: {label}: unknown step kind {kind!r}")
        if kind in ("write", "read-back") and not step.get("tool"):
            raise SystemExit(f"{path}: {label}: a {kind} step needs a tool")
        if kind == "write" and step.get("creates"):
            created = step["creates"]
            if "{side}" not in created or "{run}" not in created:
                raise SystemExit(f"{path}: {label}: creates must contain {{run}} and {{side}}: {created}")
            creates.add(created)
        if kind == "expect" and not (step.get("step") and step.get("path") and "equals" in step):
            raise SystemExit(f"{path}: {label}: an expect step needs step, path and equals")
        if kind == "cleanup":
            if step.get("for") not in creates:
                raise SystemExit(f"{path}: {label}: cleanup 'for' must name an object a previous write creates")
            if step.get("server", "own") not in ("own", "clio"):
                raise SystemExit(f"{path}: {label}: cleanup server must be 'own' or 'clio'")
            if not step.get("calls"):
                raise SystemExit(f"{path}: {label}: a cleanup step needs calls")
        for call in [step] + step.get("calls", []):
            if call.get("tool") and is_destructive(call) and not (call.get("target") or step.get("for")):
                raise SystemExit(f"{path}: {label}: destructive {call['tool']} needs a target")


def print_plan(scenarios, run_id):
    print(f"plan for run {run_id}:")
    for scenario in scenarios:
        pending = pending_reason(scenario)
        print(f"  scenario {scenario['label']}" + (f"  [skipped: {pending}]" if pending else ""))
        for step in scenario["steps"]:
            tool = step.get("tool") or ", ".join(c["tool"] for c in step.get("calls", [])) or step.get("step", "")
            server = f" via {step.get('server', 'own')} server" if step["kind"] == "cleanup" else ""
            print(f"    {step['kind']:10} {tool}{server}")
            if step.get("creates") and not pending:
                for side in SIDES:
                    print(f"               creates on {side}: {substitute(step['creates'], {'run': run_id, 'side': side})}")


def pending_reason(scenario):
    return next((step["go-tool-missing"] for step in scenario["steps"]
                 if step["kind"] in ("write", "read-back") and step.get("go-tool-missing")), None)


# ---------------------------------------------------------------------------------------------- ledger


def ledger_append(record):
    path = ledger_path()
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open("a", encoding="utf-8") as handle:
        handle.write(json.dumps(record) + "\n")


def ledger_leftovers(environment, run=None):
    path = ledger_path()
    if not path.is_file():
        return []
    created, removed = {}, set()
    for line in path.read_text(encoding="utf-8").splitlines():
        if not line.strip():
            continue
        record = json.loads(line)
        if record.get("environment") != environment or (run and record.get("run") != run):
            continue
        key = (record["run"], record["side"], record["name"])
        if record["event"] == "create":
            created[key] = record
        elif record["event"] == "removed":
            removed.add(key)
    return [record for key, record in created.items() if key not in removed]


# ---------------------------------------------------------------------------------------------- running


class Refused(Exception):
    """A step the safety rules do not allow; it is reported and never sent."""


class Harness:
    def __init__(self, servers, environment, run_id, go_env_mode="name"):
        self.servers, self.environment, self.run_id, self.go_env_mode = servers, environment, run_id, go_env_mode

    def call(self, server_side, call, arguments):
        server = self.servers[server_side]
        selector = {call.get("clio-environment-key", "environment-name"): self.environment}
        if server_side == "clio":
            arguments = {**selector, **arguments}
            if call.get("clio-run"):
                return server.call("clio-run", {"command": call["tool"], "args": arguments})
            return server.call(call["tool"], {"args": arguments})
        if self.go_env_mode == "name":
            arguments = {**selector, **arguments}
        # This server refuses a direct call to a write tool the way clio does, so a step that goes through
        # clio-run on clio goes through clio-run here too.
        if call.get("clio-run"):
            return server.call("clio-run", {"command": call["tool"], "args": arguments})
        return server.call(call["tool"], arguments)

    def guard(self, call, arguments, side_state, target_template):
        """A destructive call must target an object this run created, and its arguments must lead to it:
        either the name itself, or a value captured by a call whose arguments named it."""
        if not is_destructive(call):
            return
        target = substitute(call.get("target") or target_template or "", side_state["variables"])
        if not target or target not in side_state["created"]:
            raise Refused(f"{call['tool']} refused: its target was not created by this run")
        text = json.dumps(arguments)
        if target.lower() in text.lower():
            return
        used = placeholders(call.get("args", {}))
        if any(target in side_state["provenance"].get(name, set()) for name in used):
            return
        raise Refused(f"{call['tool']} refused: its arguments do not lead to the object this run created")

    def capture(self, call, value, arguments, side_state):
        """Store captured values per side; remember which created names the capturing call referred to."""
        named = {name for name in side_state["created"] if name.lower() in json.dumps(arguments).lower()}
        for variable, path in call.get("capture", {}).items():
            found, item = resolve_path(value, path)
            if not found or item in (None, "", []):
                return False
            side_state["variables"][variable] = item
            side_state["provenance"][variable] = named
            if isinstance(item, str) and len(item) >= 6:  # short values would replace unrelated text
                side_state["replacements"][item] = "{" + variable + "}"
        return True

    def run_scenario(self, scenario):
        label = scenario["label"]
        pending = pending_reason(scenario)
        if pending:
            print(f"{'skipped':12} {label}")
            return {"scenario": label, "verdict": "skipped", "steps": []}
        state = {side: {"variables": {"run": self.run_id, "side": side}, "created": set(), "attempted": [],
                        "provenance": {}, "replacements": {}, "answers": {}} for side in SIDES}
        steps, stopped = [], False
        try:
            for index, step in enumerate(scenario["steps"]):
                if step["kind"] == "cleanup":
                    continue
                if stopped:
                    steps.append({"step": step_label(step, index), "kind": step["kind"], "verdict": "skipped",
                                  "differences": []})
                    continue
                result = self.run_step(scenario, step, index, state)
                steps.append(result)
                if step["kind"] == "write" and result.get("stop"):
                    stopped = True
                result.pop("stop", None)
        except Exception as error:  # a server died or a refusal: report it, then clean up regardless
            steps.append({"step": "harness", "kind": "error", "verdict": "mismatch",
                          "differences": [f"$: {type(error).__name__}: {error}" if isinstance(error, Refused)
                                          else f"$: {type(error).__name__}"]})
        finally:
            steps.extend(self.cleanup(scenario, state))
        outcome = worst(s["verdict"] for s in steps)
        print(f"{outcome:12} {label}")
        for step in steps:
            print(f"{'':12}   {step['verdict']:12} {step['kind']:10} {step['step']}")
            for path in step["differences"]:
                print(f"{'':12}     {path}")
        return {"scenario": label, "verdict": outcome, "steps": steps}

    def run_step(self, scenario, step, index, state):
        label = step_label(step, index)
        if step["kind"] == "expect":
            return self.expect(step, label, state)
        answers, seconds = {}, {}
        for side in SIDES:
            side_state = state[side]
            arguments = substitute(step.get("args", {}), side_state["variables"])
            if step["kind"] == "write" and step.get("creates"):
                name = substitute(step["creates"], side_state["variables"])
                ledger_append({"event": "create", "environment": self.environment, "run": self.run_id,
                               "side": side, "name": name, "scenario": scenario["label"],
                               "cleanup": [substitute(c, side_state["variables"]) for c in scenario["steps"]
                                           if c["kind"] == "cleanup" and c["for"] == step["creates"]]})
                side_state["created"].add(name)
                side_state["attempted"].append(step["creates"])
                side_state["replacements"][name] = step["creates"]
            self.guard(step, arguments, side_state, None)
            response, seconds[side] = self.call(side, step, arguments)
            answers[side] = normalize(payload(response))
            self.capture(step, answers[side], arguments, side_state)
            side_state["answers"][label] = answers[side]
        compared = {side: neutralize(answers[side], state[side]["replacements"]) for side in SIDES}
        outcome, paths = verdict(compared["clio"], compared["go"])
        known = set(step.get("known-differences", []))
        if outcome == "mismatch" and paths and all(path.split(":")[0] in known for path in paths):
            outcome = "known-diff"
        if outcome == "match" and all(failed(answers[side]) for side in SIDES):
            # Identical refusals are not a tested write: nothing was created to read back.
            outcome = "both-failed"
        result = {"step": label, "kind": step["kind"], "verdict": outcome,
                  "clio_seconds": round(seconds["clio"], 2), "go_seconds": round(seconds["go"], 2),
                  "differences": paths}
        if any(failed(answers[side]) for side in SIDES):
            result["stop"] = True
        return result

    def expect(self, step, label, state):
        problems = []
        sides = SIDES if step.get("side", "both") == "both" else (step["side"],)
        for side in sides:
            answer = state[side]["answers"].get(step["step"])
            if answer is None:
                problems.append(f"{step['path']}: step {step['step']} has no answer in {side}")
                continue
            found, value = resolve_path(neutralize(answer, state[side]["replacements"]), step["path"])
            if not found:
                problems.append(f"{step['path']}: missing in {side}")
            elif value != step["equals"]:
                problems.append(f"{step['path']}: expectation failed in {side}")
        return {"step": label, "kind": "expect", "verdict": "mismatch" if problems else "match",
                "differences": problems}

    def cleanup(self, scenario, state):
        """Undo in reverse order across objects (last created, first removed); the calls inside one
        cleanup step keep their order, so a lookup can precede the delete that uses its result."""
        results = []
        cleanups = [(index, step) for index, step in enumerate(scenario["steps"]) if step["kind"] == "cleanup"]
        for index, step in reversed(cleanups):
            for side in SIDES:
                if step["for"] not in state[side]["attempted"]:
                    continue
                problems = run_cleanup(self, step, side, state[side])
                results.append({"step": f"{step_label(step, index)} ({side})", "kind": "cleanup",
                                "verdict": "mismatch" if problems else "match", "differences": problems})
                if not problems:
                    ledger_append({"event": "removed", "environment": self.environment, "run": self.run_id,
                                   "side": side, "name": substitute(step["for"], state[side]["variables"])})
        return results


def run_cleanup(harness, step, side, side_state):
    """Run one object's cleanup calls for one side. Returns the problems found (no values in them).
    A capture that finds nothing means the object is already gone, which counts as removed."""
    server_side = side if step.get("server", "own") == "own" else "clio"
    for call in step["calls"]:
        arguments = substitute(call.get("args", {}), side_state["variables"])
        try:
            harness.guard(call, arguments, side_state, step["for"])
            response, _ = harness.call(server_side, call, arguments)
        except Refused as error:
            return [f"$: {error}"]
        except Exception as error:
            return [f"$: {call['tool']} on {server_side} raised {type(error).__name__}"]
        value = normalize(payload(response))
        if failed(value):
            return [f"$: {call['tool']} failed on {server_side}"]
        if call.get("capture") and not harness.capture(call, value, arguments, side_state):
            return []
    return []


def step_label(step, index):
    return step.get("label") or f"{index + 1}:{step.get('tool') or step.get('kind')}"


# ---------------------------------------------------------------------------------------------- entry


def check_gate(environment, confirmed):
    path = allow_list_path()
    names = set()
    if path.is_file():
        names = {line.strip() for line in path.read_text(encoding="utf-8").splitlines()
                 if line.strip() and not line.strip().startswith("#")}
    if environment not in names:
        sys.exit(f"refused: the environment is not listed in {path}; add it there only for a disposable stand")
    if not confirmed:
        sys.exit("refused: pass --i-understand-this-writes to create and delete objects on this environment")


def start_servers(options):
    clio_command = shlex.split(options.clio_command) if options.clio_command else ["dotnet", options.clio_dll, "mcp-server"]
    go_command = shlex.split(options.go_command) if options.go_command else [options.go_bin]
    clio_env = dict(os.environ)
    if options.clio_settings:
        clio_env["CLIO_HOME"] = clio_home(options.clio_settings)
    clio = Server(clio_command, env=clio_env)
    try:
        go = Server(go_command, env=go_environment(options.clio_env, options.clio_settings, options.go_env_mode))
    except BaseException:
        clio.close()
        raise
    return {"clio": clio, "go": go}


def cleanup_ledger(options):
    leftovers = ledger_leftovers(options.clio_env)
    print(f"{len(leftovers)} leftover object(s) in the ledger for this environment")
    if not leftovers:
        return 0
    servers = start_servers(options)
    failures = 0
    try:
        for record in leftovers:
            harness = Harness(servers, options.clio_env, record["run"], options.go_env_mode)
            side_state = {"variables": {"run": record["run"], "side": record["side"]}, "created": {record["name"]},
                          "provenance": {}, "replacements": {}}
            problems = []
            for step in record["cleanup"]:
                problems = run_cleanup(harness, {**step, "for": record["name"]}, record["side"], side_state)
                if problems:
                    break
            if not record["cleanup"]:
                problems = ["$: no cleanup step recorded"]
            if problems:
                failures += 1
                print(f"{'mismatch':12} leftover from run {record['run']} ({record['side']})")
                for problem in problems:
                    print(f"{'':12}   {problem}")
            else:
                ledger_append({"event": "removed", "environment": options.clio_env, "run": record["run"],
                               "side": record["side"], "name": record["name"]})
                print(f"{'match':12} leftover from run {record['run']} ({record['side']}) removed")
    finally:
        for server in servers.values():
            server.close()
    return 1 if failures else 0


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    clio = parser.add_mutually_exclusive_group()
    clio.add_argument("--clio-dll", help="clio.dll that serves `mcp-server`")
    clio.add_argument("--clio-command", help="full command that starts clio's MCP server (instead of --clio-dll)")
    go = parser.add_mutually_exclusive_group()
    go.add_argument("--go-bin", help="built creatio-mcp-go binary")
    go.add_argument("--go-command", help="full command that starts this server (instead of --go-bin)")
    parser.add_argument("--clio-env", required=True, help="registered clio environment name; must be allow-listed")
    parser.add_argument("--clio-settings", help="clio appsettings.json (default: the platform location)")
    parser.add_argument("--go-env-mode", choices=("name", "variables"), default="name",
                        help="name: this server reads clio's settings and gets environment-name per call, as clio "
                             "does (default); variables: it gets the environment through CREATIO_* variables")
    parser.add_argument("--scenarios", nargs="+",
                        default=sorted(str(p) for p in (REPO / "scripts/write-scenarios").glob("*.json")),
                        help="scenario files; default: scripts/write-scenarios/*.json")
    parser.add_argument("--evidence", default=str(REPO / "evidence/mcp-writes-latest.json"))
    parser.add_argument("--run-id", default=None, help="alphanumeric run id for {run}; default: derived from the time")
    parser.add_argument("--plan-only", action="store_true", help="validate the scenarios and print the plan; start nothing")
    parser.add_argument("--cleanup-ledger", action="store_true", help="remove objects that earlier runs left behind")
    parser.add_argument("--i-understand-this-writes", dest="confirmed", action="store_true",
                        help="required for any run that writes")
    options = parser.parse_args(argv)
    run_id = options.run_id or new_run_id()
    if not re.fullmatch(r"[A-Za-z0-9]+", run_id):
        sys.exit("--run-id must be alphanumeric")

    scenarios = [] if options.cleanup_ledger else load_scenarios(options.scenarios)
    if options.plan_only:
        print_plan(scenarios, run_id)
        return 0
    check_gate(options.clio_env, options.confirmed)
    if not (options.clio_dll or options.clio_command) or not (options.go_bin or options.go_command):
        sys.exit("pass --clio-dll or --clio-command, and --go-bin or --go-command")
    if options.cleanup_ledger:
        return cleanup_ledger(options)

    print_plan(scenarios, run_id)
    runnable = [s for s in scenarios if not pending_reason(s)]
    results = []
    servers = start_servers(options) if runnable else None
    try:
        harness = Harness(servers, options.clio_env, run_id, options.go_env_mode)
        for scenario in scenarios:
            results.append(harness.run_scenario(scenario))
    finally:
        for server in (servers or {}).values():
            server.close()

    summary = {"schema": 1,
               "clio_startup_seconds": round(servers["clio"].startup_seconds, 2) if servers else None,
               "go_startup_seconds": round(servers["go"].startup_seconds, 2) if servers else None,
               "counts": {v: sum(r["verdict"] == v for r in results) for v in reversed(SEVERITY)},
               "scenarios": results}
    evidence = pathlib.Path(options.evidence)
    evidence.parent.mkdir(parents=True, exist_ok=True)
    evidence.write_text(json.dumps(summary, indent=2) + "\n", encoding="utf-8")
    print(json.dumps(summary["counts"]))
    leftovers = ledger_leftovers(options.clio_env, run_id)
    if leftovers:
        print(f"{len(leftovers)} object(s) were not removed; run again with --cleanup-ledger", file=sys.stderr)
        return 1
    ran = [r for r in results if r["verdict"] != "skipped"]
    if ran and all(r["verdict"] == "both-failed" for r in ran):
        print("every scenario failed on both servers: the environment is unreachable or rejects the credentials",
              file=sys.stderr)
        return 2
    return 1 if summary["counts"]["mismatch"] else 0


if __name__ == "__main__":
    sys.exit(main())
