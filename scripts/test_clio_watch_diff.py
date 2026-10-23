"""Self-tests for scripts/clio_watch_diff.py. No servers, no network.

Run from the repository root: python3 -m unittest discover -s scripts (or python3 -m pytest -q scripts)
"""
import copy, importlib.util, pathlib, unittest

SCRIPTS = pathlib.Path(__file__).resolve().parent
_spec = importlib.util.spec_from_file_location("clio_watch_diff", SCRIPTS / "clio_watch_diff.py")
watch = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(watch)


def inventory(version="8.1.0.142"):
    schema = {"type": "object", "properties": {"args": {"type": "object", "properties": {
        "environment-name": {"type": "string"}}, "required": ["environment-name"]}}, "required": ["args"]}
    contract = {"name": "get-page", "description": "Reads a page.", "input-schema": {
        "required": ["schema-name"], "properties": [{"name": "schema-name", "type": "string"}]}}
    return {
        "schema": 2, "clio": {"name": "clio", "version": version}, "generatedOn": "2026-10-20",
        "toolsList": [{"name": "get-page", "description": "Reads a page.", "inputSchema": schema,
                       "annotations": {"readOnlyHint": True}}],
        "prompts": [{"name": "p", "description": "a prompt"}],
        "resources": [{"name": "restart", "uri": "docs://help/restart"}, {"name": "flushdb", "uri": "docs://help/flushdb"},
                      {"name": "k", "uri": "docs://knowledge/lib/k", "description": "one"}],
        "resourceTemplates": [{"uriTemplate": "docs://help/command/{c}"}, {"uriTemplate": "docs://mcp/guides/{g}"}],
        "legend": f"clio {version} legend", "toolIndex": ["get-page (R): Reads a page."],
        "index": [{"name": "get-page", "purpose": "Reads a page.", "resident": True}],
        "contracts": {"get-page": contract, "get-tool-contract": {"name": "get-tool-contract", "description": "x"}},
        "shortContracts": {"get-page": {**contract, "detail": "short"}}, "defaultShort": [], "fullDetail": ["get-page"],
        "tasks": {"get-page": "T9 pages"},
    }


class ClioWatchDiffTest(unittest.TestCase):
    def test_identical_inventories_are_no_change(self):
        report = watch.compare(inventory(), inventory())
        self.assertFalse(report["changed"])
        self.assertEqual(report["info"], [])

    def test_served_marks_dates_version_and_order_only_drift_is_no_change(self):
        new = inventory("8.1.0.150")
        new["generatedOn"] = "2026-11-02"
        new["tasks"] = {"get-page": "done (served by creatio-mcp-go)"}
        new["resources"].reverse()
        new["resourceTemplates"].reverse()
        new["resources"][0]["description"] = "a newer knowledge library"
        report = watch.compare(inventory(), new)
        self.assertFalse(report["changed"], report)
        info = "\n".join(report["info"])
        self.assertIn("served marks", info)
        self.assertIn("knowledge library", info)
        self.assertIn("resources are listed in another order", info)

    def test_new_and_removed_tools_are_reported(self):
        new = inventory()
        new["index"].append({"name": "new-tool", "purpose": "New."})
        new["toolIndex"].append("new-tool: New.")
        new["contracts"]["new-tool"] = {"name": "new-tool", "description": "New."}
        del new["contracts"]["get-tool-contract"]
        report = watch.compare(inventory(), new)
        self.assertTrue(report["changed"])
        self.assertEqual(report["new_tools"], ["new-tool"])
        self.assertEqual(report["removed_tools"], ["get-tool-contract"])
        text = watch.body(report)
        self.assertIn("### New tools\n\n- `new-tool`", text)
        self.assertIn("### Removed tools\n\n- `get-tool-contract`", text)

    def test_changed_input_schema_names_the_properties(self):
        new = inventory()
        args = new["toolsList"][0]["inputSchema"]["properties"]["args"]
        args["properties"]["schema-name"] = {"type": "string"}
        args["required"] = ["environment-name", "schema-name"]
        new["contracts"]["get-page"]["input-schema"]["properties"][0]["type"] = "array"
        report = watch.compare(inventory(), new)
        self.assertTrue(report["changed"])
        schemas = "\n".join(report["schemas"])
        self.assertIn("`get-page` (tools/list): added `args.schema-name`", schemas)
        self.assertIn("`get-page` (contract): changed `schema-name`", schemas)

    def test_changed_description_and_annotations_are_reported(self):
        new = inventory()
        new["toolsList"][0]["description"] = "Reads a Freedom UI page."
        new["toolsList"][0]["annotations"] = {"readOnlyHint": False}
        report = watch.compare(inventory(), new)
        self.assertTrue(report["changed"])
        self.assertIn("`get-page` (tools/list): description, annotations", report["descriptions"])

    def test_body_lists_unexplained_contract_rows(self):
        evidence = {"counts": {"match": 3, "unexplained": 1}, "rows": [
            {"surface": "contract", "tool": "get-page", "verdict": "unexplained", "differences": ["$.description"]},
            {"surface": "index", "tool": "x", "verdict": "known:not-ported", "differences": []}]}
        text = watch.body(watch.compare(inventory(), inventory()), evidence, 1)
        self.assertIn("- contract: `get-page`: $.description", text)
        self.assertNotIn("`x`", text)


if __name__ == "__main__":
    unittest.main()
