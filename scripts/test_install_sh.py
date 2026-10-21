"""Runs scripts/install.sh against a local fake release source, with stub claude/codex and a temporary HOME.

Nothing here touches the real ~/.claude or ~/.codex: PATH holds only the stubs and system directories.
Run from the repository root: python3 -m pytest -q scripts
"""
import importlib.util, os, pathlib, shutil, subprocess, sys, tempfile, unittest

SCRIPTS = pathlib.Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("install_fixtures", SCRIPTS / "install_fixtures.py")
fixtures = importlib.util.module_from_spec(spec)
spec.loader.exec_module(fixtures)

INSTALL_SH = SCRIPTS / "install.sh"
SYSTEM_PATH = "/usr/bin:/bin:/usr/sbin:/sbin"


@unittest.skipIf(os.name == "nt", "install.sh is for macOS and Linux")
class InstallShTest(unittest.TestCase):
    shell = "sh"

    def setUp(self):
        self.tmp = pathlib.Path(tempfile.mkdtemp(prefix="install-sh-test-"))
        self.home = self.tmp / "home"
        self.home.mkdir()
        self.releases = fixtures.FakeReleases(self.tmp / "www")
        self.releases.publish("v0.2.0")
        self.releases.publish("v0.3.0")
        self.clients = fixtures.FakeClients(self.tmp / "stubs", self.home)
        self.bin_dir = self.home / ".local" / "bin"
        self.target = self.bin_dir / "creatio-mcp-go"

    def tearDown(self):
        self.releases.close()
        shutil.rmtree(self.tmp, ignore_errors=True)

    def run_install(self, *args, stubs=True, env=None):
        environment = {
            "HOME": str(self.home),
            "TMPDIR": str(self.tmp),
            "PATH": (f"{self.clients.folder}:" if stubs else "") + SYSTEM_PATH,
            "CREATIO_MCP_GO_RELEASES_API": self.releases.releases_api,
            "CREATIO_MCP_GO_DOWNLOAD_BASE": self.releases.download_base,
        }
        environment.update(env or {})
        return subprocess.run([self.shell, str(INSTALL_SH), *args], env=environment,
                              capture_output=True, text=True, timeout=60)

    def installed_tag(self):
        return subprocess.run([str(self.target), "--version"], capture_output=True, text=True).stdout.strip()

    def leftovers(self):
        return sorted(p.name for p in self.bin_dir.iterdir() if p.name != "creatio-mcp-go")

    def test_fresh_install_takes_newest_release_and_registers_both_clients(self):
        result = self.run_install()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.installed_tag(), "v0.3.0")
        self.assertTrue(os.access(self.target, os.X_OK))
        self.assertEqual(self.leftovers(), [])
        self.assertEqual(self.clients.state("claude"), {"creatio-go": str(self.target)})
        self.assertEqual(self.clients.state("codex"), {"creatio-go": str(self.target)})
        self.assertIn(["mcp", "add", "--scope", "user", "creatio-go", "--", str(self.target)],
                      self.clients.calls("claude"))
        self.assertIn(["mcp", "add", "creatio-go", "--", str(self.target)], self.clients.calls("codex"))
        self.assertIn("is not on PATH", result.stdout)
        self.assertIn("Done: creatio-mcp-go v0.3.0", result.stdout)

    def test_update_renames_new_binary_over_old_and_skips_registered_clients(self):
        self.assertEqual(self.run_install("--version", "v0.2.0").returncode, 0)
        self.assertEqual(self.installed_tag(), "v0.2.0")
        old_inode = self.target.stat().st_ino
        mutations_before = (len(self.clients.mutations("claude")), len(self.clients.mutations("codex")))

        result = self.run_install(env={"PATH": f"{self.clients.folder}:{self.bin_dir}:{SYSTEM_PATH}"})
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.installed_tag(), "v0.3.0")
        # A new inode means the file was replaced by rename: a running server keeps the old one.
        self.assertNotEqual(self.target.stat().st_ino, old_inode)
        self.assertEqual(self.leftovers(), [])
        self.assertEqual((len(self.clients.mutations("claude")), len(self.clients.mutations("codex"))),
                         mutations_before)
        self.assertIn("Claude Code: creatio-go is already registered", result.stdout)
        self.assertIn("Codex: creatio-go is already registered", result.stdout)
        self.assertNotIn("is not on PATH", result.stdout)

    def test_wrong_hash_is_refused_and_old_binary_kept(self):
        self.assertEqual(self.run_install("--version", "v0.2.0", "--no-register").returncode, 0)
        old_inode = self.target.stat().st_ino
        self.releases.publish("v0.4.0", corrupt_sums=True)

        result = self.run_install("--no-register")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("checksum mismatch", result.stderr)
        self.assertEqual(self.target.stat().st_ino, old_inode)
        self.assertEqual(self.installed_tag(), "v0.2.0")
        self.assertEqual(self.leftovers(), [])
        self.assertEqual(list(self.tmp.glob("creatio-mcp-go-install.*")), [])

    def test_no_register_leaves_clients_alone(self):
        result = self.run_install("--no-register")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.installed_tag(), "v0.3.0")
        self.assertEqual(self.clients.calls("claude"), [])
        self.assertEqual(self.clients.calls("codex"), [])
        self.assertIn("Registration skipped", result.stdout)

    def test_environment_variables_set_version_dir_name_and_no_register(self):
        other = self.tmp / "other bin"
        result = self.run_install(env={"CREATIO_MCP_GO_VERSION": "0.2.0", "CREATIO_MCP_GO_INSTALL_DIR": str(other),
                                       "CREATIO_MCP_GO_NO_REGISTER": "1"})
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(subprocess.run([str(other / "creatio-mcp-go"), "--version"],
                                        capture_output=True, text=True).stdout.strip(), "v0.2.0")
        self.assertEqual(self.clients.calls("claude"), [])

    def test_other_path_is_registered_again_and_custom_name_used(self):
        self.clients.seed("claude", "my-creatio", "/opt/old/creatio-mcp-go")
        self.clients.seed("codex", "my-creatio", "/opt/old/creatio-mcp-go")
        result = self.run_install("--name", "my-creatio", "--dir", str(self.bin_dir))
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.clients.state("claude"), {"my-creatio": str(self.target)})
        self.assertEqual(self.clients.state("codex"), {"my-creatio": str(self.target)})
        self.assertEqual(self.clients.mutations("claude")[0], ["mcp", "remove", "my-creatio", "--scope", "user"])
        self.assertEqual(self.clients.mutations("codex")[0], ["mcp", "remove", "my-creatio"])
        self.assertIn("not carried over", result.stdout)

    def test_without_clients_says_so(self):
        result = self.run_install(stubs=False)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("Neither claude nor codex was found", result.stdout)

    def test_unknown_version_fails_and_installs_nothing(self):
        result = self.run_install("--version", "v9.9.9")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("download failed", result.stderr)
        self.assertFalse(self.target.exists())

    def test_unknown_option_fails(self):
        result = self.run_install("--bogus")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("unknown option", result.stderr)

    @unittest.skipUnless(shutil.which("shellcheck"), "shellcheck is not installed")
    def test_shellcheck(self):
        result = subprocess.run(["shellcheck", "-s", "sh", str(INSTALL_SH)], capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)


DIST = os.environ.get("CREATIO_MCP_GO_TEST_DIST")  # "<dist dir>:<tag>" from scripts/build-release.sh


@unittest.skipIf(os.name == "nt" or not DIST, "set CREATIO_MCP_GO_TEST_DIST=<dist dir>:<tag> to install real archives")
class InstallRealArchiveTest(unittest.TestCase):
    """Installs the archive build-release.sh produced for this machine, so the asset names, the
    archive layout and SHA256SUMS the script expects are checked against the real build."""

    def test_install_from_build_release_output(self):
        dist, tag = DIST.rsplit(":", 1)
        tmp = pathlib.Path(tempfile.mkdtemp(prefix="install-sh-dist-"))
        self.addCleanup(shutil.rmtree, tmp, True)
        shutil.copytree(dist, tmp / "www" / "download" / tag)
        releases = fixtures.FakeReleases(tmp / "www")
        self.addCleanup(releases.close)
        home = tmp / "home"
        home.mkdir()
        result = subprocess.run(["sh", str(INSTALL_SH), "--version", tag, "--no-register"], capture_output=True, text=True,
                                timeout=120, env={"HOME": str(home), "TMPDIR": str(tmp), "PATH": SYSTEM_PATH,
                                                  "CREATIO_MCP_GO_DOWNLOAD_BASE": releases.download_base})
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        binary = home / ".local" / "bin" / "creatio-mcp-go"
        self.assertEqual(subprocess.run([str(binary), "--version"], capture_output=True, text=True).stdout.strip(), tag)


@unittest.skipUnless(shutil.which("dash"), "dash is not installed")
class InstallDashTest(InstallShTest):
    """The same cases under dash, the strictest common /bin/sh (Debian, Ubuntu)."""
    shell = "dash"


if __name__ == "__main__":
    unittest.main()
