"""Self-tests for scripts/compare-contracts.py and scripts/clio-inventory.py helpers. No servers, no network.

Run from the repository root: python3 -m unittest discover -s scripts
"""
import importlib.util, pathlib, unittest

SCRIPTS = pathlib.Path(__file__).resolve().parent


def load(name, file):
    spec = importlib.util.spec_from_file_location(name, SCRIPTS / file)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


contracts = load("compare_contracts", "compare-contracts.py")
inventory = load("clio_inventory", "clio-inventory.py")

READ_ONLY = {"destructiveHint": False, "idempotentHint": True, "openWorldHint": False, "readOnlyHint": True}


def tool(name, **extra):
    return {"name": name, "description": f"{name} tool", "inputSchema": {"type": "object"}, "annotations": READ_ONLY, **extra}


class CompareContractsTest(unittest.TestCase):
    def test_tools_list_matches_and_reports_unported_tools_as_known(self):
        rows = contracts.compare_tools_list([tool("a"), tool("b"), tool("c")], [tool("a"), tool("c")], {"b": "T9 pages"})
        verdicts = {(r["tool"], r["verdict"], r["task"]) for r in rows}
        self.assertIn(("b", "known:not-ported", "T9 pages"), verdicts)
        self.assertIn(("a", "match", ""), verdicts)
        self.assertNotIn("unexplained", {r["verdict"] for r in rows})

    def test_tools_list_reports_order_schema_and_extra_tools(self):
        changed = tool("c", inputSchema={"type": "object", "required": ["args"]})
        rows = contracts.compare_tools_list([tool("a"), tool("c")], [changed, tool("a"), tool("z")], {})
        unexplained = {r["tool"] for r in rows if r["verdict"] == "unexplained"}
        self.assertEqual(unexplained, {"(order)", "c", "z"})

    def test_a_false_hint_omitted_by_the_go_sdk_is_not_a_difference(self):
        clio = tool("get-page", annotations={**READ_ONLY, "readOnlyHint": False})
        go = tool("get-page", annotations={"destructiveHint": False, "idempotentHint": True, "openWorldHint": False})
        self.assertEqual(contracts.compare_tools_list([clio], [go], {})[0]["verdict"], "match")
        go_wrong = tool("get-page", annotations={"destructiveHint": True, "idempotentHint": True, "openWorldHint": False})
        self.assertEqual(contracts.compare_tools_list([clio], [go_wrong], {})[0]["verdict"], "unexplained")

    def test_index_entries_compare_by_name(self):
        entry = {"name": "a", "purpose": "p", "contract-available": True, "resident": False, "destructive": False}
        rows = contracts.compare_index([entry, {**entry, "name": "b"}], [{**entry, "resident": True}], {})
        self.assertEqual({(r["tool"], r["verdict"]) for r in rows}, {("a", "unexplained"), ("b", "known:not-ported")})

    def test_unknown_name_tolerates_only_different_suggestions(self):
        def answer(suggestions, code="tool-not-found"):
            error = {"code": code, "message": "m", "suggestions": suggestions}
            return {"success": False, "error": error, "not-found": [{"name": "x", "error": error}]}
        self.assertEqual(contracts.compare_not_found(answer(["a"]), answer(["a"]))["verdict"], "match")
        self.assertEqual(contracts.compare_not_found(answer(["a"]), answer(["b"]))["verdict"], "known:suggestions")
        self.assertEqual(contracts.compare_not_found(answer(["a"]), answer(["a"], "other"))["verdict"], "unexplained")

    def test_detail_full_compares_the_served_curated_contracts(self):
        clio = [{"name": "a", "x": 1}, {"name": "b", "x": 2}]
        self.assertEqual(contracts.compare_full(clio, [{"name": "a", "x": 1}], {"a"})["verdict"], "match")
        self.assertEqual(contracts.compare_full(clio, [{"name": "a", "x": 3}], {"a"})["verdict"], "unexplained")


class InventoryTest(unittest.TestCase):
    def test_tasks_bucket_done_assigned_and_unassigned(self):
        self.assertEqual(inventory.task_of("create-page", set()), "T9 pages")
        self.assertEqual(inventory.task_of("create-page", {"create-page"}), inventory.DONE)
        self.assertEqual(inventory.task_of("brand-new-clio-tool", set()), inventory.UNASSIGNED)

    def test_every_tool_is_assigned_to_one_task_at_most(self):
        names = [name for task in inventory.TASKS.values() for name in task]
        self.assertEqual(len(names), len(set(names)))

    def test_appendix_replaces_only_the_appendix(self):
        plan = "# Plan\n\ntext\n\n## Appendix: old\n\nold rows\n"
        data = {"clio": {"version": "1.2"}, "toolsList": [{"name": "a"}],
                "index": [{"name": "a"}, {"name": "create-page"}, {"name": "zz-new"}]}
        result = inventory.replace_appendix(plan, inventory.appendix(data, {"a"}))
        self.assertTrue(result.startswith("# Plan\n\ntext\n\n" + inventory.APPENDIX_HEADING))
        self.assertNotIn("old rows", result)
        self.assertIn("| done (served by creatio-mcp-go) | 1 | `a` |", result)
        self.assertIn("| T9 pages | 1 | `create-page` |", result)
        self.assertIn(f"| {inventory.UNASSIGNED} | 1 | `zz-new` |", result)


if __name__ == "__main__":
    unittest.main()
