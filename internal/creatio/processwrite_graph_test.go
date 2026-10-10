package creatio

import (
	"strings"
	"testing"
)

// Cases ported from clio's ProcessGraphValidatorTests.

func processWritePGNode(name, typ string) *ProcessGraphNode {
	node := &ProcessGraphNode{Type: &typ}
	if name != "\x00" {
		node.Name = &name
	}
	return node
}

func processWritePGEdge(source, target string, kind ProcessGraphFlowKind) *ProcessGraphEdge {
	edge := &ProcessGraphEdge{FlowKind: kind}
	if source != "\x00" {
		edge.Source = &source
	}
	if target != "\x00" {
		edge.Target = &target
	}
	return edge
}

func processWritePGSeq(source, target string) *ProcessGraphEdge {
	return processWritePGEdge(source, target, ProcessGraphSequence)
}
func processWritePGCond(source, target string) *ProcessGraphEdge {
	return processWritePGEdge(source, target, ProcessGraphConditional)
}
func processWritePGDef(source, target string) *ProcessGraphEdge {
	return processWritePGEdge(source, target, ProcessGraphDefault)
}
func processWritePGWith(edge *ProcessGraphEdge, condition string, results ...string) *ProcessGraphEdge {
	edge.Condition = &condition
	if condition == "\x00" {
		edge.Condition = nil
	}
	if results != nil {
		edge.Results = results
	}
	return edge
}

func processWritePGRules(findings []ProcessGraphFinding) []string {
	rules := []string{}
	for _, finding := range findings {
		rules = append(rules, finding.Severity+":"+finding.RuleID)
	}
	return rules
}

func processWritePGHas(findings []ProcessGraphFinding, severity, rule string, node string) bool {
	for _, finding := range findings {
		if finding.Severity == severity && finding.RuleID == rule && (node == "" || (finding.NodeName != nil && *finding.NodeName == node)) {
			return true
		}
	}
	return false
}

func TestValidateProcessGraphRuleCases(t *testing.T) {
	cases := []struct {
		label     string
		nodes     []*ProcessGraphNode
		edges     []*ProcessGraphEdge
		hasErrors bool
		want      []string // exact severity:rule sequence
	}{
		{"canonical", []*ProcessGraphNode{processWritePGNode("s", "startEvent"), processWritePGNode("r", "readDataUserTask"), processWritePGNode("e", "endEvent")},
			[]*ProcessGraphEdge{processWritePGSeq("s", "r"), processWritePGSeq("r", "e")}, false, []string{}},
		{"build tokens", []*ProcessGraphNode{processWritePGNode("s", "startEvent"), processWritePGNode("x", "changeAccessRights"), processWritePGNode("y", "callActivity"), processWritePGNode("z", "preconfiguredpage"), processWritePGNode("e", "endEvent")},
			[]*ProcessGraphEdge{processWritePGSeq("s", "x"), processWritePGSeq("x", "y"), processWritePGSeq("y", "z"), processWritePGSeq("z", "e")}, false, []string{}},
		{"R1 incoming start", []*ProcessGraphNode{processWritePGNode("s", "startEvent"), processWritePGNode("a", "activityUserTask"), processWritePGNode("e", "endEvent")},
			[]*ProcessGraphEdge{processWritePGSeq("s", "a"), processWritePGSeq("a", "e"), processWritePGSeq("a", "s")}, true, []string{"error:R1", "warning:R12"}},
		{"R2 outgoing end", []*ProcessGraphNode{processWritePGNode("s", "startEvent"), processWritePGNode("r", "readDataUserTask"), processWritePGNode("e", "endEvent")},
			[]*ProcessGraphEdge{processWritePGSeq("s", "r"), processWritePGSeq("r", "e"), processWritePGSeq("e", "r")}, true, []string{"error:R2"}},
		{"R15 missing node", []*ProcessGraphNode{processWritePGNode("s", "startEvent"), processWritePGNode("r", "readDataUserTask"), processWritePGNode("e", "endEvent")},
			[]*ProcessGraphEdge{processWritePGSeq("s", "r"), processWritePGSeq("r", "e"), processWritePGSeq("r", "ghost")}, true, []string{"error:R15"}},
		{"R3 none", []*ProcessGraphNode{processWritePGNode("a", "activityUserTask"), processWritePGNode("e", "endEvent")},
			[]*ProcessGraphEdge{processWritePGSeq("a", "e")}, true, []string{"error:R3"}},
		{"signal starts", []*ProcessGraphNode{processWritePGNode("added", "signalStart"), processWritePGNode("changed", "signalStart"), processWritePGNode("r", "readDataUserTask"), processWritePGNode("e", "endEvent")},
			[]*ProcessGraphEdge{processWritePGSeq("added", "r"), processWritePGSeq("changed", "r"), processWritePGSeq("r", "e")}, false, []string{}},
		{"R3 two simple", []*ProcessGraphNode{processWritePGNode("s1", "startEvent"), processWritePGNode("s2", "startEvent"), processWritePGNode("r", "readDataUserTask"), processWritePGNode("e", "endEvent")},
			[]*ProcessGraphEdge{processWritePGSeq("s1", "r"), processWritePGSeq("s2", "r"), processWritePGSeq("r", "e")}, true, []string{"error:R3"}},
		{"R10", []*ProcessGraphNode{processWritePGNode("s", "startEvent"), processWritePGNode("g", "eventBasedGateway"), processWritePGNode("a", "activityUserTask"), processWritePGNode("e", "endEvent")},
			[]*ProcessGraphEdge{processWritePGSeq("s", "g"), processWritePGSeq("g", "a"), processWritePGSeq("a", "e")}, true, []string{"error:R10"}},
		{"R11", []*ProcessGraphNode{processWritePGNode("s", "startEvent"), processWritePGNode("g", "parallelGateway"), processWritePGNode("a", "activityUserTask"), processWritePGNode("e", "endEvent")},
			[]*ProcessGraphEdge{processWritePGSeq("s", "g"), processWritePGCond("g", "a"), processWritePGSeq("a", "e")}, true, []string{"error:R11", "warning:R13"}},
		{"R13 from start", []*ProcessGraphNode{processWritePGNode("s", "startEvent"), processWritePGNode("a", "activityUserTask"), processWritePGNode("e", "endEvent")},
			[]*ProcessGraphEdge{processWritePGCond("s", "a"), processWritePGSeq("a", "e")}, false, []string{"warning:R13", "warning:R13"}},
		{"condition on plain and default", []*ProcessGraphNode{processWritePGNode("s", "startEvent"), processWritePGNode("a", "activityUserTask"), processWritePGNode("g", "exclusiveGateway"),
			processWritePGNode("b", "activityUserTask"), processWritePGNode("c", "activityUserTask"), processWritePGNode("e", "endEvent")},
			[]*ProcessGraphEdge{processWritePGSeq("s", "a"), processWritePGWith(processWritePGSeq("a", "g"), "[#Amount#] > 100"), processWritePGWith(processWritePGCond("g", "b"), "[#Amount#] > 100"),
				processWritePGWith(processWritePGDef("g", "c"), "[#Amount#] <= 100"), processWritePGSeq("b", "e"), processWritePGSeq("c", "e")}, false, []string{}},
		{"R7 beside a default", []*ProcessGraphNode{processWritePGNode("s", "startEvent"), processWritePGNode("g", "exclusiveGateway"), processWritePGNode("a", "activityUserTask"),
			processWritePGNode("b", "activityUserTask"), processWritePGNode("c", "activityUserTask"), processWritePGNode("e", "endEvent")},
			[]*ProcessGraphEdge{processWritePGSeq("s", "g"), processWritePGWith(processWritePGCond("g", "a"), "[#Amount#] > 100"), processWritePGDef("g", "b"), processWritePGSeq("g", "c"), processWritePGSeq("a", "e"), processWritePGSeq("b", "e"), processWritePGSeq("c", "e")},
			true, []string{"warning:R7", "error:R18"}},
		{"two defaults", []*ProcessGraphNode{processWritePGNode("s", "startEvent"), processWritePGNode("g", "exclusiveGateway"), processWritePGNode("a", "activityUserTask"),
			processWritePGNode("b", "activityUserTask"), processWritePGNode("c", "activityUserTask"), processWritePGNode("e", "endEvent")},
			[]*ProcessGraphEdge{processWritePGSeq("s", "g"), processWritePGWith(processWritePGCond("g", "a"), "[#Amount#] > 100"), processWritePGDef("g", "b"), processWritePGDef("g", "c"), processWritePGSeq("a", "e"), processWritePGSeq("b", "e"), processWritePGSeq("c", "e")},
			true, []string{"error:R14", "error:R18"}},
		{"R7 alone default plain to gateway", []*ProcessGraphNode{processWritePGNode("s", "startEvent"), processWritePGNode("g", "exclusiveGateway"), processWritePGNode("h", "exclusiveGateway"),
			processWritePGNode("b", "activityUserTask"), processWritePGNode("c", "activityUserTask"), processWritePGNode("e", "endEvent")},
			[]*ProcessGraphEdge{processWritePGSeq("s", "g"), processWritePGDef("g", "b"), processWritePGSeq("g", "h"), processWritePGSeq("h", "c"), processWritePGSeq("b", "e"), processWritePGSeq("c", "e")}, false, []string{"warning:R7"}},
		{"results silence R13", []*ProcessGraphNode{processWritePGNode("s", "startEvent"), processWritePGNode("a", "activityUserTask"), processWritePGNode("yes", "endEvent"), processWritePGNode("no", "endEvent")},
			[]*ProcessGraphEdge{processWritePGSeq("s", "a"), processWritePGWith(processWritePGCond("a", "yes"), "\x00", "Positive"), processWritePGSeq("a", "no")}, false, []string{}},
		{"R13 neither", []*ProcessGraphNode{processWritePGNode("s", "startEvent"), processWritePGNode("a", "activityUserTask"), processWritePGNode("yes", "endEvent"), processWritePGNode("no", "endEvent")},
			[]*ProcessGraphEdge{processWritePGSeq("s", "a"), processWritePGCond("a", "yes"), processWritePGSeq("a", "no")}, false, []string{"warning:R13"}},
		{"R19", []*ProcessGraphNode{processWritePGNode("s", "startEvent"), processWritePGNode("a", "activityUserTask"), processWritePGNode("yes", "endEvent")},
			[]*ProcessGraphEdge{processWritePGSeq("s", "a"), processWritePGWith(processWritePGSeq("a", "yes"), "\x00", "Positive")}, true, []string{"error:R19"}},
		{"R20", []*ProcessGraphNode{processWritePGNode("s", "startEvent"), processWritePGNode("a", "activityUserTask"), processWritePGNode("yes", "endEvent"), processWritePGNode("no", "endEvent")},
			[]*ProcessGraphEdge{processWritePGSeq("s", "a"), processWritePGWith(processWritePGCond("a", "yes"), "1 > 0", "Positive"), processWritePGSeq("a", "no")}, true, []string{"error:R20"}},
		{"R13 blank", []*ProcessGraphNode{processWritePGNode("s", "startEvent"), processWritePGNode("a", "activityUserTask"), processWritePGNode("yes", "endEvent"), processWritePGNode("no", "endEvent")},
			[]*ProcessGraphEdge{processWritePGSeq("s", "a"), processWritePGWith(processWritePGCond("a", "yes"), "  "), processWritePGSeq("a", "no")}, true, []string{"error:R13"}},
		{"R14 no conditional", []*ProcessGraphNode{processWritePGNode("s", "startEvent"), processWritePGNode("a", "activityUserTask"), processWritePGNode("b", "activityUserTask"), processWritePGNode("c", "activityUserTask"), processWritePGNode("e", "endEvent")},
			[]*ProcessGraphEdge{processWritePGSeq("s", "a"), processWritePGDef("a", "b"), processWritePGSeq("a", "c"), processWritePGSeq("b", "e"), processWritePGSeq("c", "e")}, true, []string{"error:R14"}},
		{"R15 orphan", []*ProcessGraphNode{processWritePGNode("s", "startEvent"), processWritePGNode("r", "readDataUserTask"), processWritePGNode("e", "endEvent"), processWritePGNode("orphan", "activityUserTask")},
			[]*ProcessGraphEdge{processWritePGSeq("s", "r"), processWritePGSeq("r", "e")}, true, []string{"error:R15"}},
		{"R7 no default", []*ProcessGraphNode{processWritePGNode("s", "startEvent"), processWritePGNode("g", "exclusiveGateway"), processWritePGNode("a1", "activityUserTask"), processWritePGNode("a2", "activityUserTask"), processWritePGNode("e", "endEvent")},
			[]*ProcessGraphEdge{processWritePGSeq("s", "g"), processWritePGWith(processWritePGCond("g", "a1"), "x"), processWritePGWith(processWritePGCond("g", "a2"), "y"), processWritePGSeq("a1", "e"), processWritePGSeq("a2", "e")}, false, []string{"warning:R7"}},
		{"R12", []*ProcessGraphNode{processWritePGNode("s", "startEvent"), processWritePGNode("a", "activityUserTask"), processWritePGNode("e1", "endEvent"), processWritePGNode("e2", "endEvent")},
			[]*ProcessGraphEdge{processWritePGSeq("s", "a"), processWritePGSeq("a", "e1"), processWritePGSeq("a", "e2")}, false, []string{"warning:R12"}},
		{"R17", []*ProcessGraphNode{processWritePGNode("s", "startEvent"), processWritePGNode("add", "AddDataUserTask"), processWritePGNode("a", "activityUserTask"), processWritePGNode("e", "endEvent")},
			[]*ProcessGraphEdge{processWritePGSeq("s", "add"), processWritePGSeq("add", "a"), processWritePGSeq("a", "e")}, false, []string{"warning:R17"}},
		{"no R17 into read data", []*ProcessGraphNode{processWritePGNode("s", "startEvent"), processWritePGNode("add", "addDataUserTask"), processWritePGNode("r", "readData"), processWritePGNode("e", "endEvent")},
			[]*ProcessGraphEdge{processWritePGSeq("s", "add"), processWritePGSeq("add", "r"), processWritePGSeq("r", "e")}, false, []string{}},
		{"unknown", []*ProcessGraphNode{processWritePGNode("s", "startEvent"), processWritePGNode("x", "totallyBogusType"), processWritePGNode("e", "endEvent")},
			[]*ProcessGraphEdge{processWritePGSeq("s", "x"), processWritePGSeq("x", "e")}, true, []string{"error:UNKNOWN"}},
		{"dup", []*ProcessGraphNode{processWritePGNode("s", "startEvent"), processWritePGNode("a", "activityUserTask"), processWritePGNode("a", "readDataUserTask"), processWritePGNode("e", "endEvent")},
			[]*ProcessGraphEdge{processWritePGSeq("s", "a"), processWritePGSeq("a", "e")}, true, []string{"error:DUP"}},
		{"R18 two plain", []*ProcessGraphNode{processWritePGNode("s", "startEvent"), processWritePGNode("t", "userTask"), processWritePGNode("a", "endEvent"), processWritePGNode("b", "endEvent"), processWritePGNode("c", "endEvent")},
			[]*ProcessGraphEdge{processWritePGSeq("s", "t"), processWritePGSeq("t", "a"), processWritePGSeq("t", "b"), processWritePGCond("t", "c")}, true, []string{"warning:R12", "error:R18", "warning:R13"}},
		{"unnamed", []*ProcessGraphNode{processWritePGNode("s", "startEvent"), processWritePGNode("\x00", "userTask"), processWritePGNode("e", "endEvent")},
			[]*ProcessGraphEdge{processWritePGSeq("s", "e")}, true, []string{"error:UNNAMED", "error:R15"}},
		{"blank endpoint", []*ProcessGraphNode{processWritePGNode("s", "startEvent"), processWritePGNode("e", "endEvent")},
			[]*ProcessGraphEdge{processWritePGSeq("s", "e"), processWritePGSeq("s", "\x00")}, true, []string{"error:R15"}},
		{"both endpoints blank", []*ProcessGraphNode{processWritePGNode("s", "startEvent"), processWritePGNode("e", "endEvent")},
			[]*ProcessGraphEdge{processWritePGSeq("s", "e"), processWritePGSeq("\x00", "\x00")}, true, []string{"error:R15"}},
		{"null entries", []*ProcessGraphNode{processWritePGNode("s", "startEvent"), nil, processWritePGNode("e", "endEvent")},
			[]*ProcessGraphEdge{processWritePGSeq("s", "e"), nil}, false, []string{}},
		{"R8 conditional split on an activity", []*ProcessGraphNode{processWritePGNode("s", "startEvent"), processWritePGNode("a", "activityUserTask"), processWritePGNode("b", "activityUserTask"),
			processWritePGNode("c", "activityUserTask"), processWritePGNode("join", "parallelGateway"), processWritePGNode("e", "endEvent")},
			[]*ProcessGraphEdge{processWritePGSeq("s", "a"), processWritePGWith(processWritePGCond("a", "b"), "1 > 0"), processWritePGWith(processWritePGCond("a", "c"), "2 > 0"), processWritePGSeq("b", "join"), processWritePGSeq("c", "join"), processWritePGSeq("join", "e")},
			false, []string{"warning:R8"}},
		{"no R8 plain split", []*ProcessGraphNode{processWritePGNode("s", "startEvent"), processWritePGNode("a", "activityUserTask"), processWritePGNode("b", "activityUserTask"),
			processWritePGNode("c", "activityUserTask"), processWritePGNode("join", "parallelGateway"), processWritePGNode("e", "endEvent")},
			[]*ProcessGraphEdge{processWritePGSeq("s", "a"), processWritePGSeq("a", "b"), processWritePGSeq("a", "c"), processWritePGSeq("b", "join"), processWritePGSeq("c", "join"), processWritePGSeq("join", "e")},
			false, []string{"warning:R12"}},
		{"R8 direct default into the join", []*ProcessGraphNode{processWritePGNode("s", "startEvent"), processWritePGNode("x", "exclusiveGateway"), processWritePGNode("a", "activityUserTask"),
			processWritePGNode("and", "parallelGateway"), processWritePGNode("e", "endEvent")},
			[]*ProcessGraphEdge{processWritePGSeq("s", "x"), processWritePGDef("x", "and"), processWritePGWith(processWritePGCond("x", "a"), "1 > 0"), processWritePGSeq("a", "and"), processWritePGSeq("and", "e")},
			false, []string{"warning:R8"}},
		{"self loop", []*ProcessGraphNode{processWritePGNode("s", "startEvent"), processWritePGNode("a", "activityUserTask"), processWritePGNode("e", "endEvent")},
			[]*ProcessGraphEdge{processWritePGSeq("s", "a"), processWritePGSeq("a", "a"), processWritePGSeq("a", "e")}, true, []string{"warning:R12", "error:R15"}},
	}
	for _, item := range cases {
		hasErrors, findings := ValidateProcessGraph(item.nodes, item.edges)
		if got := strings.Join(processWritePGRules(findings), ","); got != strings.Join(item.want, ",") || hasErrors != item.hasErrors {
			t.Errorf("%s: hasErrors=%v findings=%s", item.label, hasErrors, got)
		}
	}
}

func TestValidateProcessGraphMessagesAndEndpoints(t *testing.T) {
	_, findings := ValidateProcessGraph([]*ProcessGraphNode{processWritePGNode("s", "startEvent"), processWritePGNode("e", "endEvent")},
		[]*ProcessGraphEdge{processWritePGSeq("s", "e"), processWritePGSeq("\x00", "\x00")})
	if len(findings) != 1 || findings[0].Message != "Flow references a missing node (source '(missing source)', target '(missing target)')." ||
		findings[0].NodeName != nil || *findings[0].Source != "(missing source)" || *findings[0].Target != "(missing target)" {
		t.Fatalf("%+v", findings)
	}
	_, findings = ValidateProcessGraph([]*ProcessGraphNode{processWritePGNode("s", "startEvent"), processWritePGNode("t", "userTask"), processWritePGNode("a", "endEvent"), processWritePGNode("b", "endEvent"), processWritePGNode("c", "endEvent")},
		[]*ProcessGraphEdge{processWritePGSeq("s", "t"), processWritePGWith(processWritePGCond("t", "a"), "x"), processWritePGDef("t", "b"), processWritePGSeq("t", "c")})
	if !processWritePGHas(findings, "error", "R18", "t") || processWritePGHas(findings, "warning", "R12", "") ||
		!strings.Contains(findings[0].Message, "carrying 2 flows that have none ('t' -> 'b', 't' -> 'c')") || findings[0].Source != nil {
		t.Fatalf("%+v", findings)
	}
	_, findings = ValidateProcessGraph([]*ProcessGraphNode{processWritePGNode("s", "startEvent"), processWritePGNode("g", "exclusiveGateway"), processWritePGNode("a1", "activityUserTask"), processWritePGNode("a2", "activityUserTask"), processWritePGNode("e", "endEvent")},
		[]*ProcessGraphEdge{processWritePGSeq("s", "g"), processWritePGWith(processWritePGCond("g", "a1"), "x"), processWritePGWith(processWritePGCond("g", "a2"), "y"), processWritePGSeq("a1", "e"), processWritePGSeq("a2", "e")})
	want := `Diverging gateway 'g' has no default flow: if no condition matches at run time the instance stops there and the process log reads "None of the conditions were met after the element 'g'". Add a default flow, or confirm the conditions cover every case.`
	if len(findings) != 1 || findings[0].Message != want || *findings[0].NodeName != "g" {
		t.Fatalf("%+v", findings)
	}
}
