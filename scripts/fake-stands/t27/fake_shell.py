#!/usr/bin/env python3
"""A fake Creatio for page-smoke: a forms login that sets the session cookies, and a Freedom UI Shell stub page
whose DOM follows the contract page-smoke reads (crt-root, crt-navigation-schema-outlet, crt-button / crt-input with
element-name). A real headless browser opens it, so clio and creatio-mcp-go can be compared on what page-smoke reports.

    python3 scripts/fake-stands/t27/fake_shell.py serve 18927 <dir> &      # writes <dir>/appsettings.json (t27fake)
"""
import json, pathlib, sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

SHELL = """<!doctype html><html><head><title>Fake Creatio</title></head><body>
<crt-root>
  <crt-navigation-schema-outlet>
    <crt-label element-name="PageTitle">Order 42</crt-label>
    <crt-button element-name="SaveButton"><button>Save</button></crt-button>
    <crt-button element-name="SaveAndCloseButton"><button>Save</button></crt-button>
    <crt-button element-name="CloseButton"><button aria-label="Close"><mat-icon>close</mat-icon></button></crt-button>
    <crt-button element-name="RunButton"><button id="run">Run check</button></crt-button>
    <crt-button element-name="HiddenButton" style="display:none"><button>Hidden</button></crt-button>
    <crt-button element-name="ArchiveButton"><button disabled>Archive</button></crt-button>
    <crt-input element-name="Name"><label>Name</label><input value="Order 1" required></crt-input>
    <crt-number-input element-name="Amount"><label>Amount</label><input value="12.50" readonly></crt-number-input>
    <crt-checkbox element-name="Active"><label>Active</label><input type="checkbox" checked></crt-checkbox>
    <crt-password-input element-name="Secret"><label>Secret</label><input type="password" value="x"></crt-password-input>
  </crt-navigation-schema-outlet>
</crt-root>
<script>
console.error('Load failed: ' + 'widget', {a: 1});
console.error('Load failed: ' + 'widget', {a: 1});
document.getElementById('run').addEventListener('click', () => {
  console.error('Run failed: server said no');
  const toast = document.createElement('crt-notify');
  toast.textContent = 'The check could not start';
  document.body.appendChild(toast);
  history.pushState({}, '', location.pathname + '#Card/UsrX_FormPage/edit/after');
  setTimeout(() => { throw new Error('late failure'); }, 50);
});
</script></body></html>"""

LOGIN = "<!doctype html><html><body><a href='/Login/NuiLogin.aspx'>login</a><div id='loginEdit'></div></body></html>"


def serve(port, directory):
    directory = pathlib.Path(directory)
    directory.mkdir(parents=True, exist_ok=True)
    settings = {"Environments": {"t27fake": {"Uri": f"http://127.0.0.1:{port}", "Login": "example-user",
                                             "Password": "replace-me", "IsNetCore": False, "Safe": False}},
                "ActiveEnvironmentKey": "t27fake"}
    (directory / "appsettings.json").write_text(json.dumps(settings, indent=2), encoding="utf-8")

    class Handler(BaseHTTPRequestHandler):
        def log_message(self, *args):
            pass

        def reply(self, status, body, content_type, cookies=False):
            data = body.encode("utf-8")
            self.send_response(status)
            self.send_header("Content-Type", content_type)
            self.send_header("Content-Length", str(len(data)))
            if cookies:
                for cookie in (".ASPXAUTH=fakeauth; path=/; HttpOnly", "BPMCSRF=fakecsrf; path=/", "BPMLOADER=fakeloader; path=/; HttpOnly"):
                    self.send_header("Set-Cookie", cookie)
            self.end_headers()
            self.wfile.write(data)

        def do_POST(self):
            length = int(self.headers.get("Content-Length") or 0)
            self.rfile.read(length)
            if self.path.split("?")[0].endswith("/ServiceModel/AuthService.svc/Login"):
                return self.reply(200, json.dumps({"Code": 0, "Message": "", "Exception": None}), "application/json", cookies=True)
            return self.reply(404, "{}", "application/json")

        def do_GET(self):
            path = self.path.split("?")[0]
            if path in ("/0/Shell/", "/Shell/"):
                if ".ASPXAUTH=fakeauth" in (self.headers.get("Cookie") or ""):
                    return self.reply(200, SHELL, "text/html; charset=utf-8")
                return self.reply(200, LOGIN, "text/html; charset=utf-8")
            return self.reply(404, "not found", "text/plain")

    ThreadingHTTPServer(("127.0.0.1", port), Handler).serve_forever()


if __name__ == "__main__":
    if len(sys.argv) != 4 or sys.argv[1] != "serve":
        sys.exit(__doc__)
    serve(int(sys.argv[2]), sys.argv[3])
