package creatio

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// ruleWriteFake is a Creatio stand for the business-rule write path: the package list, entity design
// schemas, lookup/sys-setting queries, the page hierarchy, ExpressionService and the add-on designer.
type ruleWriteFake struct {
	t        *testing.T
	mu       sync.Mutex
	calls    []string
	bodies   map[string][]string
	schemas  map[string]string // GetSchemaDesignItem schema JSON by name
	metaData string
	extra    map[string]any
	select_  func(root string, body string) string
	formula  string
	saveBody string
	saveFail bool
	page     string // GetParentSchemas response
}

func newRuleWriteFake(t *testing.T) *ruleWriteFake {
	return &ruleWriteFake{t: t, bodies: map[string][]string{}, schemas: map[string]string{}, formula: `[]`}
}

func (f *ruleWriteFake) serve() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		name := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		f.mu.Lock()
		f.calls = append(f.calls, name)
		f.bodies[name] = append(f.bodies[name], string(body))
		f.mu.Unlock()
		switch name {
		case "Login":
			io.WriteString(w, `{"Code":0}`)
		case "SelectQuery":
			var query struct {
				Root string `json:"rootSchemaName"`
			}
			json.Unmarshal(body, &query)
			if query.Root == "SysPackage" {
				io.WriteString(w, `{"success":true,"rows":[{"Name":"Own","UId":"aaaaaaaa-0000-0000-0000-000000000001"}]}`)
				return
			}
			if f.select_ != nil {
				io.WriteString(w, f.select_(query.Root, string(body)))
				return
			}
			io.WriteString(w, `{"success":true,"rows":[]}`)
		case "GetSchemaDesignItem":
			var request struct {
				Name string `json:"name"`
			}
			json.Unmarshal(body, &request)
			schema, ok := f.schemas[request.Name]
			if !ok {
				io.WriteString(w, `{"success":true,"schema":null}`)
				return
			}
			io.WriteString(w, `{"success":true,"schema":`+schema+`}`)
		case "Validate":
			io.WriteString(w, f.formula)
		case "GetParentSchemas":
			io.WriteString(w, f.page)
		case "GetDesignPackageUId":
			io.WriteString(w, `{"success":true,"uId":"aaaaaaaa-0000-0000-0000-000000000001"}`)
		case "GetSchema":
			schema := map[string]any{"metaData": f.metaData, "resources": []any{
				map[string]any{"key": "AddonConfig.Rules.r1.Caption", "value": []any{map[string]any{"key": "en-US", "value": "Old"}}},
				map[string]any{"key": "AddonConfig.Rules.c1.Caption", "value": []any{map[string]any{"key": "en-US", "value": "Child"}}},
			}}
			for key, value := range f.extra {
				schema[key] = value
			}
			json.NewEncoder(w).Encode(map[string]any{"success": true, "schema": schema})
		case "SaveSchema":
			f.saveBody = string(body)
			if f.saveFail {
				io.WriteString(w, `{"success":false,"errorInfo":{"message":"save rejected"}}`)
				return
			}
			io.WriteString(w, `{"success":true,"value":true}`)
		case "ResetScriptCache", "BuildConfiguration":
			io.WriteString(w, `{"success":true}`)
		default:
			f.t.Errorf("unexpected %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
}

func ruleWriteColumnJSON(name string, code int, reference string) string {
	column := `{"name":"` + name + `","type":` + strconv.Itoa(code)
	if reference != "" {
		column += `,"referenceSchema":{"name":"` + reference + `"}`
	}
	return column + `}`
}

func ruleWriteEntityJSON(name, uid string, primary string, columns ...string) string {
	return `{"name":"` + name + `","uId":"` + uid + `","parentSchema":{"uId":"cccccccc-0000-0000-0000-000000000003"},` +
		`"primaryDisplayColumn":{"name":"` + primary + `"},"columns":[` + strings.Join(columns, ",") + `],"inheritedColumns":[]}`
}

func (f *ruleWriteFake) contactLike() {
	f.schemas["UsrThing"] = ruleWriteEntityJSON("UsrThing", "bbbbbbbb-0000-0000-0000-000000000002", "Name",
		ruleWriteColumnJSON("Name", 28, ""), ruleWriteColumnJSON("Age", 4, ""), ruleWriteColumnJSON("Amount", 6, ""),
		ruleWriteColumnJSON("Born", 8, ""), ruleWriteColumnJSON("Seen", 7, ""), ruleWriteColumnJSON("Active", 12, ""),
		ruleWriteColumnJSON("Account", 10, "Account"), ruleWriteColumnJSON("Country", 10, "Country"),
		ruleWriteColumnJSON("Owner", 10, "Contact"), ruleWriteColumnJSON("Notes", 43, ""))
	f.schemas["Account"] = ruleWriteEntityJSON("Account", "dddddddd-0000-0000-0000-000000000004", "Name",
		ruleWriteColumnJSON("Name", 28, ""), ruleWriteColumnJSON("Country", 10, "Country"), ruleWriteColumnJSON("Type", 10, "AccountType"),
		ruleWriteColumnJSON("Id", 0, ""), ruleWriteColumnJSON("CreatedOn", 7, ""), ruleWriteColumnJSON("Code", 1, ""))
	f.schemas["Country"] = ruleWriteEntityJSON("Country", "eeeeeeee-0000-0000-0000-000000000005", "Name",
		ruleWriteColumnJSON("Name", 28, ""), ruleWriteColumnJSON("TimeZone", 10, "TimeZone"))
	f.schemas["Contact"] = ruleWriteEntityJSON("Contact", "ffffffff-0000-0000-0000-000000000006", "Name",
		ruleWriteColumnJSON("Name", 28, ""), ruleWriteColumnJSON("Account", 10, "Account"), ruleWriteColumnJSON("Age", 4, ""))
	f.schemas["AccountType"] = ruleWriteEntityJSON("AccountType", "abababab-0000-0000-0000-000000000007", "Name", ruleWriteColumnJSON("Name", 28, ""))
}

func ruleWriteRules(t *testing.T, page bool, raw string) []*RuleWriteRule {
	t.Helper()
	var decoded any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatal(err)
	}
	rules, err := RuleWriteParseRules("create-entity-business-rules", decoded, page)
	if err != nil {
		t.Fatal(err)
	}
	return rules
}

func ruleWriteSavedMetadata(t *testing.T, body string) (*orderedObject, *orderedObject) {
	t.Helper()
	parsed, err := parseOrderedJSON([]byte(body))
	if err != nil {
		t.Fatalf("save body %v", err)
	}
	schema := parsed.(*orderedObject)
	text, _ := jsonString(schema, "metaData")
	metadata, err := parseOrderedJSON([]byte(text))
	if err != nil {
		t.Fatalf("metaData %v: %s", err, text)
	}
	return schema, metadata.(*orderedObject)
}

func ruleWriteKeys(object *orderedObject) string { return strings.Join(object.keys, ",") }

func TestRuleWriteCreateEntityBatch(t *testing.T) {
	fake := newRuleWriteFake(t)
	fake.contactLike()
	fake.metaData = `{"typeName":"Terrasoft.Core.BusinessRules.BusinessRules","rules":[{"typeName":"Terrasoft.Core.BusinessRules.BusinessRule","uId":"r1","name":"Existing","decimal":10.50}]}`
	fake.extra = map[string]any{"name": "BusinessRule", "zz": 1}
	fake.select_ = func(root, body string) string {
		if root == "Account" && strings.Contains(body, "12121212-0000-0000-0000-000000000000") {
			return `{"success":true,"rows":[{"Id":"12121212-0000-0000-0000-000000000000"}]}`
		}
		return `{"success":true,"rows":[]}`
	}
	server := fake.serve()
	defer server.Close()
	rules := ruleWriteRules(t, false, `[
	 {"caption":"  First's rule ","condition":{"logicalOperation":"or","conditions":[
	   {"leftExpression":{"type":"AttributeValue","path":"Account"},"comparisonType":"equal","rightExpression":{"type":"Const","value":"12121212-0000-0000-0000-000000000000"}},
	   {"leftExpression":{"type":"AttributeValue","path":"Seen"},"comparisonType":"greater-than","rightExpression":{"type":"Const","value":"2024-01-01T10:00:00.5+02:00"}},
	   {"leftExpression":{"type":"SysValue","sysValueName":"currentusercontact"},"comparisonType":"equal","rightExpression":{"type":"AttributeValue","path":"Owner"}}]},
	  "actions":[{"type":"make-required","items":["Name","Age"]},{"type":"set-values","items":[
	   {"expression":{"type":"AttributeValue","path":"Born"},"value":{"type":"Const","value":"1990-05-17"}},
	   {"expression":{"type":"AttributeValue","path":"Age"},"value":{"type":"Formula","expression":"age * 2"}},
	   {"expression":{"type":"AttributeValue","path":"Name"},"value":{"type":"AttributeValue","path":"Account.Name"}}]}]},
	 {"caption":"Broken","condition":{"logicalOperation":"AND","conditions":[{"leftExpression":{"type":"AttributeValue","path":"Missing"},"comparisonType":"is-filled-in"}]},"actions":[{"type":"make-required","items":["Name"]}]},
	 {"caption":"Dependent","name":"UsrDependent","enabled":false,"condition":{"logicalOperation":"AND","conditions":[]},
	  "actions":[{"type":"apply-filter","target":"Account","targetFilterPath":" Country ","source":"Country","clearValue":true,"populateValue":true}]}]`)
	result := newFormsTestClient(t, server.URL).CreateBusinessRules(context.Background(), false, RuleWriteBatchRequest{PackageName: "own", SchemaName: " UsrThing ", Rules: rules})
	if result.Succeeded != 2 || result.Failed != 1 || result.Results[0].Name != "  First's rule " || result.Results[2].RuleName == nil || *result.Results[2].RuleName != "UsrDependent" {
		t.Fatalf("%#v", result)
	}
	if !regexp.MustCompile(`^BusinessRule_[0-9a-f]{7}$`).MatchString(*result.Results[0].RuleName) {
		t.Fatalf("generated name %s", *result.Results[0].RuleName)
	}
	if *result.Results[1].Error != "Unknown attribute 'Missing' in rule.condition.conditions[*].leftExpression.path." {
		t.Fatalf("failure %s", *result.Results[1].Error)
	}
	// The add-on request: the design schema and its parent, the requested package, written indented.
	request := fake.bodies["GetSchema"][0]
	if !strings.HasPrefix(request, "{\n  \"addonName\": \"BusinessRule\",\n  \"targetSchemaUId\": \"bbbbbbbb-0000-0000-0000-000000000002\",\n  \"targetParentSchemaUId\": \"cccccccc-0000-0000-0000-000000000003\",\n  \"targetPackageUId\": \"aaaaaaaa-0000-0000-0000-000000000001\",\n  \"targetSchemaManagerName\": \"EntitySchemaManager\",\n  \"useFullHierarchy\": true\n}") {
		t.Fatalf("GetSchema request %s", request)
	}
	// Formula validation went to ExpressionService with clio's metadata shape.
	if validate := fake.bodies["Validate"]; len(validate) != 1 || !strings.Contains(validate[0], `\u0022expression\u0022: \u0022#UsrThingRecord.Age# * 2\u0022`) {
		t.Fatalf("formula validation %v", validate)
	}
	schema, metadata := ruleWriteSavedMetadata(t, fake.saveBody)
	if ruleWriteKeys(schema) != "metaData,resources,name,zz" {
		t.Fatalf("schema keys %s", ruleWriteKeys(schema))
	}
	text, _ := jsonString(schema, "metaData")
	if !strings.Contains(text, "\n      \"name\": \"Existing\",\n      \"decimal\": 10.50\n") || !strings.Contains(text, `First\u0027s rule`) {
		t.Fatalf("metaData text %s", text)
	}
	saved, _ := jsonArrayAt(metadata, "rules")
	if len(saved) != 5 {
		t.Fatalf("rules %d", len(saved))
	}
	first := saved[1].(*orderedObject)
	if ruleWriteKeys(first) != "typeName,uId,cases,triggers,name,enabled,caption" || jsonStringOrNil(first, "caption") == nil || *jsonStringOrNil(first, "caption") != "First's rule" {
		t.Fatalf("rule %s", ruleWriteKeys(first))
	}
	cases, _ := jsonArrayAt(first, "cases")
	caseObject := cases[0].(*orderedObject)
	group := jsonObjectAt(caseObject, "condition")
	if ruleWriteKeys(group) != "logicalOperation,conditions,typeName,uId" || jsonInt(group, "logicalOperation", 0) != 2 {
		t.Fatalf("group %s", ruleWriteKeys(group))
	}
	conditions, _ := jsonArrayAt(group, "conditions")
	lookup := conditions[0].(*orderedObject)
	if ruleWriteKeys(lookup) != "leftExpression,rightExpression,comparisonType,typeName,uId" {
		t.Fatalf("condition %s", ruleWriteKeys(lookup))
	}
	constant := jsonObjectAt(lookup, "rightExpression")
	if ruleWriteKeys(constant) != "typeName,uId,type,dataValueTypeName,referenceSchemaName,value" || constant.get("referenceSchemaName") != "Account" {
		t.Fatalf("const %s", ruleWriteKeys(constant))
	}
	if value := jsonObjectAt(conditions[1].(*orderedObject), "rightExpression").get("value"); value != "2024-01-01T08:00:00.5Z" {
		t.Fatalf("datetime %v", value)
	}
	sysValue := jsonObjectAt(conditions[2].(*orderedObject), "leftExpression")
	if sysValue.get("sysValueName") != "CurrentUserContact" || sysValue.get("referenceSchemaName") != "Contact" {
		t.Fatalf("sys value %v", sysValue.values)
	}
	actions, _ := jsonArrayAt(caseObject, "actions")
	required := actions[0].(*orderedObject)
	if ruleWriteKeys(required) != "items,typeName,uId,enabled" || required.get("items") != "Name,Age" {
		t.Fatalf("field action %s %v", ruleWriteKeys(required), required.get("items"))
	}
	items, _ := jsonArrayAt(actions[1].(*orderedObject), "items")
	if ruleWriteKeys(items[0].(*orderedObject)) != "expression,value,typeName,uId,enabled" ||
		jsonObjectAt(items[0].(*orderedObject), "value").get("value") != "1990-05-17T00:00:00Z" {
		t.Fatalf("set item %v", items[0])
	}
	formula := jsonObjectAt(items[1].(*orderedObject), "value")
	if ruleWriteKeys(formula) != "typeName,uId,type,parameterMappings,expressionSchema" || jsonObjectAt(formula, "expressionSchema").get("expression") != "#UsrThingRecord.Age# * 2" {
		t.Fatalf("formula %s", ruleWriteKeys(formula))
	}
	forward := jsonObjectAt(items[2].(*orderedObject), "value")
	if forward.get("path") != "Account.Name" {
		t.Fatalf("forward %v", forward.values)
	}
	triggers, _ := jsonArrayAt(first, "triggers")
	names := []string{}
	for _, trigger := range triggers {
		names = append(names, trigger.(*orderedObject).get("name").(string))
	}
	if strings.Join(names, "|") != "Account|Seen|Owner|Age||" && strings.Join(names, "|") != "Account|Seen|Owner|Age|" {
		t.Fatalf("triggers %v", names)
	}
	parent := saved[2].(*orderedObject)
	if parent.get("enabled") != false || parent.get("name") != "UsrDependent" {
		t.Fatalf("dependent %v", parent.values)
	}
	parentUID := parent.get("uId").(string)
	child := saved[3].(*orderedObject)
	if child.get("name") != "Autogenerated_"+parentUID+"_ClearValue" || child.get("parentUId") != parentUID || ruleWriteKeys(child) != "typeName,uId,cases,triggers,name,enabled,caption,parentUId,parentActionUId" {
		t.Fatalf("child %v", child.values)
	}
	resources, _ := jsonArrayAt(schema, "resources")
	keys := []string{}
	for _, resource := range resources {
		keys = append(keys, resource.(*orderedObject).get("key").(string))
	}
	if keys[0] != "r1.Caption" || keys[1] != "c1.Caption" || len(keys) != 6 {
		t.Fatalf("resources %v", keys)
	}
	order := strings.Join(fake.calls, ",")
	if !strings.HasSuffix(order, "GetSchema,SaveSchema,ResetScriptCache,BuildConfiguration") {
		t.Fatalf("calls %s", order)
	}
}

func TestRuleWriteCreateFailuresShareTheSave(t *testing.T) {
	fake := newRuleWriteFake(t)
	fake.contactLike()
	fake.metaData = `{"rules":[{"name":"Taken","uId":"r1"}]}`
	server := fake.serve()
	defer server.Close()
	client := newFormsTestClient(t, server.URL)
	rules := ruleWriteRules(t, false, `[{"caption":"A","name":"taken","condition":{"logicalOperation":"AND","conditions":[{"leftExpression":{"type":"AttributeValue","path":"Name"},"comparisonType":"is-filled-in"}]},"actions":[{"type":"make-required","items":["Age"]}]},
	 {"caption":"B","condition":{"logicalOperation":"AND","conditions":[{"leftExpression":{"type":"AttributeValue","path":"Name"},"comparisonType":"is-filled-in"}]},"actions":[{"type":"make-required","items":["Age"]}]}]`)
	result := client.CreateBusinessRules(context.Background(), false, RuleWriteBatchRequest{PackageName: "Own", SchemaName: "UsrThing", Rules: rules})
	want := "Business rule name 'taken' already exists on the target schema. Rule names must be unique; use the update tool to change an existing rule."
	if result.Failed != 2 || *result.Results[0].Error != want || *result.Results[1].Error != want || fake.saveBody != "" {
		t.Fatalf("%#v", result)
	}
	fake.metaData = ""
	fake.saveFail = true
	result = client.CreateBusinessRules(context.Background(), false, RuleWriteBatchRequest{PackageName: "Own", SchemaName: "UsrThing", Rules: rules[1:]})
	if result.Failed != 1 || *result.Results[0].Error != "save rejected" {
		t.Fatalf("%#v", result)
	}
	// A failure before the batch fails every rule on create, and is request-level on update.
	result = client.CreateBusinessRules(context.Background(), false, RuleWriteBatchRequest{PackageName: "Nope", SchemaName: "UsrThing", Rules: append(rules, nil)})
	if result.Failed != 3 || result.Results[2].Name != "" || *result.Results[2].Error != "Package 'Nope' was not found." {
		t.Fatalf("%#v", result)
	}
	update := client.UpdateBusinessRules(context.Background(), false, RuleWriteBatchRequest{PackageName: "Own", SchemaName: "Missing", Rules: rules})
	if update.Error != "Entity schema 'Missing' was not returned." || len(update.Results) != 0 {
		t.Fatalf("%#v", update)
	}
}

func TestRuleWriteUpdatePreservesIdentity(t *testing.T) {
	fake := newRuleWriteFake(t)
	fake.contactLike()
	fake.metaData = `{"typeName":"Terrasoft.Core.BusinessRules.BusinessRules","rules":[
	 {"typeName":"Terrasoft.Core.BusinessRules.BusinessRule","uId":"r1","cases":[{"uId":"case-1","condition":{"typeName":"Terrasoft.Core.BusinessRules.Models.Conditions.BusinessRuleGroupCondition","uId":"group-1"}}],
	  "triggers":[{"uId":"t-name","name":"name","type":0},{"uId":"t-load","name":"","type":2}],"name":"Target","enabled":false},
	 {"uId":"c1","name":"Autogenerated_r1_ClearValue","parentUId":"R1"},
	 {"uId":"r2","name":"Other"}]}`
	server := fake.serve()
	defer server.Close()
	rules := ruleWriteRules(t, false, `[
	 {"name":" target ","caption":"Renamed","condition":{"logicalOperation":"AND","conditions":[{"uId":"{AAAAAAAA-0000-0000-0000-00000000000A}","leftExpression":{"type":"AttributeValue","path":"Name"},"comparisonType":"is-filled-in"}]},"actions":[{"type":"make-optional","items":["Age"],"uId":"bbbbbbbb00000000000000000000000b"}]},
	 {"caption":"no name"},
	 {"name":"Target","caption":"again"},
	 {"name":"Missing","caption":"x"},
	 {"name":"Other","caption":"bad uid","condition":{"logicalOperation":"AND","conditions":[{"uId":"nope","leftExpression":{"type":"AttributeValue","path":"Name"},"comparisonType":"is-filled-in"}]},"actions":[{"type":"make-optional","items":["Age"]}]}]`)
	result := newFormsTestClient(t, server.URL).UpdateBusinessRules(context.Background(), false, RuleWriteBatchRequest{PackageName: "Own", SchemaName: "UsrThing", Rules: rules})
	errors := []string{}
	for _, item := range result.Results[1:] {
		errors = append(errors, *item.Error)
	}
	if result.Succeeded != 1 || result.Results[0].Name != "target" || *result.Results[0].RuleName != "target" || strings.Join(errors, "|") !=
		"name is required to update a business rule.|Business rule 'Target' appears more than once in the update batch.|Business rule 'Missing' was not found.|Block uId 'nope' is not a valid GUID." {
		t.Fatalf("%#v %v", result, errors)
	}
	schema, metadata := ruleWriteSavedMetadata(t, fake.saveBody)
	saved, _ := jsonArrayAt(metadata, "rules")
	if len(saved) != 2 {
		t.Fatalf("child rule not removed: %d", len(saved))
	}
	rule := saved[0].(*orderedObject)
	cases, _ := jsonArrayAt(rule, "cases")
	caseObject := cases[0].(*orderedObject)
	group := jsonObjectAt(caseObject, "condition")
	conditions, _ := jsonArrayAt(group, "conditions")
	actions, _ := jsonArrayAt(caseObject, "actions")
	triggers, _ := jsonArrayAt(rule, "triggers")
	if rule.get("uId") != "r1" || rule.get("enabled") != false || rule.get("name") != "target" || caseObject.get("uId") != "case-1" || group.get("uId") != "group-1" ||
		conditions[0].(*orderedObject).get("uId") != "aaaaaaaa-0000-0000-0000-00000000000a" || actions[0].(*orderedObject).get("uId") != "bbbbbbbb-0000-0000-0000-00000000000b" ||
		triggers[0].(*orderedObject).get("uId") != "t-name" || triggers[1].(*orderedObject).get("uId") != "t-load" {
		t.Fatalf("identity lost: %s", ruleWriteSTJ(rule, true))
	}
	resources, _ := jsonArrayAt(schema, "resources")
	if len(resources) != 1 || ruleWriteSTJ(resources[0], false) != `{"key":"r1.Caption","value":[{"key":"en-US","value":"Renamed"}]}` {
		t.Fatalf("resources %s", ruleWriteSTJ(resources, false))
	}
}

const ruleWritePageBody = `define("UsrPage", /**SCHEMA_DEPS*/[]/**SCHEMA_DEPS*/, function/**SCHEMA_ARGS*/()/**SCHEMA_ARGS*/ {
	return {
		viewConfigDiff: /**SCHEMA_VIEW_CONFIG_DIFF*/[{"operation":"insert","name":"NameField","values":{"type":"crt.Input"},"parentName":"Main","propertyName":"items","index":0},{"operation":"insert","name":"Main","values":{"type":"crt.FlexContainer","items":[]}}]/**SCHEMA_VIEW_CONFIG_DIFF*/,
		viewModelConfig: /**SCHEMA_VIEW_MODEL_CONFIG*/{"attributes":{"Name":{"modelConfig":{"path":"PDS.Name"}},"Parameter":{"modelConfig":{"path":"PageParameters.Flag"}},"Items":{"isCollection":true,"modelConfig":{"path":"PDS.Name"}}}}/**SCHEMA_VIEW_MODEL_CONFIG*/,
		modelConfig: /**SCHEMA_MODEL_CONFIG*/{"dataSources":{"PDS":{"type":"crt.EntityDataSource","config":{"entitySchemaName":"Contact"}}}}/**SCHEMA_MODEL_CONFIG*/,
		handlers: /**SCHEMA_HANDLERS*/[]/**SCHEMA_HANDLERS*/,
		converters: /**SCHEMA_CONVERTERS*/{}/**SCHEMA_CONVERTERS*/,
		validators: /**SCHEMA_VALIDATORS*/{}/**SCHEMA_VALIDATORS*/
	};
});`

func TestRuleWritePageCreate(t *testing.T) {
	fake := newRuleWriteFake(t)
	fake.contactLike()
	body, _ := json.Marshal(ruleWritePageBody)
	fake.page = `{"success":true,"values":[{"uId":"99999999-0000-0000-0000-000000000009","name":"UsrPage","body":` + string(body) +
		`,"parameters":[{"name":"Flag","type":12,"uId":"p1"}]}]}`
	fake.select_ = func(root, body string) string {
		if root == "SysSchema" {
			return `{"success":true,"rows":[{"UId":"99999999-0000-0000-0000-000000000009"}]}`
		}
		return `{"success":true,"rows":[]}`
	}
	server := fake.serve()
	defer server.Close()
	client := newFormsTestClient(t, server.URL)
	rules := ruleWriteRules(t, true, `[
	 {"caption":"Page rule","condition":{"logicalOperation":"AND","conditions":[
	   {"leftExpression":{"type":"AttributeValue","path":"PDS.Account"},"comparisonType":"is-filled-in"},
	   {"leftExpression":{"type":"AttributeValue","path":"PageParameters.Flag"},"comparisonType":"equal","rightExpression":{"type":"Const","value":true}},
	   {"leftExpression":{"type":"AttributeValue","path":"Name"},"comparisonType":"is-filled-in"}]},
	  "actions":[{"type":"hide-element","items":["NameField"]}]},
	 {"caption":"Bad","condition":{"logicalOperation":"AND","conditions":[{"leftExpression":{"type":"AttributeValue","path":"Items"},"comparisonType":"is-filled-in"}]},"actions":[{"type":"show-element","items":["Ghost"]}]}]`)
	result := client.CreateBusinessRules(context.Background(), true, RuleWriteBatchRequest{PackageName: "Own", SchemaName: "UsrPage", Rules: rules})
	if result.Succeeded != 1 || *result.Results[1].Error != "Unknown or unsupported datasource-bound page attribute 'Items' in rule.condition.conditions[*].leftExpression.path. Available condition attributes: Name, PageParameters.Flag, Parameter, PDS.Account, PDS.Age, PDS.Name." {
		t.Fatalf("%#v %s", result, *result.Results[1].Error)
	}
	var request map[string]any
	json.Unmarshal([]byte(fake.bodies["GetSchema"][0]), &request)
	if request["targetParentSchemaUId"] != "99999999-0000-0000-0000-000000000009" || request["targetSchemaManagerName"] != "ClientUnitSchemaManager" || request["targetSchemaUId"] == request["targetParentSchemaUId"] {
		t.Fatalf("add-on request %v", request)
	}
	_, metadata := ruleWriteSavedMetadata(t, fake.saveBody)
	rule := ruleWriteSTJ(metadata, false)
	for _, fragment := range []string{
		`"type":"AttributeValue","dataValueTypeName":"Lookup","path":"Account","scopeId":"PDS"`,
		`"type":"AttributeValue","dataValueTypeName":"Boolean","path":"Flag","scopeId":"PageParameters"`,
		`"type":"AttributeValue","dataValueTypeName":"MediumText","path":"Name"}`,
		`{"typeName":"Terrasoft.Core.BusinessRules.Models.Trigger","uId":`,
		`"name":"PDS","type":2,"scopeId":""}`, `"name":"PageParameters","type":2,"scopeId":""}`,
		`"items":"NameField","typeName":"Terrasoft.Core.BusinessRules.Models.Actions.BusinessRuleActionHideElement"`,
	} {
		if !strings.Contains(rule, fragment) {
			t.Fatalf("missing %s in %s", fragment, rule)
		}
	}
	// The page lookup attribute carries no referenceSchemaName (includeAttributeReferenceSchemaName=false).
	if strings.Contains(rule, `"referenceSchemaName":"Account"`) {
		t.Fatalf("page reference schema written %s", rule)
	}
}

func TestRuleWriteStaticFilterEnvelope(t *testing.T) {
	fake := newRuleWriteFake(t)
	fake.contactLike()
	fake.select_ = func(root, body string) string {
		if root == "AccountType" && strings.Contains(body, `"value":"Customer"`) {
			return `{"success":true,"rows":[{"Id":"34343434-0000-0000-0000-000000000000"}]}`
		}
		if root == "AccountType" && strings.Contains(body, `"Display"`) {
			return `{"success":true,"rows":[{"Display":"Partner"}]}`
		}
		return `{"success":true,"rows":[]}`
	}
	server := fake.serve()
	defer server.Close()
	client := newFormsTestClient(t, server.URL)
	cache := &ruleWriteSchemaCache{ctx: context.Background(), client: client, packageUID: "aaaaaaaa-0000-0000-0000-000000000001", schemas: map[string]*ruleWriteEntitySchema{}}
	scope := &ruleWriteScope{ctx: context.Background(), client: client, cache: cache, lookupIDs: map[[2]string]string{}, lookupNames: map[[2]string]*string{},
		now: func() time.Time { return time.Date(2026, 6, 10, 0, 0, 0, 0, time.FixedZone("x", 3*3600)) }}
	var filter any
	json.Unmarshal([]byte(`{"logicalOperation":"AND","filters":[
	  {"columnPath":"Type","comparisonType":"equal","value":["Customer","56565656-0000-0000-0000-00000000000A"]},
	  {"columnPath":"Code","comparisonType":"IS_NULL"},
	  {"columnPath":"CreatedOn","comparisonType":"GREATER","valueMacros":"previousndays","valueMacrosArgument":3},
	  {"columnPath":"CreatedOn","comparisonType":"EQUAL","datePart":"Time","value":"11:06"},
	  {"columnPath":"Id","comparisonType":"NOT_EQUAL","value":"x"}],
	 "groups":[{"logicalOperation":"OR","filters":[{"columnPath":"Name","comparisonType":"START_WITH","value":"<A&B>"}]}],
	 "backwardReferenceFilters":[{"referenceColumnPath":"[Contact:Account]"},{"referenceColumnPath":"[Contact:Account]","aggregationType":"SUM","aggregationColumnPath":"Age","comparisonType":"LESS","aggregationValue":2.5}]}`), &filter)
	var envelope string
	if message, failed := ruleWriteCatch(func() {
		group := ruleWriteDeserializeFilter(filter)
		ruleWriteValidateFilterStructure(group, "filter")
		scope.validateFilterSchema(group, "Account", "filter")
		envelope = scope.buildFilterEnvelope(group, "Account")
	}); failed {
		t.Fatal(message)
	}
	want := `{"rootSchemaName":"Account","filterType":6,"logicalOperation":0,"isEnabled":true,"items":{` +
		`"Filter_0":{"filterType":4,"comparisonType":3,"isEnabled":true,"trimDateTimeParameterToDate":false,"leftExpression":{"expressionType":0,"columnPath":"Type","className":"Terrasoft.ColumnExpression"},"isAggregative":false,"dataValueType":10,"referenceSchemaName":"AccountType","rightExpressions":[` +
		`{"expressionType":2,"parameter":{"dataValueType":10,"value":{"Name":"Customer","Id":"34343434-0000-0000-0000-000000000000","value":"34343434-0000-0000-0000-000000000000","displayValue":"Customer"},"className":"Terrasoft.Parameter"},"className":"Terrasoft.ParameterExpression"},` +
		`{"expressionType":2,"parameter":{"dataValueType":10,"value":{"Name":"Partner","Id":"56565656-0000-0000-0000-00000000000a","value":"56565656-0000-0000-0000-00000000000a","displayValue":"Partner"},"className":"Terrasoft.Parameter"},"className":"Terrasoft.ParameterExpression"}],"key":"","className":"Terrasoft.InFilter"},` +
		`"Filter_1":{"filterType":2,"comparisonType":1,"isNull":true,"isEnabled":true,"leftExpression":{"expressionType":0,"columnPath":"Code","className":"Terrasoft.ColumnExpression"},"key":"","className":"Terrasoft.IsNullFilter"},` +
		`"Filter_2":{"filterType":1,"comparisonType":7,"isEnabled":true,"trimDateTimeParameterToDate":true,"leftExpression":{"expressionType":0,"columnPath":"CreatedOn","className":"Terrasoft.ColumnExpression"},"isAggregative":false,"dataValueType":7,"rightExpression":{"expressionType":1,"functionType":1,"macrosType":25,"functionArgument":{"expressionType":2,"parameter":{"dataValueType":4,"value":3,"className":"Terrasoft.Parameter"},"className":"Terrasoft.ParameterExpression"},"className":"Terrasoft.FunctionExpression"},"key":"","className":"Terrasoft.CompareFilter"},` +
		`"Filter_3":{"filterType":1,"comparisonType":3,"isEnabled":true,"trimDateTimeParameterToDate":true,"leftExpression":{"expressionType":1,"functionType":3,"datePartType":7,"functionArgument":{"expressionType":0,"columnPath":"CreatedOn","className":"Terrasoft.ColumnExpression"},"className":"Terrasoft.FunctionExpression"},"isAggregative":false,"dataValueType":7,"rightExpression":{"expressionType":2,"parameter":{"dataValueType":9,"dateValue":"2026-06-10T08:06:00.000Z","value":"\u00222026-06-10T11:06:00.000\u0022","className":"Terrasoft.Parameter"},"className":"Terrasoft.ParameterExpression"},"key":"","className":"Terrasoft.CompareFilter"},` +
		`"Filter_4":{"filterType":1,"comparisonType":4,"isEnabled":true,"leftExpression":{"expressionType":0,"columnPath":"Id","className":"Terrasoft.ColumnExpression"},"rightExpression":{"expressionType":2,"parameter":{"dataValueType":0,"value":"x","className":"Terrasoft.Parameter"},"className":"Terrasoft.ParameterExpression"},"key":"","className":"Terrasoft.CompareFilter"},` +
		`"Group_0":{"filterType":6,"logicalOperation":1,"isEnabled":true,"items":{"Filter_100":{"filterType":1,"comparisonType":9,"isEnabled":true,"leftExpression":{"expressionType":0,"columnPath":"Name","className":"Terrasoft.ColumnExpression"},"rightExpression":{"expressionType":2,"parameter":{"dataValueType":28,"value":"\u003CA\u0026B\u003E","className":"Terrasoft.Parameter"},"className":"Terrasoft.ParameterExpression"},"key":"","className":"Terrasoft.CompareFilter"}},"key":"","className":"Terrasoft.FilterGroup"},` +
		`"BackwardReferenceFilter_0":{"filterType":5,"comparisonType":15,"isEnabled":true,"trimDateTimeParameterToDate":false,"leftExpression":{"expressionType":0,"columnPath":"[Contact:Account].Id","className":"Terrasoft.ColumnExpression"},"isAggregative":true,"dataValueType":4,"subFilters":{"rootSchemaName":"Contact","filterType":6,"logicalOperation":0,"isEnabled":true,"items":{},"key":"","className":"Terrasoft.FilterGroup"},"key":"","className":"Terrasoft.ExistsFilter"},` +
		`"BackwardReferenceFilter_1":{"filterType":1,"comparisonType":5,"isEnabled":true,"isAggregative":true,"leftExpression":{"expressionType":3,"functionType":2,"aggregationType":2,"columnPath":"[Contact:Account].Age","subFilters":{"rootSchemaName":"Contact","filterType":6,"logicalOperation":0,"isEnabled":true,"items":{},"key":"","className":"Terrasoft.FilterGroup"},"className":"Terrasoft.AggregationQueryExpression"},"rightExpression":{"expressionType":2,"parameter":{"dataValueType":5,"value":2.5,"className":"Terrasoft.Parameter"},"className":"Terrasoft.ParameterExpression"},"subFilters":{"rootSchemaName":"Contact","filterType":6,"logicalOperation":0,"isEnabled":true,"items":{},"key":"","className":"Terrasoft.FilterGroup"},"key":"","className":"Terrasoft.CompareFilter"}` +
		`},"key":"","className":"Terrasoft.FilterGroup"}`
	if envelope != want {
		t.Fatalf("envelope\n got %s\nwant %s", envelope, want)
	}
	// The read side turns the envelope back into the friendly filter it came from.
	friendly, err := decompileStaticFilter(envelope)
	if err != nil || !strings.Contains(ruleWriteSTJ(friendly, false), `"valueMacros":"PreviousNDays","valueMacrosArgument":3`) {
		t.Fatalf("decompile %v %s", err, ruleWriteSTJ(friendly, false))
	}
}

func TestRuleWriteFilterValidationMessages(t *testing.T) {
	cases := map[string]string{
		`"abc"`:                      "filter: must be a JSON object.",
		`{"filters":[]}`:             "filter.logicalOperation: required string property is missing or not a string.",
		`{"logicalOperation":"XOR"}`: "filter.logicalOperation: must be 'AND' or 'OR' (got 'XOR').",
		`{"logicalOperation":"AND","filters":[{"columnPath":"A","comparisonType":"LIKE"}]}`:                                                            "filter.filters[0].comparisonType: unsupported value 'LIKE'. Supported: EQUAL, NOT_EQUAL, IS_NULL, IS_NOT_NULL, GREATER, GREATER_OR_EQUAL, LESS, LESS_OR_EQUAL, CONTAIN, NOT_CONTAIN, START_WITH, NOT_START_WITH, END_WITH, NOT_END_WITH.",
		`{"logicalOperation":"AND","filters":[{"columnPath":"A","comparisonType":"EQUAL","valueMacros":"Someday"}]}`:                                   "filter.filters[0].valueMacros: unknown macros 'Someday'. Supported: CurrentHalfYear, CurrentHour, CurrentMonth, CurrentQuarter, CurrentUser, CurrentUserContact, CurrentWeek, CurrentYear, DayOfYearToday, DayOfYearTodayPlusDaysOffset, NextHalfYear, NextHour, NextMonth, NextNDays, NextNDaysOfYear, NextNHours, NextQuarter, NextWeek, NextYear, PreviousHalfYear, PreviousHour, PreviousMonth, PreviousNDays, PreviousNDaysOfYear, PreviousNHours, PreviousQuarter, PreviousWeek, PreviousYear, PrimaryColorColumn, PrimaryColumn, PrimaryDisplayColumn, PrimaryImageColumn, Today, Tomorrow, Yesterday.",
		`{"logicalOperation":"AND","filters":[{"columnPath":"A","comparisonType":"EQUAL","datePart":"Era","value":1}]}`:                                "filter.filters[0].datePart: unknown date part 'Era'. Supported: Day, Hour, HourMinute, Month, Time, Week, Weekday, Year.",
		`{"logicalOperation":"AND","backwardReferenceFilters":[{"referenceColumnPath":"Contact.Account"}]}`:                                            "filter.backwardReferenceFilters[0].referenceColumnPath: must use shape '[Schema:Column]' (got 'Contact.Account').",
		`{"logicalOperation":"AND","backwardReferenceFilters":[{"referenceColumnPath":"[A:B]","aggregationType":"COUNT","comparisonType":"GREATER"}]}`: "filter.backwardReferenceFilters[0].aggregationValue: required number when aggregationType is set (e.g. COUNT GREATER 10).",
		`{"logicalOperation":"AND","filters":[{"columnPath":"A","comparisonType":"GREATER","value":["x"]}]}`:                                           "filter.filters[0].value: array values are only supported when comparisonType is EQUAL or NOT_EQUAL (multi-value IN on Lookup).",
	}
	for raw, want := range cases {
		var value any
		json.Unmarshal([]byte(raw), &value)
		message, failed := ruleWriteCatch(func() { ruleWriteValidateFilterStructure(ruleWriteDeserializeFilter(value), "filter") })
		if !failed || message != want {
			t.Errorf("%s\n got %s\nwant %s", raw, message, want)
		}
	}
}

func TestRuleWriteBinding(t *testing.T) {
	shape := "invalid-parameter-type: argument 'rules' for MCP tool 't' contains a value that does not match the documented shape. Received an incompatible JSON value."
	cases := []struct {
		raw  string
		page bool
		want string
	}{
		{`"x"`, false, "invalid-parameter-type: argument 'rules' for MCP tool 't' must be an array. Received an incompatible JSON value."},
		{`[5]`, false, shape},
		{`[{"caption":5}]`, false, shape},
		{`[{"enabled":"yes"}]`, false, shape},
		{`[{"actions":[{"type":"bogus"}]}]`, false, shape},
		{`[{"actions":[{"type":"hide-element","items":["a"]}]}]`, false, shape},
		{`[{"actions":[{"type":"set-values","items":[]}]}]`, true, shape},
		{`[{"actions":[{"Type":"make-required"}]}]`, false, "invalid-parameter-type: argument 'args' for MCP tool 't' must be an object. Received an incompatible JSON value."},
		{`[{"actions":[{"type":"apply-filter","clearValue":null}]}]`, false, shape},
		{`[{"actions":[{"type":"make-required","items":[1]}]}]`, false, shape},
		{`[{"Caption":"x","zzz":1,"condition":{"Conditions":[null]},"actions":[null,{"items":["a"],"type":"make-required"}]}]`, false, ""},
	}
	for _, item := range cases {
		var value any
		json.Unmarshal([]byte(item.raw), &value)
		rules, err := RuleWriteParseRules("t", value, item.page)
		got := ""
		if err != nil {
			got = err.Error()
		}
		if got != item.want {
			t.Errorf("%s: got %q want %q", item.raw, got, item.want)
		}
		if item.want == "" && (ruleWriteText(rules[0].Caption) != "x" || len(rules[0].Condition.Conditions) != 1 || rules[0].Actions[0] != nil || rules[0].Actions[1].Type != "make-required") {
			t.Errorf("bound %#v", rules[0])
		}
	}
}

func TestRuleWriteSTJWriter(t *testing.T) {
	object := ruleWriteObject("a", "é<>&'+\"\\`", "b", []any{}, "c", newOrdered(), "d", []any{json.Number("10.50"), true, nil}, "e", (*string)(nil))
	if got := ruleWriteSTJ(object, true); got != "{\n  \"a\": \"\\u00E9\\u003C\\u003E\\u0026\\u0027\\u002B\\u0022\\\\\\u0060\",\n  \"b\": [],\n  \"c\": {},\n  \"d\": [\n    10.50,\n    true,\n    null\n  ]\n}" {
		t.Fatalf("%s", got)
	}
	for raw, want := range map[string]string{"2024-02-29": "2024-02-29T00:00:00Z", "2024-2-29": ""} {
		got, _ := ruleWriteDateTimeConstant(raw, "Date")
		if got != want {
			t.Errorf("date %s = %s", raw, got)
		}
	}
	if got, ok := ruleWriteDateTimeConstant("10:30:15+02:00", "Time"); !ok || got != "0001-01-01T08:30:15Z" {
		t.Errorf("time %s", got)
	}
	if _, ok := ruleWriteDateTimeConstant("2024-01-01T10:00:00", "DateTime"); ok {
		t.Errorf("a date-time without a zone must be refused")
	}
}
