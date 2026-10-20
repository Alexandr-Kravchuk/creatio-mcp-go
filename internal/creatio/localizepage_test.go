package creatio

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestLocalizeParseResourcesMatchesNewtonsoft(t *testing.T) {
	prefix := "resources must be a JSON object of key → value strings: "
	cases := []struct {
		text, problem string
		values        map[string]string
	}{
		{text: " ", values: map[string]string{}},
		{text: "null", values: map[string]string{}},
		{text: `{"a":"x","b":5,"c":true,"d":1.5,"a":"y",}`, values: map[string]string{"a": "y", "b": "5", "c": "true", "d": "1.5"}},
		{text: `{"a":null,"b":"x","c":null}`, problem: "resources must map every key to a string value; key(s) with a null value: a, c."},
		{text: `{"a":" ","b":""}`, problem: "resources must map every key to a non-blank value; key(s) with an empty or whitespace-only value: a, b. Nothing was saved."},
		{text: `{"a":{"b":1}}`, problem: prefix + "Unexpected character encountered while parsing value: {. Path 'a', line 1, position 6."},
		{text: "{\n \"a\": [1]}", problem: prefix + "Unexpected character encountered while parsing value: [. Path 'a', line 2, position 7."},
		{text: `true`, problem: prefix + "Error converting value True to type 'System.Collections.Generic.Dictionary`2[System.String,System.String]'. Path '', line 1, position 4."},
		{text: `"x"`, problem: prefix + "Error converting value \"x\" to type 'System.Collections.Generic.Dictionary`2[System.String,System.String]'. Path '', line 1, position 3."},
	}
	for _, c := range cases {
		resources, problem := localizeParseResources(c.text)
		if problem != c.problem {
			t.Errorf("%q: problem %q, want %q", c.text, problem, c.problem)
			continue
		}
		if c.problem != "" {
			continue
		}
		if len(resources.keys) != len(c.values) {
			t.Errorf("%q: keys %v", c.text, resources.keys)
		}
		for key, want := range c.values {
			if got := resources.values[key]; got == nil || *got != want {
				t.Errorf("%q: %s = %v, want %s", c.text, key, got, want)
			}
		}
	}
	if _, problem := localizeParseResources("[1]"); !strings.Contains(problem, "Cannot deserialize the current JSON array") ||
		!strings.HasSuffix(problem, "Path '', line 1, position 1.") {
		t.Errorf("array root: %s", problem)
	}
}

func TestLocalizeUnstorable(t *testing.T) {
	for _, value := range []string{"a\u0001", "\u0085", "x￾", "￿"} {
		if !localizeUnstorable(value) {
			t.Errorf("%q should be unstorable", value)
		}
	}
	for _, value := range []string{"", "tab\there", "line\nbreak\r", "emoji 😀", "café"} {
		if localizeUnstorable(value) {
			t.Errorf("%q should be storable", value)
		}
	}
}

// localizeStand serves one page (UsrPage_FormPage) and keeps what SaveSchema stores, with a checksum per save.
type localizeStand struct {
	mu       sync.Mutex
	schema   map[string]any
	checksum int
	saves    int
}

func newLocalizeStand(t *testing.T) (*localizeStand, *Client) {
	stand := &localizeStand{schema: map[string]any{
		"uId": "11111111-0000-0000-0000-000000000001", "name": "UsrPage_FormPage", "package": map[string]any{"name": "UsrPkg"},
		"body":               "",
		"localizableStrings": []any{map[string]any{"uId": "x", "name": "UsrTitle", "values": []any{map[string]any{"cultureName": "en-US", "value": "Title"}}}},
		"caption":            []any{map[string]any{"cultureName": "en-US", "value": "Page"}},
		"parent":             map[string]any{"caption": []any{map[string]any{"cultureName": "en-US", "value": "Blank"}, map[string]any{"cultureName": "es-ES", "value": "Vacía"}}},
	}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stand.mu.Lock()
		defer stand.mu.Unlock()
		body, _ := io.ReadAll(r.Body)
		route := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		write := func(value any) { encoded, _ := json.Marshal(value); w.Write(encoded) }
		switch {
		case route == "Login":
			io.WriteString(w, `{"Code":0}`)
		case route == "SelectQuery" && strings.Contains(string(body), "SysCulture"):
			io.WriteString(w, `{"success":true,"rows":[{"Name":"en-US","Active":true},{"Name":"es-ES","Active":true}]}`)
		case route == "SelectQuery" && strings.Contains(string(body), "Checksum"):
			write(map[string]any{"success": true, "rows": []any{map[string]any{"Checksum": "c" + string(rune('0'+stand.checksum)), "ModifiedOn": "m"}}})
		case route == "SelectQuery":
			io.WriteString(w, `{"success":true,"rows":[{"UId":"11111111-0000-0000-0000-000000000001"}]}`)
		case route == "GetDesignPackageUId":
			io.WriteString(w, `{"success":true,"uId":"22222222-0000-0000-0000-000000000001"}`)
		case route == "GetParentSchemas":
			io.WriteString(w, `{"success":true,"values":[{"uId":"11111111-0000-0000-0000-000000000001","package":{"uId":"22222222-0000-0000-0000-000000000001","name":"UsrPkg"}}]}`)
		case route == "GetSchema":
			write(map[string]any{"success": true, "schema": stand.schema})
		case route == "SaveSchema":
			var saved map[string]any
			json.Unmarshal(body, &saved)
			stand.schema, stand.checksum, stand.saves = saved, stand.checksum+1, stand.saves+1
			io.WriteString(w, `{"success":true}`)
		case route == "ResetScriptCache":
			io.WriteString(w, `{"success":true}`)
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return stand, newFormsTestClient(t, server.URL)
}

func writeLocalizeBaseline(t *testing.T, directory, checksum string) string {
	t.Helper()
	path := filepath.Join(directory, ".clio-pages", "UsrPage_FormPage", "meta.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	meta := map[string]any{"fetchedAt": "f", "page": map[string]any{}, "baseline": map[string]any{"schemaName": "UsrPage_FormPage",
		"environmentName": "dev", "editableSchemaExists": true, "editableSchemaUId": "11111111-0000-0000-0000-000000000001", "checksum": checksum}}
	encoded, _ := json.Marshal(meta)
	if err := os.WriteFile(path, encoded, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func localizeRequest(directory, culture, resources, caption string) LocalizePageRequest {
	name := "UsrPage_FormPage"
	request := LocalizePageRequest{SchemaName: &name, Culture: &culture, OutputDirectory: directory, EnvironmentName: "dev"}
	if resources != "" {
		request.Resources = &resources
	}
	if caption != "" {
		request.Caption = &caption
	}
	return request
}

func TestLocalizePageSavesAndRefreshesTheBaseline(t *testing.T) {
	stand, client := newLocalizeStand(t)
	directory := t.TempDir()
	metaPath := writeLocalizeBaseline(t, directory, "c0")
	response := client.LocalizePage(context.Background(), localizeRequest(directory, "es-es", `{"UsrTitle":"Título"}`, " Página "))
	if !response.Success || !response.Saved || stand.saves != 1 || localizeText(response.Culture) != "es-ES" {
		t.Fatalf("response=%+v error=%s", response, localizeText(response.Error))
	}
	if response.Coverage == nil || response.Coverage.Translated != 1 || localizeText(response.Coverage.CaptionValue) != "Página" {
		t.Fatalf("coverage=%+v", response.Coverage)
	}
	meta, _ := os.ReadFile(metaPath)
	if !strings.Contains(string(meta), `"checksum":"c1"`) {
		t.Fatalf("baseline not refreshed: %s", meta)
	}
	again := client.LocalizePage(context.Background(), localizeRequest(directory, "es-ES", `{"UsrTitle":"Título"}`, "Página"))
	if !again.Success || again.Saved || len(again.Unchanged) != 1 || localizeText(again.CaptionOutcome) != "unchanged" || stand.saves != 1 {
		t.Fatalf("rerun=%+v", again)
	}
}

func TestLocalizePageLeavesAStaleBaselineAndReportsInheritedCaption(t *testing.T) {
	_, client := newLocalizeStand(t)
	directory := t.TempDir()
	metaPath := writeLocalizeBaseline(t, directory, "old")
	response := client.LocalizePage(context.Background(), localizeRequest(directory, "es-ES", "", "Vacía"))
	if !response.Success || !response.Saved {
		t.Fatalf("response=%+v error=%s", response, localizeText(response.Error))
	}
	stale := "The .clio-pages baseline of 'UsrPage_FormPage' was stale (the page changed after the get-page that wrote it), so it was " +
		"left unchanged; update-page will report the conflict. Re-read the page with get-page and merge before saving."
	if len(response.Warnings) != 2 || response.Warnings[1] != stale {
		t.Fatalf("warnings=%q", response.Warnings)
	}
	if !response.Coverage.CaptionInherited {
		t.Fatalf("caption taken from the parent template is not reported as inherited: %+v", response.Coverage)
	}
	meta, _ := os.ReadFile(metaPath)
	if !strings.Contains(string(meta), `"checksum":"old"`) {
		t.Fatalf("stale baseline was rewritten: %s", meta)
	}
}
