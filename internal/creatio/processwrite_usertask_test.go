package creatio

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	processWriteTaskPackageUID = "7d2b9c4e-5a1f-4e3b-9c8d-13f13f13f001"
	processWriteTaskSchemaUID  = "aaaaaaaa-1111-2222-3333-444444444444"
)

func processWriteTaskWorkspace(t *testing.T, metadata string) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		".clio/workspaceSettings.json":    `{"Packages":["UsrPkg"]}`,
		"packages/UsrPkg/descriptor.json": "\xef\xbb\xbf" + `{"Descriptor":{"UId":"` + processWriteTaskPackageUID + `","Name":"UsrPkg","Type":0}}`,
	}
	if metadata != "" {
		files["packages/UsrPkg/Schemas/UsrTask/metadata.json"] = metadata
	}
	for path, content := range files {
		full := filepath.Join(root, path)
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func processWriteTaskCalls(calls []processWriteCall, suffix string) []processWriteCall {
	found := []processWriteCall{}
	for _, call := range calls {
		if strings.HasSuffix(call.Path, suffix) {
			found = append(found, call)
		}
	}
	return found
}

const processWriteTaskNewSchema = `{"success":true,"schema":{"uId":"` + processWriteTaskSchemaUID + `","id":"bbbbbbbb-1111-2222-3333-444444444444","name":"UsrProcessUserTask1",` +
	`"caption":[],"parameters":[],"dependencies":{"x":1},"unknownField":true,"color":"","package":null,"smallSvgImage":{"_isChanged":true,"imageContent":"svg"}}}`

func TestCreateUserTaskCreatesSavesAndBuilds(t *testing.T) {
	root := processWriteTaskWorkspace(t, "")
	client, calls := processWriteServer(t, "", func(call processWriteCall) (int, string) {
		switch {
		case strings.HasSuffix(call.Path, "/CreateNewSchema"):
			return 200, processWriteTaskNewSchema
		case strings.HasSuffix(call.Path, "/GetAvailableEntitySchemas"):
			return 200, `{"items":[{"uId":"cccccccc-1111-2222-3333-444444444444","name":"Account","caption":"Account"}]}`
		case strings.HasSuffix(call.Path, "/SaveSchema"):
			return 200, `{"success":true,"schemaUid":"` + processWriteTaskSchemaUID + `"}`
		}
		return 200, `{"success":true}`
	})
	description := "Does it"
	parameters := []ProcessWriteTaskParameter{
		{Code: "Account", Title: "Account", Type: "lookup", Lookup: " Account ", Required: processWriteBoolPointer(true)},
		{Code: "Rows", Title: "Rows", Type: "Serializable list of composite values", Items: []ProcessWriteTaskParameter{{Code: "RowId", Title: "Row id", Type: "Guid"}}},
	}
	result := client.CreateUserTask(context.Background(), ProcessWriteCreateTaskRequest{Code: " UsrTask ", Package: "UsrPkg", Title: "Task", Description: &description,
		Culture: "en-US", WorkspacePath: " " + root + " ", Parameters: ProcessWriteTaskDefinitions(parameters), ParameterItems: ProcessWriteTaskNestedItems(parameters)})
	got := processWriteMessages(result)
	want := []string{"Info: Created user task schema 'UsrTask' (" + processWriteTaskSchemaUID + ").", "Info: Building package 'UsrPkg'...", "Info: Done add-user-task"}
	if result.ExitCode != 0 || result.Note != "" || strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("%d %q", result.ExitCode, got)
	}
	create := processWriteTaskCalls(*calls, "/CreateNewSchema")[0]
	if create.Path != "/0/ServiceModel/ProcessUserTaskSchemaDesignerService.svc/CreateNewSchema" || create.Body != `{"packageUId":"`+processWriteTaskPackageUID+`","cultures":null}` {
		t.Fatalf("create %+v", create)
	}
	if lookup := processWriteTaskCalls(*calls, "/GetAvailableEntitySchemas")[0]; lookup.Body != `{"packageUId":"`+processWriteTaskPackageUID+`"}` {
		t.Fatalf("lookup %+v", lookup)
	}
	if build := processWriteTaskCalls(*calls, "/BuildPackage")[0]; build.Path != "/0/ServiceModel/WorkspaceExplorerService.svc/BuildPackage" || build.Body != `{"packageName":"UsrPkg"}` {
		t.Fatalf("build %+v", build)
	}
	save := processWriteTaskCalls(*calls, "/SaveSchema")[0].Body
	var schema map[string]any
	if err := json.Unmarshal([]byte(save), &schema); err != nil {
		t.Fatal(err)
	}
	keys := []string{}
	decoder := json.NewDecoder(strings.NewReader(save))
	_, _ = decoder.Token()
	for decoder.More() {
		key, _ := decoder.Token()
		keys = append(keys, key.(string))
		var skip json.RawMessage
		_ = decoder.Decode(&skip)
	}
	if strings.Join(keys, ",") != "uId,id,name,body,metaData,caption,description,parameters,localizableStrings,addonTypes,optionalProperties,dependencies,isReadOnly,useFullHierarchy,forceSave,userLevelSchema,isFullHierarchyDesignSchema,partial,userTask,customEventHandler,editPageSchemaUId,dcmEditPageSchemaUId,smallSvgImage,largeSvgImage,titleSvgImage,dcmSmallSvgImage,color,serializeToDB,package" {
		t.Fatalf("keys %v", keys)
	}
	wantMeta := `{"metaData":{"schema":{"managerName":"ProcessUserTaskSchemaManager","uId":"` + processWriteTaskSchemaUID + `","name":"UsrTask","methods":[],"localizableStrings":[],"usings":[],"packageUId":"` + processWriteTaskPackageUID + `","parameters":[],"color":"#839DC3"}}}`
	if schema["name"] != "UsrTask" || schema["body"] != "" || schema["metaData"] != wantMeta || schema["color"] != "#839DC3" || schema["userTask"] != true ||
		schema["serializeToDB"] != true || schema["addonTypes"] != nil || processWriteJSONText(schema["dependencies"]) != `{"x":1}` || schema["unknownField"] != nil ||
		processWriteJSONText(schema["package"]) != `{"name":"UsrPkg","type":0,"uId":"`+processWriteTaskPackageUID+`"}` ||
		processWriteJSONText(schema["smallSvgImage"]) != `{"_isChanged":true,"imageContent":"svg"}` || processWriteJSONText(schema["largeSvgImage"]) != `{"_isChanged":false,"imageContent":null}` ||
		processWriteJSONText(schema["caption"]) != `[{"cultureName":"en-US","value":"Task"}]` || processWriteJSONText(schema["description"]) != `[{"cultureName":"en-US","value":"Does it"}]` {
		t.Fatalf("schema %s", save)
	}
	parametersSaved := schema["parameters"].([]any)
	account, rows := parametersSaved[0].(map[string]any), parametersSaved[1].(map[string]any)
	if account["name"] != "Account" || account["type"] != float64(10) || account["lookup"] != "cccccccc-1111-2222-3333-444444444444" || account["required"] != true ||
		account["resulting"] != true || account["serializable"] != true || account["direction"] != float64(2) || account["icon"] != "data-type-lookup-icon.svg" ||
		account["schema"] != nil || account["copyValue"] != nil {
		t.Fatalf("account %v", account)
	}
	item := rows["itemProperties"].([]any)[0].(map[string]any)
	if rows["type"] != float64(39) || rows["resulting"] != false || rows["serializable"] != false || item["name"] != "RowId" || item["type"] != float64(0) ||
		item["resulting"] != false || item["icon"] != "data-type-guid-icon.svg" {
		t.Fatalf("rows %v", rows)
	}
}

func processWriteBoolPointer(value bool) *bool { return &value }

func processWriteJSONText(value any) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func TestCreateUserTaskAppliesExplicitDirectionsThroughTheWorkspace(t *testing.T) {
	root := processWriteTaskWorkspace(t, `{"MetaData":{"Schema":{"FJ1":[{"A2":"Amount","L12":2},{"A2":"Note"}]}}}`)
	loaded := false
	client, calls := processWriteServer(t, "", func(call processWriteCall) (int, string) {
		switch {
		case strings.HasSuffix(call.Path, "/CreateNewSchema"):
			return 200, processWriteTaskNewSchema
		case strings.HasSuffix(call.Path, "/SaveSchema"):
			return 200, `{"success":true,"schemaUid":"` + processWriteTaskSchemaUID + `"}`
		case strings.HasSuffix(call.Path, "/GetIsFileDesignMode"):
			return 200, `{"success":true,"value":true}`
		case strings.HasSuffix(call.Path, "/LoadPackagesToDB"):
			loaded = true
			return 200, `{"success":true,"errors":[]}`
		}
		return 200, `{}`
	})
	parameters := []ProcessWriteTaskParameter{{Code: "Amount", Title: "Amount", Type: "Money", Direction: "Out"}, {Code: "Note", Title: "Note", Type: "Text", Direction: "in"}}
	result := client.CreateUserTask(context.Background(), ProcessWriteCreateTaskRequest{Code: "UsrTask", Package: "UsrPkg", Title: "Task", Culture: "en-US",
		WorkspacePath: root, Parameters: ProcessWriteTaskDefinitions(parameters)})
	got := processWriteMessages(result)
	want := []string{"Info: Created user task schema 'UsrTask' (" + processWriteTaskSchemaUID + ").", "Info: Building package 'UsrPkg'...",
		"Info: Applying direction metadata for 2 parameter(s) on 'UsrTask'...", "Info: Loading workspace packages to database to apply direction metadata changes...",
		"None: Start load packages to database on a web application", "Info: Load packages to database on a web application completed",
		"Info: Building package 'UsrPkg'...", "Info: Done add-user-task"}
	if result.ExitCode != 0 || strings.Join(got, "\n") != strings.Join(want, "\n") || !loaded || len(processWriteTaskCalls(*calls, "/BuildPackage")) != 2 {
		t.Fatalf("%q", got)
	}
	metadata, _ := os.ReadFile(filepath.Join(root, "packages/UsrPkg/Schemas/UsrTask/metadata.json"))
	if !strings.Contains(string(metadata), `"L12": 1`) || !strings.Contains(string(metadata), `"A2": "Note",`+"\n"+`          "L12": 0`) {
		t.Fatalf("metadata %s", metadata)
	}
}

func TestCreateUserTaskStopsWhenFileDesignModeIsOff(t *testing.T) {
	root := processWriteTaskWorkspace(t, `{"MetaData":{"Schema":{"FJ1":[{"A2":"Amount"}]}}}`)
	client, calls := processWriteServer(t, "", func(call processWriteCall) (int, string) {
		switch {
		case strings.HasSuffix(call.Path, "/CreateNewSchema"):
			return 200, processWriteTaskNewSchema
		case strings.HasSuffix(call.Path, "/SaveSchema"):
			return 200, `{"success":true,"schemaUid":"` + processWriteTaskSchemaUID + `"}`
		case strings.HasSuffix(call.Path, "/GetIsFileDesignMode"):
			return 200, `{"success":true,"value":false}`
		}
		return 200, `{}`
	})
	result := client.CreateUserTask(context.Background(), ProcessWriteCreateTaskRequest{Code: "UsrTask", Package: "UsrPkg", Title: "Task", Culture: "en-US",
		WorkspacePath: root, Parameters: []string{"code=Amount;title=A;type=Money;direction=1"}})
	last := result.Messages[len(result.Messages)-1]
	if result.ExitCode != 1 || last.Value != "Parameter direction metadata for user task 'UsrTask' was written to the workspace but not applied on the environment: "+
		"file system development mode is disabled on the environment. The user task schema 'UsrTask' has already been created and built on the environment, so do not "+
		"re-run add-user-task: enable file system development mode (clio turn-fsm on) and run 'clio pkg-to-db' followed by 'clio compile-package UsrPkg' to finish applying the directions." ||
		len(processWriteTaskCalls(*calls, "/LoadPackagesToDB")) != 0 {
		t.Fatalf("%+v", result)
	}
}

func TestCreateUserTaskRefusals(t *testing.T) {
	root := processWriteTaskWorkspace(t, "")
	client, calls := processWriteServer(t, "", func(call processWriteCall) (int, string) {
		switch {
		case strings.HasSuffix(call.Path, "/CreateNewSchema"):
			return 200, processWriteTaskNewSchema
		case strings.HasSuffix(call.Path, "/GetAvailableEntitySchemas"):
			return 200, `{"items":[{"uId":"cccccccc-1111-2222-3333-444444444444","name":"Account"},{"uId":"dddddddd-1111-2222-3333-444444444444","name":"account"}]}`
		case strings.HasSuffix(call.Path, "/SaveSchema"):
			return 200, `{"success":false,"validationErrors":[{"source":"A","reference":"B","package":"C"}]}`
		}
		return 200, `{}`
	})
	cases := []struct {
		parameters []string
		items      []string
		want       string
	}{
		{[]string{"code=A;title=A;type=Text", "code=a;title=B;type=Text"}, nil, "Parameter 'a' is defined more than once. Parameter names must be unique."},
		{[]string{"code=A;title=A;type=Weird"}, nil, "Unsupported parameter type 'Weird'. Supported types: Boolean, Date, DateTime, Float, Guid, Integer, Lookup, Money, Unique identifier, Serializable list of composite values, Text, Unlimited text, Time."},
		{[]string{"code=A;title=A;type=Lookup"}, nil, "Lookup parameter definition 'code=A;title=A;type=Lookup' must include a non-empty 'lookup' value."},
		{[]string{"code=A;title=A;type=Text;lookup=Account"}, nil, "Parameter definition 'code=A;title=A;type=Text;lookup=Account' can only use 'lookup' when type=Lookup."},
		{[]string{"code=A;title=A;type=Lookup;lookup=Account"}, nil, "Lookup schema 'Account' resolved to multiple Creatio schemas."},
		{[]string{"code=A;title=;type=Text"}, nil, "Parameter definition 'code=A;title=;type=Text' must include a non-empty 'title' value."},
		{[]string{"code=A;title=A;type=Text;required=maybe"}, nil, "Parameter definition 'code=A;title=A;type=Text;required=maybe' has invalid boolean value 'maybe' for 'required'."},
		{[]string{"code=A;title=x;y;type=Text"}, nil, "Parameter definition 'code=A;title=x;y;type=Text' must use '<key>=<value>' pairs separated by ';'."},
		{[]string{"code=A;title=A;type=Text"}, []string{"parent=A;code=B;title=B;type=Text"}, "Parameter 'A' is not type=Serializable list of composite values."},
		{nil, []string{"parent=Missing;code=B;title=B;type=Text"}, "Composite list parameter 'Missing' was not found."},
		{[]string{"code=A;title=A;type=Text"}, nil, "SaveSchema failed validation: A -> B (C)"},
	}
	for _, item := range cases {
		result := client.CreateUserTask(context.Background(), ProcessWriteCreateTaskRequest{Code: "UsrTask", Package: "UsrPkg", Title: "Task", Culture: "en-US",
			WorkspacePath: root, Parameters: item.parameters, ParameterItems: item.items})
		last := result.Messages[len(result.Messages)-1]
		if result.ExitCode != 1 || last.MessageType != "Error" || last.Value != item.want {
			t.Errorf("%v: %+v", item.parameters, result)
		}
	}
	before := len(*calls)
	result := client.CreateUserTask(context.Background(), ProcessWriteCreateTaskRequest{Code: "UsrTask", Package: "UsrPkg", Title: "Task", Culture: "en-US",
		WorkspacePath: root, Parameters: []string{"code=A;title=A;type=Text;direction=Up"}})
	if result.ExitCode != 1 || len(*calls) != before {
		t.Fatalf("a direction refusal must come before any request: %+v", result)
	}
}

func TestModifyUserTaskParametersEditsTheWorkspaceTask(t *testing.T) {
	root := processWriteTaskWorkspace(t, `{"MetaData":{"Schema":{"FJ1":[{"A2":"Keep","L12":1},{"A2":"Drop","L12":0}]}}}`)
	client, calls := processWriteServer(t, "", func(call processWriteCall) (int, string) {
		switch {
		case strings.HasSuffix(call.Path, "/GetWorkspaceItems"):
			return 200, `{"items":[{"uId":"` + processWriteTaskSchemaUID + `","name":"usrtask","packageUId":"` + processWriteTaskPackageUID + `","packageName":"usrpkg"},{"uId":"` + processWriteTaskSchemaUID + `","name":"UsrTask","packageName":"Other"}]}`
		case strings.HasSuffix(call.Path, "/GetSchema"):
			return 200, `{"success":true,"schema":{"uId":"` + processWriteTaskSchemaUID + `","name":"UsrTask","parameters":[` +
				`{"uId":"11111111-0000-0000-0000-000000000001","name":"Keep","caption":[],"itemProperties":[],"type":1,"resulting":true,"serializable":true,"icon":"i","extra":1},` +
				`{"uId":"11111111-0000-0000-0000-000000000002","name":"Drop","caption":[],"itemProperties":[],"type":1,"resulting":true,"serializable":true,"icon":"i"}]}}`
		case strings.HasSuffix(call.Path, "/SaveSchema"):
			return 200, `{"success":true,"schemaUid":"` + processWriteTaskSchemaUID + `"}`
		case strings.HasSuffix(call.Path, "/GetIsFileDesignMode"):
			return 200, `{"success":true,"value":true}`
		case strings.HasSuffix(call.Path, "/LoadPackagesToDB"):
			return 200, `{"success":true}`
		}
		return 200, `{}`
	})
	result := client.ModifyUserTaskParameters(context.Background(), ProcessWriteModifyTaskRequest{UserTaskName: "UsrTask", Culture: "en-US", WorkspacePath: root,
		AddParameters: []string{"code=Added;title=Added;type=Integer"}, RemoveParameters: []string{"drop"}, SetDirections: []string{"Added=Out"}})
	got := processWriteMessages(result)
	want := []string{"Info: Updated user task schema 'UsrTask' (" + processWriteTaskSchemaUID + ").", "Info: Building package 'usrpkg'...",
		"Info: Applying direction metadata for 2 parameter(s) on 'UsrTask'..."}
	if result.ExitCode != 1 || strings.Join(got[:3], "\n") != strings.Join(want, "\n") ||
		got[len(got)-1] != "Error: Parameter 'Added' was not found in metadata for user task 'UsrTask'." {
		t.Fatalf("%q", got)
	}
	if body := processWriteTaskCalls(*calls, "/GetSchema")[0].Body; body != `{"schemaUId":"`+processWriteTaskSchemaUID+`","useFullHierarchy":false,"cultures":[]}` {
		t.Fatalf("get %s", body)
	}
	if body := processWriteTaskCalls(*calls, "/GetWorkspaceItems")[0].Body; body != "" {
		t.Fatalf("items body %q", body)
	}
	var saved processWriteTaskSchema
	_ = json.Unmarshal([]byte(processWriteTaskCalls(*calls, "/SaveSchema")[0].Body), &saved)
	if len(saved.Parameters) != 2 || *saved.Parameters[0].Name != "Keep" || *saved.Parameters[1].Name != "Added" || *saved.Parameters[1].Direction != 1 ||
		saved.Parameters[0].Direction != nil || strings.Contains(processWriteTaskCalls(*calls, "/SaveSchema")[0].Body, `"extra"`) {
		t.Fatalf("saved %s", processWriteTaskCalls(*calls, "/SaveSchema")[0].Body)
	}
}

func TestModifyUserTaskParametersRefusals(t *testing.T) {
	root := processWriteTaskWorkspace(t, "")
	client, calls := processWriteServer(t, "", func(call processWriteCall) (int, string) {
		if strings.HasSuffix(call.Path, "/GetWorkspaceItems") {
			return 200, `{"items":[{"uId":"` + processWriteTaskSchemaUID + `","name":"UsrTask","packageName":"UsrPkg"}]}`
		}
		if strings.HasSuffix(call.Path, "/GetSchema") {
			return 200, `{"success":true,"schema":{"name":"UsrTask","parameters":[{"name":"A","type":1}]}}`
		}
		return 200, `{}`
	})
	cases := []struct {
		request ProcessWriteModifyTaskRequest
		want    string
	}{
		{ProcessWriteModifyTaskRequest{UserTaskName: "UsrTask"}, "Specify at least one `--add-parameter`, `--add-parameter-item`, `--remove-parameter`, or `--set-direction` operation."},
		{ProcessWriteModifyTaskRequest{UserTaskName: "UsrTask", RemoveParameters: []string{" "}}, "Remove parameter name cannot be empty."},
		{ProcessWriteModifyTaskRequest{UserTaskName: "UsrTask", SetDirections: []string{"=In"}}, "Set direction value '=In' must be in '<name>=<direction>' format."},
		{ProcessWriteModifyTaskRequest{UserTaskName: "UsrTask", SetDirections: []string{"A=In", "a:Out"}}, "Parameter 'a' is listed more than once for direction updates."},
		{ProcessWriteModifyTaskRequest{UserTaskName: "UsrTask", RemoveParameters: []string{"Missing"}}, "Parameter 'Missing' does not exist on user task 'UsrTask'."},
		{ProcessWriteModifyTaskRequest{UserTaskName: "UsrTask", AddParameters: []string{"code=a;title=A;type=Text"}}, "Parameter 'a' already exists on user task 'UsrTask'."},
		{ProcessWriteModifyTaskRequest{UserTaskName: "Other", RemoveParameters: []string{"A"}}, "User task 'Other' is not part of the current workspace."},
	}
	for _, item := range cases {
		item.request.WorkspacePath, item.request.Culture = root, "en-US"
		result := client.ModifyUserTaskParameters(context.Background(), item.request)
		last := result.Messages[len(result.Messages)-1]
		if result.ExitCode != 1 || last.Value != item.want {
			t.Errorf("%+v: %+v", item.request, result)
		}
	}
	if len(processWriteTaskCalls(*calls, "/SaveSchema")) != 0 {
		t.Fatal("a refused edit was saved")
	}
}

func TestProcessWriteTaskDefinitionsFollowClio(t *testing.T) {
	parameters := []ProcessWriteTaskParameter{{Code: "L", Title: "List", Type: "CompositeValueList", Lookup: " ", Direction: " Out ", LazyLoad: processWriteBoolPointer(false),
		Items: []ProcessWriteTaskParameter{{Code: "C", Title: "Child", Type: "Text", Items: []ProcessWriteTaskParameter{{Code: "G", Title: "Grand", Type: "Text"}}}}}}
	if got := strings.Join(ProcessWriteTaskDefinitions(parameters), "|"); got != "code=L;title=List;type=CompositeValueList;direction=Out;lazyLoad=false" {
		t.Fatalf("%s", got)
	}
	if got := strings.Join(ProcessWriteTaskNestedItems(parameters), "|"); got != "parent=L;code=C;title=Child;type=Text|parent=C;code=G;title=Grand;type=Text" {
		t.Fatalf("%s", got)
	}
	items := []ProcessWriteTaskParameter{{Parent: "L", Code: "X", Title: "X", Type: "Text", Items: []ProcessWriteTaskParameter{{Code: "Y", Title: "Y", Type: "Text"}}}}
	if got := strings.Join(ProcessWriteTaskItems(items), "|"); got != "parent=L;code=X;title=X;type=Text|parent=X;code=Y;title=Y;type=Text" {
		t.Fatalf("%s", got)
	}
}
