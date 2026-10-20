#!/usr/bin/env python3
"""Run the T6 write tools on clio's MCP server and on this Go server side by side and compare what they do.

Every scenario starts both servers on their own fresh temporary clio home (CLIO_HOME, HOME, TMPDIR and
CLIO_TELEMETRY_HOME all point inside --work), seeded with the same appsettings.json: the file clio itself
wrote at its first start, with its automatic updates switched off. Nothing reaches the network or this
machine's real clio settings, knowledge cache or coding agents:
  - knowledge is published by a local fixture that serves signed test bundles as a GitHub release and as a
    NuGet v3 feed, and clio's curated-library bootstrap points at a closed loopback port;
  - telemetry uploads go to the same fixture;
  - the component registry is a local file (CLIO_COMPONENT_REGISTRY_LOCAL_FILE);
  - coding agents are stub CLIs on PATH and a local toolkit checkout.
The calls go through clio-run. Compared: each answer (paths, GUIDs and timestamps normalized), the
knowledge, knowledge-feedback and features sections of appsettings.json, and the files a scenario lists.

Needs: dotnet, git, Python's `cryptography` package. Usage:
  python3 scripts/compare-t6-writes.py --clio-dll <clio.dll> --go-bin <creatio-mcp-go> --work <scratch dir> \
      [--evidence <file>] [--scenario NAME ...]
"""
import argparse, hashlib, http.server, io, json, os, re, shutil, socket, subprocess, sys, threading, time
import urllib.parse, urllib.request, zipfile

KNOWN = {
    "git-transport": "a git-type source is installed by clio (git CLI); this server refuses with a pointer to clio",
    "clio-version": "telemetry's clio_version attribute names this server's build, not clio's",
}


# ------------------------------------------------------------------------------------------------ fixture

class Publisher:
    """Signed test bundles as a GitHub release (/repos/o/r/...) and a NuGet feed (/v3/index.json), plus an
    OTLP collector (/v1/logs). /control switches what is published."""

    def __init__(self, work):
        from cryptography.hazmat.primitives.asymmetric import ec
        from cryptography.hazmat.primitives import serialization
        self.dir = os.path.join(work, "fixture")
        os.makedirs(self.dir, exist_ok=True)
        self.key = ec.generate_private_key(ec.SECP256R1())
        self.public_key = os.path.join(self.dir, "public.pem")
        open(self.public_key, "wb").write(self.key.public_key().public_bytes(
            serialization.Encoding.PEM, serialization.PublicFormat.SubjectPublicKeyInfo))
        self.sequences = {"1.0.0": 1000, "1.1.0": 1001, "1.2.0": 1002}
        self.bundles = {v: self.bundle(v, "com.example.kb") for v in self.sequences}
        self.nu_bundles = {v: self.bundle(v, "com.example.nu") for v in self.sequences}
        self.state = {"latest": "1.0.0", "nuget": ["1.0.0"], "immutable": True, "post": 200}
        self.received = []
        with socket.socket() as probe:
            probe.bind(("127.0.0.1", 0))
            self.port = probe.getsockname()[1]
        self.base = f"http://127.0.0.1:{self.port}"
        server = http.server.ThreadingHTTPServer(("127.0.0.1", self.port), self.handler())
        threading.Thread(target=server.serve_forever, daemon=True).start()

    def bundle(self, version, library):
        from cryptography.hazmat.primitives.asymmetric import ec, utils
        from cryptography.hazmat.primitives import hashes
        texts = {"core-rules": f"core rules {version}\n", "routing": f"routing {version}\n",
                 "knowledge-feedback": f"feedback policy {version}\n"}
        resources = []
        for item, text in texts.items():
            data = text.encode()
            resources.append({"itemId": item, "topicId": "example." + item, "role": "guidance", "title": "Title " + item,
                              "description": "Description " + item, "uri": f"docs://knowledge/{library}/{item}",
                              "path": f"resources/{item}.md", "mediaType": "text/plain", "length": len(data),
                              "digest": hashlib.sha256(data).hexdigest()})
        manifest = {"contractVersion": "1.0.0", "bundleSchemaVersion": "1.0.0", "libraryId": library, "libraryVersion": version,
                    "sequence": self.sequences[version], "source": {"repository": "example/kb", "commit": "0" * 40},
                    "compatibility": {"clio": {"min": "8.1.0", "max": "8.1.999"}, "mcpToolContract": {"min": "1.1.0", "max": "1.1.0"}},
                    "requirements": {"tools": ["get-guidance"], "itemIds": list(texts),
                                     "resourceUris": [f"docs://knowledge/{library}/{i}" for i in texts]},
                    "digestAlg": "SHA-256", "signature": {"algorithm": "ECDSA-P256-SHA256", "keyId": "test-key"},
                    "resources": resources}
        manifest_bytes = json.dumps(manifest, separators=(",", ":")).encode()
        r, s = utils.decode_dss_signature(self.key.sign(manifest_bytes, ec.ECDSA(hashes.SHA256())))
        buffer = io.BytesIO()
        with zipfile.ZipFile(buffer, "w", zipfile.ZIP_DEFLATED) as archive:
            archive.writestr("manifest.json", manifest_bytes)
            archive.writestr("manifest.sig", r.to_bytes(32, "big") + s.to_bytes(32, "big"))
            for item, text in texts.items():
                archive.writestr(f"resources/{item}.md", text.encode())
        return buffer.getvalue()

    def nupkg(self, version):
        buffer = io.BytesIO()
        with zipfile.ZipFile(buffer, "w", zipfile.ZIP_DEFLATED) as archive:
            archive.writestr("example.kb.nuspec", "<package/>")
            archive.writestr("content/knowledge-bundle.zip", self.nu_bundles[version])
        return buffer.getvalue()

    def handler(self):
        publisher = self

        class Handler(http.server.BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass

            def reply(self, status, body=b"{}", content_type="application/json", headers=None):
                self.send_response(status)
                self.send_header("Content-Type", content_type)
                self.send_header("Content-Length", str(len(body)))
                for key, value in (headers or {}).items():
                    self.send_header(key, value)
                self.end_headers()
                self.wfile.write(body)

            def do_POST(self):
                body = self.rfile.read(int(self.headers.get("Content-Length") or 0))
                publisher.received.append({"path": self.path, "ingest": self.headers.get("X-Ingest-Key"),
                                           "body": json.loads(body or b"null")})
                self.reply(publisher.state["post"])

            def do_GET(self):
                url = urllib.parse.urlparse(self.path)
                query = urllib.parse.parse_qs(url.query)
                state = publisher.state
                if url.path == "/control":
                    if "reset" in query:
                        publisher.received.clear()
                    for key in ("latest", "post"):
                        if key in query:
                            state[key] = int(query[key][0]) if key == "post" else query[key][0]
                    if "nuget" in query:
                        state["nuget"] = [v for v in query["nuget"][0].split(",") if v]
                    if "immutable" in query:
                        state["immutable"] = query["immutable"][0] == "1"
                    return self.reply(200)
                if url.path == "/received":
                    return self.reply(200, json.dumps(publisher.received).encode())
                if url.path == "/repos/o/r/releases/latest":
                    version = state["latest"]
                    data = publisher.bundles[version]
                    release = {"tag_name": version, "draft": False, "prerelease": False, "immutable": state["immutable"],
                               "assets": [{"name": "b.zip", "state": "uploaded", "content_type": "application/zip",
                                           "size": len(data), "digest": "sha256:" + hashlib.sha256(data).hexdigest(),
                                           "url": f"{publisher.base}/assets/{version}/b.zip"}]}
                    return self.reply(200, json.dumps(release).encode(), headers={"ETag": f'"{version}"'})
                if url.path.startswith("/assets/"):
                    return self.reply(200, publisher.bundles[url.path.split("/")[2]], "application/octet-stream")
                if url.path == "/v3/index.json":
                    return self.reply(200, json.dumps({"resources": [{"@id": f"{publisher.base}/flat/",
                                                                      "@type": "PackageBaseAddress/3.0.0"}]}).encode())
                if url.path == "/flat/example.kb/index.json":
                    return self.reply(200, json.dumps({"versions": state["nuget"]}).encode())
                parts = url.path.split("/")
                if len(parts) == 5 and parts[1] == "flat" and parts[4].endswith(".nupkg"):
                    return self.reply(200, publisher.nupkg(parts[3]), "application/octet-stream")
                self.reply(404)

        return Handler


STUB_CLI = """#!/bin/sh
name=$(basename "$0")
echo "$*" >> "$HOME/$name.log"
state="$HOME/.$name-marketplace"
case "$1 $2 $3" in
"plugin marketplace remove")
  if [ -f "$state" ]; then rm "$state"; exit 0; fi
  echo "Marketplace 'creatio' not found" >&2; exit 1;;
"plugin marketplace add")
  if [ -f "$state" ]; then echo 'Error: marketplace "creatio" already registered' >&2; exit 1; fi
  echo "$4" > "$state"; exit 0;;
esac
exit 0
"""


def prepare_agents(work):
    stubs = os.path.join(work, "stubs")
    os.makedirs(stubs, exist_ok=True)
    for name in ("claude", "codex", "copilot"):
        path = os.path.join(stubs, name)
        open(path, "w").write(STUB_CLI)
        os.chmod(path, 0o755)
    repository = os.path.join(work, "toolkit-repo")
    shutil.rmtree(repository, ignore_errors=True)
    files = {".release-manifest.json": json.dumps({"plugin_runtime": ["AGENTS.md", "context", "runtime", "missing.txt"]}),
             "AGENTS.md": "agents\n", "context/INDEX.md": "idx\n", "runtime/scripts/mcp_client.py": "py\n",
             "runtime/scripts/__pycache__/x.pyc": "c"}
    for path, text in files.items():
        full = os.path.join(repository, path)
        os.makedirs(os.path.dirname(full), exist_ok=True)
        open(full, "w").write(text)
    identity = dict(os.environ, GIT_AUTHOR_NAME="t", GIT_AUTHOR_EMAIL="t@example.com", GIT_COMMITTER_NAME="t",
                    GIT_COMMITTER_EMAIL="t@example.com")
    for args in (["init", "-q"], ["add", "-A"], ["commit", "-qm", "fixture"]):
        subprocess.run(["git", *args], cwd=repository, env=identity, check=True)
    registry = os.path.join(work, "registry.json")
    open(registry, "w").write(json.dumps({"components": [
        {"componentType": "crt.Button", "inputs": {"caption": {"type": "string"}, "icon": {"deprecated": True}}},
        {"componentType": "crt.Label", "properties": {"caption": {}}}, {"componentType": "crt.Empty"}],
        "composites": [{"caption": "Card"}]}))
    return stubs, repository, registry


# ------------------------------------------------------------------------------------------------ servers

def run_calls(command, home, calls, env_extra, publisher):
    os.makedirs(os.path.join(home, "home"), exist_ok=True)
    os.makedirs(os.path.join(home, "tmp"), exist_ok=True)
    env = dict(os.environ)
    env.update({"HOME": os.path.join(home, "home"), "USERPROFILE": os.path.join(home, "home"), "CLIO_HOME": home,
                "CLIO_TELEMETRY_HOME": os.path.join(home, "telemetry"), "TMPDIR": os.path.join(home, "tmp"),
                "CLIO_KNOWLEDGE_CURATED_API_BASE_URL": "http://127.0.0.1:9/", "DOTNET_CLI_TELEMETRY_OPTOUT": "1",
                "CLIO_TELEMETRY_ENABLED": "false", "CLIO_COMPONENT_REGISTRY_CDN_BASE_URL": "http://127.0.0.1:9/"})
    env.update(env_extra)
    process = subprocess.Popen(command, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, env=env,
                               text=True, bufsize=1, encoding="utf-8", cwd=home)
    next_id = [0]

    def request(method, params):
        next_id[0] += 1
        process.stdin.write(json.dumps({"jsonrpc": "2.0", "id": next_id[0], "method": method, "params": params}) + "\n")
        process.stdin.flush()
        while True:
            line = process.stdout.readline()
            if not line:
                raise RuntimeError("server exited")
            try:
                message = json.loads(line)
            except json.JSONDecodeError:
                continue
            if message.get("id") == next_id[0]:
                return message

    request("initialize", {"protocolVersion": "2025-06-18", "capabilities": {}, "clientInfo": {"name": "t6", "version": "1"}})
    process.stdin.write(json.dumps({"jsonrpc": "2.0", "method": "notifications/initialized", "params": {}}) + "\n")
    process.stdin.flush()
    out = []
    try:
        for call in calls:
            if "sleep" in call:
                time.sleep(call["sleep"])
            elif "control" in call:
                urllib.request.urlopen(publisher.base + "/control?" + call["control"]).read()
            elif "received" in call:
                out.append({"label": "fixture received", "text": json.loads(urllib.request.urlopen(publisher.base + "/received").read())})
            elif "write" in call:
                target = os.path.join(home, call["write"])
                os.makedirs(os.path.dirname(target), exist_ok=True)
                open(target, "w", encoding="utf-8", newline="").write(call["content"])
            elif "mkdir" in call:
                os.makedirs(os.path.join(home, call["mkdir"]), exist_ok=True)
            elif "files" in call:
                listing = []
                for base, _, files in os.walk(os.path.join(home, call["files"])):
                    if "mcp-hosts" not in base:  # clio's own server-presence marker
                        listing += [os.path.relpath(os.path.join(base, f), home) for f in files]
                out.append({"label": "files " + call["files"], "text": sorted(listing)})
            elif "cat" in call:
                import glob
                for path in sorted(glob.glob(os.path.join(home, call["cat"]))):
                    raw = open(path, encoding="utf-8").read()
                    entry = {"label": "file", "text": raw}
                    try:  # JSON is compared as data (volatile members normalized) and as escaped text
                        entry = {"label": "file", "text": json.loads(raw),
                                 "escapes": sorted(set(re.findall(r"\\u[0-9A-Fa-f]{4}", raw)))}
                    except ValueError:
                        pass
                    out.append(entry)
            else:
                response = request("tools/call", {"name": "clio-run", "arguments": {"command": call["command"], "args": call["args"]}})
                result = response.get("result") or response.get("error") or {}
                text = (result.get("content") or [{}])[0].get("text")
                try:
                    text = json.loads(text)
                except (TypeError, ValueError):
                    pass
                out.append({"label": call["command"], "known": call.get("known"), "isError": bool(result.get("isError")), "text": text})
    finally:
        process.kill()
        process.wait()
    return out


VOLATILE_ATTRIBUTES = {"event_timestamp", "installation_id", "event_id", "duration_ms", "duration_since_session_start_ms"}
TIMESTAMP = re.compile(r"\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d+)?(?:Z|[+-]\d\d:\d\d|\\u002B\d\d:\d\d)")


def normalize(value, home, work):
    if isinstance(value, dict):
        if value.get("key") in VOLATILE_ATTRIBUTES:
            return {"key": value["key"], "value": "<volatile>"}
        if value.get("key") == "clio_version":
            return {"key": "clio_version", "value": "<known:clio-version>"}
        return {k: ("<nano>" if k in ("time_unix_nano", "timeUnixNano") else normalize(v, home, work))
                for k, v in value.items() if k != "correlation-id"}
    if isinstance(value, list):
        return [normalize(v, home, work) for v in value]
    if isinstance(value, str):
        value = value.replace(home, "<HOME>").replace(work, "<WORK>")
        value = re.sub(r"\b\d{8}T\d{9}Z_", "<STAMP>_", value)
        value = re.sub(r"(?<![0-9a-f])[0-9a-f]{32}(?![0-9a-f])", "<GUID>", value)
        return TIMESTAMP.sub("<TIME>", value)
    return value


# ------------------------------------------------------------------------------------------------ scenarios

def scenarios(publisher, stubs, repository, registry):
    key, fx = publisher.public_key, publisher.base
    source = {"alias": "ex", "libraryId": "com.example.kb", "type": "github-release", "location": "https://api.github.com",
              "repositoryOwner": "o", "repositoryName": "r", "assetName": "b.zip"}
    sources = [
        {"command": "install-knowledge", "args": {"source": "nope"}},
        {"command": "install-knowledge", "args": {"source": 5}},
        {"command": "delete-knowledge", "args": {"confirmed": "yes"}},
        {"command": "add-knowledge-source", "args": source},
        {"command": "add-knowledge-source", "args": {**source, "confirmed": True}},
        {"command": "add-knowledge-source", "args": {**source, "alias": "ex2", "confirmed": True}},
        {"command": "add-knowledge-source", "args": {"alias": "bad", "libraryId": "com.example.k2", "type": "svn", "location": "https://x", "confirmed": True}},
        {"command": "add-knowledge-source", "args": {"alias": "bad", "libraryId": "com.example.k2", "type": "nuget", "location": "ftp://x", "confirmed": True}},
        {"command": "add-knowledge-source", "args": {"alias": "bad", "libraryId": "com.example.k2", "type": "nuget", "location": "https://x/v3/index.json",
                                                     "packageId": "P", "trustedKeyId": "k", "trustedPublicKeyPath": "/nonexistent/key.pem", "confirmed": True}},
        {"command": "add-knowledge-source", "args": {"alias": "g1", "libraryId": "com.example.g1", "type": "git", "location": "https://example.com/r.git",
                                                     "branch": "", "priority": 5, "participation": "Isolated", "enabled": False, "confirmed": True}},
        {"command": "enable-knowledge-source", "args": {"alias": "g1"}},
        {"command": "disable-knowledge-source", "args": {"alias": "EX"}},
        {"command": "disable-knowledge-source", "args": {"alias": "missing"}},
        {"command": "enable-knowledge-source", "args": {}},
        {"command": "install-knowledge", "args": {"source": "g1"}, "known": "git-transport"},
        {"command": "update-knowledge", "args": {"source": "g1"}},
        {"command": "delete-knowledge", "args": {"source": "ex", "confirmed": True}},
        {"command": "remove-knowledge-source", "args": {"alias": "creatio-curated", "confirmed": True}},
        {"command": "remove-knowledge-source", "args": {"alias": "g1"}},
        {"command": "remove-knowledge-source", "args": {"alias": "g1", "confirmed": True}},
        {"command": "list-knowledge-sources", "args": {}},
        {"command": "configure-knowledge-feedback-policy", "args": {"mode": "auto"}},
        {"command": "configure-knowledge-feedback-policy", "args": {"mode": "auto", "confirmed": True}},
        {"command": "configure-knowledge-feedback-policy", "args": {"mode": "bogus"}},
        {"command": "configure-knowledge-feedback-policy", "args": {"mode": "off"}},
        {"command": "configure-knowledge-feedback-policy", "args": {"destination": "https://github.com/a/b.git", "reporting-scope": "FULL"}},
        {"command": "configure-knowledge-feedback-policy", "args": {"mode": "ask", "destination": "http://github.com/a/b"}},
    ]
    release = {"alias": "ex", "libraryId": "com.example.kb", "type": "github-release", "location": fx, "repositoryOwner": "o",
               "repositoryName": "r", "assetName": "b.zip", "trustedKeyId": "test-key", "trustedPublicKeyPath": key, "confirmed": True, "priority": 10}
    nuget = {"alias": "nu", "libraryId": "com.example.nu", "type": "nuget", "location": fx + "/v3/index.json", "packageId": "Example.KB",
             "trustedKeyId": "test-key", "trustedPublicKeyPath": key, "confirmed": True}
    install = [
        {"control": "latest=1.0.0&nuget=1.0.0&immutable=1"},
        {"command": "add-knowledge-source", "args": release}, {"command": "add-knowledge-source", "args": nuget},
        {"command": "update-knowledge", "args": {"source": "ex"}}, {"command": "install-knowledge", "args": {"source": "ex"}},
        {"command": "install-knowledge", "args": {"source": "ex"}}, {"command": "update-knowledge", "args": {"source": "ex"}},
        {"control": "latest=1.1.0"}, {"command": "update-knowledge", "args": {"source": "ex"}},
        {"command": "info-knowledge", "args": {"source": "ex"}}, {"command": "install-knowledge", "args": {"source": "nu"}},
        {"command": "info-knowledge", "args": {"checkUpdates": True}}, {"control": "nuget=1.0.0,1.1.0"},
        {"command": "info-knowledge", "args": {"source": "nu", "checkUpdates": True}}, {"command": "update-knowledge", "args": {"source": "nu"}},
        {"control": "latest=1.0.0"}, {"command": "update-knowledge", "args": {"source": "ex"}},
        {"control": "latest=1.2.0&immutable=0"}, {"command": "update-knowledge", "args": {"source": "ex"}},
        {"command": "get-guidance", "args": {"name": "core-rules"}}, {"command": "disable-knowledge-source", "args": {"alias": "ex"}},
        {"command": "update-knowledge", "args": {"source": "ex"}}, {"command": "delete-knowledge", "args": {"source": "ex", "confirmed": True}},
        {"command": "info-knowledge", "args": {"source": "ex"}}, {"command": "enable-knowledge-source", "args": {"alias": "ex"}},
        {"command": "install-knowledge", "args": {}}, {"command": "update-knowledge", "args": {}},
        {"command": "remove-knowledge-source", "args": {"alias": "nu", "confirmed": True}},
        {"command": "info-knowledge", "args": {}}, {"command": "list-knowledge-sources", "args": {}},
        {"files": "knowledge/sources/5312fb609f60384731fcfcb9"}, {"cat": "knowledge/sources/5312fb609f60384731fcfcb9/current.json"},
        {"cat": "knowledge/sources/.history/*.json"},
    ]
    telemetry = [
        {"control": "reset=1&post=200"}, {"command": "get-telemetry-consent", "args": {}},
        {"command": "send-telemetry", "args": {"session_id": "s1", "event_name": "workflow_started"}},
        {"command": "send-telemetry", "args": {"session_id": "s1", "event_name": "workflow_started", "telemetry_consent": "maybe"}},
        {"command": "send-telemetry", "args": {"session_id": "bad id!", "event_name": "workflow_started"}},
        {"command": "send-telemetry", "args": {"session_id": "s1", "event_name": "workflow_started", "workflow": "Bad Flow"}},
        {"command": "send-telemetry", "args": {"session_id": "s1", "event_name": "workflow_started", "zzz": 1, "extra": "x"}},
        {"command": "send-telemetry", "args": {"session_id": "s1", "event_name": "nope"}},
        {"command": "send-telemetry", "args": {"session_id": "", "event_name": "nope"}},
        {"command": "send-telemetry", "args": {"session_id": "s1", "event_name": "workflow_started", "coding_agent": "a" * 69}},
        {"command": "send-telemetry", "args": {"session_id": "s1", "event_name": "workflow_started", "duration_ms": -1}},
        {"command": "send-telemetry", "args": {"session_id": "s1", "event_name": "workflow_started", "input_tokens": -5}},
        {"command": "send-telemetry", "args": {"session_id": "s1", "event_name": "workflow_started", "input_tokens": 1.5}},
        {"command": "send-telemetry", "args": {"session_id": 7, "event_name": "workflow_started"}},
        {"command": "send-telemetry", "args": {}},
        {"command": "send-telemetry", "args": {"session_id": "s1", "event_name": "workflow_started", "telemetry_consent": "granted",
                                               "workflow": "app-creation", "coding_agent": "Claude Code", "model": "claude-opus-5",
                                               "input_tokens": 10, "plugin_version": "1.2.3"}},
        {"command": "get-telemetry-consent", "args": {}}, {"sleep": 1},
        {"command": "send-telemetry", "args": {"session_id": "s1", "event_name": "plan_presented", "workflow": "app-creation"}},
        {"sleep": 4}, {"received": True}, {"files": "telemetry"}, {"control": "post=503"},
        {"command": "send-telemetry", "args": {"session_id": "s1", "event_name": "workflow_completed", "workflow": "app-creation", "variant": "full"}},
        {"sleep": 12}, {"files": "telemetry"}, {"cat": "telemetry/events/*.json"}, {"cat": "telemetry/sessions/*.json"},
        {"command": "withdraw-telemetry-consent", "args": {}}, {"files": "telemetry"},
        {"command": "send-telemetry", "args": {"session_id": "s1", "event_name": "workflow_started", "telemetry_consent": "granted"}},
        {"command": "withdraw-telemetry-consent", "args": {}}, {"command": "get-telemetry-consent", "args": {}},
    ]
    experimental = [{"command": "experimental", "args": args} for args in (
        {}, {"name": "my-flag", "enable": True}, {"name": "  Ring ", "enable": True}, {"name": "knowledge-allow-unsequenced", "enable": True},
        {"name": "knowledge-allow-unsequenced", "disable": True}, {"name": "my-flag"}, {"name": "my-flag", "enable": True, "disable": True},
        {"enable": True}, {"disable": True}, {"name": "a-very-long-feature-name-that-goes-past-forty-characters-for-sure", "disable": True},
        {"name": 5}, {"enable": "yes"}, {"name": "x", "enable": True, "bogus": 1}, {})]
    registry_calls = [{"command": "export-component-registry", "args": args} for args in (
        {}, {}, {"version": "8.3.4"}, {"version": " 8.3 ", "schema-type": "mobile"}, {"version": "x"},
        {"version": "8.3.4", "environment-name": "dev"}, {"schemaType": "mobile", "zzz": 1},
        {"version": "8.3.4", "output-file": "out/reg.json"}, {"version": "8.3.4", "output-file": "out/reg.json"},
        {"output-file": "/etc/clio-t6-parity.json"}, {"schema-type": "tablet", "version": "8.3.4"}, {"version": 8})]
    registry_calls += [{"files": ".clio-migration"}, {"files": "out"}, {"cat": ".clio-migration/component-registry/8.3.4.json"}]
    settings = {"theme": "dark", "emoji": "\u2713 <tag> & 'q' +1",
                "extraKnownMarketplaces": {"creatio": {"source": {"source": "directory", "path": "/x"}, "autoUpdate": False}, "other": {"a": 1.50}}}
    toml = ('model = "o3"\n\n[marketplaces.creatio]\nurl = "x"\n\n[plugins."creatio-ai-app-development-toolkit@creatio"]\nenabled = true\n\n'
            '[[skills.config]]\nname = "creatio-ai-app-development-toolkit:creatio-app-orchestrator"\nenabled = false\n\n'
            '[[skills.config]]\nname = "keep"\n\n[profiles.x]\nmodel = "y"')
    toolkit = [{"command": "install-toolkit", "args": {}}] + [{"mkdir": f"home/.{a}"} for a in ("claude", "codex", "cursor", "copilot")] + [
        {"write": "home/.claude/settings.json", "content": json.dumps(settings, ensure_ascii=False, indent=4)},
        {"write": "home/.codex/config.toml", "content": toml},
        {"write": "home/.agents/plugins/marketplace.json", "content": json.dumps({"name": "creatio", "plugins": [
            {"name": "creatio-ai-app-development-toolkit"}, {"name": "other"}]})},
        {"mkdir": "home/.agents/plugins/creatio-ai-app-development-toolkit/x"},
        {"command": "install-toolkit", "args": {"target": "bogus"}}, {"command": "install-toolkit", "args": {"repo": "-x"}},
        {"command": "install-toolkit", "args": {"repo": "ftp://x"}}, {"command": "install-toolkit", "args": {"repo": "ext::sh"}},
        {"command": "install-toolkit", "args": {"target": " Claude "}}, {"command": "install-toolkit", "args": {"repo": repository}},
        {"cat": "home/.claude/settings.json"}, {"cat": "home/.codex/config.toml"}, {"cat": "home/.agents/plugins/marketplace.json"},
        {"cat": "home/.cursor/mcp.json"}, {"cat": "home/.cursor/rules/*.mdc"}, {"cat": "home/.clio/managed-skills.json"},
        {"files": "home/.cursor"}, {"files": "home/.agents"},
        {"command": "update-toolkit", "args": {"repo": repository}}, {"command": "update-toolkit", "args": {"target": "copilot"}},
        {"cat": "home/*.log"}, {"command": "delete-toolkit", "args": {"target": "codex"}},
        {"command": "delete-toolkit", "args": {"repo": "ignored"}}, {"cat": "home/.claude/settings.json"},
        {"files": "home/.cursor"}, {"files": "home/.clio"},
        {"command": "install-toolkit", "args": {"target": "cursor", "repo": "/nonexistent/repo/x.git"}},
        {"command": "install-toolkit", "args": {"target": 5}},
    ]
    collector = {"CLIO_TELEMETRY_ENABLED": "true", "CLIO_TELEMETRY_ENDPOINT": fx + "/v1/logs", "CLIO_TELEMETRY_INGEST_KEY": "k1"}
    local_registry = {"CLIO_COMPONENT_REGISTRY_LOCAL_FILE": registry, "CLIO_MOBILE_COMPONENT_REGISTRY_LOCAL_FILE": registry}
    agents_path = {"PATH": os.pathsep.join([stubs, os.path.dirname(shutil.which("dotnet")), "/usr/bin", "/bin"])}
    return {"knowledge-sources": (sources, {}), "knowledge-install": (install, {}), "telemetry": (telemetry, collector),
            "experimental": (experimental, {}), "export-component-registry": (registry_calls, local_registry),
            "toolkit": (toolkit, agents_path)}


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--clio-dll", required=True)
    parser.add_argument("--go-bin", required=True)
    parser.add_argument("--work", required=True, help="scratch directory; every home and fixture lives here")
    parser.add_argument("--evidence")
    parser.add_argument("--scenario", nargs="*")
    args = parser.parse_args()
    work = os.path.realpath(args.work)
    os.makedirs(work, exist_ok=True)
    publisher = Publisher(work)
    stubs, repository, registry = prepare_agents(work)
    servers = {"clio": ["dotnet", args.clio_dll, "mcp-server"], "go": [os.path.realpath(args.go_bin)]}
    summary = {"schema": 1, "known-differences": KNOWN, "scenarios": {}}
    unexplained = 0
    for name, (calls, env_extra) in scenarios(publisher, stubs, repository, registry).items():
        if args.scenario and name not in args.scenario:
            continue
        base = os.path.join(work, "homes", name)
        shutil.rmtree(base, ignore_errors=True)
        seed = os.path.join(base, "seed")
        run_calls(servers["clio"], seed, [], env_extra, publisher)  # clio's first start writes the seed settings
        settings = json.load(open(os.path.join(seed, "appsettings.json")))
        settings.get("knowledge", {}).pop("root-path", None)
        for target in ("clio", "knowledge", "toolkit"):
            settings.setdefault("autoupdate", {}).setdefault(target, {})["enabled"] = False
        answers, sections = {}, {}
        for side, command in servers.items():
            home = os.path.join(base, side)
            os.makedirs(home)
            json.dump(settings, open(os.path.join(home, "appsettings.json"), "w"), indent=2)
            answers[side] = normalize(run_calls(command, home, calls, env_extra, publisher), home, work)
            written = json.load(open(os.path.join(home, "appsettings.json")))
            written.get("knowledge", {}).pop("root-path", None)  # clio's startup bootstrap persists it
            sections[side] = {key: written.get(key) for key in ("knowledge", "knowledge-feedback", "features")}
        rows = []
        for left, right in zip(answers["clio"], answers["go"]):
            verdict = "match" if left == right else ("known:" + left["known"] if left.get("known") else "unexplained")
            rows.append({"label": left["label"], "verdict": verdict})
            if verdict == "unexplained":
                unexplained += 1
                print(f"[{name}] {left['label']}\n  clio: {json.dumps(left)[:600]}\n  go:   {json.dumps(right)[:600]}")
        settings_verdict = "match" if sections["clio"] == sections["go"] else "unexplained"
        unexplained += settings_verdict == "unexplained"
        rows.append({"label": "appsettings.json sections", "verdict": settings_verdict})
        counts = {}
        for row in rows:
            counts[row["verdict"]] = counts.get(row["verdict"], 0) + 1
        summary["scenarios"][name] = {"counts": counts, "rows": rows}
        print(f"{name}: {counts}")
    if args.evidence:
        json.dump(summary, open(args.evidence, "w"), indent=1)
    print(f"unexplained: {unexplained}")
    return 1 if unexplained else 0


if __name__ == "__main__":
    sys.exit(main())
