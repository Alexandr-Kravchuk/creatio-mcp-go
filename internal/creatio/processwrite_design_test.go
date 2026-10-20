package creatio

import (
	"context"
	"strings"
	"testing"
)

func TestCreateBusinessProcessBuildsAndReportsTheSchema(t *testing.T) {
	processWriteNoArchive(t)
	client, calls := processWriteServer(t, "1.6.6.103", func(call processWriteCall) (int, string) {
		return 200, `{"BuildProcessResult":{"success":true,"schemaName":"UsrOrder","schemaUId":"aaaaaaaa-0000-0000-0000-000000000001","warnings":["Lane added."]}}`
	})
	result := client.CreateBusinessProcess(context.Background(), ProcessWriteBuildRequest{Environment: "dev",
		Descriptor: `{"name":"UsrOrder","packageName":"Old","elements":[{"name":"s","type":"startEvent"}],"n":1.50}`, PackageName: "UsrPkg"})
	got := processWriteMessages(result)
	want := []string{"Info: Building process 'UsrOrder' on 'dev'...", "Info: Process 'UsrOrder' created (UId: aaaaaaaa-0000-0000-0000-000000000001).", "Warning: Lane added.",
		"Warning: The write landed, but reading the process back for its summary failed: unexpected server response shape. describe-business-process mode=summary confirms the saved state."}
	if result.ExitCode != 0 || result.Note != processWriteCompileNote || strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("%+v", result)
	}
	builds := processWriteTaskCalls(*calls, "/BuildProcess")
	last := builds[len(builds)-1]
	if last.Path != "/0/rest/ProcessDesignService/BuildProcess" ||
		last.Body != `{"request":{"name":"UsrOrder","packageName":"UsrPkg","elements":[{"name":"s","type":"startEvent"}],"n":1.5}}` {
		t.Fatalf("request = %+v", last)
	}
}

func TestCreateBusinessProcessFailures(t *testing.T) {
	processWriteNoArchive(t)
	cases := []struct{ descriptor, response, want string }{
		{`{"name":"UsrX"}`, `{"BuildProcessResult":{"success":false,"errorMessage":"Element 'x' is unknown."}}`, "Element 'x' is unknown."},
		{`{"name":"UsrX"}`, `{"BuildProcessResult":{"success":false}}`, "BuildProcess failed."},
		{`{"name":"UsrX"}`, `{"BuildProcessResult":{"success":true,"warnings":"x"}}`, processWriteBuildUnknown},
		{`[1]`, ``, "Process descriptor must be a JSON object."},
	}
	for _, item := range cases {
		client, calls := processWriteServer(t, "1.6.6.103", func(call processWriteCall) (int, string) { return 200, item.response })
		result := client.CreateBusinessProcess(context.Background(), ProcessWriteBuildRequest{Environment: "dev", Descriptor: item.descriptor})
		last := result.Messages[len(result.Messages)-1]
		if result.ExitCode != 1 || last.MessageType != "Error" || last.Value != item.want || result.Note != "" {
			t.Errorf("%s: %+v", item.response, result)
		}
		if item.descriptor == `[1]` && len(*calls) != 1 {
			t.Errorf("a refused descriptor reached the service: %+v", *calls)
		}
	}
	client, _ := processWriteServer(t, "1.6.6.103", func(processWriteCall) (int, string) { return 200, "" })
	result := client.CreateBusinessProcess(context.Background(), ProcessWriteBuildRequest{Environment: "dev", Descriptor: `{"name":`})
	if result.ExitCode != 1 || !strings.HasPrefix(result.Messages[0].Value, "Process descriptor is not valid JSON: ") {
		t.Fatalf("%+v", result)
	}
}

func TestModifyBusinessProcessSendsOperationsAndRelaysALayoutChange(t *testing.T) {
	processWriteNoArchive(t)
	client, calls := processWriteServer(t, "1.7.0.0", func(call processWriteCall) (int, string) {
		return 200, `{"ModifyProcessResult":{"success":false,"errorMessage":"The edit would re-draw the diagram.","failedOperationIndex":2,"layoutChange":{"reason":"r","elements":["A"," ","B"]}}}`
	})
	result := client.ModifyBusinessProcess(context.Background(), ProcessWriteModifyRequest{Environment: "dev", ProcessUID: "u1", Operations: `[{"op":"removeElement","elementName":"A"}]`})
	want := "The edit would re-draw the diagram. The operation at index 2 is the one that refused. Affected elements: A, B. ASK THE USER which they want, and send nothing until they answer: the same operations to modify-business-process-as-new-version, which leaves this process and its diagram untouched and creates an INACTIVE version they can look at first, or the same operations here again with confirm-layout-change, which re-draws this process in place."
	got := processWriteMessages(result)
	if result.ExitCode != 1 || len(got) != 2 || got[0] != "Info: Editing process 'u1' on 'dev'..." || got[1] != "Error: "+want {
		t.Fatalf("%q", got)
	}
	edits := processWriteTaskCalls(*calls, "/ModifyProcess")
	last := edits[len(edits)-1]
	if last.Path != "/0/rest/ProcessDesignService/ModifyProcess" || last.Body != `{"request":{"uid":"u1","operations":[{"op":"removeElement","elementName":"A"}],"confirmLayoutChange":false}}` {
		t.Fatalf("request = %+v", last)
	}
	client, _ = processWriteServer(t, "1.7.0.0", func(call processWriteCall) (int, string) {
		return 200, `{"ModifyProcessResult":{"success":true,"schemaName":"UsrP","schemaUId":"u1","appliedOperations":1,"warnings":["Re-run the build until the configuration is compiled."]}}`
	})
	result = client.ModifyBusinessProcess(context.Background(), ProcessWriteModifyRequest{Environment: "dev", ProcessName: "UsrP", Operations: `[]`, ConfirmLayoutChange: true})
	got = processWriteMessages(result)
	if result.ExitCode != 0 || result.Note != "" || got[1] != "Info: Process 'UsrP' edited (1 operation(s) applied; UId: u1)." {
		t.Fatalf("a compile warning must suppress the note: %+v", result)
	}
	for text, want := range map[string]string{`{"op":1}`: "Operations content must be a JSON array of operations.", `[`: "Operations content is not valid JSON: "} {
		result = client.ModifyBusinessProcess(context.Background(), ProcessWriteModifyRequest{Environment: "dev", ProcessName: "UsrP", Operations: text})
		if result.ExitCode != 1 || !strings.HasPrefix(result.Messages[0].Value, want) {
			t.Errorf("%s: %+v", text, result)
		}
	}
}

func TestModifyBusinessProcessAsNewVersionReportsTheVersion(t *testing.T) {
	processWriteNoArchive(t)
	client, calls := processWriteServer(t, "1.6.6.103", func(call processWriteCall) (int, string) {
		return 200, `{"ModifyProcessAsNewVersionResult":{"success":true,"versionName":"UsrP_v3","versionSchemaUId":"v3","version":3,"isActiveVersion":false,"versionRootSchemaUId":"root","appliedOperations":0,"layoutChange":{"elements":["A"]},"warnings":["w"]}}`
	})
	result := client.ModifyBusinessProcessAsNewVersion(context.Background(), ProcessWriteModifyRequest{Environment: "dev", ProcessName: "UsrP", PackageName: "UsrPkg"})
	got := processWriteMessages(result)
	want := []string{
		"Info: Saving a new version of process 'UsrP' on 'dev'...",
		"Info: Version 3 'UsrP_v3' created (0 operation(s) applied; UId: v3; family root: root).",
		"Info: The source version is still the actual one and keeps running. ASK THE USER to open this version and say whether to make it actual; call set-active-business-process-version only once they have. Do not chain the two - a version is created inactive precisely so they get to look first.",
		"Warning: The diagram of this version is laid out afresh. Affected elements: A.",
		"Warning: w",
		"Warning: The write landed, but reading the process back for its summary failed: unexpected server response shape. describe-business-process mode=summary confirms the saved state.",
	}
	if result.ExitCode != 0 || result.Note != processWriteCompileNote || strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("%q", got)
	}
	versions := processWriteTaskCalls(*calls, "/ModifyProcessAsNewVersion")
	if body := versions[len(versions)-1].Body; body != `{"request":{"name":"UsrP","packageName":"UsrPkg","operations":[]}}` {
		t.Fatalf("body = %s", body)
	}
	client, _ = processWriteServer(t, "1.6.6.103", func(call processWriteCall) (int, string) {
		return 200, `{"ModifyProcessAsNewVersionResult":{"success":false,"errorMessage":"Bad op.","failedOperationIndex":1,"appliedOperations":1,"layoutChange":{"elements":["A"]},"versionName":"UsrP_v4","versionSchemaUId":"v4","warnings":["x","y"]}}`
	})
	result = client.ModifyBusinessProcessAsNewVersion(context.Background(), ProcessWriteModifyRequest{Environment: "dev", ProcessName: "UsrP", Operations: `[{},{}]`})
	failure := "Bad op. The operation at index 1 is the one that refused. 1 operation(s) had been applied to the version draft. Affected elements: A. The version 'UsrP_v4' (UId: v4) WAS created and still exists — a version cannot be deleted. x y"
	if result.ExitCode != 1 || result.Messages[len(result.Messages)-1].Value != failure {
		t.Fatalf("%+v", result)
	}
	for body, want := range map[string]string{
		`{"ModifyProcessAsNewVersionResult":{"success":true,"versionName":"v","isActiveVersion":true}}`: "Info: This version is reported ACTIVE — unexpected for a create; verify with describe-business-process.",
		`{"ModifyProcessAsNewVersionResult":{"success":true,"versionName":"v"}}`:                        "Info: The environment did not report which version is actual — verify with describe-business-process before reporting what runs.",
	} {
		client, _ = processWriteServer(t, "1.6.6.103", func(call processWriteCall) (int, string) { return 200, body })
		result = client.ModifyBusinessProcessAsNewVersion(context.Background(), ProcessWriteModifyRequest{Environment: "dev", ProcessName: "UsrP"})
		got = processWriteMessages(result)
		if result.ExitCode != 0 || got[1] != "Info: A new version 'v' created (0 operation(s) applied; UId: ; family root: )." || got[2] != want {
			t.Errorf("%s: %q", body, got)
		}
	}
}

func TestProcessWritePageCheckRefusesUnknownNamesAndWarnsOnNonCandidates(t *testing.T) {
	payload, _ := parseJNode([]byte(`{"elements":[{"preconfiguredPage":{"page":"UsrPage","buttons":[{"name":"Save"}],"nested":{"preconfiguredPage":{"page":"Other"}}}}]}`))
	blocks := processWritePageBlocks(payload)
	if len(blocks) != 2 || processWriteNodeString(blocks[0].get("page")) != "UsrPage" || processWriteNodeString(blocks[1].get("page")) != "Other" {
		t.Fatalf("blocks %d", len(blocks))
	}
	facts := &processWritePageFacts{allButtons: []string{"Save", "Custom"}, candidates: []string{"Save"}, dataSourcesKnown: true,
		allDataSources: []string{"PDS", "Extra"}, offeredNames: []string{"PDS"}, offeredEntities: map[string]string{"PDS": "Account"}}
	warnings := []string{}
	if refusal := processWriteCheckButtons("UsrPage", []string{"Save", "Custom"}, facts, &warnings); refusal != "" || len(warnings) != 1 ||
		!strings.HasPrefix(warnings[0], "Button 'Custom' exists on page 'UsrPage' but is not among its completing-button candidates") {
		t.Fatalf("%q %q", refusal, warnings)
	}
	refusal := processWriteCheckButtons("UsrPage", []string{"Nope"}, facts, &warnings)
	if !strings.HasPrefix(refusal, "Page 'UsrPage' has no button named 'Nope'.") || !strings.HasSuffix(refusal, "(the page carries: 'Save', 'Custom').") {
		t.Fatalf("%q", refusal)
	}
	warnings = []string{}
	sources := []processWriteNamedSource{{name: "PDS", entity: "Contact"}, {name: "Extra"}}
	if refusal := processWriteCheckSources("UsrPage", sources, facts, &warnings); refusal != "" || len(warnings) != 2 ||
		!strings.HasPrefix(warnings[0], "Data source 'PDS' on page 'UsrPage' is over 'Account', not 'Contact'.") ||
		!strings.HasPrefix(warnings[1], "Data source 'Extra' is declared on page 'UsrPage' but is not one of its page-scoped") {
		t.Fatalf("%q %q", refusal, warnings)
	}
	refusal = processWriteCheckSources("UsrPage", []processWriteNamedSource{{name: "Gone"}}, facts, &warnings)
	if !strings.HasSuffix(refusal, "Read the page's data sources with get-process-page-facts (the page offers: 'PDS').") {
		t.Fatalf("%q", refusal)
	}
}

func TestProcessWritePageCheckSkipsAPageThatCannotBeRead(t *testing.T) {
	processWriteNoArchive(t)
	client, calls := processWriteServer(t, "1.6.6.103", func(call processWriteCall) (int, string) {
		if strings.Contains(call.Path, "BuildProcess") {
			return 200, `{"BuildProcessResult":{"success":true,"schemaName":"UsrP","schemaUId":"u"}}`
		}
		return 500, `{}`
	})
	result := client.CreateBusinessProcess(context.Background(), ProcessWriteBuildRequest{Environment: "dev",
		Descriptor: `{"name":"UsrP","elements":[{"preconfiguredPage":{"page":"UsrMissing_FormPage","buttons":[{"name":"Save"}]}}]}`})
	if result.ExitCode != 0 || len(result.Messages) != 3 || !strings.HasPrefix(result.Messages[2].Value, "The write landed, but reading the process back for its summary failed:") {
		t.Fatalf("%+v", result)
	}
	read := false
	for _, call := range *calls {
		if !strings.Contains(call.Path, "ProcessDesignService") && call.Root != "SysPackage" {
			read = true
		}
	}
	if !read {
		t.Fatal("the page was not read before the build")
	}
}
