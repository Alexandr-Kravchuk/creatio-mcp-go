"""Fake release source and fake MCP clients for the installer tests (test_install_sh.py, test_install_ps1.py).

The fake release has the layout scripts/build-release.sh produces: one archive per platform holding
creatio-mcp-go_<ver>_<os>_<arch>/creatio-mcp-go(.exe), and SHA256SUMS with `<hash>  <file>` lines.
The fake binary is a shell script that prints its tag for --version.
"""
import hashlib, http.server, io, json, os, pathlib, sys, tarfile, threading, zipfile

POSIX_PLATFORMS = ["darwin_arm64", "darwin_amd64", "linux_amd64", "linux_arm64"]


def fake_binary(tag):
    return f'#!/bin/sh\nif [ "$1" = --version ]; then echo {tag}; exit 0; fi\nexit 0\n'.encode()


def _tar(member, data):
    buf = io.BytesIO()
    with tarfile.open(fileobj=buf, mode="w:gz") as tar:
        folder = tarfile.TarInfo(member.split("/")[0])
        folder.type, folder.mode = tarfile.DIRTYPE, 0o755
        tar.addfile(folder)
        info = tarfile.TarInfo(member)
        info.size, info.mode = len(data), 0o755
        tar.addfile(info, io.BytesIO(data))
    return buf.getvalue()


def _zip(member, data):
    buf = io.BytesIO()
    with zipfile.ZipFile(buf, "w", zipfile.ZIP_DEFLATED) as archive:
        archive.writestr(member, data)
    return buf.getvalue()


class FakeReleases:
    """Serves <root>/api/releases.json (the GitHub list, newest first) and <root>/download/<tag>/<asset>."""

    def __init__(self, root):
        self.root = pathlib.Path(root)
        self.tags = []
        handler = lambda *a, **kw: _QuietHandler(*a, directory=str(self.root), **kw)
        self.server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), handler)
        self.base = f"http://127.0.0.1:{self.server.server_address[1]}"
        threading.Thread(target=self.server.serve_forever, daemon=True).start()

    @property
    def releases_api(self):
        return f"{self.base}/api/releases.json"

    @property
    def download_base(self):
        return f"{self.base}/download"

    def publish(self, tag, corrupt_sums=False):
        """Adds a release (it becomes the newest). corrupt_sums writes wrong hashes into SHA256SUMS."""
        folder = self.root / "download" / tag
        folder.mkdir(parents=True, exist_ok=True)
        version = tag[1:]
        lines = []
        for platform in POSIX_PLATFORMS + ["windows_amd64"]:
            stage = f"creatio-mcp-go_{version}_{platform}"
            if platform.startswith("windows"):
                name, body = f"{stage}.zip", _zip(f"{stage}/creatio-mcp-go.exe", fake_binary(tag))
            else:
                name, body = f"{stage}.tar.gz", _tar(f"{stage}/creatio-mcp-go", fake_binary(tag))
            (folder / name).write_bytes(body)
            digest = hashlib.sha256(body + (b"x" if corrupt_sums else b"")).hexdigest()
            lines.append(f"{digest}  {name}\n")
        (folder / "SHA256SUMS").write_text("".join(lines))
        self.tags.insert(0, tag)
        api = self.root / "api"
        api.mkdir(exist_ok=True)
        listing = [{"tag_name": t, "name": t, "draft": False, "prerelease": True} for t in self.tags]
        (api / "releases.json").write_text(json.dumps(listing, indent=2))

    def close(self):
        self.server.shutdown()
        self.server.server_close()


class _QuietHandler(http.server.SimpleHTTPRequestHandler):
    def log_message(self, *args):
        pass


STUB = r'''
import json, os, sys
client, args = sys.argv[1], sys.argv[2:]
home = os.environ["HOME"] if os.name != "nt" else os.environ["USERPROFILE"]
state_path = os.path.join(home, f".stub-{client}.json")
state = json.load(open(state_path)) if os.path.exists(state_path) else {}
with open(os.path.join(home, f".stub-{client}.log"), "a") as log:
    log.write(json.dumps(args) + "\n")
if args[:2] == ["mcp", "get"]:
    name = args[2]
    if name not in state:
        sys.stderr.write(f'No MCP server named "{name}".\n')
        sys.exit(1)
    if client == "claude":
        print(f"{name}:\n  Scope: User config (available in all your projects)\n  Status: Connected\n"
              f"  Type: stdio\n  Command: {state[name]}\n  Args: \n")
    else:
        print(f"{name}\n  enabled: true\n  transport: stdio\n  command: {state[name]}\n  args: -\n")
    sys.exit(0)
if args[:2] == ["mcp", "add"]:
    rest = [a for a in args[2:] if a not in ("--scope", "user")]
    sep = rest.index("--")
    state[rest[0]] = rest[sep + 1]
elif args[:2] == ["mcp", "remove"]:
    state.pop(args[2], None)
else:
    sys.exit(2)
json.dump(state, open(state_path, "w"))
'''


class FakeClients:
    """Writes `claude` and `codex` stubs into <folder>; their state lives in the (temporary) HOME."""

    def __init__(self, folder, home):
        self.folder, self.home = pathlib.Path(folder), pathlib.Path(home)
        self.folder.mkdir(parents=True, exist_ok=True)
        stub = self.folder / "mcp_client_stub.py"
        stub.write_text(STUB)
        for client in ("claude", "codex"):
            if os.name == "nt":
                (self.folder / f"{client}.cmd").write_text(f'@"{sys.executable}" -I "{stub}" {client} %*\r\n')
            else:
                path = self.folder / client
                path.write_text(f'#!/bin/sh\nexec "{sys.executable}" -I "{stub}" {client} "$@"\n')
                path.chmod(0o755)

    def state(self, client):
        path = self.home / f".stub-{client}.json"
        return json.loads(path.read_text()) if path.exists() else {}

    def seed(self, client, name, command):
        state = self.state(client)
        state[name] = command
        (self.home / f".stub-{client}.json").write_text(json.dumps(state))

    def calls(self, client):
        path = self.home / f".stub-{client}.log"
        return [json.loads(line) for line in path.read_text().splitlines()] if path.exists() else []

    def mutations(self, client):
        return [c for c in self.calls(client) if c[:2] in (["mcp", "add"], ["mcp", "remove"])]
