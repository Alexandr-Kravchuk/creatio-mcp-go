package creatio

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

const (
	connA = "aaaaaaaa-0000-0000-0000-00000000000a"
	connB = "aaaaaaaa-0000-0000-0000-00000000000b"
	connC = "aaaaaaaa-0000-0000-0000-00000000000c"
	connD = "aaaaaaaa-0000-0000-0000-00000000000d"
)

// connectionsMerge is the server's merge: the add-on items, then the table rows, removals of every layer left out.
func connectionsMerge(content connectionsContent, table []string) []string {
	removed := map[string]bool{}
	for _, uid := range content.removed {
		removed[uid] = true
	}
	merged := []string{}
	seen := map[string]bool{}
	for _, uid := range append(append([]string{}, content.items...), table...) {
		if !removed[uid] && !seen[uid] {
			seen[uid] = true
			merged = append(merged, uid)
		}
	}
	return merged
}

func content(items, removed []string) connectionsContent {
	return connectionsContent{items: items, removed: removed, other: newObject()}
}

// The cases of clio's EntityConnectionsAddonMetadataTests.Build_*.
func TestBuildConnectionsContentMatchesClio(t *testing.T) {
	empty := content([]string{}, []string{})
	for _, test := range []struct {
		name               string
		base               connectionsContent
		effective, desired []string
		items, removed     []string
		table              []string
	}{
		{"remove a table connection", empty, []string{connA, connB, connC}, []string{connA, connC}, []string{}, []string{connB}, []string{connA, connB, connC}},
		{"move a column up", empty, []string{connA, connB, connC, connD}, []string{connC, connA, connB, connD}, []string{connC}, []string{}, []string{connA, connB, connC, connD}},
		{"swap two columns", empty, []string{connA, connB, connC, connD}, []string{connA, connC, connB, connD}, []string{connA, connC}, []string{}, []string{connA, connB, connC, connD}},
		{"add a column without a table row", empty, []string{connA, connB}, []string{connA, connB, connC}, []string{connA, connB, connC}, []string{}, []string{connA, connB}},
		{"add a removed column back", content([]string{connD}, []string{connB, connC}), []string{connD, connA}, []string{connD, connA, connB},
			[]string{connD, connA, connB}, []string{connC}, []string{connA, connB, connC}},
		{"remove a loaded item", content([]string{connD}, []string{}), []string{connD, connA, connB}, []string{connA, connB}, []string{}, []string{connD}, []string{connA, connB}},
		{"remove every column", empty, []string{connA, connB}, []string{}, []string{}, []string{connA, connB}, []string{connA, connB}},
	} {
		result, changed := buildConnectionsContent(test.base, test.effective, test.desired)
		if !changed || !sameUIDs(result.items, test.items) || !sameUIDs(result.removed, test.removed) {
			t.Fatalf("%s: items %v removed %v", test.name, result.items, result.removed)
		}
		if merged := connectionsMerge(result, test.table); !sameUIDs(merged, test.desired) {
			t.Fatalf("%s: the server would list %v", test.name, merged)
		}
	}
	if _, changed := buildConnectionsContent(empty, []string{connA, connB}, []string{connA, connB}); changed {
		t.Fatal("an unchanged list needs no write")
	}
}

func TestConnectionsContentParseAndSerialize(t *testing.T) {
	parsed, err := parseConnectionsContent(`{"Items":[{"columnUId":"` + connB + `","position":"1"},{"ColumnUId":"` + connA + `","Position":0},` +
		`{"ColumnUId":"not-a-guid"}],"REMOVED":[{"ColumnUId":"` + connC + `"}],"futureField":{"enabled":true}}`)
	if err != nil || !sameUIDs(parsed.items, []string{connA, connB}) || !sameUIDs(parsed.removed, []string{connC}) {
		t.Fatalf("parsed = %#v, %v", parsed, err)
	}
	written := serializeConnectionsContent(parsed)
	want := `{"futureField":{"enabled":true},"items":[{"UId":"` + connA + `","ColumnUId":"` + connA + `","Position":0},{"UId":"` + connB +
		`","ColumnUId":"` + connB + `","Position":1}],"removed":[{"UId":"` + connC + `","ColumnUId":"` + connC + `"}]}`
	if written != want {
		t.Fatalf("written = %s", written)
	}
	if _, err := parseConnectionsContent("{"); err == nil || !strings.HasPrefix(err.Error(), "The EntityConnections add-on metadata is not valid JSON: ") {
		t.Fatalf("err = %v", err)
	}
	cleaned, dropped := parsed.withoutColumns(map[string]bool{connB: true, connC: true})
	if !dropped || !sameUIDs(cleaned.items, []string{connA}) || len(cleaned.removed) != 0 {
		t.Fatalf("cleaned = %#v", cleaned)
	}
	if _, dropped := parsed.withoutColumns(map[string]bool{connD: true}); dropped {
		t.Fatal("no referenced column, no clean-up write")
	}
}

type connectionsStand struct {
	mu    sync.Mutex
	meta  string
	table []string
	saved []string
}

func (s *connectionsStand) serve(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		s.mu.Lock()
		defer s.mu.Unlock()
		reply := func(value any) { encoded, _ := json.Marshal(value); _, _ = w.Write(encoded) }
		column := func(uid, name string, dataType int) map[string]any {
			return map[string]any{"uId": uid, "name": name, "type": dataType, "caption": []any{map[string]any{"cultureName": "en-US", "value": name + " caption"}},
				"referenceSchema": map[string]any{"name": "Contact"}}
		}
		switch path := r.URL.Path; {
		case strings.HasSuffix(path, "/Login"):
			_, _ = w.Write([]byte(`{"Code":0}`))
		case strings.HasSuffix(path, "/SelectQuery") && body["rootSchemaName"] == "SysPackage":
			reply(map[string]any{"success": true, "rows": []any{map[string]any{"UId": "44444444-4444-4444-4444-444444444444"}}})
		case strings.HasSuffix(path, "/GetSchemaDesignItem"):
			reply(map[string]any{"success": true, "schema": map[string]any{"uId": "55555555-5555-5555-5555-555555555555", "name": "Account",
				"columns":          []any{column(connA, "Owner", 10), column(connB, "Name", 1)},
				"inheritedColumns": []any{column(connC, "PrimaryContact", 10), column(connD, "Type", 10)}}})
		case strings.HasSuffix(path, "/AddonSchemaDesignerService.svc/GetSchema"):
			reply(map[string]any{"success": true, "schema": map[string]any{"uId": "99999999-9999-9999-9999-999999999999",
				"targetSchemaUId": "55555555-5555-5555-5555-555555555555", "metaData": s.meta, "resources": []any{}}})
		case strings.HasSuffix(path, "/AddonSchemaDesignerService.svc/SaveSchema"):
			s.meta = body["metaData"].(string)
			s.saved = append(s.saved, s.meta)
			reply(map[string]any{"success": true})
		case strings.HasSuffix(path, "/EntityConnectionsService/GetConnections"):
			parsed, _ := parseConnectionsContent(s.meta)
			connections := []any{}
			for _, uid := range connectionsMerge(parsed, s.table) {
				connections = append(connections, map[string]any{"columnUId": uid})
			}
			reply(map[string]any{"success": true, "connections": connections})
		case strings.HasSuffix(path, "/ResetScriptCache"):
			reply(map[string]any{"success": true})
		default:
			t.Errorf("unexpected route %s", path)
			http.NotFound(w, r)
		}
	}
}

func TestUpdateEntityConnectionsWritesTheAddonAndReadsItBack(t *testing.T) {
	stand := &connectionsStand{table: []string{connA}}
	server := httptest.NewServer(stand.serve(t))
	defer server.Close()
	client := newFormsTestClient(t, server.URL)
	names := func(values ...string) []*string {
		list := make([]*string, len(values))
		for index := range values {
			list[index] = &values[index]
		}
		return list
	}
	result := client.UpdateEntityConnections(context.Background(), "Account", "Custom", names("Owner", "Name", "Zzz"))
	if result.Success || result.Error != "Connections were not changed. Object 'Account' has no column 'Zzz'. Only lookup columns can be connections; not a lookup: 'Name' (text)." {
		t.Fatalf("result = %#v", result)
	}
	result = client.UpdateEntityConnections(context.Background(), "Account", "Custom", names(" type ", "Owner"))
	if !result.Success || result.Changed == nil || !*result.Changed || len(result.Connections) != 2 || *result.Connections[0].ColumnName != "Type" ||
		*result.Connections[0].Caption != "Type caption" || result.PackageLayer.Items[0] != "Type" || result.Warnings != nil {
		t.Fatalf("result = %#v", result)
	}
	if len(stand.saved) != 1 || !strings.Contains(stand.saved[0], `"ColumnUId":"`+connD+`"`) {
		t.Fatalf("saved = %v", stand.saved)
	}
	result = client.UpdateEntityConnections(context.Background(), "Account", "Custom", names("Type", "Owner"))
	if !result.Success || result.Changed == nil || *result.Changed || len(stand.saved) != 1 {
		t.Fatalf("an unchanged list must not be written: %#v", result)
	}
	if result := client.UpdateEntityConnections(context.Background(), "Account", "Custom", names("Owner", "owner")); result.Error !=
		"connections lists a column more than once: Owner. Each column can be one connection." {
		t.Fatalf("result = %#v", result)
	}
	encoded, _ := json.Marshal(client.GetEntityConnections(context.Background(), "Account", "Custom"))
	if !strings.Contains(string(encoded), `"connections":[{"columnName":"Type"`) || strings.Contains(string(encoded), `"changed"`) {
		t.Fatalf("get = %s", encoded)
	}
	encoded, _ = json.Marshal(EntityConnectionsFailure("x"))
	if string(encoded) != `{"success":false,"error":"x"}` {
		t.Fatalf("failure = %s", encoded)
	}
}
