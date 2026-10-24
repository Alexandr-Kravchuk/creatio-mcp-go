package creatio

import "strings"

// clio 8.1.0.142's ToolMiniContract (ENG-103025): the answer to a write tool called without a required
// argument names what the tool needs and one minimal valid call. A refusal keeps its leading sentence and
// exit code; the mini-contract follows it.

const miniContractEnvironmentLine = "environment-name (registered environment)"

const (
	miniContractCreateAppExample = `{"environment-name":"<env>","name":"Todo list","code":"TodoList"}`

	miniContractCreateBusinessProcessExample = `{"environment-name":"<env>","descriptor":{"name":"UsrDemoProcess","caption":"Demo process",` +
		`"packageName":"<package>","elements":[{"name":"Start","type":"startEvent","caption":"Start"},` +
		`{"name":"End","type":"endEvent","caption":"End"}],"flows":[{"source":"Start","target":"End"}]}}`

	miniContractModifyBusinessProcessExample = `{"environment-name":"<env>","process-name":"UsrDemoProcess",` +
		`"operations":[{"op":"addParameter","parameter":{"name":"UsrAmount","type":"Integer"}}]}`

	miniContractCreateEntitySchemaExample = `{"environment-name":"<env>","package-name":"<package>","schema-name":"UsrOrder",` +
		`"title-localizations":{"en-US":"Order"},"columns":[{"name":"UsrAmount","type":"Decimal2",` +
		`"title-localizations":{"en-US":"Amount"}}]}`
)

// miniContractBuild is ToolMiniContract.Build.
func miniContractBuild(tool string, missing, required []string, example string) string {
	noun := "arguments"
	if len(missing) == 1 {
		noun = "argument"
	}
	lead := tool + " was called with no arguments."
	if len(missing) > 0 {
		quoted := make([]string, len(missing))
		for i, name := range missing {
			quoted[i] = "'" + name + "'"
		}
		lead = tool + ": missing required " + noun + " " + strings.Join(quoted, ", ") + "."
	}
	return lead + " Required: " + strings.Join(required, "; ") + ". Minimal valid arguments: " + example
}

// MiniContractDecorate is ToolMiniContract.Decorate: the refusal text, then the mini-contract when the call
// lacks a required argument; no missing argument leaves the refusal untouched.
func MiniContractDecorate(refusal string, missing []string, render func(missing []string) string) string {
	if len(missing) == 0 {
		return refusal
	}
	return refusal + " " + render(missing)
}

// MiniContractCreateApp is ToolMiniContract.ForCreateApp.
func MiniContractCreateApp(missing []string) string {
	return miniContractBuild("create-app", missing, []string{"name (display name)",
		"code (business part of the app code; the schema prefix is added for you)", miniContractEnvironmentLine},
		miniContractCreateAppExample)
}

// MiniContractCreateBusinessProcess is ToolMiniContract.ForCreateBusinessProcess.
func MiniContractCreateBusinessProcess(missing []string) string {
	return miniContractBuild("create-business-process", missing, []string{miniContractEnvironmentLine,
		"descriptor (JSON object: name, caption, packageName, elements[], flows[]); read get-guidance name=process-modeling for the rest"},
		miniContractCreateBusinessProcessExample)
}

// MiniContractModifyBusinessProcess is ToolMiniContract.ForModifyBusinessProcess.
func MiniContractModifyBusinessProcess(missing []string) string {
	return miniContractBuild("modify-business-process", missing, []string{miniContractEnvironmentLine,
		"process-name OR process-uid (exactly one)",
		"operations (JSON array of {op, ...}; each op carries its payload under a named key: addElement element, " +
			"addParameter parameter, addMapping mapping, setElement elementUpdate; get-tool-contract " +
			"tool-names=[modify-business-process] lists every op shape)"},
		miniContractModifyBusinessProcessExample)
}

// MiniContractCreateEntitySchema is ToolMiniContract.ForCreateEntitySchema.
func MiniContractCreateEntitySchema(missing []string) string {
	return miniContractBuild("create-entity-schema", missing, []string{miniContractEnvironmentLine,
		"package-name (existing package)", "schema-name (with the environment's prefix)",
		"title-localizations (object with an en-US value)", "columns (optional)"},
		miniContractCreateEntitySchemaExample)
}
