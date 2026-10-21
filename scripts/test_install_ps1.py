"""Runs scripts/install.ps1 under pwsh against a local fake release source, with stub claude/codex
and a temporary HOME/USERPROFILE/LOCALAPPDATA.

Runs on Windows when pwsh is installed (the CI windows job). On macOS/Linux it runs only with
CREATIO_MCP_GO_TEST_PS1=1, and then sets CREATIO_MCP_GO_INSTALL_TEST=1 so the script skips its
Windows-only checks; the user PATH is never changed there. On a local Windows machine the user PATH
is left alone too (CREATIO_MCP_GO_NO_PATH_UPDATE=1); only GitHub Actions lets the script write it.
Run from the repository root: python3 -m pytest -q scripts
"""
import importlib.util, os, pathlib, shutil, subprocess, tempfile, unittest

SCRIPTS = pathlib.Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("install_fixtures", SCRIPTS / "install_fixtures.py")
fixtures = importlib.util.module_from_spec(spec)
spec.loader.exec_module(fixtures)

INSTALL_PS1 = SCRIPTS / "install.ps1"
PWSH = shutil.which("pwsh")
ON_WINDOWS = os.name == "nt"
ENABLED = PWSH is not None and (ON_WINDOWS or os.environ.get("CREATIO_MCP_GO_TEST_PS1") == "1")
ON_ACTIONS = os.environ.get("GITHUB_ACTIONS") == "true"


def ps_quote(value):
    return "'" + str(value).replace("'", "''") + "'"


@unittest.skipUnless(ENABLED, "needs pwsh, and Windows or CREATIO_MCP_GO_TEST_PS1=1")
class InstallPs1Test(unittest.TestCase):
    def setUp(self):
        # resolve(): long names on Windows, where TEMP is often an 8.3 path the script would not echo back.
        self.tmp = pathlib.Path(tempfile.mkdtemp(prefix="install-ps1-test-")).resolve()
        self.home = self.tmp / "home"
        self.home.mkdir()
        self.releases = fixtures.FakeReleases(self.tmp / "www")
        self.releases.publish("v0.2.0")
        self.releases.publish("v0.3.0")
        self.clients = fixtures.FakeClients(self.tmp / "stubs", self.home)
        self.local_app_data = self.home / "AppData" / "Local"
        self.bin_dir = self.local_app_data / "creatio-mcp-go" / "bin"
        self.target = self.bin_dir / "creatio-mcp-go.exe"

    def tearDown(self):
        self.releases.close()
        shutil.rmtree(self.tmp, ignore_errors=True)

    def environment(self, stubs=True, extra=None):
        env = dict(os.environ)
        if ON_WINDOWS:
            system_root = os.environ.get("SystemRoot", r"C:\Windows")
            system_path = f"{system_root}\\System32;{system_root};{system_root}\\System32\\WindowsPowerShell\\v1.0"
        else:
            system_path = "/usr/bin:/bin"
        env.update({
            "HOME": str(self.home),
            "USERPROFILE": str(self.home),
            "LOCALAPPDATA": str(self.local_app_data),
            "PATH": (str(self.clients.folder) + os.pathsep if stubs else "") + system_path,
            "CREATIO_MCP_GO_RELEASES_API": self.releases.releases_api,
            "CREATIO_MCP_GO_DOWNLOAD_BASE": self.releases.download_base,
        })
        for key in list(env):
            if key.startswith("CREATIO_MCP_GO_") and key not in ("CREATIO_MCP_GO_RELEASES_API", "CREATIO_MCP_GO_DOWNLOAD_BASE"):
                del env[key]
        if not ON_WINDOWS:
            env["CREATIO_MCP_GO_INSTALL_TEST"] = "1"
        elif not ON_ACTIONS:
            env["CREATIO_MCP_GO_NO_PATH_UPDATE"] = "1"
        env.update(extra or {})
        return env

    def run_ps(self, command, stubs=True, env=None):
        return subprocess.run([PWSH, "-NoProfile", "-NonInteractive", "-Command", command],
                              env=self.environment(stubs, env), capture_output=True, text=True, timeout=180)

    def run_file(self, *args, stubs=True, env=None):
        return subprocess.run([PWSH, "-NoProfile", "-NonInteractive", "-File", str(INSTALL_PS1), *args],
                              env=self.environment(stubs, env), capture_output=True, text=True, timeout=180)

    def installed_tag(self):
        body = self.target.read_bytes()
        for tag in ("v0.2.0", "v0.3.0", "v0.4.0"):
            if f"echo {tag};".encode() in body:
                return tag
        return None

    def leftovers(self):
        return sorted(p.name for p in self.bin_dir.iterdir() if p.name != "creatio-mcp-go.exe")

    def test_fresh_install_takes_newest_release_and_registers_both_clients(self):
        result = self.run_file()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(self.installed_tag(), "v0.3.0")
        self.assertEqual(self.leftovers(), [])
        self.assertEqual(self.clients.state("claude"), {"creatio-go": str(self.target)})
        self.assertEqual(self.clients.state("codex"), {"creatio-go": str(self.target)})
        self.assertIn("Done: creatio-mcp-go v0.3.0", result.stdout)
        if ON_WINDOWS and ON_ACTIONS:
            user_path = subprocess.run([PWSH, "-NoProfile", "-Command",
                                        "[Environment]::GetEnvironmentVariable('Path','User')"],
                                       capture_output=True, text=True).stdout
            self.assertIn(str(self.bin_dir).lower(), user_path.lower())

    def test_update_moves_old_binary_aside_and_skips_registered_clients(self):
        result = self.run_ps(f"& ([scriptblock]::Create((Get-Content -Raw -LiteralPath {ps_quote(INSTALL_PS1)}))) -Version v0.2.0")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(self.installed_tag(), "v0.2.0")
        old_inode = self.target.stat().st_ino
        mutations_before = (len(self.clients.mutations("claude")), len(self.clients.mutations("codex")))

        result = self.run_file()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(self.installed_tag(), "v0.3.0")
        self.assertNotEqual(self.target.stat().st_ino, old_inode)
        self.assertEqual(self.leftovers(), [])
        self.assertEqual((len(self.clients.mutations("claude")), len(self.clients.mutations("codex"))),
                         mutations_before)
        self.assertIn("Claude Code: creatio-go is already registered", result.stdout)
        self.assertIn("Codex: creatio-go is already registered", result.stdout)

    def test_wrong_hash_is_refused_and_old_binary_kept(self):
        self.assertEqual(self.run_file("-Version", "v0.2.0", "-NoRegister").returncode, 0)
        old_inode = self.target.stat().st_ino
        self.releases.publish("v0.4.0", corrupt_sums=True)

        result = self.run_file("-NoRegister")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("checksum mismatch", result.stdout + result.stderr)
        self.assertEqual(self.target.stat().st_ino, old_inode)
        self.assertEqual(self.installed_tag(), "v0.2.0")
        self.assertEqual(self.leftovers(), [])

    def test_iex_form_reads_environment_variables(self):
        other = self.tmp / "other bin"
        result = self.run_ps(f"Get-Content -Raw -LiteralPath {ps_quote(INSTALL_PS1)} | Invoke-Expression",
                             env={"CREATIO_MCP_GO_VERSION": "0.2.0", "CREATIO_MCP_GO_INSTALL_DIR": str(other),
                                  "CREATIO_MCP_GO_NAME": "my-creatio"})
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn("echo v0.2.0;", (other / "creatio-mcp-go.exe").read_text())
        self.assertEqual(self.clients.state("claude"), {"my-creatio": str(other / "creatio-mcp-go.exe")})

    def test_no_register_leaves_clients_alone(self):
        result = self.run_file("-NoRegister")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(self.clients.calls("claude"), [])
        self.assertEqual(self.clients.calls("codex"), [])

    def test_other_path_is_registered_again(self):
        self.clients.seed("claude", "creatio-go", "C:\\old\\creatio-mcp-go.exe")
        self.clients.seed("codex", "creatio-go", "C:\\old\\creatio-mcp-go.exe")
        result = self.run_file()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(self.clients.state("claude"), {"creatio-go": str(self.target)})
        self.assertEqual(self.clients.state("codex"), {"creatio-go": str(self.target)})
        self.assertIn("not carried over", result.stdout)

    def test_unknown_version_fails_and_installs_nothing(self):
        result = self.run_file("-Version", "v9.9.9")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("download failed", result.stdout + result.stderr)
        self.assertFalse(self.target.exists())


if __name__ == "__main__":
    unittest.main()
