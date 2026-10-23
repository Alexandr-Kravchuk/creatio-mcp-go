package browsersession

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The cache file name and its content clio 8.1.0.142 wrote for the same environment and login answer.
func TestCachePathAndStorageStateMatchClio(t *testing.T) {
	service := Service{Env: Environment{URI: "http://127.0.0.1:18930", Login: "Supervisor", Password: "pw"}, ClioHome: "/clio"}
	if got, want := service.CachePath(), filepath.Join("/clio", "sessions", "127.0.0.1-18930_21dc4404d282387a.storageState.json"); got != want {
		t.Fatalf("cache path = %s, want %s", got, want)
	}
	core := Service{Env: Environment{URI: "http://127.0.0.1:18937", Login: "Supervisor", Password: "pw", IsNetCore: true}, ClioHome: "/clio"}
	if got := filepath.Base(core.CachePath()); got != "127.0.0.1-18937_f9fcc01f1411bacb.storageState.json" {
		t.Fatalf("core cache file = %s", got)
	}

	logins, shells := 0, 0
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ServiceModel/AuthService.svc/Login":
			logins++
			w.Header().Add("Set-Cookie", ".ASPXAUTH=AUTHVALUE123; path=/; HttpOnly")
			w.Header().Add("Set-Cookie", "BPMCSRF=CSRF456; path=/")
			w.Header().Add("Set-Cookie", "BPMLOADER=LDR; path=/; HttpOnly")
			w.Header().Add("Set-Cookie", "UserName=83|117; path=/")
			w.Header().Add("Set-Cookie", "SecureOnly=x; path=/; secure")
			_, _ = w.Write([]byte(`{"Code":0}`))
		case "/0/Shell/":
			shells++
			if !strings.Contains(r.Header.Get("Cookie"), ".ASPXAUTH=AUTHVALUE123") {
				w.Header().Set("Location", "/Login/NuiLogin.aspx")
				w.WriteHeader(http.StatusFound)
				return
			}
			_, _ = w.Write([]byte("<html><body>shell</body></html>"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(site.Close)
	home := t.TempDir()
	service = Service{Env: Environment{URI: site.URL, Login: "Supervisor", Password: "pw"}, ClioHome: home}
	path, err := service.SessionPath(context.Background(), false)
	if err != nil || path != service.CachePath() {
		t.Fatalf("session path = %s, %v", path, err)
	}
	data, _ := os.ReadFile(path)
	want := `{"cookies":[{"name":".ASPXAUTH","value":"AUTHVALUE123","domain":"127.0.0.1","path":"/","httpOnly":true,"secure":false,"sameSite":"Lax","expires":-1},` +
		`{"name":"BPMCSRF","value":"CSRF456","domain":"127.0.0.1","path":"/","httpOnly":false,"secure":false,"sameSite":"Lax","expires":-1},` +
		`{"name":"BPMLOADER","value":"LDR","domain":"127.0.0.1","path":"/","httpOnly":true,"secure":false,"sameSite":"Lax","expires":-1},` +
		`{"name":"UserName","value":"83|117","domain":"127.0.0.1","path":"/","httpOnly":false,"secure":false,"sameSite":"Lax","expires":-1}],"origins":[]}`
	if string(data) != want {
		t.Fatalf("storageState\n got %s\nwant %s", data, want)
	}
	if info, _ := os.Stat(path); runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("session file mode = %v", info.Mode())
	}
	if _, err := service.SessionPath(context.Background(), false); err != nil || logins != 1 || shells != 1 {
		t.Fatalf("reuse: logins %d, shell checks %d, err %v", logins, shells, err)
	}
	if _, err := service.SessionPath(context.Background(), true); err != nil || logins != 2 || shells != 1 {
		t.Fatalf("force refresh: logins %d, shell checks %d, err %v", logins, shells, err)
	}
	if err := service.Clear(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("cleared session still exists: %v", err)
	}
	if err := service.Clear(); err != nil {
		t.Fatalf("clearing twice: %v", err)
	}
	oauth := Service{Env: Environment{URI: site.URL, ClientID: "c"}, ClioHome: home}
	if _, err := oauth.SessionPath(context.Background(), false); err == nil || !IsAuthenticationError(err) ||
		!strings.Contains(err.Error(), "requires forms-auth credentials (login + password)") {
		t.Fatalf("oauth-only: %v", err)
	}
}
