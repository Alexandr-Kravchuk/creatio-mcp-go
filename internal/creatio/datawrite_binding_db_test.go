package creatio

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// dataWriteBindingFake is a Creatio answering the DB-first binding calls; it records each write's body.
type dataWriteBindingFake struct {
	t           *testing.T
	boundRows   string // GetBoundSchemaData items
	bindingRows string // SysPackageSchemaData select rows
	existing    string // entity select rows (names and row probes)
	fileDesign  string
	writes      map[string][]map[string]any
}

func (f *dataWriteBindingFake) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var decoded map[string]any
	_ = json.Unmarshal(body, &decoded)
	record := func(name string) { f.writes[name] = append(f.writes[name], decoded) }
	switch {
	case strings.HasSuffix(r.URL.Path, "/AuthService.svc/Login"):
		io.WriteString(w, `{"Code":0}`)
	case strings.HasSuffix(r.URL.Path, "/RuntimeEntitySchemaRequest"):
		io.WriteString(w, `{"success":true,"schema":{"uId":"11111111-1111-1111-1111-111111111111","name":"ContactType","primaryColumnUId":"22222222-2222-2222-2222-222222222222",
			"columns":{"items":{"a":{"uId":"22222222-2222-2222-2222-222222222222","name":"Id","dataValueType":0},
			"b":{"uId":"33333333-3333-3333-3333-333333333333","name":"Name","dataValueType":28},
			"c":{"uId":"44444444-4444-4444-4444-444444444444","name":"Description","dataValueType":29},
			"d":{"uId":"55555555-5555-5555-5555-555555555555","name":"Color","dataValueType":18}}}}}`)
	case strings.HasSuffix(r.URL.Path, "/SelectQuery"):
		switch decoded["rootSchemaName"] {
		case "SysPackage":
			io.WriteString(w, `{"success":true,"rows":[{"Name":"UsrParityPkg","UId":"66666666-6666-6666-6666-666666666666","InstallType":0}]}`)
		case "SysPackageSchemaData":
			io.WriteString(w, `{"success":true,"rows":[`+f.bindingRows+`]}`)
		default:
			if len(decoded["filters"].(map[string]any)["items"].(map[string]any)) > 0 {
				io.WriteString(w, `{"success":true,"rows":[]}`) // the by-Id probe: no such row
				return
			}
			io.WriteString(w, `{"success":true,"rows":[`+f.existing+`]}`)
		}
	case strings.HasSuffix(r.URL.Path, "/GetBoundSchemaData"):
		items, _ := json.Marshal(f.boundRows)
		io.WriteString(w, `{"success":true,"items":`+string(items)+`}`)
	case strings.HasSuffix(r.URL.Path, "/GetIsFileDesignMode"):
		io.WriteString(w, f.fileDesign)
	default:
		name := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		record(name)
		io.WriteString(w, `{"success":true}`)
	}
}

func newDataWriteBindingFake(t *testing.T) (*dataWriteBindingFake, *Client) {
	fake := &dataWriteBindingFake{t: t, fileDesign: `{"success":true,"value":false}`, writes: map[string][]map[string]any{}}
	server := httptest.NewServer(http.HandlerFunc(fake.serve))
	t.Cleanup(server.Close)
	client, _ := NewClient(Config{BaseURL: server.URL, Login: "example-user", Password: "replace-me"})
	return fake, client
}

func TestDataWriteCreateBindingInsertsRowsAndSavesTheProjection(t *testing.T) {
	fake, client := newDataWriteBindingFake(t)
	fake.existing = `{"Name":"UsrParityTaken","Id":"77777777-7777-7777-7777-777777777777"}`
	result := client.CreateDataBindingDb(context.Background(), DataWriteBindingDbRequest{PackageName: "usrparitypkg", SchemaName: "ContactType",
		RowsJSON: `[{"values":{"Name":"UsrParityNew","Description":"one"}},{"values":{"Name":"usrparitytaken"}},{"other":1}]`})
	if result.ExitCode != 0 || len(result.Messages) != 3 {
		t.Fatalf("result %#v", result)
	}
	if !strings.HasPrefix(result.Messages[0].Value, "Created row: ") || !strings.HasSuffix(result.Messages[0].Value, " (Name=UsrParityNew, Description=one)") ||
		result.Messages[1].Value != "Skipped existing row: 77777777-7777-7777-7777-777777777777 (Name=usrparitytaken)" || result.Messages[2].Value != "Done" {
		t.Fatalf("messages %#v", result.Messages)
	}
	inserts := fake.writes["InsertQuery"]
	if len(inserts) != 1 {
		t.Fatalf("inserts %#v", fake.writes)
	}
	items := inserts[0]["columnValues"].(map[string]any)["items"].(map[string]any)
	if items["Name"].(map[string]any)["parameter"].(map[string]any)["dataValueType"] != float64(1) || items["Id"] == nil {
		t.Fatalf("insert items %#v", items)
	}
	saves := fake.writes["SaveSchema"]
	if len(saves) != 1 {
		t.Fatalf("saves %#v", fake.writes)
	}
	save := saves[0]
	if save["name"] != "ContactType" || save["entitySchemaName"] != "ContactType" || save["package"].(map[string]any)["name"] != "UsrParityPkg" {
		t.Fatalf("save %#v", save)
	}
	columns := save["columns"].([]any)
	if len(columns) != 3 || columns[0].(map[string]any)["name"] != "Description" || columns[1].(map[string]any)["isKey"] != true ||
		columns[2].(map[string]any)["dataValueTypeUId"] != "ddb3a1ee-07e8-4d62-b7a9-d0e618b00fbd" {
		t.Fatalf("columns %#v", columns)
	}
	if ids := save["boundRecordIds"].([]any); len(ids) != 2 || ids[1] != "77777777-7777-7777-7777-777777777777" {
		t.Fatalf("bound ids %#v", ids)
	}
}

func TestDataWriteUpsertUpdatesABoundRowAndWarnsUnderFileDesignMode(t *testing.T) {
	fake, client := newDataWriteBindingFake(t)
	fake.fileDesign = `{"success":true,"value":true}`
	fake.bindingRows = `{"UId":"88888888-8888-8888-8888-888888888888","EntitySchemaName":"ContactType"}`
	fake.boundRows = `[{"Id":"99999999-9999-9999-9999-999999999999","Name":"UsrParityRow"}]`
	result := client.UpsertDataBindingRowDb(context.Background(), DataWriteBindingDbRequest{PackageName: "UsrParityPkg", BindingName: "UsrParityBind",
		ValuesJSON: `{"Id":"99999999-9999-9999-9999-999999999999","Description":"two","Color":"#AABBCC"}`})
	if result.ExitCode != 0 || len(result.Messages) != 2 || result.Messages[0].MessageType != "Warning" ||
		!strings.HasPrefix(result.Messages[0].Value, "File system development mode is enabled: data binding 'UsrParityBind' of package 'UsrParityPkg' was re-saved in the database only.") {
		t.Fatalf("result %#v", result)
	}
	updates := fake.writes["UpdateQuery"]
	if len(updates) != 1 || len(fake.writes["InsertQuery"]) != 0 {
		t.Fatalf("writes %#v", fake.writes)
	}
	items := updates[0]["columnValues"].(map[string]any)["items"].(map[string]any)
	if items["Id"] != nil || items["Color"].(map[string]any)["parameter"].(map[string]any)["value"] != "#AABBCC" {
		t.Fatalf("update items %#v", items)
	}
	save := fake.writes["SaveSchema"][0]
	if save["uId"] != "88888888-8888-8888-8888-888888888888" || len(save["columns"].([]any)) != 4 {
		t.Fatalf("save %#v", save)
	}

	// A Color that is not a string is refused before any write.
	before := len(fake.writes["UpdateQuery"])
	result = client.UpsertDataBindingRowDb(context.Background(), DataWriteBindingDbRequest{PackageName: "UsrParityPkg", BindingName: "UsrParityBind",
		ValuesJSON: `{"Color":5}`})
	if result.ExitCode != 1 || result.Messages[0].Value != "Column 'Color' value '5' is not valid for data type 'Color'." || len(fake.writes["UpdateQuery"]) != before {
		t.Fatalf("color %#v", result)
	}
}

func TestDataWriteRemoveLastRowDeletesTheBinding(t *testing.T) {
	fake, client := newDataWriteBindingFake(t)
	fake.fileDesign = `not json`
	fake.bindingRows = `{"UId":"88888888-8888-8888-8888-888888888888","EntitySchemaName":"ContactType"}`
	fake.boundRows = `[{"Id":"99999999-9999-9999-9999-999999999999","Name":"UsrParityRow"}]`
	result := client.RemoveDataBindingRowDb(context.Background(), DataWriteBindingDbRequest{PackageName: "UsrParityPkg", BindingName: "UsrParityBind",
		KeyValue: "99999999-9999-9999-9999-999999999999"})
	if result.ExitCode != 0 || result.Messages[0].MessageType != "Warning" ||
		!strings.HasPrefix(result.Messages[0].Value, "Data binding 'UsrParityBind' of package 'UsrParityPkg' was removed from the database, but clio could not read") {
		t.Fatalf("result %#v", result)
	}
	if len(fake.writes["DeleteQuery"]) != 1 || len(fake.writes["DeletePackageSchemaDataRequest"]) != 1 || len(fake.writes["SaveSchema"]) != 0 {
		t.Fatalf("writes %#v", fake.writes)
	}
	if fake.writes["DeletePackageSchemaDataRequest"][0]["packageSchemaDataName"] != "UsrParityBind" {
		t.Fatalf("delete binding %#v", fake.writes["DeletePackageSchemaDataRequest"][0])
	}

	result = client.RemoveDataBindingRowDb(context.Background(), DataWriteBindingDbRequest{PackageName: "UsrParityPkg", BindingName: "UsrParityBind", KeyValue: "nope"})
	if result.ExitCode != 1 || result.Messages[0].Value != "Row with key 'nope' was not found in binding 'UsrParityBind'." {
		t.Fatalf("missing key %#v", result)
	}
}

func TestDataWriteBindingParsesLikeClio(t *testing.T) {
	for text, want := range map[string]string{
		"not json": "--rows must contain valid JSON. 'not json' is an invalid JSON literal. Expected the literal 'null'. LineNumber: 0 | BytePositionInLine: 1.",
		"{}":       "--rows must be a JSON array.",
		"<x>":      "--rows must contain valid JSON. '<' is an invalid start of a value. LineNumber: 0 | BytePositionInLine: 0.",
	} {
		if _, err := dataWriteBindingParseRows(text); err == nil || err.Error() != want {
			t.Errorf("%q: %v", text, err)
		}
	}
	if _, err := dataWriteBindingParseValues("[]"); err == nil || err.Error() != "--values must be a JSON object keyed by column name." {
		t.Fatalf("values err=%v", err)
	}
}
