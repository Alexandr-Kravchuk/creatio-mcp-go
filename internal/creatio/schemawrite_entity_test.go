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
	"time"
)

// schemaWriteEntFake is a Creatio stand for the entity-schema write tools: one editable package, a schema
// store keyed by name, the designer service, SchemaDesignerRequest, the OData build routes and the
// DataService reads they make. calls records every request in order as "Method" names.
type schemaWriteEntFake struct {
	t        *testing.T
	mu       sync.Mutex
	calls    []string
	bodies   map[string][]json.RawMessage
	schemas  map[string]map[string]any
	odata    []bool
	override map[string]func(w http.ResponseWriter, body []byte) bool
	// existing lists find-entity-schema rows: name -> package.
	existing map[string]string
}

const schemaWriteEntTestPackageUID = "11111111-1111-1111-1111-111111111111"

func newSchemaWriteEntFake(t *testing.T) (*schemaWriteEntFake, *Client) {
	fake := &schemaWriteEntFake{t: t, bodies: map[string][]json.RawMessage{}, schemas: map[string]map[string]any{},
		override: map[string]func(http.ResponseWriter, []byte) bool{}, existing: map[string]string{}}
	server := httptest.NewServer(http.HandlerFunc(fake.serve))
	t.Cleanup(server.Close)
	client, err := NewClient(Config{BaseURL: server.URL, Login: "example", Password: "replace-me"})
	if err != nil {
		t.Fatal(err)
	}
	previous := schemaWriteEntSleep
	schemaWriteEntSleep = func(context.Context, time.Duration) {}
	t.Cleanup(func() { schemaWriteEntSleep = previous })
	return fake, client
}

func (f *schemaWriteEntFake) record(name string, body []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, name)
	f.bodies[name] = append(f.bodies[name], json.RawMessage(body))
}

func (f *schemaWriteEntFake) last(name string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	list := f.bodies[name]
	if len(list) == 0 {
		f.t.Fatalf("no %s call", name)
	}
	var value map[string]any
	if err := json.Unmarshal(list[len(list)-1], &value); err != nil {
		f.t.Fatalf("%s body: %v", name, err)
	}
	return value
}

func (f *schemaWriteEntFake) count(name string) int {
	n := 0
	for _, call := range f.calls {
		if call == name {
			n++
		}
	}
	return n
}

func writeJSON(w http.ResponseWriter, value any) {
	encoded, _ := json.Marshal(value)
	w.Write(encoded)
}

func (f *schemaWriteEntFake) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	path := r.URL.Path
	if strings.HasSuffix(path, "/Login") {
		io.WriteString(w, `{"Code":0}`)
		return
	}
	name := path[strings.LastIndex(path, "/")+1:]
	if strings.HasSuffix(path, "SchemaDataDesignerService.svc/SaveSchema") {
		name = "SaveSchemaData"
	}
	if name == "SelectQuery" {
		var query map[string]any
		json.Unmarshal(body, &query)
		name = "Select:" + query["rootSchemaName"].(string)
	}
	if name == "SchemaDesignerRequest" {
		var request map[string]any
		json.Unmarshal(body, &request)
		if request["buildWorkspace"] == true {
			name = "Publish"
		} else {
			name = "SaveSchemaDbStructure"
		}
	}
	f.record(name, body)
	if handler, ok := f.override[name]; ok && handler(w, body) {
		return
	}
	switch name {
	case "GetApplicationInfo":
		io.WriteString(w, `{"applicationInfo":{"sysValues":{"userCulture":{"displayValue":"en-US"}}}}`)
	case "Select:SysPackage":
		writeJSON(w, map[string]any{"success": true, "rows": []any{map[string]any{"Name": "UsrParityPkg", "UId": schemaWriteEntTestPackageUID, "Maintainer": "Customer", "Version": "", "InstallType": 0}}})
	case "Select:SysCulture":
		writeJSON(w, map[string]any{"success": true, "rows": []any{map[string]any{"Name": "en-US", "Active": true}, map[string]any{"Name": "uk-UA", "Active": false}}})
	case "Select:SysSchema":
		var rows []any
		for schema, pkg := range f.existing {
			if strings.Contains(string(body), `"`+schema+`"`) {
				rows = append(rows, map[string]any{"Name": schema, "PackageName": pkg, "Maintainer": "Customer", "ParentSchemaName": "BaseEntity"})
			}
		}
		if rows == nil {
			rows = []any{}
		}
		writeJSON(w, map[string]any{"success": true, "rows": rows})
	case "Select:Lookup", "Select:SysPackageSchemaData", "Select:SysInstalledApp":
		io.WriteString(w, `{"success":true,"rows":[]}`)
	case "CheckUniqueSchemaName":
		var request map[string]any
		json.Unmarshal(body, &request)
		_, exists := f.schemas[request["schemaName"].(string)]
		writeJSON(w, map[string]any{"success": true, "value": !exists})
	case "CreateNewSchema":
		writeJSON(w, map[string]any{"success": true, "schema": map[string]any{"uId": "22222222-2222-2222-2222-222222222222", "columns": []any{}, "unknownDesignerField": 1}})
	case "GetAvailableParentSchemas", "GetAvailableReferenceSchemas":
		writeJSON(w, map[string]any{"success": true, "items": []any{
			map[string]any{"uId": "33333333-3333-3333-3333-333333333333", "name": "BaseEntity", "caption": "Base object"},
			map[string]any{"uId": "44444444-4444-4444-4444-444444444444", "name": "BaseLookup", "caption": "Base lookup"},
			map[string]any{"uId": "55555555-5555-5555-5555-555555555555", "name": "Contact", "caption": "Contact"}}})
	case "AssignParentSchema":
		var request map[string]any
		json.Unmarshal(body, &request)
		schema := request["designSchema"].(map[string]any)
		schema["parentSchema"] = map[string]any{"uId": request["parentSchemaUId"], "name": "BaseEntity"}
		schema["inheritedColumns"] = []any{
			map[string]any{"uId": "66666666-6666-6666-6666-666666666666", "name": "Id", "type": 0, "caption": []any{map[string]any{"cultureName": "en-US", "value": "Id"}}},
			map[string]any{"uId": "77777777-7777-7777-7777-777777777777", "name": "Name", "type": 28, "caption": []any{map[string]any{"cultureName": "en-US", "value": "Name"}}}}
		writeJSON(w, map[string]any{"success": true, "schema": schema})
	case "SaveSchema":
		var schema map[string]any
		json.Unmarshal(body, &schema)
		f.schemas[schema["name"].(string)] = schema
		writeJSON(w, map[string]any{"success": true, "schemaUid": schema["uId"]})
	case "SaveSchemaDbStructure", "Publish", "RunODataBuild", "InsertQuery", "SaveSchemaData":
		io.WriteString(w, `{"success":true}`)
	case "IsODataBuildRunning":
		running := false
		if len(f.odata) > 0 {
			running, f.odata = f.odata[0], f.odata[1:]
		}
		writeJSON(w, map[string]any{"success": true, "value": running})
	case "GetSchemaDesignItem":
		var request map[string]any
		json.Unmarshal(body, &request)
		schema, ok := f.schemas[request["name"].(string)]
		if !ok {
			io.WriteString(w, `{"success":true,"schema":null}`)
			return
		}
		writeJSON(w, map[string]any{"success": true, "schema": schema})
	case "RuntimeEntitySchemaRequest":
		var request map[string]any
		json.Unmarshal(body, &request)
		schemaName, _ := request["Name"].(string)
		items := map[string]any{"Id": map[string]any{"uId": "66666666-6666-6666-6666-666666666666", "name": "Id", "dataValueType": 0},
			"Name":      map[string]any{"uId": "77777777-7777-7777-7777-777777777777", "name": "Name", "dataValueType": 28},
			"CreatedBy": map[string]any{"uId": "88888888-8888-8888-8888-888888888888", "name": "CreatedBy", "dataValueType": 10}}
		if saved, ok := f.schemas[schemaName]; ok {
			for _, column := range saved["columns"].([]any) {
				item := column.(map[string]any)
				items[item["name"].(string)] = map[string]any{"uId": item["uId"], "name": item["name"], "dataValueType": item["type"]}
			}
		} else if schemaName != "Lookup" {
			io.WriteString(w, `{"success":false,"errorInfo":{"message":"Can't find EntitySchema by Name: `+schemaName+`"}}`)
			return
		}
		writeJSON(w, map[string]any{"success": true, "schema": map[string]any{"uId": "99999999-9999-9999-9999-999999999999", "name": schemaName,
			"primaryColumnUId": "66666666-6666-6666-6666-666666666666", "columns": map[string]any{"items": items}}})
	default:
		f.t.Errorf("unexpected call %s (%s)", name, path)
		http.NotFound(w, r)
	}
}

func schemaWriteEntTestTitles(value string) schemaWriteEntLocMap {
	return schemaWriteEntLocMap{{Key: "en-US", Value: value}}
}

func schemaWriteEntTestString(value string) *string { return &value }

func schemaWriteEntTestMessages(result SchemaWriteEntResult) string {
	var lines []string
	for _, message := range result.Messages {
		lines = append(lines, message.MessageType+": "+message.Value)
	}
	return strings.Join(lines, "\n")
}

func TestCreateEntitySchemaSendsClioSequenceAndPayload(t *testing.T) {
	fake, client := newSchemaWriteEntFake(t)
	args := SchemaWriteEntCreateArgs{PackageName: "UsrParityPkg", SchemaName: "UsrParityThing", TitleLocalizations: schemaWriteEntTestTitles("Parity thing"),
		Columns: []SchemaWriteEntColumnArgs{
			{Name: schemaWriteEntTestString("UsrCode"), Type: schemaWriteEntTestString("ShortText"), Required: func() *bool { v := true; return &v }()},
			{ColumnName: schemaWriteEntTestString("UsrOwner"), DataValueType: schemaWriteEntTestString("Lookup"), ReferenceSchema: schemaWriteEntTestString("Contact")},
		}}
	result := client.CreateEntitySchema(context.Background(), args, nil)
	if result.ExitCode != 0 {
		t.Fatalf("exit %d\n%s", result.ExitCode, schemaWriteEntTestMessages(result))
	}
	want := []string{"CheckUniqueSchemaName", "Select:SysPackage", "CreateNewSchema", "CheckUniqueSchemaName",
		"GetAvailableParentSchemas", "AssignParentSchema", "GetApplicationInfo", "GetAvailableReferenceSchemas", "Select:SysCulture",
		"SaveSchema", "SaveSchemaDbStructure", "IsODataBuildRunning", "Publish", "RunODataBuild", "GetSchemaDesignItem"}
	if strings.Join(fake.calls, ",") != strings.Join(want, ",") {
		t.Fatalf("calls\n got %v\nwant %v", fake.calls, want)
	}
	unique := fake.bodies["CheckUniqueSchemaName"]
	var first map[string]any
	json.Unmarshal(unique[0], &first)
	if first["managerName"] != "EntitySchemaManager" || first["excludeUId"] != emptyGUID || first["schemaName"] != "UsrParityThing" {
		t.Fatalf("first unique check %v", first)
	}
	saved := fake.last("SaveSchema")
	if saved["name"] != "UsrParityThing" || saved["package"].(map[string]any)["uId"] != schemaWriteEntTestPackageUID {
		t.Fatalf("saved %v", saved)
	}
	if _, kept := saved["unknownDesignerField"]; kept {
		t.Fatal("a field clio's DTO does not model must not be sent back")
	}
	caption := saved["caption"].([]any)[0].(map[string]any)
	if caption["cultureName"] != "en-US" || caption["value"] != "Parity thing" {
		t.Fatalf("caption %v", caption)
	}
	columns := saved["columns"].([]any)
	code, owner := columns[0].(map[string]any), columns[1].(map[string]any)
	if code["name"] != "UsrCode" || code["type"] != float64(27) || code["requirementType"] != float64(1) || code["defValue"] != nil {
		t.Fatalf("code column %v", code)
	}
	if owner["type"] != float64(10) || owner["referenceSchema"].(map[string]any)["name"] != "Contact" {
		t.Fatalf("owner column %v", owner)
	}
	if saved["primaryDisplayColumn"].(map[string]any)["name"] != "UsrCode" {
		t.Fatalf("primary display %v", saved["primaryDisplayColumn"])
	}
	publish := fake.last("Publish")
	if publish["buildChangedConfiguration"] != true || len(publish["saveSchemaDBStructure"].([]any)) != 0 {
		t.Fatalf("publish %v", publish)
	}
	if structure := fake.last("SaveSchemaDbStructure"); structure["saveSchemaDBStructure"].([]any)[0] != "22222222-2222-2222-2222-222222222222" {
		t.Fatalf("db structure %v", structure)
	}
	text := schemaWriteEntTestMessages(result)
	if !strings.Contains(text, "Info: OData entities rebuild requested for 'UsrParityThing'.") ||
		!strings.Contains(text, "Info: Entity schema 'UsrParityThing' created in package 'UsrParityPkg'.") ||
		!strings.HasSuffix(text, "Info: Done") {
		t.Fatal(text)
	}
}

func TestCreateEntitySchemaReportsDesignerRefusal(t *testing.T) {
	fake, client := newSchemaWriteEntFake(t)
	fake.override["SaveSchema"] = func(w http.ResponseWriter, _ []byte) bool {
		io.WriteString(w, `{"success":false,"errorInfo":{"message":"Column name is reserved"}}`)
		return true
	}
	result := client.CreateEntitySchema(context.Background(), SchemaWriteEntCreateArgs{PackageName: "UsrParityPkg",
		SchemaName: "UsrParityThing", TitleLocalizations: schemaWriteEntTestTitles("Parity thing")}, nil)
	if result.ExitCode != 1 || result.Messages[len(result.Messages)-1].Value != "Column name is reserved" {
		t.Fatalf("%d %s", result.ExitCode, schemaWriteEntTestMessages(result))
	}
	if fake.count("Publish") != 0 || fake.count("SaveSchemaDbStructure") != 0 {
		t.Fatalf("a refused save must not publish: %v", fake.calls)
	}
}

func TestCreateEntitySchemaRefusesBeforeAnyWrite(t *testing.T) {
	fake, client := newSchemaWriteEntFake(t)
	fake.schemas["UsrParityThing"] = map[string]any{"name": "UsrParityThing"}
	result := client.CreateEntitySchema(context.Background(), SchemaWriteEntCreateArgs{PackageName: "UsrParityPkg",
		SchemaName: "UsrParityThing", TitleLocalizations: schemaWriteEntTestTitles("Parity")}, nil)
	if result.ExitCode != 1 || result.Messages[0].Value != "Schema 'UsrParityThing' already exists." {
		t.Fatalf("%v", result)
	}
	if strings.Join(fake.calls, ",") != "CheckUniqueSchemaName" {
		t.Fatalf("calls %v", fake.calls)
	}
	if message := SchemaWriteEntCreatePrecheck(SchemaWriteEntCreateArgs{SchemaName: "UsrA", PackageName: "P",
		TitleLocalizations: schemaWriteEntTestTitles("A"), Columns: []SchemaWriteEntColumnArgs{{Type: schemaWriteEntTestString("Text")}}}, false); message !=
		"Schema 'UsrA' column #1 is missing the target column. Send it as 'column-name' (or its alias 'name'). (Parameter 'column')" {
		t.Fatal(message)
	}
}

func TestCreateLookupRegistersInLookupCatalog(t *testing.T) {
	fake, client := newSchemaWriteEntFake(t)
	result := client.CreateLookup(context.Background(), SchemaWriteEntCreateArgs{PackageName: "UsrParityPkg",
		SchemaName: "UsrParityKind", TitleLocalizations: schemaWriteEntTestTitles("Parity kind")}, nil)
	if result.ExitCode != 0 {
		t.Fatalf("%s", schemaWriteEntTestMessages(result))
	}
	assign := fake.last("AssignParentSchema")
	if assign["parentSchemaUId"] != "44444444-4444-4444-4444-444444444444" {
		t.Fatalf("lookup parent %v", assign["parentSchemaUId"])
	}
	insert := fake.last("InsertQuery")
	items := insert["columnValues"].(map[string]any)["items"].(map[string]any)
	if insert["rootSchemaName"] != "Lookup" || items["Name"].(map[string]any)["parameter"].(map[string]any)["value"] != "Parity kind" ||
		items["SysEntitySchemaUId"].(map[string]any)["parameter"].(map[string]any)["value"] != "99999999-9999-9999-9999-999999999999" {
		t.Fatalf("insert %v", insert)
	}
	binding := fake.last("SaveSchemaData")
	if binding["name"] != "Lookup_UsrParityKind" || binding["entitySchemaName"] != "Lookup" {
		t.Fatalf("binding %v", binding)
	}
	for _, column := range binding["columns"].([]any) {
		if column.(map[string]any)["name"] == "CreatedBy" {
			t.Fatal("CreatedBy must not be bound")
		}
	}
	if !strings.Contains(schemaWriteEntTestMessages(result), "Info: Lookup 'UsrParityKind' registered in Lookups.") {
		t.Fatal(schemaWriteEntTestMessages(result))
	}
	if shadow := SchemaWriteEntLookupShadowError([]SchemaWriteEntColumnArgs{{Name: schemaWriteEntTestString("name")},
		{ColumnName: schemaWriteEntTestString("Description")}}); shadow != "create-lookup inherits BaseLookup columns. Do not add inherited columns: Description, name." {
		t.Fatal(shadow)
	}
}

func TestCreateLookupReportsRegistrationFailureAfterCreate(t *testing.T) {
	fake, client := newSchemaWriteEntFake(t)
	fake.override["InsertQuery"] = func(w http.ResponseWriter, _ []byte) bool {
		io.WriteString(w, `{"success":false,"errorInfo":{"message":"denied"}}`)
		return true
	}
	result := client.CreateLookup(context.Background(), SchemaWriteEntCreateArgs{PackageName: "UsrParityPkg",
		SchemaName: "UsrParityKind", TitleLocalizations: schemaWriteEntTestTitles("Parity kind")}, nil)
	text := schemaWriteEntTestMessages(result)
	if result.ExitCode != 1 || !strings.Contains(text, "Info: Done") || !strings.HasSuffix(text, "Error: InsertQuery failed: denied") {
		t.Fatalf("%d\n%s", result.ExitCode, text)
	}
	if fake.count("SaveSchemaData") != 0 {
		t.Fatal("no binding after a failed catalog insert")
	}
}

func schemaWriteEntSeedSchema(fake *schemaWriteEntFake) {
	fake.schemas["UsrParityThing"] = map[string]any{"uId": "22222222-2222-2222-2222-222222222222", "name": "UsrParityThing",
		"caption":      []any{map[string]any{"cultureName": "en-US", "value": "Thing"}, map[string]any{"cultureName": "uk-UA", "value": "Річ"}},
		"package":      map[string]any{"uId": schemaWriteEntTestPackageUID, "name": "UsrParityPkg"},
		"parentSchema": map[string]any{"uId": "33333333-3333-3333-3333-333333333333", "name": "BaseEntity"},
		"columns": []any{map[string]any{"uId": "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", "name": "UsrCode", "type": 27,
			"caption": []any{map[string]any{"cultureName": "en-US", "value": "Code"}}}},
		"inheritedColumns": []any{map[string]any{"uId": "77777777-7777-7777-7777-777777777777", "name": "Name", "type": 28,
			"caption": []any{map[string]any{"cultureName": "en-US", "value": "Name"}}}},
		"primaryDisplayColumn": map[string]any{"uId": "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", "name": "UsrCode"}}
}

func TestUpdateEntitySchemaAddsColumnAndNotesNoCompile(t *testing.T) {
	fake, client := newSchemaWriteEntFake(t)
	schemaWriteEntSeedSchema(fake)
	args, err := ParseUpdateEntitySchemaArgs(map[string]any{"package-name": "UsrParityPkg", "schema-name": "UsrParityThing",
		"operations": []any{map[string]any{"action": "add", "column-name": "UsrDueDate", "data-value-type": "Date"}}})
	if err != nil {
		t.Fatal(err)
	}
	result := client.UpdateEntitySchema(context.Background(), args, nil)
	if result.ExitCode != 0 || result.Note != "compile-creatio not required" {
		t.Fatalf("%d %s", result.ExitCode, schemaWriteEntTestMessages(result))
	}
	want := "Select:SysPackage,GetSchemaDesignItem,GetApplicationInfo,RuntimeEntitySchemaRequest,Select:SysCulture,SaveSchema,SaveSchemaDbStructure,IsODataBuildRunning,Publish,RunODataBuild,GetSchemaDesignItem"
	if strings.Join(fake.calls, ",") != want {
		t.Fatalf("calls %v", fake.calls)
	}
	saved := fake.last("SaveSchema")
	columns := saved["columns"].([]any)
	added := columns[len(columns)-1].(map[string]any)
	if added["name"] != "UsrDueDate" || added["type"] != float64(7) || added["caption"].([]any)[0].(map[string]any)["value"] != "Due Date" {
		t.Fatalf("added %v", added)
	}
	if !strings.Contains(schemaWriteEntTestMessages(result), "Info: Column 'UsrDueDate' action 'add' completed for schema 'UsrParityThing'.") {
		t.Fatal(schemaWriteEntTestMessages(result))
	}
}

func TestUpdateEntitySchemaFailsOnMissingColumnWithoutSaving(t *testing.T) {
	fake, client := newSchemaWriteEntFake(t)
	schemaWriteEntSeedSchema(fake)
	args, _ := ParseUpdateEntitySchemaArgs(map[string]any{"package-name": "UsrParityPkg", "schema-name": "UsrParityThing",
		"operations": []any{map[string]any{"action": "modify", "column-name": "UsrMissing", "required": true}}})
	result := client.UpdateEntitySchema(context.Background(), args, nil)
	if result.ExitCode != 1 || result.Note != "" || result.Messages[len(result.Messages)-1].Value != "Column 'UsrMissing' was not found in schema 'UsrParityThing'." {
		t.Fatalf("%v", result)
	}
	if fake.count("SaveSchema") != 0 {
		t.Fatal("nothing may be saved")
	}
	if UpdateEntitySchemaPrecheck(UpdateEntitySchemaArgs{SchemaName: "S", Operations: []SchemaWriteEntOperationArgs{{Action: schemaWriteEntTestString("modify"),
		ColumnName: schemaWriteEntTestString("UsrA"), Title: schemaWriteEntTestString("A")}}}) != "Schema 'S' operation #1 does not accept legacy 'title'. Use 'title-localizations' instead." {
		t.Fatal("legacy title on modify must be refused")
	}
}

func TestModifyEntitySchemaColumnOverridesInheritedCaptionOnly(t *testing.T) {
	fake, client := newSchemaWriteEntFake(t)
	schemaWriteEntSeedSchema(fake)
	parsed, _ := ParseModifyEntitySchemaColumnArgs(map[string]any{"package-name": "UsrParityPkg", "schema-name": "UsrParityThing",
		"action": "modify", "column-name": "Name", "required": true})
	result := client.ModifyEntitySchemaColumn(context.Background(), parsed)
	if result.ExitCode != 1 || !strings.HasPrefix(result.Messages[len(result.Messages)-1].Value, "Column 'Name' is inherited; only its caption") {
		t.Fatalf("%v", result)
	}
	if result.DataForge != nil || fake.count("SaveSchema") != 0 {
		t.Fatal("refusal must not save or enrich")
	}
	parsed, _ = ParseModifyEntitySchemaColumnArgs(map[string]any{"package-name": "UsrParityPkg", "schema-name": "UsrParityThing",
		"action": "modify", "column-name": "Name", "title-localizations": map[string]any{"en-US": "Title"}})
	fake.override["GetSchemaDesignItem"] = nil
	delete(fake.override, "GetSchemaDesignItem")
	result = client.ModifyEntitySchemaColumn(context.Background(), parsed)
	// The fake reloads the saved document, whose inherited column now carries the override.
	if result.ExitCode != 0 || fake.count("RunODataBuild") != 0 {
		t.Fatalf("%d %s %v", result.ExitCode, schemaWriteEntTestMessages(result), fake.calls)
	}
}

func TestSetEntitySchemaPropertiesVerifiesReadback(t *testing.T) {
	fake, client := newSchemaWriteEntFake(t)
	schemaWriteEntSeedSchema(fake)
	isView := false
	result := client.SetEntitySchemaProperties(context.Background(), SetEntitySchemaPropertiesArgs{PackageName: "UsrParityPkg",
		SchemaName: "UsrParityThing", PrimaryDisplayColumn: "name", TitleLocalizations: schemaWriteEntTestTitles("Renamed"), IsDBView: &isView})
	if result.ExitCode != 0 {
		t.Fatalf("%s", schemaWriteEntTestMessages(result))
	}
	saved := fake.last("SaveSchema")
	if saved["primaryDisplayColumn"].(map[string]any)["name"] != "Name" {
		t.Fatalf("primary display %v", saved["primaryDisplayColumn"])
	}
	captions := saved["caption"].([]any)
	if len(captions) != 2 || captions[0].(map[string]any)["value"] != "Renamed" || captions[1].(map[string]any)["value"] != "Річ" {
		t.Fatalf("captions %v", captions)
	}
	if fake.count("RunODataBuild") != 0 {
		t.Fatal("schema properties leave the OData contract unchanged")
	}
	text := schemaWriteEntTestMessages(result)
	for _, line := range []string{"Info: Database-view flag set to 'False' for schema 'UsrParityThing'.",
		"Info: Primary-display column set to 'name' for schema 'UsrParityThing'.",
		"Info: Schema caption set to 'Renamed' (en-US) for schema 'UsrParityThing'."} {
		if !strings.Contains(text, line) {
			t.Fatalf("missing %q in\n%s", line, text)
		}
	}
}

func TestSetEntitySchemaPropertiesFailsWhenCaptionIsNotPersisted(t *testing.T) {
	fake, client := newSchemaWriteEntFake(t)
	schemaWriteEntSeedSchema(fake)
	fake.override["SaveSchema"] = func(w http.ResponseWriter, _ []byte) bool {
		io.WriteString(w, `{"success":true,"schemaUid":"22222222-2222-2222-2222-222222222222"}`)
		return true
	}
	result := client.SetEntitySchemaProperties(context.Background(), SetEntitySchemaPropertiesArgs{PackageName: "UsrParityPkg",
		SchemaName: "UsrParityThing", TitleLocalizations: schemaWriteEntTestTitles("Renamed")})
	if result.ExitCode != 1 || result.Messages[len(result.Messages)-1].Value != "Schema caption 'Renamed' (en-US) was not persisted for schema 'UsrParityThing'. The server returned 'Thing'." {
		t.Fatalf("%s", schemaWriteEntTestMessages(result))
	}
	none := client.SetEntitySchemaProperties(context.Background(), SetEntitySchemaPropertiesArgs{PackageName: "P", SchemaName: "S"})
	if none.Messages[0].Value != schemaWriteEntNoPropertyToSet+" (Parameter 'options')" {
		t.Fatal(none.Messages[0].Value)
	}
}

func TestPublishWaitsForRunningODataBuild(t *testing.T) {
	fake, client := newSchemaWriteEntFake(t)
	schemaWriteEntSeedSchema(fake)
	fake.odata = []bool{true, true, false}
	result := client.SetEntitySchemaProperties(context.Background(), SetEntitySchemaPropertiesArgs{PackageName: "UsrParityPkg",
		SchemaName: "UsrParityThing", PrimaryDisplayColumn: "UsrCode"})
	if result.ExitCode != 0 || fake.count("IsODataBuildRunning") != 3 {
		t.Fatalf("%v %s", fake.calls, schemaWriteEntTestMessages(result))
	}
	if !strings.Contains(schemaWriteEntTestMessages(result), "Info: Waiting for the running OData entities build to finish before 'UsrParityThing'.") {
		t.Fatal(schemaWriteEntTestMessages(result))
	}
}

func TestLoadSchemaNamesTheMissingDependency(t *testing.T) {
	fake, client := newSchemaWriteEntFake(t)
	fake.existing["UsrParityElsewhere"] = "UsrOtherPkg"
	fake.override["GetPackageProperties"] = func(w http.ResponseWriter, _ []byte) bool {
		io.WriteString(w, `{"success":true,"package":{"uId":"`+schemaWriteEntTestPackageUID+`","name":"UsrParityPkg","dependsOnPackages":[{"name":"Base"}]}}`)
		return true
	}
	parsed, _ := ParseModifyEntitySchemaColumnArgs(map[string]any{"package-name": "UsrParityPkg", "schema-name": "UsrParityElsewhere",
		"action": "add", "column-name": "UsrA", "type": "Text"})
	result := client.ModifyEntitySchemaColumn(context.Background(), parsed)
	message := result.Messages[len(result.Messages)-1].Value
	if result.ExitCode != 1 || !strings.Contains(message, "These packages contribute 'UsrParityElsewhere' and are not already dependencies of 'UsrParityPkg': UsrOtherPkg.") ||
		!strings.HasSuffix(message, "Underlying failure: GetSchemaDesignItem returned no schema for 'UsrParityElsewhere'.") {
		t.Fatal(message)
	}
}

func TestSchemaSyncConvergesAndStopsAtUnsupportedSeed(t *testing.T) {
	fake, client := newSchemaWriteEntFake(t)
	args, err := ParseSchemaSyncArgs(map[string]any{"package-name": "UsrParityPkg", "operations": []any{
		map[string]any{"type": "create-entity", "schema-name": "UsrParityThing", "title-localizations": map[string]any{"en-US": "Thing"},
			"columns": []any{map[string]any{"name": "UsrCode", "type": "Text"}}},
		map[string]any{"type": "seed-data", "schema-name": "UsrParityThing", "seed-rows": []any{map[string]any{"values": map[string]any{"UsrCode": "A"}}}},
		map[string]any{"type": "update-entity", "schema-name": "UsrParityThing", "columns": []any{map[string]any{"name": "UsrCode", "type": "Text"}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	var stages []string
	response := SchemaSync(context.Background(), client, "", args, nil, func(stage string) { stages = append(stages, stage) })
	if response.Success || len(response.Results) != 2 || response.Results[0].Outcome != "created" || response.Results[1].Type != "seed-data" {
		encoded, _ := json.Marshal(response)
		t.Fatalf("%s", encoded)
	}
	if fake.count("SaveSchema") != 1 || strings.Join(stages, "|") != "1/3: create-entity UsrParityThing|2/3: seed-data UsrParityThing" {
		t.Fatalf("%v %v", fake.calls, stages)
	}
	plan := response.ResumePlan
	if plan == nil || plan.FailedOperation.OperationIndex != 1 || len(plan.Operations) != 2 || plan.NotRunOperationIndexes[0] != 2 {
		t.Fatalf("plan %#v", plan)
	}
	fake.existing["UsrParityThing"] = "UsrParityPkg"
	replay, _ := ParseSchemaSyncArgs(map[string]any{"package-name": "UsrParityPkg", "operations": []any{
		map[string]any{"type": "update-entity", "schema-name": "UsrParityThing", "columns": []any{map[string]any{"name": "UsrCode", "type": "text"}}}}})
	again := SchemaSync(context.Background(), client, "", replay, nil, nil)
	if !again.Success || again.Results[0].Outcome != "already-satisfied" || again.ResumePlan != nil {
		encoded, _ := json.Marshal(again)
		t.Fatalf("%s", encoded)
	}
}

func TestSchemaSyncReportsCrossPackageCollision(t *testing.T) {
	fake, client := newSchemaWriteEntFake(t)
	fake.existing["UsrParityThing"] = "UsrOtherPkg"
	args, _ := ParseSchemaSyncArgs(map[string]any{"package-name": "UsrParityPkg", "operations": []any{
		map[string]any{"type": "create-entity", "schema-name": "UsrParityThing", "title-localizations": map[string]any{"en-US": "Thing"}}}})
	response := SchemaSync(context.Background(), client, "", args, nil, nil)
	result := response.Results[0]
	if response.Success || result.Outcome != "collision" || result.CollisionInfo == nil || result.CollisionInfo.ExistingPackageName != "UsrOtherPkg" {
		encoded, _ := json.Marshal(response)
		t.Fatalf("%s", encoded)
	}
	if fake.count("SaveSchema") != 0 || fake.count("CreateNewSchema") != 0 {
		t.Fatal("a collision must not write")
	}
	if rejection := SchemaSyncValidateTopLevel(SchemaSyncArgs{Extension: map[string]any{"ops": 1}}); rejection == nil ||
		*rejection.Results[0].Error != "sync-schemas arguments are invalid: Rename: 'ops' -> 'operations'. Nothing was applied." {
		t.Fatalf("%v", rejection)
	}
}

func TestSchemaWriteEntTypeVocabulary(t *testing.T) {
	for name, want := range map[string]int{"Money": 6, "date": 7, "Float": 32, "image-link": 16, "ShortText": 27, "EmailText": 45} {
		if got, ok := schemaWriteEntResolveType(name); !ok || got != want {
			t.Fatalf("%s -> %d %v", name, got, ok)
		}
	}
	if _, ok := schemaWriteEntResolveType("Enum"); ok {
		t.Fatal("Enum is not writable")
	}
	if !schemaWriteEntTypesEquivalent("MediumText", "MediumText") || schemaWriteEntTypesEquivalent("Text", "Integer") || !schemaWriteEntTypesEquivalent("Float2", "Float") {
		t.Fatal("type equivalence")
	}
	if humanized := schemaWriteEntHumanize("UsrDueDateUTC"); humanized != "Due Date UTC" {
		t.Fatal(humanized)
	}
}
