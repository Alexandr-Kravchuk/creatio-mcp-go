package creatio

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// The describe-business-process mode=full reply clio 8.1.0.142 gave for a process create-business-process had just
// built on the write stand (live run T23, window/t13.json), and the digest line clio's create reported for it.
const (
	processWriteDigestClioGraph = `{"name":"UsrParitytn89mnclioBpProc","caption":"Parity tn89mn process","schemaUId":"216f2309-c886-4755-b514-d5a9e1a8923f","version":0,"isActiveVersion":true,"activeVersionSchemaUId":"216f2309-c886-4755-b514-d5a9e1a8923f","activeVersionName":"UsrParitytn89mnclioBpProc","versionRootSchemaUId":"216f2309-c886-4755-b514-d5a9e1a8923f","activeVersionSource":"process-library-view","versions":[{"schemaUId":"216f2309-c886-4755-b514-d5a9e1a8923f","name":"UsrParitytn89mnclioBpProc","caption":"Parity tn89mn process","version":0,"isActiveVersion":true,"isRoot":true,"packageUId":"02eb5fbf-b5a0-497b-bdc1-97387203d0ba","packageName":"UsrParitytn89mnclioBp","enabled":true,"modifiedOn":"2026-10-21T00:42:04"}],"elements":[{"name":"Start1","uid":"de6e720f-59b7-4f31-a856-13ad07bded9f","caption":"Start","type":"ProcessSchemaStartEvent","buildType":"startevent","managerItemUId":"53818048-7868-48f6-ada0-0ebaa65af628","position":"60;184","size":"31;31","useBackgroundMode":false,"parameters":[],"accessRights":null,"addData":null,"changeData":null,"deleteData":null,"readData":null},{"name":"End1","uid":"1c13e244-cb50-4b13-ae6f-5e6274e5e7e3","caption":"End","type":"ProcessSchemaTerminateEvent","buildType":"endevent","managerItemUId":"1bd93619-0574-454e-bb4e-cf53b9eb9470","position":"240;184","size":"31;31","useBackgroundMode":false,"parameters":[],"accessRights":null,"addData":null,"changeData":null,"deleteData":null,"readData":null}],"flows":[{"source":"Start1","target":"End1","kind":"sequence","branchesOnActivityResult":false,"geometry":{"start":"91;200","points":[],"end":"240;200","exitSide":"right","entrySide":"left"},"name":"SequenceFlow_Start1_End1"}],"parameters":[],"usings":[]}`
	processWriteDigestClioLine  = `{"summary":{"name":"UsrParitytn89mnclioBpProc","caption":"Parity tn89mn process","schemaUId":"216f2309-c886-4755-b514-d5a9e1a8923f","version":0,"isActiveVersion":true,"versionCount":1,"checksum":"0980ec6c34b1","elements":[{"name":"Start1","type":"startevent","caption":"Start"},{"name":"End1","type":"endevent","caption":"End"}],"flows":["Start1 -> End1"],"parameters":[]}}`
)

func processWriteDigestGraph(t *testing.T, text string) *jnode {
	t.Helper()
	graph, err := parseJNode([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	return graph
}

func processWriteDigestLines(log *processWriteLog) []string {
	lines := []string{}
	for _, message := range log.result(0).Messages {
		lines = append(lines, message.MessageType+": "+message.Value)
	}
	return lines
}

func TestProcessWriteDigestReproducesClioCreateDigest(t *testing.T) {
	log := &processWriteLog{}
	processWriteReportDigest(log, processWriteRead{graph: processWriteDigestGraph(t, processWriteDigestClioGraph)}, nil, true, "", false)
	if got := processWriteDigestLines(log); len(got) != 1 || got[0] != "Info: "+processWriteDigestClioLine {
		t.Fatalf("%q", got)
	}
}

func TestProcessWriteDigestVersionWarning(t *testing.T) {
	graph := `{"name":"P","caption":"P","schemaUId":"u","versionReadWarning":"the process library read did not complete within 3s, so the version facts were not established","elements":[],"flows":[],"parameters":[]}`
	log := &processWriteLog{}
	processWriteReportDigest(log, processWriteRead{graph: processWriteDigestGraph(t, graph)}, nil, true, "", false)
	want := `Info: {"summary":{"name":"P","caption":"P","schemaUId":"u","versionReadWarning":"version standing not read; the summary and changes still confirm the write - no re-describe is needed (the process library read did not complete within 3s, so the version facts were not established)","checksum":`
	if got := processWriteDigestLines(log); len(got) != 1 || !strings.HasPrefix(got[0], want) {
		t.Fatalf("%q", got)
	}
	// A read that did not ask for the version standing carries a warning that says nothing about the process.
	log = &processWriteLog{}
	processWriteReportDigest(log, processWriteRead{graph: processWriteDigestGraph(t, graph)}, nil, false, "", false)
	if got := processWriteDigestLines(log); len(got) != 1 || strings.Contains(got[0], "versionReadWarning") {
		t.Fatalf("%q", got)
	}
}

func TestProcessWriteDigestFailuresAreWarnings(t *testing.T) {
	log := &processWriteLog{}
	processWriteReportDigest(log, processWriteRead{err: errors.New("Locked.")}, nil, true, "", false)
	want := "Warning: The write landed, but reading the process back for its summary failed: Locked.. describe-business-process mode=summary confirms the saved state."
	if got := processWriteDigestLines(log); len(got) != 1 || got[0] != want {
		t.Fatalf("%q", got)
	}
	log = &processWriteLog{}
	processWriteReportDigest(log, processWriteRead{err: errors.New("Locked.")}, nil, true, "", true)
	if got := processWriteDigestLines(log); len(got) != 0 {
		t.Fatalf("a failure a guard already reported is not reported twice: %q", got)
	}
	graph := processWriteDigestGraph(t, `{"name":"P","schemaUId":"u","elements":[],"flows":[],"parameters":[]}`)
	log = &processWriteLog{}
	processWriteReportDigest(log, processWriteRead{graph: graph}, &processWriteRead{err: errors.New("gone")}, true, "the source version 'P'", false)
	got := processWriteDigestLines(log)
	if len(got) != 1 || !strings.HasSuffix(got[0], `"parameters":[]},"changesAgainst":"the source version 'P'","changesUnavailable":"the read before the write failed: gone"}`) {
		t.Fatalf("%q", got)
	}
}

func TestProcessWriteDigestChanges(t *testing.T) {
	before := processWriteDigestGraph(t, `{"name":"P","schemaUId":"u","elements":[`+
		`{"name":"Start1","uid":"a","type":"t","buildType":"startevent","position":"1;1"},`+
		`{"name":"Task1","uid":"b","caption":"Old","buildType":"formulatask"},`+
		`{"name":"Gone1","buildType":"formulatask"}],`+
		`"flows":[{"source":"Start1","target":"Task1","kind":"sequence"},{"source":"Start1","target":"Task1","kind":"sequence","label":"x"},`+
		`{"source":"Task1","target":"Gone1","kind":"conditional","condition":"c","geometry":{"start":"1"}}],`+
		`"parameters":[{"name":"In1","type":"Text"},{"name":"Old1","type":"Integer"}]}`)
	after := processWriteDigestGraph(t, `{"name":"P","schemaUId":"u","elements":[`+
		`{"name":"Start1","uid":"z","type":"t","buildType":"startevent","position":"9;9","size":"2;2"},`+
		`{"name":"Task1","uid":"b","caption":"New","buildType":"formulatask"},`+
		`{"name":"New1","buildType":"endevent"}],`+
		`"flows":[{"source":"Start1","target":"Task1","kind":"sequence","label":"x"},{"source":"Task1","target":"New1","kind":"sequence","geometry":{"start":"2"}}],`+
		`"parameters":[{"name":"In1","type":"Text","uid":"p"},{"name":"New1","type":"Boolean"}]}`)
	changes := processDescribeWrite(processWriteBuildChanges(before, after))
	// Layout (position, size, geometry) and identities (uid) are not changes; a caption is. Of two parallel flows
	// the one removed is reported, not the survivor.
	want := `{"elementsAdded":["New1"],"elementsRemoved":["Gone1"],"elementsChanged":["Task1"],` +
		`"flowsAdded":["Task1 -> New1"],"flowsRemoved":["Start1 -> Task1","Task1 -> Gone1 [conditional, condition]"],` +
		`"parametersAdded":["New1"],"parametersRemoved":["Old1"]}`
	if changes != want {
		t.Fatalf("%s", changes)
	}
	if same := processDescribeWrite(processWriteBuildChanges(after, after)); same != `{}` {
		t.Fatalf("%s", same)
	}
	many := []string{}
	for index := 0; index < 27; index++ {
		many = append(many, fmt.Sprintf(`{"name":"E%d"}`, index))
	}
	wide := processWriteDigestGraph(t, `{"elements":[`+strings.Join(many, ",")+`]}`)
	capped := processWriteBuildChanges(processWriteDigestGraph(t, `{"elements":[]}`), wide)
	if added := capped.get("elementsAdded"); len(added.items) != 25 || processDescribeWrite(capped.get("elementsAddedOmitted")) != "2" {
		t.Fatalf("%s", processDescribeWrite(capped))
	}
}

func TestProcessWriteWhyAnEditIsNotReadBack(t *testing.T) {
	operations := func(text string) *jnode { return processWriteParseBlocks(text) }
	caller := processWriteRead{graph: processWriteDigestGraph(t, `{"elements":[{"name":"Sub1","buildType":"subprocess"},{"name":"Sub2","subProcess":{"process":"X"}}]}`)}
	plain := processWriteRead{graph: processWriteDigestGraph(t, `{"elements":[{"name":"Task1","buildType":"formulatask","subProcess":null}]}`)}
	failed := processWriteRead{err: errors.New("x")}
	cases := []struct {
		name       string
		operations string
		before     processWriteRead
		want       string
	}{
		{"plain edit", `[{"op":"setElement","elementName":"Task1","elementUpdate":{"caption":"c"}}]`, plain, ""},
		{"adds a sub-process", `[{"op":"addElement","element":{"name":"S","subProcess":{}}}]`, plain, processWriteCallerNotReadBack},
		{"sets a sub-process", `[{"op":"setElement","elementName":"Task1","elementUpdate":{"subProcess":{}}}]`, plain, processWriteCallerNotReadBack},
		{"keeps a caller", `[{"op":"removeElement","elementName":"Sub1"}]`, caller, processWriteCallerNotReadBack},
		{"removes every sub-process", `[{"op":"removeElement","elementName":"sub1"},{"op":"removeElement","elementName":"SUB2"}]`, caller, ""},
		{"unknown before", `[]`, failed, processWritePossibleCallerNotReadBack},
		{"binds a process parameter", `[{"op":"addMapping","mapping":{"targetProcessParameter":"P1"}}]`, caller, ""},
	}
	for _, item := range cases {
		if got := processWriteWhyAnEditIsNotReadBack(operations(item.operations), item.before); got != item.want {
			t.Errorf("%s: %q", item.name, got)
		}
	}
}

func TestProcessWriteFactsWithinAbandonsASlowRead(t *testing.T) {
	client := &Client{}
	release := make(chan struct{})
	defer close(release)
	facts := client.processWriteFactsWithin(context.Background(), 10*time.Millisecond, func(ctx context.Context) processDescribeFacts {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return processDescribeFacts{}
	})
	if facts.Warning != "the process library read did not complete within 0.01s, so the version facts were not established" {
		t.Fatalf("%q", facts.Warning)
	}
	if got := processWriteSeconds(processWriteDigestBudget); got != "3" {
		t.Fatalf("%q", got)
	}
}

// processWriteDigestServer answers the process designer, the version library (one family: root UsrP, inactive
// version UsrP_v2) and DescribeProcess for both members.
func processWriteDigestServer(t *testing.T, write string) (*Client, *[]processWriteCall) {
	t.Helper()
	processWriteNoArchive(t)
	const root, second = "aaaaaaaa-0000-0000-0000-000000000001", "aaaaaaaa-0000-0000-0000-000000000002"
	rootRow := `{"UId":"` + root + `","Name":"UsrP","Caption":"P","VersionParentUId":"` + root + `","IsActiveVersion":true,"Version":0,"PackageUId":"cccccccc-0000-0000-0000-000000000001","Enabled":true}`
	secondRow := `{"UId":"` + second + `","Name":"UsrP_v2","Caption":"P","VersionParentUId":"` + root + `","IsActiveVersion":false,"Version":1,"PackageUId":"cccccccc-0000-0000-0000-000000000001","Enabled":true}`
	graph := func(name, uid, caption string) string {
		return `{"DescribeProcessResult":{"success":true,"name":"` + name + `","caption":"P","schemaUId":"` + uid + `","elements":[` +
			`{"name":"Start1","uid":"s","caption":"Start","buildType":"startevent","position":"1;1"},` +
			`{"name":"Task1","uid":"t","caption":"` + caption + `","buildType":"formulatask"}],` +
			`"flows":[{"source":"Start1","target":"Task1","kind":"sequence"}],"parameters":[]}}`
	}
	return processWriteServer(t, "1.6.6.103", func(call processWriteCall) (int, string) {
		switch {
		case call.Root == "VwProcessLib":
			switch {
			case strings.Contains(call.Body, `"VersionParentUId":{"comparisonType"`):
				return 200, `{"success":true,"rows":[` + rootRow + `,` + secondRow + `]}`
			case strings.Contains(call.Body, second) || strings.Contains(call.Body, `"UsrP_v2"`):
				return 200, `{"success":true,"rows":[` + secondRow + `]}`
			default:
				return 200, `{"success":true,"rows":[` + rootRow + `]}`
			}
		case strings.HasSuffix(call.Path, "/DescribeProcess"):
			if strings.Contains(call.Body, second) || strings.Contains(call.Body, "UsrP_v2") {
				return 200, graph("UsrP_v2", second, "Edited")
			}
			return 200, graph("UsrP", root, "Original")
		case strings.Contains(call.Path, "ProcessDesignService"):
			return 200, write
		}
		return 200, `{"success":true,"rows":[]}`
	})
}

func TestCreateBusinessProcessReportsTheDigest(t *testing.T) {
	client, _ := processWriteDigestServer(t, `{"BuildProcessResult":{"success":true,"schemaName":"UsrP","schemaUId":"aaaaaaaa-0000-0000-0000-000000000001"}}`)
	result := client.CreateBusinessProcess(context.Background(), ProcessWriteBuildRequest{Environment: "dev", Descriptor: `{"name":"UsrP"}`})
	got := processWriteMessages(result)
	if result.ExitCode != 0 || len(got) != 3 || !strings.HasPrefix(got[2], `Info: {"summary":{"name":"UsrP","caption":"P","schemaUId":"aaaaaaaa-0000-0000-0000-000000000001","version":0,"isActiveVersion":true,"versionCount":2,"checksum":"`) ||
		!strings.HasSuffix(got[2], `","elements":[{"name":"Start1","type":"startevent","caption":"Start"},{"name":"Task1","type":"formulatask","caption":"Original"}],"flows":["Start1 -> Task1"],"parameters":[]}}`) {
		t.Fatalf("%q", got)
	}
}

func TestCreateBusinessProcessDoesNotReadBackACaller(t *testing.T) {
	client, calls := processWriteDigestServer(t, `{"BuildProcessResult":{"success":true,"schemaName":"UsrP","schemaUId":"u"}}`)
	result := client.CreateBusinessProcess(context.Background(), ProcessWriteBuildRequest{Environment: "dev",
		Descriptor: `{"name":"UsrP","elements":[{"name":"Sub1","type":"subProcess","subProcess":{"process":"UsrOther"}}]}`})
	got := processWriteMessages(result)
	if result.ExitCode != 0 || len(got) != 3 || got[2] != "Info: "+processWriteCallerNotReadBack {
		t.Fatalf("%q", got)
	}
	if describe := processWriteTaskCalls(*calls, "/DescribeProcess"); len(describe) != 0 {
		t.Fatalf("a caller was read back: %+v", describe)
	}
}

func TestModifyBusinessProcessReportsWhatTheEditChanged(t *testing.T) {
	client, calls := processWriteDigestServer(t, `{"ModifyProcessResult":{"success":true,"schemaName":"UsrP","schemaUId":"aaaaaaaa-0000-0000-0000-000000000001","appliedOperations":1}}`)
	// The fake answers the same graph before and after, so nothing changed.
	result := client.ModifyBusinessProcess(context.Background(), ProcessWriteModifyRequest{Environment: "dev", ProcessName: "UsrP",
		Operations: `[{"op":"setElement","elementName":"Task1","elementUpdate":{"caption":"x"}}]`})
	got := processWriteMessages(result)
	if result.ExitCode != 0 || len(got) != 3 || !strings.HasSuffix(got[2], `"parameters":[]},"changes":{}}`) {
		t.Fatalf("%q", got)
	}
	if describe := processWriteTaskCalls(*calls, "/DescribeProcess"); len(describe) != 2 {
		t.Fatalf("%+v", describe)
	}
}

func TestModifyBusinessProcessAsNewVersionReportsChangesAgainstTheSource(t *testing.T) {
	client, _ := processWriteDigestServer(t, `{"ModifyProcessAsNewVersionResult":{"success":true,"versionName":"UsrP_v2","versionSchemaUId":"aaaaaaaa-0000-0000-0000-000000000002","version":1,"isActiveVersion":false,"versionRootSchemaUId":"aaaaaaaa-0000-0000-0000-000000000001"}}`)
	result := client.ModifyBusinessProcessAsNewVersion(context.Background(), ProcessWriteModifyRequest{Environment: "dev", ProcessName: "UsrP"})
	got := processWriteMessages(result)
	last := got[len(got)-1]
	if result.ExitCode != 0 || !strings.HasPrefix(last, `Info: {"summary":{"name":"UsrP_v2","caption":"P","schemaUId":"aaaaaaaa-0000-0000-0000-000000000002","version":1,"isActiveVersion":false,"activeVersionName":"UsrP","activeVersionSchemaUId":"aaaaaaaa-0000-0000-0000-000000000001","versionCount":2,`) ||
		!strings.HasSuffix(last, `"changesAgainst":"the source version 'UsrP'","changes":{"elementsChanged":["Task1"]}}`) {
		t.Fatalf("%q", got)
	}
	client, _ = processWriteDigestServer(t, `{"ModifyProcessAsNewVersionResult":{"success":true,"isActiveVersion":false}}`)
	result = client.ModifyBusinessProcessAsNewVersion(context.Background(), ProcessWriteModifyRequest{Environment: "dev", ProcessName: "UsrP"})
	got = processWriteMessages(result)
	if got[len(got)-1] != "Warning: The version was saved, but the environment named no version to summarize. describe-business-process mode=summary confirms the saved state." {
		t.Fatalf("%q", got)
	}
}

func TestSetActiveBusinessProcessVersionReportsChangesAgainstThePreviousVersion(t *testing.T) {
	client, calls := processWriteDigestServer(t, `{"SetActiveProcessVersionResult":{"success":true,"activeVersionName":"UsrP_v2","activeVersionSchemaUId":"aaaaaaaa-0000-0000-0000-000000000002"}}`)
	result := client.SetActiveBusinessProcessVersion(context.Background(), ProcessWriteActivateRequest{Environment: "dev", VersionName: "UsrP_v2"})
	got := processWriteMessages(result)
	last := got[len(got)-1]
	if result.ExitCode != 0 || !strings.HasPrefix(last, `Info: {"summary":{"name":"UsrP_v2",`) ||
		!strings.HasSuffix(last, `"changesAgainst":"the previously active version 'UsrP'","changes":{"elementsChanged":["Task1"]}}`) {
		t.Fatalf("%q", got)
	}
	// Version standing read by name before the write, then the activated graph, then the previous one by UId.
	describe := processWriteTaskCalls(*calls, "/DescribeProcess")
	if len(describe) != 2 || describe[0].Body != `{"request":{"name":"UsrP_v2"}}` || describe[1].Body != `{"request":{"uid":"aaaaaaaa-0000-0000-0000-000000000001"}}` {
		t.Fatalf("%+v", describe)
	}
	client, _ = processWriteDigestServer(t, `{"SetActiveProcessVersionResult":{"success":true,"activeVersionName":"UsrP","activeVersionSchemaUId":"aaaaaaaa-0000-0000-0000-000000000001"}}`)
	result = client.SetActiveBusinessProcessVersion(context.Background(), ProcessWriteActivateRequest{Environment: "dev", VersionName: "UsrP"})
	got = processWriteMessages(result)
	if last = got[len(got)-1]; !strings.HasSuffix(last, `"changesAgainst":"itself: this version was already the active one","changes":{}}`) {
		t.Fatalf("%q", got)
	}
}
