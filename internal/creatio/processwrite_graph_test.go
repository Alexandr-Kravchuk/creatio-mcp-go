package creatio

import (
	"strings"
	"testing"
)

// Cases ported from clio's ProcessGraphValidatorTests.

func pgNode(name, typ string) *ProcessGraphNode {
	node := &ProcessGraphNode{Type: &typ}
	if name != "\x00" {
		node.Name = &name
	}
	return node
}

func pgEdge(source, target string, kind ProcessGraphFlowKind) *ProcessGraphEdge {
	edge := &ProcessGraphEdge{FlowKind: kind}
	if source != "\x00" {
		edge.Source = &source
	}
	if target != "\x00" {
		edge.Target = &target
	}
	return edge
}

func pgSeq(source, target string) *ProcessGraphEdge {
	return pgEdge(source, target, ProcessGraphSequence)
}
func pgCond(source, target string) *ProcessGraphEdge {
	return pgEdge(source, target, ProcessGraphConditional)
}
func pgDef(source, target string) *ProcessGraphEdge {
	return pgEdge(source, target, ProcessGraphDefault)
}
func pgWith(edge *ProcessGraphEdge, condition string, results ...string) *ProcessGraphEdge {
	edge.Condition = &condition
	if condition == "\x00" {
		edge.Condition = nil
	}
	if results != nil {
		edge.Results = results
	}
	return edge
}

func pgRules(findings []ProcessGraphFinding) []string {
	rules := []string{}
	for _, finding := range findings {
		rules = append(rules, finding.Severity+":"+finding.RuleID)
	}
	return rules
}

func pgHas(findings []ProcessGraphFinding, severity, rule string, node string) bool {
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
		{"canonical", []*ProcessGraphNode{pgNode("s", "startEvent"), pgNode("r", "readDataUserTask"), pgNode("e", "endEvent")},
			[]*ProcessGraphEdge{pgSeq("s", "r"), pgSeq("r", "e")}, false, []string{}},
		{"build tokens", []*ProcessGraphNode{pgNode("s", "startEvent"), pgNode("x", "changeAccessRights"), pgNode("y", "callActivity"), pgNode("z", "preconfiguredpage"), pgNode("e", "endEvent")},
			[]*ProcessGraphEdge{pgSeq("s", "x"), pgSeq("x", "y"), pgSeq("y", "z"), pgSeq("z", "e")}, false, []string{}},
		{"R1 incoming start", []*ProcessGraphNode{pgNode("s", "startEvent"), pgNode("a", "activityUserTask"), pgNode("e", "endEvent")},
			[]*ProcessGraphEdge{pgSeq("s", "a"), pgSeq("a", "e"), pgSeq("a", "s")}, true, []string{"error:R1", "warning:R12"}},
		{"R2 outgoing end", []*ProcessGraphNode{pgNode("s", "startEvent"), pgNode("r", "readDataUserTask"), pgNode("e", "endEvent")},
			[]*ProcessGraphEdge{pgSeq("s", "r"), pgSeq("r", "e"), pgSeq("e", "r")}, true, []string{"error:R2"}},
		{"R15 missing node", []*ProcessGraphNode{pgNode("s", "startEvent"), pgNode("r", "readDataUserTask"), pgNode("e", "endEvent")},
			[]*ProcessGraphEdge{pgSeq("s", "r"), pgSeq("r", "e"), pgSeq("r", "ghost")}, true, []string{"error:R15"}},
		{"R3 none", []*ProcessGraphNode{pgNode("a", "activityUserTask"), pgNode("e", "endEvent")},
			[]*ProcessGraphEdge{pgSeq("a", "e")}, true, []string{"error:R3"}},
		{"signal starts", []*ProcessGraphNode{pgNode("added", "signalStart"), pgNode("changed", "signalStart"), pgNode("r", "readDataUserTask"), pgNode("e", "endEvent")},
			[]*ProcessGraphEdge{pgSeq("added", "r"), pgSeq("changed", "r"), pgSeq("r", "e")}, false, []string{}},
		{"R3 two simple", []*ProcessGraphNode{pgNode("s1", "startEvent"), pgNode("s2", "startEvent"), pgNode("r", "readDataUserTask"), pgNode("e", "endEvent")},
			[]*ProcessGraphEdge{pgSeq("s1", "r"), pgSeq("s2", "r"), pgSeq("r", "e")}, true, []string{"error:R3"}},
		{"R10", []*ProcessGraphNode{pgNode("s", "startEvent"), pgNode("g", "eventBasedGateway"), pgNode("a", "activityUserTask"), pgNode("e", "endEvent")},
			[]*ProcessGraphEdge{pgSeq("s", "g"), pgSeq("g", "a"), pgSeq("a", "e")}, true, []string{"error:R10"}},
		{"R11", []*ProcessGraphNode{pgNode("s", "startEvent"), pgNode("g", "parallelGateway"), pgNode("a", "activityUserTask"), pgNode("e", "endEvent")},
			[]*ProcessGraphEdge{pgSeq("s", "g"), pgCond("g", "a"), pgSeq("a", "e")}, true, []string{"error:R11", "warning:R13"}},
		{"R13 from start", []*ProcessGraphNode{pgNode("s", "startEvent"), pgNode("a", "activityUserTask"), pgNode("e", "endEvent")},
			[]*ProcessGraphEdge{pgCond("s", "a"), pgSeq("a", "e")}, false, []string{"warning:R13", "warning:R13"}},
		{"condition on plain and default", []*ProcessGraphNode{pgNode("s", "startEvent"), pgNode("a", "activityUserTask"), pgNode("g", "exclusiveGateway"),
			pgNode("b", "activityUserTask"), pgNode("c", "activityUserTask"), pgNode("e", "endEvent")},
			[]*ProcessGraphEdge{pgSeq("s", "a"), pgWith(pgSeq("a", "g"), "[#Amount#] > 100"), pgWith(pgCond("g", "b"), "[#Amount#] > 100"),
				pgWith(pgDef("g", "c"), "[#Amount#] <= 100"), pgSeq("b", "e"), pgSeq("c", "e")}, false, []string{}},
		{"R7 beside a default", []*ProcessGraphNode{pgNode("s", "startEvent"), pgNode("g", "exclusiveGateway"), pgNode("a", "activityUserTask"),
			pgNode("b", "activityUserTask"), pgNode("c", "activityUserTask"), pgNode("e", "endEvent")},
			[]*ProcessGraphEdge{pgSeq("s", "g"), pgWith(pgCond("g", "a"), "[#Amount#] > 100"), pgDef("g", "b"), pgSeq("g", "c"), pgSeq("a", "e"), pgSeq("b", "e"), pgSeq("c", "e")},
			true, []string{"warning:R7", "error:R18"}},
		{"two defaults", []*ProcessGraphNode{pgNode("s", "startEvent"), pgNode("g", "exclusiveGateway"), pgNode("a", "activityUserTask"),
			pgNode("b", "activityUserTask"), pgNode("c", "activityUserTask"), pgNode("e", "endEvent")},
			[]*ProcessGraphEdge{pgSeq("s", "g"), pgWith(pgCond("g", "a"), "[#Amount#] > 100"), pgDef("g", "b"), pgDef("g", "c"), pgSeq("a", "e"), pgSeq("b", "e"), pgSeq("c", "e")},
			true, []string{"error:R14", "error:R18"}},
		{"R7 alone default plain to gateway", []*ProcessGraphNode{pgNode("s", "startEvent"), pgNode("g", "exclusiveGateway"), pgNode("h", "exclusiveGateway"),
			pgNode("b", "activityUserTask"), pgNode("c", "activityUserTask"), pgNode("e", "endEvent")},
			[]*ProcessGraphEdge{pgSeq("s", "g"), pgDef("g", "b"), pgSeq("g", "h"), pgSeq("h", "c"), pgSeq("b", "e"), pgSeq("c", "e")}, false, []string{"warning:R7"}},
		{"results silence R13", []*ProcessGraphNode{pgNode("s", "startEvent"), pgNode("a", "activityUserTask"), pgNode("yes", "endEvent"), pgNode("no", "endEvent")},
			[]*ProcessGraphEdge{pgSeq("s", "a"), pgWith(pgCond("a", "yes"), "\x00", "Positive"), pgSeq("a", "no")}, false, []string{}},
		{"R13 neither", []*ProcessGraphNode{pgNode("s", "startEvent"), pgNode("a", "activityUserTask"), pgNode("yes", "endEvent"), pgNode("no", "endEvent")},
			[]*ProcessGraphEdge{pgSeq("s", "a"), pgCond("a", "yes"), pgSeq("a", "no")}, false, []string{"warning:R13"}},
		{"R19", []*ProcessGraphNode{pgNode("s", "startEvent"), pgNode("a", "activityUserTask"), pgNode("yes", "endEvent")},
			[]*ProcessGraphEdge{pgSeq("s", "a"), pgWith(pgSeq("a", "yes"), "\x00", "Positive")}, true, []string{"error:R19"}},
		{"R20", []*ProcessGraphNode{pgNode("s", "startEvent"), pgNode("a", "activityUserTask"), pgNode("yes", "endEvent"), pgNode("no", "endEvent")},
			[]*ProcessGraphEdge{pgSeq("s", "a"), pgWith(pgCond("a", "yes"), "1 > 0", "Positive"), pgSeq("a", "no")}, true, []string{"error:R20"}},
		{"R13 blank", []*ProcessGraphNode{pgNode("s", "startEvent"), pgNode("a", "activityUserTask"), pgNode("yes", "endEvent"), pgNode("no", "endEvent")},
			[]*ProcessGraphEdge{pgSeq("s", "a"), pgWith(pgCond("a", "yes"), "  "), pgSeq("a", "no")}, true, []string{"error:R13"}},
		{"R14 no conditional", []*ProcessGraphNode{pgNode("s", "startEvent"), pgNode("a", "activityUserTask"), pgNode("b", "activityUserTask"), pgNode("c", "activityUserTask"), pgNode("e", "endEvent")},
			[]*ProcessGraphEdge{pgSeq("s", "a"), pgDef("a", "b"), pgSeq("a", "c"), pgSeq("b", "e"), pgSeq("c", "e")}, true, []string{"error:R14"}},
		{"R15 orphan", []*ProcessGraphNode{pgNode("s", "startEvent"), pgNode("r", "readDataUserTask"), pgNode("e", "endEvent"), pgNode("orphan", "activityUserTask")},
			[]*ProcessGraphEdge{pgSeq("s", "r"), pgSeq("r", "e")}, true, []string{"error:R15"}},
		{"R7 no default", []*ProcessGraphNode{pgNode("s", "startEvent"), pgNode("g", "exclusiveGateway"), pgNode("a1", "activityUserTask"), pgNode("a2", "activityUserTask"), pgNode("e", "endEvent")},
			[]*ProcessGraphEdge{pgSeq("s", "g"), pgWith(pgCond("g", "a1"), "x"), pgWith(pgCond("g", "a2"), "y"), pgSeq("a1", "e"), pgSeq("a2", "e")}, false, []string{"warning:R7"}},
		{"R12", []*ProcessGraphNode{pgNode("s", "startEvent"), pgNode("a", "activityUserTask"), pgNode("e1", "endEvent"), pgNode("e2", "endEvent")},
			[]*ProcessGraphEdge{pgSeq("s", "a"), pgSeq("a", "e1"), pgSeq("a", "e2")}, false, []string{"warning:R12"}},
		{"R17", []*ProcessGraphNode{pgNode("s", "startEvent"), pgNode("add", "AddDataUserTask"), pgNode("a", "activityUserTask"), pgNode("e", "endEvent")},
			[]*ProcessGraphEdge{pgSeq("s", "add"), pgSeq("add", "a"), pgSeq("a", "e")}, false, []string{"warning:R17"}},
		{"no R17 into read data", []*ProcessGraphNode{pgNode("s", "startEvent"), pgNode("add", "addDataUserTask"), pgNode("r", "readData"), pgNode("e", "endEvent")},
			[]*ProcessGraphEdge{pgSeq("s", "add"), pgSeq("add", "r"), pgSeq("r", "e")}, false, []string{}},
		{"unknown", []*ProcessGraphNode{pgNode("s", "startEvent"), pgNode("x", "totallyBogusType"), pgNode("e", "endEvent")},
			[]*ProcessGraphEdge{pgSeq("s", "x"), pgSeq("x", "e")}, true, []string{"error:UNKNOWN"}},
		{"dup", []*ProcessGraphNode{pgNode("s", "startEvent"), pgNode("a", "activityUserTask"), pgNode("a", "readDataUserTask"), pgNode("e", "endEvent")},
			[]*ProcessGraphEdge{pgSeq("s", "a"), pgSeq("a", "e")}, true, []string{"error:DUP"}},
		{"R18 two plain", []*ProcessGraphNode{pgNode("s", "startEvent"), pgNode("t", "userTask"), pgNode("a", "endEvent"), pgNode("b", "endEvent"), pgNode("c", "endEvent")},
			[]*ProcessGraphEdge{pgSeq("s", "t"), pgSeq("t", "a"), pgSeq("t", "b"), pgCond("t", "c")}, true, []string{"warning:R12", "error:R18", "warning:R13"}},
		{"unnamed", []*ProcessGraphNode{pgNode("s", "startEvent"), pgNode("\x00", "userTask"), pgNode("e", "endEvent")},
			[]*ProcessGraphEdge{pgSeq("s", "e")}, true, []string{"error:UNNAMED", "error:R15"}},
		{"blank endpoint", []*ProcessGraphNode{pgNode("s", "startEvent"), pgNode("e", "endEvent")},
			[]*ProcessGraphEdge{pgSeq("s", "e"), pgSeq("s", "\x00")}, true, []string{"error:R15"}},
		{"both endpoints blank", []*ProcessGraphNode{pgNode("s", "startEvent"), pgNode("e", "endEvent")},
			[]*ProcessGraphEdge{pgSeq("s", "e"), pgSeq("\x00", "\x00")}, true, []string{"error:R15"}},
		{"null entries", []*ProcessGraphNode{pgNode("s", "startEvent"), nil, pgNode("e", "endEvent")},
			[]*ProcessGraphEdge{pgSeq("s", "e"), nil}, false, []string{}},
		{"R8 conditional split on an activity", []*ProcessGraphNode{pgNode("s", "startEvent"), pgNode("a", "activityUserTask"), pgNode("b", "activityUserTask"),
			pgNode("c", "activityUserTask"), pgNode("join", "parallelGateway"), pgNode("e", "endEvent")},
			[]*ProcessGraphEdge{pgSeq("s", "a"), pgWith(pgCond("a", "b"), "1 > 0"), pgWith(pgCond("a", "c"), "2 > 0"), pgSeq("b", "join"), pgSeq("c", "join"), pgSeq("join", "e")},
			false, []string{"warning:R8"}},
		{"no R8 plain split", []*ProcessGraphNode{pgNode("s", "startEvent"), pgNode("a", "activityUserTask"), pgNode("b", "activityUserTask"),
			pgNode("c", "activityUserTask"), pgNode("join", "parallelGateway"), pgNode("e", "endEvent")},
			[]*ProcessGraphEdge{pgSeq("s", "a"), pgSeq("a", "b"), pgSeq("a", "c"), pgSeq("b", "join"), pgSeq("c", "join"), pgSeq("join", "e")},
			false, []string{"warning:R12"}},
		{"R8 direct default into the join", []*ProcessGraphNode{pgNode("s", "startEvent"), pgNode("x", "exclusiveGateway"), pgNode("a", "activityUserTask"),
			pgNode("and", "parallelGateway"), pgNode("e", "endEvent")},
			[]*ProcessGraphEdge{pgSeq("s", "x"), pgDef("x", "and"), pgWith(pgCond("x", "a"), "1 > 0"), pgSeq("a", "and"), pgSeq("and", "e")},
			false, []string{"warning:R8"}},
		{"self loop", []*ProcessGraphNode{pgNode("s", "startEvent"), pgNode("a", "activityUserTask"), pgNode("e", "endEvent")},
			[]*ProcessGraphEdge{pgSeq("s", "a"), pgSeq("a", "a"), pgSeq("a", "e")}, true, []string{"warning:R12", "error:R15"}},
	}
	for _, item := range cases {
		hasErrors, findings := ValidateProcessGraph(item.nodes, item.edges)
		if got := strings.Join(pgRules(findings), ","); got != strings.Join(item.want, ",") || hasErrors != item.hasErrors {
			t.Errorf("%s: hasErrors=%v findings=%s", item.label, hasErrors, got)
		}
	}
}

func TestValidateProcessGraphMessagesAndEndpoints(t *testing.T) {
	_, findings := ValidateProcessGraph([]*ProcessGraphNode{pgNode("s", "startEvent"), pgNode("e", "endEvent")},
		[]*ProcessGraphEdge{pgSeq("s", "e"), pgSeq("\x00", "\x00")})
	if len(findings) != 1 || findings[0].Message != "Flow references a missing node (source '(missing source)', target '(missing target)')." ||
		findings[0].NodeName != nil || *findings[0].Source != "(missing source)" || *findings[0].Target != "(missing target)" {
		t.Fatalf("%+v", findings)
	}
	_, findings = ValidateProcessGraph([]*ProcessGraphNode{pgNode("s", "startEvent"), pgNode("t", "userTask"), pgNode("a", "endEvent"), pgNode("b", "endEvent"), pgNode("c", "endEvent")},
		[]*ProcessGraphEdge{pgSeq("s", "t"), pgWith(pgCond("t", "a"), "x"), pgDef("t", "b"), pgSeq("t", "c")})
	if !pgHas(findings, "error", "R18", "t") || pgHas(findings, "warning", "R12", "") ||
		!strings.Contains(findings[0].Message, "carrying 2 flows that have none ('t' -> 'b', 't' -> 'c')") || findings[0].Source != nil {
		t.Fatalf("%+v", findings)
	}
	_, findings = ValidateProcessGraph([]*ProcessGraphNode{pgNode("s", "startEvent"), pgNode("g", "exclusiveGateway"), pgNode("a1", "activityUserTask"), pgNode("a2", "activityUserTask"), pgNode("e", "endEvent")},
		[]*ProcessGraphEdge{pgSeq("s", "g"), pgWith(pgCond("g", "a1"), "x"), pgWith(pgCond("g", "a2"), "y"), pgSeq("a1", "e"), pgSeq("a2", "e")})
	want := `Diverging gateway 'g' has no default flow: if no condition matches at run time the instance stops there and the process log reads "None of the conditions were met after the element 'g'". Add a default flow, or confirm the conditions cover every case.`
	if len(findings) != 1 || findings[0].Message != want || *findings[0].NodeName != "g" {
		t.Fatalf("%+v", findings)
	}
}
