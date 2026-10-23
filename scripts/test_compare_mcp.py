"""Self-tests for scripts/compare-mcp.py helpers. No network.

Run from the repository root: python3 -m pytest scripts -q
"""
import importlib.util, pathlib, unittest

SCRIPTS = pathlib.Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("compare_mcp", SCRIPTS / "compare-mcp.py")
compare_mcp = importlib.util.module_from_spec(spec)
spec.loader.exec_module(compare_mcp)


class KnownDifferenceTest(unittest.TestCase):
    def test_exact_path(self):
        known = {"$.checkedutc"}
        self.assertTrue(compare_mcp.known_difference("$.checkedutc: value differs", known))
        self.assertFalse(compare_mcp.known_difference("$.checkedutcx: value differs", known))

    def test_any_index(self):
        known = {"$.compilationhistory[*].finishedsecondsago"}
        self.assertTrue(compare_mcp.known_difference("$.compilationhistory[0].finishedsecondsago: value differs", known))
        self.assertTrue(compare_mcp.known_difference("$.compilationhistory[12].finishedsecondsago: value differs", known))
        self.assertFalse(compare_mcp.known_difference("$.compilationhistory[0].finishedutc: value differs", known))
        self.assertFalse(compare_mcp.known_difference("$.compilationhistory.finishedsecondsago: value differs", known))


class CaseKnownDifferencesTest(unittest.TestCase):
    CASE = {"known-differences": ["$.checkedutc"], "known-differences-variables": ["$.executionlogmessages[0].value"]}

    def test_name_mode_keeps_only_the_listed_paths(self):
        self.assertEqual(compare_mcp.case_known_differences(self.CASE, "name"), {"$.checkedutc"})
        self.assertEqual(compare_mcp.case_known_differences({}, "name"), set())

    def test_variables_mode_adds_the_environment_name_and_the_mode_paths(self):
        self.assertEqual(compare_mcp.case_known_differences(self.CASE, "variables"),
                         {"$.checkedutc", "$.environmentname", "$.executionlogmessages[0].value"})
        self.assertEqual(compare_mcp.case_known_differences({}, "variables"), {"$.environmentname"})


class ClioToolsDirTest(unittest.TestCase):
    def test_tool_path_of_a_dll_inside_a_tool_store(self):
        import tempfile
        with tempfile.TemporaryDirectory() as temp:
            tools = pathlib.Path(temp).resolve() / "clio142"
            dll = tools / ".store/clio/8.1.0.142/clio/8.1.0.142/tools/net10.0/any/clio.dll"
            self.assertEqual(compare_mcp.clio_tools_dir(str(dll)), str(tools))
            env = compare_mcp.with_clio_tools({"A": "1"}, str(dll))
            self.assertEqual(env, {"A": "1", compare_mcp.CLIO_TOOLS_VARIABLE: str(tools)})

    def test_no_tool_store_leaves_the_environment(self):
        self.assertIsNone(compare_mcp.clio_tools_dir("/opt/clio/clio.dll"))
        self.assertIsNone(compare_mcp.clio_tools_dir(None))
        self.assertEqual(compare_mcp.with_clio_tools({"A": "1"}, "/opt/clio/clio.dll"), {"A": "1"})

if __name__ == "__main__":
    unittest.main()
