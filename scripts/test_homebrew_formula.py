"""Self-tests for scripts/homebrew-formula.py and scripts/update-homebrew-tap.sh. No network: the tap
is a local bare repository.

Run from the repository root: python3 -m pytest scripts -q
"""
import importlib.util
import os
import pathlib
import shutil
import subprocess
import tempfile
import unittest

SCRIPTS = pathlib.Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("homebrew_formula", SCRIPTS / "homebrew-formula.py")
homebrew_formula = importlib.util.module_from_spec(spec)
spec.loader.exec_module(homebrew_formula)

REPO = "Alexandr-Kravchuk/creatio-mcp-go"
PLATFORM_SHA = {
    "darwin_amd64": "a" * 64,
    "darwin_arm64": "b" * 64,
    "linux_amd64": "c" * 64,
    "linux_arm64": "d" * 64,
}


def sums_text(version="0.2.0", skip=()):
    lines = []
    for platform, digest in PLATFORM_SHA.items():
        if platform not in skip:
            lines.append(f"{digest}  creatio-mcp-go_{version}_{platform}.tar.gz")
    lines.append(f"{'e' * 64}  creatio-mcp-go_{version}_windows_amd64.zip")
    return "\n".join(lines) + "\n"


def block(formula, os_block, cpu_block):
    """Returns the text of `os_block do ... cpu_block do ... end` from the formula."""
    os_start = formula.index(f"  {os_block} do\n")
    os_end = formula.index("\n  end\n", os_start)
    cpu_start = formula.index(f"    {cpu_block} do\n", os_start, os_end)
    return formula[cpu_start:formula.index("\n    end", cpu_start)]


class FormulaTest(unittest.TestCase):
    def render(self, version="0.2.0", text=None):
        return homebrew_formula.render_formula(version, homebrew_formula.parse_sums(text or sums_text(version)))

    def test_every_platform_gets_its_own_url_and_sha(self):
        formula = self.render()
        expected = [
            ("on_macos", "on_arm", "darwin_arm64"),
            ("on_macos", "on_intel", "darwin_amd64"),
            ("on_linux", "on_arm", "linux_arm64"),
            ("on_linux", "on_intel", "linux_amd64"),
        ]
        for os_block, cpu_block, platform in expected:
            text = block(formula, os_block, cpu_block)
            url = f"https://github.com/{REPO}/releases/download/v0.2.0/creatio-mcp-go_0.2.0_{platform}.tar.gz"
            self.assertIn(f'url "{url}"', text, platform)
            self.assertIn(f'sha256 "{PLATFORM_SHA[platform]}"', text, platform)
        self.assertEqual(formula.count('url "'), 4)
        self.assertNotIn("windows", formula)

    def test_binary_install_test_and_caveats(self):
        formula = self.render()
        self.assertIn("class CreatioMcpGo < Formula", formula)
        self.assertIn('bin.install "creatio-mcp-go"', formula)
        self.assertNotIn("depends_on", formula)
        self.assertIn('assert_equal "v#{version}", shell_output("#{bin}/creatio-mcp-go --version").strip', formula)
        self.assertIn("claude mcp add --scope user creatio-go -- #{opt_bin}/creatio-mcp-go", formula)
        self.assertIn("codex mcp add creatio-go -- #{opt_bin}/creatio-mcp-go", formula)
        self.assertIn("appsettings.json", formula)
        # brew audit --strict rejects a version line that repeats the version scanned from the URL.
        self.assertNotIn('  version "', formula)

    def test_prerelease_version(self):
        formula = self.render("0.3.0-rc.1")
        self.assertIn("/releases/download/v0.3.0-rc.1/creatio-mcp-go_0.3.0-rc.1_darwin_arm64.tar.gz", formula)

    def test_missing_platform_is_refused_with_its_name(self):
        sums = homebrew_formula.parse_sums(sums_text(skip=("linux_arm64", "darwin_amd64")))
        with self.assertRaises(homebrew_formula.FormulaError) as raised:
            homebrew_formula.render_formula("0.2.0", sums)
        self.assertIn("creatio-mcp-go_0.2.0_linux_arm64.tar.gz", str(raised.exception))
        self.assertIn("creatio-mcp-go_0.2.0_darwin_amd64.tar.gz", str(raised.exception))

    def test_sums_of_another_version_are_refused(self):
        with self.assertRaises(homebrew_formula.FormulaError):
            homebrew_formula.render_formula("0.2.1", homebrew_formula.parse_sums(sums_text("0.2.0")))

    def test_malformed_sums_line_is_refused(self):
        with self.assertRaises(homebrew_formula.FormulaError):
            homebrew_formula.parse_sums("not-a-hash  creatio-mcp-go_0.2.0_linux_amd64.tar.gz\n")
        with self.assertRaises(homebrew_formula.FormulaError):
            homebrew_formula.parse_sums("only-one-field\n")

    def test_version_is_normalized(self):
        self.assertEqual(homebrew_formula.normalize_version("v0.2.0"), "0.2.0")
        self.assertEqual(homebrew_formula.normalize_version("0.2.0"), "0.2.0")
        self.assertEqual(homebrew_formula.normalize_version("v1.0.0-rc.2"), "1.0.0-rc.2")
        for bad in ("latest", "v1.2", "v1.2.3 extra"):
            with self.assertRaises(homebrew_formula.FormulaError):
                homebrew_formula.normalize_version(bad)

    def test_main_writes_formula_and_readme_and_fails_on_missing_platform(self):
        with tempfile.TemporaryDirectory() as tmp:
            tmp = pathlib.Path(tmp)
            sums = tmp / "SHA256SUMS"
            sums.write_text(sums_text())
            self.assertEqual(homebrew_formula.main(
                ["--version", "v0.2.0", "--sums", str(sums), "--out", str(tmp / "tap"), "--readme"]), 0)
            self.assertIn("on_macos do", (tmp / "tap" / "Formula" / "creatio-mcp-go.rb").read_text())
            self.assertIn("brew install alexandr-kravchuk/tap/creatio-mcp-go", (tmp / "tap" / "README.md").read_text())
            sums.write_text(sums_text(skip=("linux_amd64",)))
            self.assertEqual(homebrew_formula.main(
                ["--version", "v0.2.0", "--sums", str(sums), "--out", str(tmp / "other")]), 1)
            self.assertFalse((tmp / "other").exists())


@unittest.skipIf(shutil.which("git") is None or shutil.which("bash") is None, "git and bash are required")
class UpdateTapScriptTest(unittest.TestCase):
    def setUp(self):
        self.tmp = pathlib.Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, self.tmp, True)
        self.env = dict(os.environ)
        self.env.update({
            "GIT_AUTHOR_NAME": "Test", "GIT_AUTHOR_EMAIL": "test@example.com",
            "GIT_COMMITTER_NAME": "Test", "GIT_COMMITTER_EMAIL": "test@example.com",
            "GIT_CONFIG_GLOBAL": os.devnull, "GIT_CONFIG_NOSYSTEM": "1",
        })
        self.bare = self.tmp / "tap.git"
        self.git("init", "--quiet", "--bare", "--initial-branch=main", str(self.bare))
        seed = self.tmp / "seed"
        self.git("clone", "--quiet", str(self.bare), str(seed))
        (seed / "README.md").write_text("tap\n")
        self.git("-C", str(seed), "add", "README.md")
        self.git("-C", str(seed), "commit", "--quiet", "-m", "init")
        self.git("-C", str(seed), "push", "--quiet", "origin", "HEAD:main")
        self.sums = self.tmp / "SHA256SUMS"
        self.sums.write_text(sums_text())
        self.env["HOMEBREW_TAP_URL"] = self.bare.as_uri()

    def git(self, *args):
        return subprocess.run(["git", *args], check=True, capture_output=True, text=True, env=self.env).stdout

    def run_script(self, *args):
        return subprocess.run(["bash", str(SCRIPTS / "update-homebrew-tap.sh"), *args],
                              capture_output=True, text=True, env=self.env)

    def log(self):
        return self.git("--git-dir", str(self.bare), "log", "--format=%s", "main").splitlines()

    def test_dry_run_prints_formula_and_pushes_nothing(self):
        result = self.run_script("--dry-run", "v0.2.0", str(self.sums))
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("would clone", result.stdout)
        self.assertIn("class CreatioMcpGo < Formula", result.stdout)
        self.assertEqual(self.log(), ["init"])

    def test_update_commits_and_pushes_then_is_idempotent(self):
        result = self.run_script("v0.2.0", str(self.sums))
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.log(), ["creatio-mcp-go 0.2.0", "init"])
        formula = self.git("--git-dir", str(self.bare), "show", "main:Formula/creatio-mcp-go.rb")
        self.assertIn(f'sha256 "{PLATFORM_SHA["linux_arm64"]}"', formula)
        again = self.run_script("v0.2.0", str(self.sums))
        self.assertEqual(again.returncode, 0, again.stderr)
        self.assertIn("nothing to push", again.stdout)
        self.assertEqual(len(self.log()), 2)

    def test_unreachable_tap_exits_3(self):
        self.env["HOMEBREW_TAP_URL"] = (self.tmp / "missing.git").as_uri()
        result = self.run_script("v0.2.0", str(self.sums))
        self.assertEqual(result.returncode, 3, result.stderr)
        self.assertIn("not reachable", result.stderr)

    def test_missing_platform_pushes_nothing(self):
        self.sums.write_text(sums_text(skip=("darwin_arm64",)))
        result = self.run_script("v0.2.0", str(self.sums))
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("darwin_arm64", result.stderr)
        self.assertEqual(self.log(), ["init"])


if __name__ == "__main__":
    unittest.main()
