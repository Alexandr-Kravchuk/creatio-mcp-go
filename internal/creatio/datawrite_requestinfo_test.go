package creatio

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const dataWriteRequestRegistry = `{"requests":[
 {"requestType":"crt.OpenPageRequest","description":"Opens a page","parameters":{"schemaName":{"type":"string","description":"Page schema"},"mode":{"type":"PageMode","values":["edit","add"]}},
  "references":{"docs":["request-docs/open-page.request.md"],"typeDefinitions":{"PageMode":{"type":"string"}}}},
 {"requestType":"crt.ClosePageRequest","description":"Closes the page","parameters":{}},
 {"requestType":"crt.OldRequest","description":"","deprecated":true,"deprecationReason":"Use crt.OpenPageRequest instead."}],
 "references":{"baseParameters":{"$context":{"type":"BaseRequestContext"}},"typeDefinitions":{"RequestBindingConfig":{"type":"object","properties":{"params":{"type":"Record"}}},"Record":{"type":"object"}}}}`

func dataWriteRequestInfoSetup(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	registry := filepath.Join(dir, "RequestRegistry.json")
	if err := os.WriteFile(registry, []byte(dataWriteRequestRegistry), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "request-docs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "request-docs", "open-page.request.md"), []byte("# Open page"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLIO_REQUEST_REGISTRY_LOCAL_FILE", registry)
	t.Setenv("CLIO_HOME", dir)
}

func TestDataWriteRequestInfoListDetailAndNotFound(t *testing.T) {
	dataWriteRequestInfoSetup(t)
	list := DataWriteRequestInfoGet(context.Background(), DataWriteRequestInfoRequest{})
	if !list.Success || list.Count != 3 || (*list.Items)[0].RequestType != "crt.ClosePageRequest" || list.ResolvedFrom == nil ||
		*list.ResolvedFrom != "latest-fallback" || list.RequiresVersionConfirmation == nil {
		t.Fatalf("list %#v", list)
	}
	if deprecated := (*list.Items)[1]; deprecated.Deprecated == nil || deprecated.Description != nil {
		t.Fatalf("deprecated item %#v", deprecated)
	}
	search := DataWriteRequestInfoGet(context.Background(), DataWriteRequestInfoRequest{Search: "ADD"})
	if search.Count != 1 || (*search.Items)[0].RequestType != "crt.OpenPageRequest" {
		t.Fatalf("enum search %#v", search)
	}
	detail := DataWriteRequestInfoGet(context.Background(), DataWriteRequestInfoRequest{RequestType: "crt.openpagerequest"})
	encoded, _ := json.Marshal(detail)
	text := string(encoded)
	for _, want := range []string{`"mode":"detail"`, `"requestType":"crt.OpenPageRequest"`, `"baseParameters":{"$context"`,
		`"typeDefinitions":{"PageMode":{"type":"string"},"RequestBindingConfig":`, `"Record":{"type":"object"}`, `"documentation":"# Open page"`,
		`"documentationSource":"local"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("detail lacks %s: %s", want, text)
		}
	}
	empty := DataWriteRequestInfoGet(context.Background(), DataWriteRequestInfoRequest{RequestType: "crt.ClosePageRequest"})
	if encoded, _ := json.Marshal(empty); !strings.Contains(string(encoded), `"parameters":{}`) {
		t.Fatalf("an empty parameters map must stay on the wire: %s", encoded)
	}
	missing := DataWriteRequestInfoGet(context.Background(), DataWriteRequestInfoRequest{RequestType: "crt.ClosPageRequest"})
	if missing.Success || !strings.HasPrefix(*missing.Error, "'crt.ClosPageRequest' is not a request type. Showing the 3 closest") ||
		(*missing.Items)[0].RequestType != "crt.ClosePageRequest" {
		t.Fatalf("missing %#v", missing)
	}
	conflict := DataWriteRequestInfoGet(context.Background(), DataWriteRequestInfoRequest{Version: "8.3.3", EnvironmentName: "x"})
	if conflict.Success || *conflict.Error != "'version' and 'environment-name'/'uri' are mutually exclusive. Pass one or neither." {
		t.Fatalf("conflict %#v", conflict)
	}
}

func TestDataWriteRequestCatalogRefusesBadEnvelopes(t *testing.T) {
	for payload, want := range map[string]string{
		`[]`:              "Request registry stream must be an object with a 'requests' array.",
		`{"requests":{}}`: "Request registry stream must be an object with a 'requests' array.",
		`{"requests":[]}`: "Request registry stream is empty or invalid.",
		`{"requests":[{"requestType":"a"},{"requestType":"A"}]}`: "Request registry stream contains duplicate request types: a.",
		`{"requests":[{"requestType":" "}]}`:                     "Request registry stream does not contain valid request types.",
	} {
		if _, err := dataWriteParseRequestCatalog([]byte(payload), "latest"); err == nil || err.Error() != want {
			t.Errorf("%s: %v", payload, err)
		}
	}
}
