package creatio

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckThemingAccessAsksRightsAndLicenseServices(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		switch r.URL.Path {
		case "/ServiceModel/AuthService.svc/Login":
			_, _ = w.Write([]byte(`{"Code":0}`))
		case "/0/rest/RightsService/GetCanExecuteOperation":
			if string(body) != `{"operation":"CanManageThemes"}` {
				t.Errorf("rights body = %s", body)
			}
			_, _ = w.Write([]byte(`{"GetCanExecuteOperationResult":true}`))
		case "/0/ServiceModel/LicenseService.svc/GetLicOperationStatuses":
			if string(body) != `{"licOperationCodes":["CanCustomizeBranding"]}` {
				t.Errorf("license body = %s", body)
			}
			_, _ = w.Write([]byte(`{"GetLicOperationStatusesResult":{"success":true,"licOperationStatuses":[{"Key":"cancustomizebranding","Value":true}]}}`))
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
		}
	}))
	defer server.Close()
	result := newFormsTestClient(t, server.URL).CheckThemingAccess(context.Background())
	if !result.Success || result.CanManageThemes == nil || !*result.CanManageThemes || result.CanCustomizeBranding == nil ||
		!*result.CanCustomizeBranding || result.ThemeServiceMinVersion != "10.0.0" || result.Error != "" {
		t.Fatalf("result = %#v", result)
	}
}

func TestCheckThemingAccessReportsAnEmptyResponseByRoute(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/Login") {
			_, _ = w.Write([]byte(`{"Code":0}`))
		}
	}))
	defer server.Close()
	result := newFormsTestClient(t, server.URL).CheckThemingAccess(context.Background())
	if result.Success || result.CanManageThemes != nil || result.Error != "Empty response from rest/RightsService/GetCanExecuteOperation." {
		t.Fatalf("result = %#v", result)
	}
}

func themeGetTestServer(t *testing.T, catalog, css string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ServiceModel/AuthService.svc/Login":
			_, _ = w.Write([]byte(`{"Code":0}`))
		case "/0/ServiceModel/ThemeService.svc/GetAvailableThemes":
			_, _ = w.Write([]byte(catalog))
		case "/0/conf/themes/dark.css":
			if r.Method != http.MethodGet || r.URL.RawQuery != "h=1" {
				t.Errorf("css request = %s %s", r.Method, r.URL)
			}
			_, _ = w.Write([]byte(css))
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
}

const themeGetTestCatalog = `{"success":true,"values":[{"id":"6f1b2c3d-0000-4000-8000-000000000001","caption":"Dark` + "\\u0007" +
	`","cssClassName":"dark","cssFilePath":"/conf/themes/dark.css?h=1"}]}`

func TestGetThemeReturnsCatalogFieldsAndCSSByParsedGUID(t *testing.T) {
	server := themeGetTestServer(t, themeGetTestCatalog, "\uFEFF.dark{color:#fff}😀")
	defer server.Close()
	result := newFormsTestClient(t, server.URL).GetTheme(context.Background(), "{6F1B2C3D-0000-4000-8000-000000000001}", "")
	if !result.Success || result.ID != "6f1b2c3d-0000-4000-8000-000000000001" || result.Caption != "Dark\a" ||
		result.CSSFilePath != "/conf/themes/dark.css?h=1" || result.CSSContent != ".dark{color:#fff}😀" ||
		result.CSSContentLength == nil || *result.CSSContentLength != 19 {
		t.Fatalf("result = %#v", result)
	}
}

func TestGetThemeWritesOutputFileOnceAndRefusesToOverwrite(t *testing.T) {
	server := themeGetTestServer(t, themeGetTestCatalog, ".dark{}")
	defer server.Close()
	client := newFormsTestClient(t, server.URL)
	target := filepath.Join(t.TempDir(), "nested", "theme.css")
	result := client.GetTheme(context.Background(), "6f1b2c3d-0000-4000-8000-000000000001", target)
	if !result.Success || result.CSSContent != "" || result.CSSContentLength == nil || *result.CSSContentLength != 7 {
		t.Fatalf("result = %#v", result)
	}
	written, err := os.ReadFile(target)
	if err != nil || string(written) != ".dark{}" {
		t.Fatalf("file = %q, err = %v", written, err)
	}
	again := client.GetTheme(context.Background(), "6f1b2c3d-0000-4000-8000-000000000001", target)
	if again.Success || !strings.Contains(again.Error, "already exists; refusing to overwrite it") || strings.Contains(again.Error, target) {
		t.Fatalf("second call = %#v", again)
	}
}

func TestGetThemeFailuresUseClioMessages(t *testing.T) {
	cases := []struct {
		name, catalog, css, id, output, want string
	}{
		{"bad id", "", "", "abc", "", "Theme id must be a GUID. Received: 'abc'."},
		{"outside", "", "", "6f1b2c3d-0000-4000-8000-000000000001", "/etc/theme.css",
			"output-file '[redacted-path]' resolves outside the allowed locations; it must be inside the workspace or the OS temp directory."},
		{"empty catalog", `{"success":true,"values":[]}`, "", "6f1b2c3d-0000-4000-8000-000000000002", "",
			"Theme '6f1b2c3d-0000-4000-8000-000000000002' was not found and no custom themes are listed on this environment. " + themeGetEmptyCatalogCaveat},
		{"not found", themeGetTestCatalog, "", "6f1b2c3d-0000-4000-8000-000000000002", "",
			"Theme '6f1b2c3d-0000-4000-8000-000000000002' was not found. Run 'clio list-themes' to see the available theme ids."},
		{"catalog failure", `{"success":false,"errorInfo":{"message":"Denied"}}`, "", "6f1b2c3d-0000-4000-8000-000000000001", "",
			"GetAvailableThemes failed: Denied"},
		{"html", themeGetTestCatalog, " <html>login</html>", "6f1b2c3d-0000-4000-8000-000000000001", "",
			"The environment returned an HTML page or a JSON error envelope instead of the theme CSS for '6f1b2c3d-0000-4000-8000-000000000001'. " +
				"The CSS file may be missing on the server or the request was redirected."},
		{"empty css", themeGetTestCatalog, "  ", "6f1b2c3d-0000-4000-8000-000000000001", "",
			"The environment served no content for the theme CSS of '6f1b2c3d-0000-4000-8000-000000000001' ('/conf/themes/dark.css?h=1'). " +
				"The file is missing, empty or unreadable on the server, or the request failed."},
		{"traversal", `{"success":true,"values":[{"id":"6f1b2c3d-0000-4000-8000-000000000001","cssFilePath":"../x.css"}]}`, "",
			"6f1b2c3d-0000-4000-8000-000000000001", "",
			"Theme '6f1b2c3d-0000-4000-8000-000000000001' reports an unexpected CSS file path in the theme catalog ('../x.css'); refusing to fetch it."},
	}
	for _, c := range cases {
		server := themeGetTestServer(t, c.catalog, c.css)
		result := newFormsTestClient(t, server.URL).GetTheme(context.Background(), c.id, c.output)
		server.Close()
		if result.Success || result.Error != c.want || result.ID != "" || result.CSSContentLength != nil {
			t.Errorf("%s: result = %#v\nwant %q", c.name, result, c.want)
		}
	}
}

func TestThemeGetOutputConfinementRefusesClioHomeAndSymlinkEscapes(t *testing.T) {
	clioHome := t.TempDir()
	t.Setenv("CLIO_HOME", clioHome)
	if _, err := themeGetResolveOutputFile(filepath.Join(clioHome, "x.css")); err == nil ||
		!strings.Contains(err.Error(), "inside clio's own configuration directory") {
		t.Fatalf("clio home err = %v", err)
	}
	link := filepath.Join(t.TempDir(), "escape")
	if err := os.Symlink("/etc", link); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	if _, err := themeGetResolveOutputFile(filepath.Join(link, "x.css")); err == nil || !strings.Contains(err.Error(), "resolves outside the allowed locations") {
		t.Fatalf("symlink err = %v", err)
	}
}
