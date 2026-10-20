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


if __name__ == "__main__":
    unittest.main()
