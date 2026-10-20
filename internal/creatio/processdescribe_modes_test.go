package creatio

import (
	"strings"
	"testing"
)

func TestProcessDescribeModeRefusalsMatchClio142(t *testing.T) {
	for _, c := range []struct{ mode, element, want string }{
		{"brief", "", "mode 'brief' is not supported; use one of summary, full, versions, element."},
		{"element", "", "mode 'element' needs element=<name>."},
		{"summary", "Start1", "element=<name> selects the element reply; it cannot be combined with mode 'summary'."},
		{" FULL ", "", ""},
		{"Element", "Start1", ""},
		{"", "Start1", ""},
	} {
		if got := processDescribeValidateMode(c.mode, c.element); got != c.want {
			t.Errorf("%q/%q: %q", c.mode, c.element, got)
		}
	}
}

func describeTestGraph(t *testing.T) *jnode {
	t.Helper()
	// Server order differs from clio's model on purpose: the projection must write the model's order.
	raw := `{"name":"UsrDesc","caption":"Opis <x>","schemaUId":"aaaaaaaa-0000-0000-0000-000000000006","version":0,"isActiveVersion":true,` +
		`"elements":[{"position":"1;1","name":"Start1","caption":"Start","type":"T","buildType":"startSignal","signal":{"on":"modified","entity":"Contact","extra":1}},` +
		`{"name":"Read1","uid":"u1","type":"ReadDataUserTask","buildType":"readData","filterDecodedCompletely":true,` +
		`"filter":{"conditions":[{"value":"x","column":"Name"}],"object":"Contact","logicalOperation":"and"},` +
		`"parameters":[{"value":"{raw}","name":"DataSourceFilters","dropped":true,"type":"Text"}]}],` +
		`"flows":[{"name":"f1","source":"Start1","target":"Read1","kind":"conditional","condition":"x"}],"parameters":[],"extra":null}`
	result, err := parseJNode([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	graph := processDescribeOutput(result, processDescribeFacts{})
	processDescribeOmitDecodedFilters(graph)
	return graph
}

func TestProcessDescribeProjectsClioModelAndOmitsDecodedFilter(t *testing.T) {
	full := processDescribeWrite(describeTestGraph(t))
	want := `{"name":"UsrDesc","caption":"Opis <x>","schemaUId":"aaaaaaaa-0000-0000-0000-000000000006",` +
		`"elements":[{"name":"Start1","caption":"Start","type":"T","buildType":"startSignal","position":"1;1","signal":{"entity":"Contact","on":"modified"}},` +
		`{"name":"Read1","uid":"u1","type":"ReadDataUserTask","buildType":"readData",` +
		`"parameters":[{"name":"DataSourceFilters","type":"Text","valueOmitted":"decoded into the element's filter"}],` +
		`"filter":{"object":"Contact","logicalOperation":"and","conditions":[{"column":"Name","value":"x"}]},"filterDecodedCompletely":true}],` +
		`"flows":[{"source":"Start1","target":"Read1","kind":"conditional","condition":"x","name":"f1"}],"parameters":[],"extra":null}`
	if full != want {
		t.Fatalf("full\n%s\nwant\n%s", full, want)
	}
}

func TestProcessDescribeRepliesByMode(t *testing.T) {
	graph := describeTestGraph(t)
	reply, refusal := processDescribeBuildReply(graph, "", "")
	if refusal != "" || !strings.HasPrefix(reply, `{"mode":"full","name":"UsrDesc",`) {
		t.Fatalf("default %s %s", reply, refusal)
	}
	checksum := processDescribeChecksum(graph)
	summary, _ := processDescribeBuildReply(graph, "summary", "")
	if !strings.HasPrefix(summary, `{"mode":"summary","name":"UsrDesc","caption":"Opis <x>",`) ||
		!strings.Contains(summary, `"checksum":"`+checksum+`"`) || !strings.Contains(summary, `"flows":["Start1 -> Read1 [conditional, condition]"]`) ||
		!strings.Contains(summary, `"startSignals":[{"element":"Start1","entity":"Contact","on":"modified"}]`) {
		t.Fatalf("summary %s", summary)
	}
	element, _ := processDescribeBuildReply(graph, "", " read1 ")
	if !strings.HasPrefix(element, `{"mode":"element","name":"UsrDesc","schemaUId":"aaaaaaaa-0000-0000-0000-000000000006","checksum":"`+checksum+`","element":{"name":"Read1"`) ||
		!strings.Contains(element, `"flowsIn":[{"source":"Start1"`) || !strings.HasSuffix(element, `"flowsOut":[]}`) {
		t.Fatalf("element %s", element)
	}
	if _, refusal := processDescribeBuildReply(graph, "", "Nope"); refusal != "process 'UsrDesc' has no element named 'Nope'. Elements: Start1, Read1." {
		t.Fatalf("missing element %q", refusal)
	}
	// Layout, identities and culture-resolved text do not change the checksum; content does.
	moved := describeTestGraph(t)
	first := moved.get("elements").items[0]
	first.set("position", newString("9;9"))
	first.set("caption", newString("Другий"))
	moved.get("elements").items[1].set("uid", newString("other"))
	if processDescribeChecksum(moved) != checksum || len(checksum) != 12 {
		t.Fatalf("checksum moved %s %s", processDescribeChecksum(moved), checksum)
	}
	first.set("type", newString("Other"))
	if processDescribeChecksum(moved) == checksum {
		t.Fatalf("checksum ignored a content change")
	}
}

func TestProcessDescribeEscapesAsClioMcpResultEncoder(t *testing.T) {
	text := "a\"b\\c<&>'" + string(rune(0xe9)) + string(rune(0x200b)) + string(rune(0x2028)) + string(rune(0x1F600)) + "\x01"
	got := processDescribeWrite(newString(text))
	want := strings.ReplaceAll(`"a\"b\\c<&>'`+string(rune(0xe9))+`Xu200BXu2028XuD83DXuDE00Xu0001"`, "X", `\`)
	if got != want {
		t.Fatalf("%s, want %s", got, want)
	}
	if size := processDescribeCommandReplySize(`{"a":1}`); size != len(processDescribeCommandPrefix)+len(`"{\"a\":1}"`)+len(processDescribeCommandSuffix) {
		t.Fatalf("size %d", size)
	}
}
