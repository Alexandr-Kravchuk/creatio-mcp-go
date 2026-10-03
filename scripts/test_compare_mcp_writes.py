"""Self-tests for scripts/compare-mcp-writes.py against two fake stdio MCP servers. No network.

Run from the repository root: python3 -m unittest
"""
import json, os, pathlib, shlex, subprocess, sys, tempfile, unittest

SCRIPTS = pathlib.Path(__file__).resolve().parent
HARNESS = SCRIPTS / "compare-mcp-writes.py"
FAKE = SCRIPTS / "testdata/fake_mcp_server.py"
ENVIRONMENT = "fakestand"
RUN = "t1"
CREATED = "UsrParity{run}{side}Setting"

SCENARIO = [{
    "label": "sys-setting round trip",
    "steps": [
        {"kind": "write", "label": "create", "tool": "create-sys-setting", "creates": CREATED,
         "args": {"code": CREATED, "name": "Parity", "value-type-name": "ShortText", "value": "parity-value"}},
        {"kind": "cleanup", "for": CREATED, "server": "clio", "calls": [
            {"tool": "odata-read", "args": {"entity": "SysSettings", "select": ["Id"],
                                            "filters": {"all": [{"field": "Code", "op": "eq", "value": CREATED}]}},
             "capture": {"id": "$.value[0].id"}},
            {"tool": "odata-delete", "args": {"entity": "SysSettings", "id": "{id}", "confirm": True}}]},
        {"kind": "read-back", "label": "read", "tool": "get-sys-setting", "args": {"code": CREATED}},
        {"kind": "expect", "step": "read", "path": "$.value", "equals": "parity-value"},
        {"kind": "expect", "step": "read", "path": "$.code", "equals": CREATED},
    ],
}]


class WriteHarnessTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = pathlib.Path(self.temp.name)
        self.home = self.root / "home"
        self.state = self.root / "state.json"
        self.evidence = self.root / "evidence.json"
        self.settings = self.root / "appsettings.json"
        self.settings.write_text(json.dumps({"Environments": {ENVIRONMENT: {
            "Uri": "http://127.0.0.1:1", "Login": "u", "Password": "p"}}}), encoding="utf-8")
        self.allow(ENVIRONMENT)

    def tearDown(self):
        self.temp.cleanup()

    def allow(self, *names):
        path = self.home / ".config/creatio-mcp-go/write-stands"
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text("# disposable stands\n" + "".join(name + "\n" for name in names), encoding="utf-8")

    def scenario_file(self, scenarios):
        path = self.root / "scenarios.json"
        path.write_text(json.dumps(scenarios), encoding="utf-8")
        return path

    def fake(self, flavor, *faults):
        parts = [sys.executable, str(FAKE), "--flavor", flavor, "--state", str(self.state)]
        for fault in faults:
            parts += ["--fault", fault]
        return shlex.join(parts)

    def run_harness(self, *extra, scenarios=SCENARIO, go_faults=(), clio_faults=(), confirm=True):
        arguments = [sys.executable, str(HARNESS), "--clio-env", ENVIRONMENT, "--clio-settings", str(self.settings),
                     "--clio-command", self.fake("clio", *clio_faults), "--go-command", self.fake("go", *go_faults),
                     "--scenarios", str(self.scenario_file(scenarios)), "--evidence", str(self.evidence),
                     "--run-id", RUN, *extra]
        if confirm:
            arguments.append("--i-understand-this-writes")
        environment = {**os.environ, "HOME": str(self.home)}
        return subprocess.run(arguments, capture_output=True, text=True, env=environment, timeout=60)

    def stand(self):
        return json.loads(self.state.read_text(encoding="utf-8")) if self.state.is_file() else {}

    def ledger(self):
        path = self.home / ".cache/creatio-mcp-go/write-ledger.jsonl"
        return [json.loads(line) for line in path.read_text(encoding="utf-8").splitlines()] if path.is_file() else []

    def evidence_data(self):
        return json.loads(self.evidence.read_text(encoding="utf-8"))

    def assert_evidence_is_clean(self):
        text = self.evidence.read_text(encoding="utf-8")
        for secret in (ENVIRONMENT, "127.0.0.1", "UsrParityt1", "parity-value"):
            self.assertNotIn(secret, text)

    def test_success_path_matches_and_cleans_up(self):
        result = self.run_harness()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn("creates on clio: UsrParityt1clioSetting", result.stdout)
        self.assertIn("creates on go: UsrParityt1goSetting", result.stdout)
        data = self.evidence_data()
        self.assertEqual(data["counts"]["match"], 1)
        self.assertEqual([s["verdict"] for s in data["scenarios"][0]["steps"]], ["match"] * 6)
        self.assertEqual(self.stand()["settings"], {})
        events = sorted((r["event"], r["side"]) for r in self.ledger())
        self.assertEqual(events, [("create", "clio"), ("create", "go"), ("removed", "clio"), ("removed", "go")])
        # Both objects are removed through clio, since the cleanup step says server: clio.
        self.assertNotIn("go:odata-delete", self.stand()["_calls"])
        self.assert_evidence_is_clean()

    def test_read_back_mismatch_is_reported_and_cleaned_up(self):
        result = self.run_harness(go_faults=("mangle-read",))
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        steps = {s["step"]: s for s in self.evidence_data()["scenarios"][0]["steps"]}
        self.assertEqual(steps["read"]["verdict"], "mismatch")
        self.assertEqual(steps["read"]["differences"], ["$.value: value differs"])
        self.assertEqual(self.evidence_data()["scenarios"][0]["verdict"], "mismatch")
        self.assertEqual(self.stand()["settings"], {})
        self.assert_evidence_is_clean()

    def test_failing_write_still_runs_cleanup(self):
        result = self.run_harness(go_faults=("fail-create",))
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        steps = self.evidence_data()["scenarios"][0]["steps"]
        self.assertEqual(steps[0]["verdict"], "mismatch")
        self.assertEqual([s["verdict"] for s in steps[1:4]], ["skipped"] * 3)
        cleanups = [s for s in steps if s["kind"] == "cleanup"]
        # clio's object is removed; go's was never created, so its lookup finds nothing and counts as gone.
        self.assertEqual([s["verdict"] for s in cleanups], ["match", "match"])
        self.assertEqual(self.stand()["settings"], {})
        self.assertNotIn("clio:get-sys-setting", self.stand()["_calls"])

    def test_write_failing_on_both_servers_is_not_a_green_run(self):
        result = self.run_harness(go_faults=("fail-create",), clio_faults=("fail-create",))
        self.assertEqual(result.returncode, 2, result.stdout + result.stderr)
        self.assertEqual(self.evidence_data()["scenarios"][0]["verdict"], "both-failed")
        self.assertIn("unreachable", result.stderr)

    def test_refuses_environment_not_in_allow_list(self):
        self.allow("someotherstand")
        result = self.run_harness()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("not listed", result.stderr)
        self.assertFalse(self.state.exists(), "no server may start before the gate")
        self.assertEqual(self.ledger(), [])

    def test_refuses_without_allow_list_file(self):
        (self.home / ".config/creatio-mcp-go/write-stands").unlink()
        result = self.run_harness()
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(self.state.exists())

    def test_refuses_without_confirmation_flag(self):
        result = self.run_harness(confirm=False)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("--i-understand-this-writes", result.stderr)
        self.assertFalse(self.state.exists())

    def test_refuses_destructive_call_on_object_not_created_by_run(self):
        scenario = json.loads(json.dumps(SCENARIO))
        scenario[0]["steps"].insert(2, {"kind": "write", "label": "foreign delete", "tool": "odata-delete",
                                        "target": "UsrForeign{side}", "args": {"entity": "SysSettings",
                                                                               "id": "UsrForeign", "confirm": True}})
        self.state.write_text(json.dumps({"settings": {"UsrForeign": {"id": "UsrForeign", "type": "ShortText",
                                                                      "value": ""}}}), encoding="utf-8")
        result = self.run_harness(scenarios=scenario)
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertIn("refused", result.stdout)
        self.assertEqual(list(self.stand()["settings"]), ["UsrForeign"])

    def test_cleanup_ledger_removes_leftovers_of_crashed_run(self):
        # A crashed run: both objects exist on the stand, the ledger has only their create records.
        self.state.write_text(json.dumps({"settings": {
            "UsrParityoldclioSetting": {"id": "aaaaaaaa-0000-0000-0000-000000000001", "type": "ShortText", "value": ""},
            "UsrParityoldgoSetting": {"id": "aaaaaaaa-0000-0000-0000-000000000002", "type": "ShortText", "value": ""},
            "UsrUnrelated": {"id": "aaaaaaaa-0000-0000-0000-000000000003", "type": "ShortText", "value": ""}}}),
            encoding="utf-8")
        cleanup = SCENARIO[0]["steps"][1]
        ledger = self.home / ".cache/creatio-mcp-go/write-ledger.jsonl"
        ledger.parent.mkdir(parents=True, exist_ok=True)
        records = []
        for side in ("clio", "go"):
            name = f"UsrParityold{side}Setting"
            concrete = json.loads(json.dumps(cleanup).replace("{run}", "old").replace("{side}", side))
            records.append({"event": "create", "environment": ENVIRONMENT, "run": "old", "side": side,
                            "name": name, "scenario": "x", "cleanup": [concrete]})
        records.append({"event": "create", "environment": "otherstand", "run": "old", "side": "clio",
                        "name": "UsrUnrelated", "scenario": "x", "cleanup": [cleanup]})
        ledger.write_text("".join(json.dumps(r) + "\n" for r in records), encoding="utf-8")

        result = self.run_harness("--cleanup-ledger")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(list(self.stand()["settings"]), ["UsrUnrelated"])
        removed = sorted(r["name"] for r in self.ledger() if r["event"] == "removed")
        self.assertEqual(removed, ["UsrParityoldclioSetting", "UsrParityoldgoSetting"])
        again = self.run_harness("--cleanup-ledger")
        self.assertIn("0 leftover", again.stdout)

    def test_cleanup_ledger_passes_the_same_gate(self):
        result = self.run_harness("--cleanup-ledger", confirm=False)
        self.assertNotEqual(result.returncode, 0)

    def test_repository_scenarios_are_valid(self):
        scenarios = sorted(str(p) for p in (SCRIPTS / "write-scenarios").glob("*.json"))
        self.assertTrue(scenarios)
        result = subprocess.run([sys.executable, str(HARNESS), "--clio-env", "none", "--plan-only",
                                 "--run-id", "plan", "--scenarios", *scenarios],
                                capture_output=True, text=True, env={**os.environ, "HOME": str(self.home)}, timeout=60)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn("skipped: create-sys-setting is not implemented", result.stdout)
        self.assertFalse(self.state.exists())


if __name__ == "__main__":
    unittest.main()
