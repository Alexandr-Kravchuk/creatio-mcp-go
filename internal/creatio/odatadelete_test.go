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

// deleteFixture is a canned Creatio for odata-delete: the same tree the T20 comparison with clio 8.1.0.142
// ran against (an Account with an owned AccountAddress, a platform-cascade AccountCommunication and, for
// record A only, a linked Opportunity).
type deleteFixture struct {
	mu      sync.Mutex
	deletes []string
}

const (
	fixtureA   = "aaaaaaaa-0000-0000-0000-000000000001"
	fixtureB   = "aaaaaaaa-0000-0000-0000-000000000002"
	fixtureC   = "aaaaaaaa-0000-0000-0000-000000000003"
	fixtureD   = "aaaaaaaa-0000-0000-0000-000000000004"
	fixtureAA2 = "bbbbbbbb-0000-0000-0000-000000000002"
	fixtureAC2 = "cccccccc-0000-0000-0000-000000000002"
)

func fixtureSchema(name string, columns string) string {
	return `{"success":true,"schema":{"uId":"11111111-1111-1111-1111-0000000000` + map[string]string{"Account": "01", "AccountAddress": "02",
		"AccountCommunication": "03", "Opportunity": "04"}[name] + `","name":"` + name + `","parentUId":"11111111-1111-1111-1111-0000000000ff",` +
		`"isDBView":false,"isVirtual":false,"columns":{"items":{"id":{"name":"Id"}` + columns + `}}}}`
}

func (f *deleteFixture) serve(t *testing.T) *httptest.Server {
	rows := func(items ...string) string { return `{"success":true,"rows":[` + strings.Join(items, ",") + `]}` }
	type rule struct {
		root, contains, body string
	}
	selects := []rule{
		{"SysLookup", "", rows()},
		{"SysSchema", "", rows(`{"UId":"11111111-1111-1111-1111-0000000000ff","Name":"BaseEntity"}`)},
		{"SysEntitySchemaReference", `"value":"Account"`, rows(`{"SchemaName":"AccountAddress","ColumnName":"Account"}`,
			`{"SchemaName":"AccountAddress","ColumnName":"Account"}`, `{"SchemaName":"AccountCommunication","ColumnName":"Account"}`,
			`{"SchemaName":"Opportunity","ColumnName":"Account"}`, `{"SchemaName":"Contact","ColumnName":"CreatedBy"}`)},
		{"SysEntitySchemaReference", "", rows()},
		{"Account", fixtureD, rows()},
		{"Account", "", rows(`{"Id":"x"}`)},
		{"AccountAddress", fixtureA, rows(`{"Id":"bbbbbbbb-0000-0000-0000-000000000001"}`)},
		{"AccountAddress", fixtureB, rows(`{"Id":"` + fixtureAA2 + `"}`)},
		{"AccountAddress", "bbbbbbbb", rows(`{"Id":"x"}`)},
		{"AccountCommunication", fixtureA, rows(`{"Id":"cccccccc-0000-0000-0000-000000000001"}`)},
		{"AccountCommunication", fixtureB, rows(`{"Id":"` + fixtureAC2 + `"}`)},
		{"AccountCommunication", "cccccccc", rows(`{"Id":"x"}`)},
		{"AccountAddress", "", rows()},
		{"AccountCommunication", "", rows()},
		{"Opportunity", fixtureA, rows(`{"Id":"dddddddd-0000-0000-0000-000000000001"}`)},
		{"Opportunity", "", rows()},
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		text := string(body)
		switch {
		case strings.HasSuffix(r.URL.Path, "/Login"):
			io.WriteString(w, `{"Code":0}`)
		case strings.HasSuffix(r.URL.Path, "/RuntimeEntitySchemaRequest"):
			var request struct{ Name string }
			json.Unmarshal(body, &request)
			switch request.Name {
			case "Account", "Opportunity":
				io.WriteString(w, fixtureSchema(request.Name, `,"name":{"name":"Name"}`))
			case "AccountAddress":
				io.WriteString(w, fixtureSchema(request.Name, `,"account":{"name":"Account","isRequired":true}`))
			case "AccountCommunication":
				io.WriteString(w, fixtureSchema(request.Name, `,"account":{"name":"Account","isCascade":true}`))
			default:
				io.WriteString(w, `{"success":false}`)
			}
		case strings.HasSuffix(r.URL.Path, "/SelectQuery"):
			for _, candidate := range selects {
				if strings.Contains(text, `"rootSchemaName":"`+candidate.root+`"`) && strings.Contains(text, candidate.contains) {
					io.WriteString(w, candidate.body)
					return
				}
			}
			t.Errorf("unexpected select %s", text)
			io.WriteString(w, `{"success":false}`)
		case r.Method == http.MethodDelete:
			f.mu.Lock()
			f.deletes = append(f.deletes, strings.TrimPrefix(r.URL.Path, "/0/odata/"))
			f.mu.Unlock()
			if strings.Contains(r.URL.Path, fixtureC) {
				w.WriteHeader(500)
				io.WriteString(w, `{"error":{"code":"","message":"An error has occurred.","innererror":{"message":"Item cannot be deleted because it is being used.\nsecond line"}}}`)
				return
			}
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
}

func fixtureClient(t *testing.T, server *httptest.Server) *Client {
	client, err := NewClient(Config{BaseURL: server.URL, Login: "example-user", Password: "replace-me"})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestODataDeleteDryRunCascadePlanAndHashMatchClio(t *testing.T) {
	fixture := &deleteFixture{}
	server := fixture.serve(t)
	defer server.Close()
	result := ODataDelete(context.Background(), fixtureClient(t, server), nil,
		ODataDeleteRequest{Entity: "Account", ID: fixtureB, DryRun: true, Cascade: true})
	// The hash clio 8.1.0.142 returned for the same tree.
	if !result.Success || result.CascadePlanHash == nil || *result.CascadePlanHash != "bcc9c9f14a8552a6" || result.Diagnostic != nil {
		t.Fatalf("result %#v", result)
	}
	plan := result.Plan[0]
	if len(plan.CascadeWouldDelete) != 1 || plan.CascadeWouldDelete[0] != (RecordKey{"AccountAddress", fixtureAA2}) ||
		len(plan.PlatformCascade) != 1 || plan.PlatformCascade[0] != (RecordKey{"AccountCommunication", fixtureAC2}) || plan.BlockedBecause != nil {
		t.Fatalf("plan %#v", plan)
	}
	if len(fixture.deletes) != 0 {
		t.Fatalf("a dry run deleted %v", fixture.deletes)
	}
	encoded, _ := json.Marshal(result)
	if strings.Contains(string(encoded), `"diagnostic"`) || !strings.Contains(string(encoded), `"note":"Rows this user cannot read`) {
		t.Fatalf("answer %s", encoded)
	}
}

func TestODataDeleteDryRunExplainsLinkedRows(t *testing.T) {
	fixture := &deleteFixture{}
	server := fixture.serve(t)
	defer server.Close()
	result := ODataDelete(context.Background(), fixtureClient(t, server), nil,
		ODataDeleteRequest{Entity: "Account", ID: fixtureA, DryRun: true, Cascade: true})
	plan := result.Plan[0]
	if plan.CascadeRefusal == nil || !strings.Contains(*plan.CascadeRefusal, "linked rows (Opportunity.Account)") ||
		plan.BlockedBecause == nil || !strings.HasPrefix(*plan.BlockedBecause, "Rows that still reference it: AccountAddress.Account x1 (owned); Opportunity.Account x1 (linked).") {
		t.Fatalf("plan %#v", plan)
	}
	if *result.CascadePlanHash != "96c07a347d4f71e5" {
		t.Fatalf("hash %s", *result.CascadePlanHash)
	}
}

func TestODataDeleteCascadeDeletesOwnedRowsFirstOnlyWithTheDryRunHash(t *testing.T) {
	fixture := &deleteFixture{}
	server := fixture.serve(t)
	defer server.Close()
	client := fixtureClient(t, server)
	stale := ODataDelete(context.Background(), client, nil, ODataDeleteRequest{Entity: "Account", ID: fixtureB, Cascade: true, Confirm: true,
		CascadePlanHash: "0000000000000000"})
	if stale.Success || !strings.HasPrefix(*stale.Error, "The cascade plan is not the one cascade-plan-hash was made from: it now deletes 1 owned row(s)") ||
		len(fixture.deletes) != 0 {
		t.Fatalf("stale %#v deletes %v", stale, fixture.deletes)
	}
	result := ODataDelete(context.Background(), client, nil, ODataDeleteRequest{Entity: "Account", ID: fixtureB, Cascade: true, Confirm: true,
		CascadePlanHash: "BCC9C9F14A8552A6"})
	if !result.Success || *result.ID != fixtureB || len(result.CascadeDeleted) != 1 || result.CascadeDeleted[0].ID != fixtureAA2 {
		t.Fatalf("result %#v", result)
	}
	if strings.Join(fixture.deletes, " ") != "AccountAddress("+fixtureAA2+") Account("+fixtureB+")" {
		t.Fatalf("deletes %v", fixture.deletes)
	}
	if !result.Diagnostic.WriteAttempted {
		t.Fatalf("diagnostic %#v", result.Diagnostic)
	}
}

func TestODataDeleteIdsExplainEachRefusal(t *testing.T) {
	fixture := &deleteFixture{}
	server := fixture.serve(t)
	defer server.Close()
	result := ODataDelete(context.Background(), fixtureClient(t, server), nil,
		ODataDeleteRequest{Entity: "Account", IDs: []string{fixtureC, fixtureD, " " + fixtureB, fixtureB}, Confirm: true})
	if result.Success || *result.Error != "2 of 3 records were not deleted; results explains each." || len(result.Results) != 3 {
		t.Fatalf("result %#v", result)
	}
	refused, missing, deleted := result.Results[0], result.Results[1], result.Results[2]
	if *refused.Error != "An error has occurred." || !strings.HasPrefix(*refused.Explanation,
		"The server's own reason: [untrusted-source-text begin] Item cannot be deleted because it is being used. [untrusted-source-text end].") {
		t.Fatalf("refused %#v", refused)
	}
	if !strings.HasSuffix(*missing.Error, "was not found: it does not exist, was already deleted, or this user cannot read it. Nothing was deleted.") {
		t.Fatalf("missing %#v", missing)
	}
	if !deleted.Success || deleted.ID != fixtureB {
		t.Fatalf("deleted %#v", deleted)
	}
	// The refused id goes again after the pass that deleted another one, then stops when a pass deletes nothing.
	if strings.Count(strings.Join(fixture.deletes, " "), fixtureC) != 2 {
		t.Fatalf("deletes %v", fixture.deletes)
	}
}

func TestODataDeleteValidationMatchesClio(t *testing.T) {
	ids := make([]string, 51)
	for i := range ids {
		ids[i] = fixtureA
	}
	for _, c := range []struct {
		request ODataDeleteRequest
		want    string
	}{
		{ODataDeleteRequest{Entity: "Contact"}, "id is required and must be a record GUID (or pass ids, up to 50); keyless mass delete is not allowed."},
		{ODataDeleteRequest{Entity: "Contact", ID: fixtureA, IDs: []string{fixtureA}}, "Pass id or ids, not both."},
		{ODataDeleteRequest{Entity: "Co ntact"}, "entity must be a valid OData entity set name (letters, digits, underscore)."},
		{ODataDeleteRequest{Entity: "Contact", IDs: ids}, "ids takes at most 50 record GUIDs per call."},
		{ODataDeleteRequest{Entity: "Contact", IDs: []string{fixtureA, "zz"}}, "id is required and must be a record GUID; keyless mass delete is not allowed."},
		{ODataDeleteRequest{Entity: "Account", IDs: []string{fixtureA, fixtureA}}, "Refusing to delete Account(" + fixtureA + ") without confirmation. " +
			"This is a destructive operation; re-call odata-delete with \"confirm\": true to authorize this deletion."},
		{ODataDeleteRequest{Entity: "contact", ID: fixtureA, Cascade: true, DryRun: true}, "cascade is refused for contact: rows that require a contact only refer to it, they are not owned by it. Delete without cascade, and read explanation for what blocks it."},
		{ODataDeleteRequest{Entity: "Account", IDs: ids[:11], Cascade: true, DryRun: true}, "Refusing"},
	} {
		result := ODataDelete(context.Background(), nil, errUnreachable, c.request)
		if c.want == "Refusing" {
			// ids are de-duplicated before the cascade limit counts them: one id remains, so the environment is next.
			if result.Error == nil || *result.Error != "unreachable" {
				t.Fatalf("%#v -> %#v", c.request, result)
			}
			continue
		}
		if result.Success || result.Error == nil || *result.Error != c.want || result.Diagnostic == nil {
			t.Fatalf("%#v -> %#v", c.request, result)
		}
	}
}

type unreachableError struct{}

func (unreachableError) Error() string { return "unreachable" }

var errUnreachable error = unreachableError{}
