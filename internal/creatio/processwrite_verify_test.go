package creatio

import (
	"context"
	"strings"
	"testing"
)

func processWriteVerifyServer(t *testing.T, build, describe string) (*Client, *[]processWriteCall) {
	t.Helper()
	processWriteNoArchive(t)
	return processWriteServer(t, "1.6.6.103", func(call processWriteCall) (int, string) {
		switch {
		case strings.HasSuffix(call.Path, "/BuildProcess"):
			return 200, build
		case strings.HasSuffix(call.Path, "/ModifyProcess"):
			return 200, build
		case strings.HasSuffix(call.Path, "/DescribeProcess"):
			return 200, describe
		}
		return 200, `{}`
	})
}

func processWriteWarnings(result ProcessWriteResult) []string {
	warnings := []string{}
	for _, message := range result.Messages {
		if message.MessageType == "Warning" {
			warnings = append(warnings, message.Value)
		}
	}
	return warnings
}

// Since 8.1.0.142 (ENG-103023) a build without blocks or labels is read back too: the read is the digest.
func TestCreateBusinessProcessReadsBackWithoutBlocksForTheDigest(t *testing.T) {
	client, calls := processWriteVerifyServer(t, `{"BuildProcessResult":{"success":true,"schemaName":"UsrP"}}`, `{}`)
	result := client.CreateBusinessProcess(context.Background(), ProcessWriteBuildRequest{Environment: "dev",
		Descriptor: `{"name":"UsrP","elements":[{"name":"s","type":"startEvent"}],"flows":[{"source":"s","target":"e"}]}`})
	warnings := processWriteWarnings(result)
	if result.ExitCode != 0 || len(warnings) != 1 || !strings.HasPrefix(warnings[0], "The write landed, but reading the process back for its summary failed: unexpected server response shape.") {
		t.Fatalf("%+v", result)
	}
	if describe := processWriteTaskCalls(*calls, "/DescribeProcess"); len(describe) != 1 || describe[0].Body != `{"request":{"name":"UsrP"}}` {
		t.Fatalf("describe %+v", describe)
	}
}

func TestCreateBusinessProcessReportsDroppedBlocks(t *testing.T) {
	client, calls := processWriteVerifyServer(t, `{"BuildProcessResult":{"success":true,"schemaName":"UsrP","schemaUId":"u"}}`,
		`{"DescribeProcessResult":{"success":true,"elements":[{"name":"Rights1"},{"name":"Mail1"},{"name":"Rights2","accessRights":{},"filter":{"conditions":[{"column":"Id"}]}}],`+
			`"flows":[{"source":"s","target":"a","label":"No"}]}}`)
	result := client.CreateBusinessProcess(context.Background(), ProcessWriteBuildRequest{Environment: "dev",
		Descriptor: `{"name":"UsrP","elements":[{"name":"Rights1","accessRights":{}},{"name":"Rights2","accessRights":{}},{"name":"Mail1","email":{"subject":"s"}}],` +
			`"flows":[{"source":"s","target":"a","label":"Yes"}]}`})
	warnings := processWriteWarnings(result)
	if result.ExitCode != 0 || len(warnings) != 3 ||
		!strings.HasPrefix(warnings[0], "The operation reported success, but the saved process does NOT carry the 'accessRights' configuration for the element 'Rights1'") ||
		!strings.HasPrefix(warnings[1], "The operation reported success, but the saved process does NOT carry the 'email' configuration for the element 'Mail1'") ||
		!strings.HasPrefix(warnings[2], "The operation reported success. The read-back shows DIFFERENT text on the flow s -> a (asked for 'Yes', drawn 'No').") {
		t.Fatalf("%q", warnings)
	}
	describe := processWriteTaskCalls(*calls, "/DescribeProcess")
	if len(describe) != 1 || describe[0].Body != `{"request":{"name":"UsrP"}}` {
		t.Fatalf("describe %+v", describe)
	}
}

func TestCreateBusinessProcessReportsAnUnverifiableReadBack(t *testing.T) {
	client, _ := processWriteVerifyServer(t, `{"BuildProcessResult":{"success":true,"schemaName":"UsrP"}}`, `{"DescribeProcessResult":{"success":false,"errorMessage":"Locked."}}`)
	result := client.CreateBusinessProcess(context.Background(), ProcessWriteBuildRequest{Environment: "dev",
		Descriptor: `{"name":"UsrP","elements":[{"name":"R","accessRights":{}}],"flows":[{"source":"s","target":"a","label":"L"}]}`})
	warnings := processWriteWarnings(result)
	if len(warnings) != 2 || !strings.HasPrefix(warnings[0], "Could not verify that the 'accessRights' configuration for the element 'R' landed: Locked.. The operation itself succeeded") ||
		!strings.HasPrefix(warnings[1], "Could not verify that the diagram label on the flow s -> a ('L') landed: Locked..") {
		t.Fatalf("%q", warnings)
	}
	client, _ = processWriteVerifyServer(t, `{"BuildProcessResult":{"success":true}}`, `{}`)
	result = client.CreateBusinessProcess(context.Background(), ProcessWriteBuildRequest{Environment: "dev",
		Descriptor: `{"name":"UsrP","elements":[{"name":"R","accessRights":{}}]}`})
	if warnings = processWriteWarnings(result); len(warnings) != 1 || !strings.Contains(warnings[0], "landed: the operation returned no process name to read back.") {
		t.Fatalf("%q", warnings)
	}
}

func TestModifyBusinessProcessWarnsOnIgnoredAddElementRightsAndReadsBackByUID(t *testing.T) {
	client, calls := processWriteVerifyServer(t, `{"ModifyProcessResult":{"success":true,"appliedOperations":2}}`,
		`{"DescribeProcessResult":{"success":true,"elements":[{"name":"Clear1","userTaskName":"changeadminrightsusertask"}]}}`)
	result := client.ModifyBusinessProcess(context.Background(), ProcessWriteModifyRequest{Environment: "dev", ProcessUID: "bbbbbbbb-0000-0000-0000-000000000001",
		Operations: `[{"op":"addElement","element":{"name":"A1","accessRights":{}}},{"op":"clearFilter","elementName":"Clear1"}]`})
	warnings := processWriteWarnings(result)
	if len(warnings) != 2 || !strings.HasPrefix(warnings[0], "The 'accessRights' block sent with addElement for the element 'A1' was NOT applied") ||
		!strings.HasPrefix(warnings[1], "The record filter on the element 'Clear1' was changed, but this environment's CrtProcessBuilder does not report") {
		t.Fatalf("%q", warnings)
	}
	// One read before the edit (the digest's baseline), one after it.
	if describe := processWriteTaskCalls(*calls, "/DescribeProcess"); len(describe) != 2 || describe[0].Body != `{"request":{"uid":"bbbbbbbb-0000-0000-0000-000000000001"}}` ||
		describe[1].Body != describe[0].Body {
		t.Fatalf("describe %+v", describe)
	}
}

func TestProcessWriteFlowLabelsFollowTheLastWrite(t *testing.T) {
	operations := processWriteParseBlocks(`[{"op":"addFlow","source":"a","target":"b","label":" one\u0001 "},{"op":"setFlow","source":"A","target":"B","label":"two"},` +
		`{"op":"addFlow","source":"b","target":"c","label":"x"},{"op":"removeFlow","source":"B","target":"C"},{"op":"addElement","source":"c","target":"d","label":"no"}]`)
	labels := processWriteFlowLabelsFromOperations(operations)
	if len(labels) != 1 || labels[0] != (processWriteFlowLabel{source: "A", target: "B", label: "two"}) {
		t.Fatalf("%+v", labels)
	}
	descriptor := processWriteParseBlocks(`{"flows":[{"source":"a","target":"b","label":" one\u0001 "}]}`)
	if labels = processWriteFlowLabelsFromDescriptor(descriptor); len(labels) != 1 || labels[0].label != "one" {
		t.Fatalf("%+v", labels)
	}
	if got := processWriteSanitize(strings.Repeat("é", 201)); got != strings.Repeat("é", 200)+"..." {
		t.Fatalf("%q", got)
	}
}
