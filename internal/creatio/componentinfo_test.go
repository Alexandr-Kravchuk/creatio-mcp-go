package creatio

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const componentInfoTestRegistry = `{
  "components": [
    {"componentType": "crt.TabContainer", "description": "Single tab within a TabPanel.", "compositeOnly": true,
     "inputs": {"caption": {"type": "string"}}},
    {"componentType": "crt.Input", "description": "Generic text input.",
     "inputs": {"id": {"type": "ButtonIcon | string"}, "label": {"type": "string", "description": "Caption Unrelated"}},
     "outputs": {"valueChange": {"type": "RequestBindingConfig"}},
     "references": {"docs": ["docs/input.component.md"], "typeDefinitions": {"ButtonIcon": {"fields": {"icon": {"type": "IconName"}}}}},
     "whenToUse": "Use for short text.", "appliesToCustomEntities": false},
    {"componentType": "crt.Gallery", "description": "Gallery list component.", "synonyms": ["photo grid"]},
    {"componentType": "crt.Button", "description": "Clickable action element."}
  ],
  "composites": [
    {"caption": "Next steps", "description": "Upcoming activities.", "docs": ["docs/next-steps.component.md"]},
    {"caption": "Expanded list", "description": "Related records in an expandable group.", "docs": ["docs/expanded-list.component.md"]}
  ],
  "references": {
    "baseInputs": {"id": {"type": "unknown"}, "classes": {"type": "array"}},
    "typeDefinitions": {"RequestBindingConfig": {"fields": {"params": {"type": "Record", "valueType": "RequestParams"}}},
      "RequestParams": {"fields": {}}, "IconName": {"values": ["A"]}, "Unrelated": {"fields": {}}}
  }
}`

const componentInfoTestMobileRegistry = `{"components": [{"componentType": "crt.Toggle", "inputs": {"value": {"type": "boolean"}}}]}`

// componentInfoTestCDN serves registries and docs per version and counts the requests per path.
type componentInfoTestCDN struct {
	mu       sync.Mutex
	hits     map[string]int
	files    map[string]string
	statuses map[string]int
	server   *httptest.Server
}

func componentInfoNewTestCDN(t *testing.T, files map[string]string) *componentInfoTestCDN {
	t.Helper()
	cdn := &componentInfoTestCDN{hits: map[string]int{}, files: files, statuses: map[string]int{}}
	cdn.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cdn.mu.Lock()
		cdn.hits[r.URL.Path]++
		status, forced := cdn.statuses[r.URL.Path]
		body, ok := cdn.files[r.URL.Path]
		cdn.mu.Unlock()
		switch {
		case forced:
			w.WriteHeader(status)
		case !ok:
			http.NotFound(w, r)
		default:
			w.Header().Set("ETag", `"abc-1"`)
			w.Header().Set("Last-Modified", "Fri, 02 Oct 2026 02:05:02 GMT")
			_, _ = w.Write([]byte(body))
		}
	}))
	t.Cleanup(cdn.server.Close)
	home := t.TempDir()
	t.Setenv("CLIO_HOME", home)
	t.Setenv(componentInfoCDNBaseURLVariable, cdn.server.URL+"/api/mcp/")
	t.Setenv(componentInfoWebFlavor.localVariable, "")
	t.Setenv(componentInfoMobileFlavor.localVariable, "")
	previousDelay := componentInfoDelay
	componentInfoDelay = func(ctx context.Context, _ time.Duration) bool { return ctx.Err() == nil }
	t.Cleanup(func() {
		componentInfoBackground.Wait()
		componentInfoDelay = previousDelay
	})
	return cdn
}

func (c *componentInfoTestCDN) count(path string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hits[path]
}

func (c *componentInfoTestCDN) set(path string, status int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.statuses[path] = status
}

func componentInfoTestFiles() map[string]string {
	return map[string]string{
		"/api/mcp/latest/ComponentRegistry.json":          componentInfoTestRegistry,
		"/api/mcp/8.3.3/ComponentRegistry.json":           componentInfoTestRegistry,
		"/api/mcp/latest/MobileComponentRegistry.json":    componentInfoTestMobileRegistry,
		"/api/mcp/latest/docs/input.component.md":         "# Input",
		"/api/mcp/latest/docs/expanded-list.component.md": "# Expanded list",
		"/api/mcp/latest/docs/next-steps.component.md":    "# Next steps",
		"/api/mcp/8.3.3/docs/input.component.md":          "# Input 8.3.3",
	}
}

func componentInfoEncode(t *testing.T, value any) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}

func TestComponentInfoRegistryCachesCDNAndReusesItWithinTTL(t *testing.T) {
	cdn := componentInfoNewTestCDN(t, componentInfoTestFiles())
	first, err := componentInfoFetchRegistry(context.Background(), componentInfoWebFlavor, "latest")
	if err != nil || first.source != componentInfoSourceCDN || first.resolvedVersion != "latest" {
		t.Fatalf("first fetch = %v %v", first.source, err)
	}
	root := filepath.Join(os.Getenv("CLIO_HOME"), "cache", "component-registry")
	meta, err := os.ReadFile(filepath.Join(root, "latest.meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"sourceUrl":"` + cdn.server.URL + `/api/mcp/latest/ComponentRegistry.json"`, `"etag":"\u0022abc-1\u0022"`,
		`"lastModified":"2026-10-02T02:05:02+00:00"`, `"contentSha256":"`} {
		if !strings.Contains(string(meta), want) {
			t.Fatalf("meta %s lacks %s", meta, want)
		}
	}
	second, err := componentInfoFetchRegistry(context.Background(), componentInfoWebFlavor, "latest")
	if err != nil || second.source != componentInfoSourceCache || string(second.content) != componentInfoTestRegistry {
		t.Fatalf("second fetch = %v %v", second.source, err)
	}
	if hits := cdn.count("/api/mcp/latest/ComponentRegistry.json"); hits != 1 {
		t.Fatalf("CDN hits = %d, want 1 (the fresh cache serves the second call)", hits)
	}
	mobile, err := componentInfoFetchRegistry(context.Background(), componentInfoMobileFlavor, "latest")
	if err != nil || mobile.source != componentInfoSourceCDN || !componentInfoFileExists(filepath.Join(root, "mobile", "latest.json")) {
		t.Fatalf("mobile fetch = %v %v", mobile.source, err)
	}
}

func TestComponentInfoRegistryReadsClioWrittenCacheAndRefreshesStaleInBackground(t *testing.T) {
	cdn := componentInfoNewTestCDN(t, componentInfoTestFiles())
	root := filepath.Join(os.Getenv("CLIO_HOME"), "cache", "component-registry")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	stale := `{"fetchedAt":"2026-10-03T18:14:26.775329+00:00","expiresAt":"2026-10-03T18:19:26.775329+00:00","sourceUrl":"x","etag":"\u0022e\u0022","contentSha256":"00"}`
	_ = os.WriteFile(filepath.Join(root, "latest.json"), []byte(`[{"componentType":"crt.Old"}]`), 0o644)
	_ = os.WriteFile(filepath.Join(root, "latest.meta.json"), []byte(stale), 0o644)
	result, err := componentInfoFetchRegistry(context.Background(), componentInfoWebFlavor, "latest")
	if err != nil || result.source != componentInfoSourceCache || !strings.Contains(string(result.content), "crt.Old") {
		t.Fatalf("stale read = %v %v %s", result.source, err, result.content)
	}
	componentInfoBackground.Wait()
	if cdn.count("/api/mcp/latest/ComponentRegistry.json") != 1 {
		t.Fatal("a stale entry must be refreshed in the background")
	}
	refreshed, _ := os.ReadFile(filepath.Join(root, "latest.json"))
	if string(refreshed) != componentInfoTestRegistry {
		t.Fatalf("background refresh did not rewrite the cache: %s", refreshed)
	}
	_ = os.WriteFile(filepath.Join(root, "latest.meta.json"), []byte("{broken"), 0o644)
	if cached := componentInfoRegistryStore(componentInfoWebFlavor).read("latest"); cached != nil || componentInfoFileExists(filepath.Join(root, "latest.json")) {
		t.Fatal("a corrupt sidecar must remove both cache files")
	}
}

func TestComponentInfoRegistryFallsBackToCachedLatestWhenCDNFails(t *testing.T) {
	cdn := componentInfoNewTestCDN(t, componentInfoTestFiles())
	if _, err := componentInfoFetchRegistry(context.Background(), componentInfoWebFlavor, "latest"); err != nil {
		t.Fatal(err)
	}
	cdn.set("/api/mcp/8.4.0/ComponentRegistry.json", http.StatusBadGateway)
	result, err := componentInfoFetchRegistry(context.Background(), componentInfoWebFlavor, "8.4.0")
	if err != nil || result.source != componentInfoSourceCache || result.resolvedVersion != "latest" {
		t.Fatalf("fallback = %v %q %v", result.source, result.resolvedVersion, err)
	}
	if hits := cdn.count("/api/mcp/8.4.0/ComponentRegistry.json"); hits != componentInfoCDNAttempts {
		t.Fatalf("a 5xx is retried: hits = %d", hits)
	}
	if _, err := componentInfoFetchRegistry(context.Background(), componentInfoWebFlavor, "8.5.0"); err != nil || cdn.count("/api/mcp/8.5.0/ComponentRegistry.json") != 1 {
		t.Fatalf("a 4xx is not retried: hits = %d, err = %v", cdn.count("/api/mcp/8.5.0/ComponentRegistry.json"), err)
	}
	cdn.set("/api/mcp/latest/MobileComponentRegistry.json", http.StatusNotFound)
	_, err = componentInfoFetchRegistry(context.Background(), componentInfoMobileFlavor, "latest")
	want := "Component registry version 'latest' is unavailable: file cache is empty and the CDN at '" + cdn.server.URL +
		"/api/mcp/' could not be reached. Set CLIO_MOBILE_COMPONENT_REGISTRY_LOCAL_FILE to a local registry JSON"
	if err == nil || !strings.HasPrefix(err.Error(), want) {
		t.Fatalf("unavailable error = %v", err)
	}
}

func TestComponentInfoListAndSearch(t *testing.T) {
	componentInfoNewTestCDN(t, componentInfoTestFiles())
	response := componentInfoEncode(t, ComponentInfoGet(context.Background(), ComponentInfoRequest{}))
	items := response["items"].([]any)
	if response["success"] != true || response["mode"] != "list" || response["count"] != float64(4) || len(items) != 4 ||
		items[0].(map[string]any)["componentType"] != "crt.Button" || items[3].(map[string]any)["componentType"] != "crt.TabContainer" {
		t.Fatalf("list = %v", response)
	}
	if items[3].(map[string]any)["compositeOnly"] != true || response["composites"].([]any)[0].(map[string]any)["caption"] != "Expanded list" {
		t.Fatalf("list flags = %v", response)
	}
	if response["resolvedFrom"] != "latest-fallback" || response["requiresVersionConfirmation"] != true ||
		response["resolvedFromReason"] != "no-active-environment" || response["versionWarning"] != componentInfoLatestFallbackWarning {
		t.Fatalf("version markers = %v", response)
	}
	searched := componentInfoEncode(t, ComponentInfoGet(context.Background(), ComponentInfoRequest{ComponentType: "LIST", Search: "photo"}))
	if searched["count"] != float64(1) || searched["composites"] != nil {
		t.Fatalf("search = %v", searched)
	}
	unrelated := componentInfoEncode(t, ComponentInfoGet(context.Background(), ComponentInfoRequest{Search: "unrelated"}))
	if unrelated["count"] != float64(1) {
		t.Fatalf("search reaches binding descriptions: %v", unrelated)
	}
}

func TestComponentInfoDetailMergesBindingsTypesAndDocs(t *testing.T) {
	componentInfoNewTestCDN(t, componentInfoTestFiles())
	response := ComponentInfoGet(context.Background(), ComponentInfoRequest{ComponentType: " crt.input ", SchemaType: "moblie"})
	encoded, _ := json.Marshal(response)
	text := string(encoded)
	for _, want := range []string{`"mode":"detail"`, `"componentType":"crt.Input"`,
		`"inputs":{"id":{"type":"ButtonIcon | string"},"classes":{"type":"array"},"label":`,
		`"dataSourceBindingContract":"This is a data-source-bound field component. Standard field components`,
		`"documentation":"# Input"`, `"documentationSource":"cdn"`, `"whenToUse":"Use for short text.","appliesToCustomEntities":false`,
		`"schemaTypeWarning":"Unrecognized schema-type 'moblie'.`} {
		if !strings.Contains(text, want) {
			t.Fatalf("detail lacks %s:\n%s", want, text)
		}
	}
	var types []string
	for _, binding := range response.References.TypeDefinitions {
		types = append(types, binding.Key)
	}
	if strings.Join(types, ",") != "ButtonIcon,IconName,RequestBindingConfig,RequestParams" {
		t.Fatalf("type closure = %v", types)
	}
	again := ComponentInfoGet(context.Background(), ComponentInfoRequest{ComponentType: "crt.Input"})
	if *again.DocumentationSource != "cache" {
		t.Fatalf("second detail documentation source = %s", *again.DocumentationSource)
	}
	tab := componentInfoEncode(t, ComponentInfoGet(context.Background(), ComponentInfoRequest{ComponentType: "crt.TabContainer"}))
	if tab["compositeOnly"] != true || tab["compositeOnlyHint"] != componentInfoCompositeOnlyHint || tab["documentationSource"] != nil {
		t.Fatalf("composite-only detail = %v", tab)
	}
}

func TestComponentInfoCompositeModes(t *testing.T) {
	componentInfoNewTestCDN(t, componentInfoTestFiles())
	composite := componentInfoEncode(t, ComponentInfoGet(context.Background(), ComponentInfoRequest{Composite: " expanded LIST "}))
	if composite["mode"] != "composite" || composite["caption"] != "Expanded list" || composite["documentation"] != "# Expanded list" {
		t.Fatalf("composite = %v", composite)
	}
	missing := componentInfoEncode(t, ComponentInfoGet(context.Background(), ComponentInfoRequest{Composite: "Nope"}))
	if missing["success"] != false || missing["mode"] != "composite" ||
		missing["error"] != "Composite 'Nope' was not found (known composites: 'Expanded list', 'Next steps'). Omit 'composite' and use list mode to see every composite with its description." {
		t.Fatalf("missing composite = %v", missing)
	}
	mobile := componentInfoEncode(t, ComponentInfoGet(context.Background(), ComponentInfoRequest{Composite: "Nope", SchemaType: "Mobile"}))
	if !strings.Contains(mobile["error"].(string), "composites are a web-only Designer feature") {
		t.Fatalf("mobile composite = %v", mobile)
	}
	both := componentInfoEncode(t, ComponentInfoGet(context.Background(), ComponentInfoRequest{Composite: "Expanded list", ComponentType: "crt.Input"}))
	if both["success"] != false || both["mode"] != "list" || both["resolvedFrom"] != "latest-fallback" ||
		!strings.HasPrefix(both["error"].(string), "'composite' and 'component-type' are mutually exclusive.") {
		t.Fatalf("both = %v", both)
	}
	routed := componentInfoEncode(t, ComponentInfoGet(context.Background(), ComponentInfoRequest{ComponentType: "Expanded list"}))
	if routed["count"] != float64(0) || !strings.Contains(routed["error"].(string), `REQUIRED: call get-component-info composite="Expanded list"`) ||
		len(routed["composites"].([]any)) != 1 {
		t.Fatalf("routed = %v", routed)
	}
}

func TestComponentInfoUnknownTypeSuggestions(t *testing.T) {
	componentInfoNewTestCDN(t, componentInfoTestFiles())
	closest := componentInfoEncode(t, ComponentInfoGet(context.Background(), ComponentInfoRequest{ComponentType: "crt.Buton"}))
	items := closest["items"].([]any)
	if closest["success"] != false || closest["count"] != float64(4) || items[0].(map[string]any)["componentType"] != "crt.Button" ||
		!strings.HasPrefix(closest["error"].(string), "'crt.Buton' is not a component type and does not match any composite. Showing the 4 closest") {
		t.Fatalf("closest = %v", closest)
	}
	named := componentInfoEncode(t, ComponentInfoGet(context.Background(), ComponentInfoRequest{ComponentType: "gallery"}))
	if named["count"] != float64(1) || !strings.HasPrefix(named["error"].(string), "'gallery' is not a component type. Showing 1 component(s) matching 'gallery'") ||
		len(named["composites"].([]any)) != 2 {
		t.Fatalf("named = %v", named)
	}
}

func TestComponentInfoVersionValidationAndResolution(t *testing.T) {
	cdn := componentInfoNewTestCDN(t, componentInfoTestFiles())
	resolved := false
	both := componentInfoEncode(t, ComponentInfoGet(context.Background(), ComponentInfoRequest{Version: "8.3.3", EnvironmentName: "dev",
		Resolve: func() (*Client, error) { resolved = true; return nil, nil }}))
	if resolved || both["error"] != "'version' and 'environment-name'/'uri' are mutually exclusive. Pass one or neither." || both["items"] == nil {
		t.Fatalf("version and environment = %v", both)
	}
	bad := componentInfoEncode(t, ComponentInfoGet(context.Background(), ComponentInfoRequest{Version: "abc"}))
	if bad["error"] != "'version' value 'abc' is not a valid platform version. Use a 3-part semver, for example '8.3.3'." {
		t.Fatalf("bad version = %v", bad)
	}
	exact := componentInfoEncode(t, ComponentInfoGet(context.Background(), ComponentInfoRequest{Version: "8.3.3", ComponentType: "crt.Input"}))
	if exact["resolvedFrom"] != "environment" || exact["resolvedTargetVersion"] != "8.3.3" || exact["versionWarning"] != nil ||
		exact["documentation"] != "# Input 8.3.3" {
		t.Fatalf("explicit version = %v", exact)
	}
	superset := componentInfoEncode(t, ComponentInfoGet(context.Background(), ComponentInfoRequest{Version: " 8.3 "}))
	if superset["resolvedFrom"] != "environment-superset" || superset["resolvedTargetVersion"] != "latest" ||
		superset["versionWarning"] != componentInfoEnvironmentSupersetWarning || cdn.count("/api/mcp/8.3/ComponentRegistry.json") != 1 {
		t.Fatalf("unpublished version = %v", superset)
	}
	failed := componentInfoEncode(t, ComponentInfoGet(context.Background(), ComponentInfoRequest{EnvironmentName: "missing", SchemaType: "x",
		Resolve: func() (*Client, error) { return nil, os.ErrNotExist }}))
	if failed["success"] != false || failed["error"] != "file does not exist" || failed["schemaTypeWarning"] == nil {
		t.Fatalf("resolution failure = %v", failed)
	}
	for input, want := range map[string]string{"8.3": "8.3.0", " 8.3.3.1234 ": "8.3.3", "+8.3.1": "8.3.1"} {
		if got, ok := componentInfoThreePartVersion(input); !ok || got != want {
			t.Errorf("%q -> %q %v", input, got, ok)
		}
	}
	for _, input := range []string{"8", "8.-1", "8.3.3.3.3", "8..3", "x.1", "8.2147483648"} {
		if _, ok := componentInfoThreePartVersion(input); ok {
			t.Errorf("%q must be rejected", input)
		}
	}
}

func TestComponentInfoProbesEnvironmentVersion(t *testing.T) {
	componentInfoNewTestCDN(t, componentInfoTestFiles())
	cases := map[string]struct {
		status        int
		appInfo       string
		sysInfo       string
		resolvedFrom  string
		reason        string
		targetVersion string
	}{
		"application info":  {http.StatusOK, `{"applicationInfo":{"sysValues":{"coreVersion":"8.3.3.1520"}}}`, `{}`, "environment", "", "8.3.3"},
		"cliogate fallback": {http.StatusOK, `{}`, `{"SysInfo":{"CoreVersion":"8.3.3.1"}}`, "environment", "", "8.3.3"},
		"unpublished":       {http.StatusOK, `{"applicationInfo":{"sysValues":{"coreVersion":"8.1.0.1"}}}`, `{}`, "environment-superset", "", "latest"},
		"no core version":   {http.StatusOK, `{}`, `{}`, "latest-fallback", "core-version-missing", "latest"},
		"unparseable":       {http.StatusOK, `{"applicationInfo":{"sysValues":{"coreVersion":"dev"}}}`, `{}`, "latest-fallback", "core-version-unparseable", "latest"},
		"failing service":   {http.StatusInternalServerError, `{}`, `{}`, "latest-fallback", "probe-error", "latest"},
	}
	for name, c := range cases {
		server := groupCServer(t, func(path string, _ map[string]any) (int, string) {
			switch path {
			case "/0/ServiceModel/ApplicationInfoService.svc/GetApplicationInfo":
				return c.status, c.appInfo
			case "/0/rest/CreatioApiGateway/GetSysInfo":
				return c.status, c.sysInfo
			}
			return http.StatusNotFound, ""
		})
		client := newFormsTestClient(t, server.URL)
		response := componentInfoEncode(t, ComponentInfoGet(context.Background(), ComponentInfoRequest{EnvironmentName: "dev",
			Resolve: func() (*Client, error) { return client, nil }}))
		server.Close()
		if response["resolvedFrom"] != c.resolvedFrom || response["resolvedTargetVersion"] != c.targetVersion ||
			(c.reason != "" && response["resolvedFromReason"] != c.reason) || (c.reason == "" && response["resolvedFromReason"] != nil) {
			t.Errorf("%s: %v %v %v", name, response["resolvedFrom"], response["resolvedTargetVersion"], response["resolvedFromReason"])
		}
	}
}

func TestComponentInfoLocalOverride(t *testing.T) {
	componentInfoNewTestCDN(t, componentInfoTestFiles())
	directory := t.TempDir()
	registry := filepath.Join(directory, "ComponentRegistry.json")
	_ = os.WriteFile(registry, []byte(componentInfoTestRegistry), 0o644)
	_ = os.MkdirAll(filepath.Join(directory, "docs"), 0o755)
	_ = os.WriteFile(filepath.Join(directory, "docs", "next-steps.component.md"), []byte("# Local next steps"), 0o644)
	t.Setenv(componentInfoWebFlavor.localVariable, registry)
	local := componentInfoEncode(t, ComponentInfoGet(context.Background(), ComponentInfoRequest{Composite: "Next steps"}))
	if local["documentation"] != "# Local next steps" || local["documentationSource"] != "local" {
		t.Fatalf("local composite = %v", local)
	}
	missing := componentInfoEncode(t, ComponentInfoGet(context.Background(), ComponentInfoRequest{Composite: "Expanded list"}))
	if missing["documentationUnavailable"] != true || missing["documentationSource"] != "none" ||
		!strings.Contains(missing["documentationWarning"].(string), "'docs/expanded-list.component.md' (expected in the directory of CLIO_COMPONENT_REGISTRY_LOCAL_FILE)") {
		t.Fatalf("local miss = %v", missing)
	}
	t.Setenv(componentInfoWebFlavor.localVariable, filepath.Join(directory, "absent.json"))
	absent := componentInfoEncode(t, ComponentInfoGet(context.Background(), ComponentInfoRequest{}))
	if absent["success"] != false || !strings.Contains(absent["error"].(string), "Component registry override file does not exist") {
		t.Fatalf("absent override = %v", absent)
	}
}

func TestComponentInfoCatalogRejectsMalformedRegistries(t *testing.T) {
	for payload, want := range map[string]string{
		`{"items": []}`: "must be either a JSON array of component entries or an object with a 'components' array.",
		`[]`:            "is empty or invalid.",
		`[{"componentType":"crt.A"},{"componentType":"CRT.a"}]`:                                     "contains duplicate component types: crt.A.",
		`{"components":[{"componentType":"crt.A"}],"composites":[{"caption":" "}]}`:                 "contains 1 composite(s) with a blank caption.",
		`{"components":[{"componentType":"crt.A"}],"composites":[{"caption":"X"},{"caption":"x"}]}`: "contains duplicate composite captions: X.",
	} {
		if _, err := componentInfoParseCatalog([]byte(payload), "latest", componentInfoSourceCDN); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", payload, err, want)
		}
	}
}
