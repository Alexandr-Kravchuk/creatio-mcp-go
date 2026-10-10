package creatio

// validate-process-graph: clio's ProcessGraphValidator, the BPMN connection rules a planned graph is checked
// against before it is built. It never fails on malformed input: every problem is a finding. The order of
// the findings is clio's: naming, duplicates, unknown types, missing endpoints, start count, the per-element
// rules, then the conditional-flow, self-loop, parallel-join and reachability passes.

import (
	"context"
	"fmt"
	"strings"
)

// ProcessGraphNode is one planned element: its name (handle) and its designer data-id or build token.
type ProcessGraphNode struct {
	Name *string
	Type *string
}

// ProcessGraphFlowKind is ProcessFlowKind.
type ProcessGraphFlowKind int

const (
	ProcessGraphSequence ProcessGraphFlowKind = iota
	ProcessGraphConditional
	ProcessGraphDefault
)

// ProcessGraphEdge is one planned flow. Results is nil when the caller sent none.
type ProcessGraphEdge struct {
	Source    *string
	Target    *string
	FlowKind  ProcessGraphFlowKind
	Condition *string
	Results   []string
}

// ProcessGraphFinding is clio's ValidateProcessGraphFinding.
type ProcessGraphFinding struct {
	Severity string  `json:"severity"`
	RuleID   string  `json:"rule-id"`
	Message  string  `json:"message"`
	NodeName *string `json:"node-name,omitempty"`
	Source   *string `json:"source,omitempty"`
	Target   *string `json:"target,omitempty"`
}

// ProcessGraphResponse is clio's ValidateProcessGraphResponse.
type ProcessGraphResponse struct {
	Success   bool                  `json:"success"`
	Error     string                `json:"error,omitempty"`
	HasErrors *bool                 `json:"has-errors,omitempty"`
	Findings  []ProcessGraphFinding `json:"findings,omitzero"`
}

// ProcessGraphPackageGate is validate-process-graph's [RequiresPackage(CrtProcessBuilder)] check (presence
// only, then convergence): the refusal text, or "" when the package qualifies.
func (c *Client) ProcessGraphPackageGate(ctx context.Context) (string, error) {
	return c.processWriteGate(ctx, "")
}

// processGraphEventType is the slice of ManagerMap.EventType the rules distinguish.
type processGraphEventType int

const (
	processGraphUnknown processGraphEventType = iota
	processGraphStartEvent
	processGraphStartSignal
	processGraphStartTimer
	processGraphStartMessage
	processGraphEndEvent
	processGraphExclusive
	processGraphParallel
	processGraphInclusive
	processGraphEventBased
	processGraphFormulaTask
	processGraphScriptTask
	processGraphWebService
	processGraphSubProcess
	processGraphEventSubProcess
	processGraphUserTask
	processGraphCatchSignal
	processGraphThrowSignal
)

type processGraphRole int

const (
	processGraphRoleOther processGraphRole = iota
	processGraphRoleStart
	processGraphRoleEnd
	processGraphRoleActivity
	processGraphRoleGateway
	processGraphRoleIntermediate
)

// processGraphResolveType is ManagerMap.ResolveDataId: case-insensitive, both the canvas data-ids and the
// build/describe tokens.
func processGraphResolveType(dataID *string) processGraphEventType {
	if dataID == nil || strings.TrimSpace(*dataID) == "" {
		return processGraphUnknown
	}
	id := strings.ToLower(strings.TrimSpace(*dataID))
	switch id {
	case "startevent":
		return processGraphStartEvent
	case "starteventsignal", "signalstart":
		return processGraphStartSignal
	case "starteventtimer":
		return processGraphStartTimer
	case "starteventmessage":
		return processGraphStartMessage
	case "endevent":
		return processGraphEndEvent
	case "exclusivegateway":
		return processGraphExclusive
	case "parallelgateway":
		return processGraphParallel
	case "inclusivegateway":
		return processGraphInclusive
	case "eventbasedgateway":
		return processGraphEventBased
	case "formulatask":
		return processGraphFormulaTask
	case "scripttask":
		return processGraphScriptTask
	case "webservice":
		return processGraphWebService
	case "callactivity", "subprocess":
		return processGraphSubProcess
	case "eventsubprocessexpanded":
		return processGraphEventSubProcess
	case "usertask", "performtask", "sendemail", "approval", "openeditpage", "readdata", "changedata", "adddata",
		"changeaccessrights", "preconfiguredpage":
		return processGraphUserTask
	}
	switch {
	case strings.HasPrefix(id, "intermediatecatchevent"):
		return processGraphCatchSignal
	case strings.HasPrefix(id, "intermediatethrowevent"):
		return processGraphThrowSignal
	case strings.HasSuffix(id, "usertask"):
		return processGraphUserTask
	}
	return processGraphUnknown
}

// processGraphResolveRole is ManagerMap.ResolveRole.
func processGraphResolveRole(eventType processGraphEventType) processGraphRole {
	switch eventType {
	case processGraphStartEvent, processGraphStartMessage, processGraphStartSignal, processGraphStartTimer:
		return processGraphRoleStart
	case processGraphEndEvent:
		return processGraphRoleEnd
	case processGraphParallel, processGraphInclusive, processGraphExclusive, processGraphEventBased:
		return processGraphRoleGateway
	case processGraphCatchSignal, processGraphThrowSignal:
		return processGraphRoleIntermediate
	case processGraphUserTask, processGraphScriptTask, processGraphFormulaTask, processGraphWebService,
		processGraphSubProcess, processGraphEventSubProcess:
		return processGraphRoleActivity
	}
	return processGraphRoleOther
}

// processGraphNode and processGraphEdge are the named working copies the rules run over. An edge's key is
// its record identity: equal fields are one edge, except that a results list compares by reference.
type processGraphNode struct {
	name string
	typ  *string
}

type processGraphEdge struct {
	source, target string
	kind           ProcessGraphFlowKind
	condition      *string
	results        []string
	key            string
}

type processGraphValidator struct {
	findings []ProcessGraphFinding
}

func (v *processGraphValidator) add(severity, rule, message string, node *string, edge *processGraphEdge) {
	finding := ProcessGraphFinding{Severity: severity, RuleID: rule, Message: message, NodeName: node}
	if edge != nil {
		source, target := edge.source, edge.target
		finding.Source, finding.Target = &source, &target
	}
	v.findings = append(v.findings, finding)
}

func processGraphText(value string) *string { return &value }

func processGraphTypeOf(node processGraphNode) processGraphEventType {
	return processGraphResolveType(node.typ)
}

func processGraphRoleOf(node processGraphNode) processGraphRole {
	return processGraphResolveRole(processGraphTypeOf(node))
}

// ValidateProcessGraph is ProcessGraphValidator.Validate.
func ValidateProcessGraph(nodes []*ProcessGraphNode, edges []*ProcessGraphEdge) (bool, []ProcessGraphFinding) {
	v := &processGraphValidator{findings: []ProcessGraphFinding{}}
	named, connected := v.nameTheNameless(nodes, edges)

	order := []string{}
	groups := map[string][]processGraphNode{}
	for _, node := range named {
		if _, seen := groups[node.name]; !seen {
			order = append(order, node.name)
		}
		groups[node.name] = append(groups[node.name], node)
	}
	byName := map[string]processGraphNode{}
	for _, name := range order {
		byName[name] = groups[name][0]
		if len(groups[name]) > 1 {
			v.add("error", "DUP", fmt.Sprintf("Duplicate element name '%s'. Element names must be unique within a process.", name), processGraphText(name), nil)
		}
	}
	for _, node := range named {
		if processGraphTypeOf(node) == processGraphUnknown {
			v.add("error", "UNKNOWN", fmt.Sprintf("Element '%s' has an unrecognized type '%s'.", node.name, processGraphDeref(node.typ)), processGraphText(node.name), nil)
		}
	}
	for index := range connected {
		edge := &connected[index]
		_, sourceKnown := byName[edge.source]
		_, targetKnown := byName[edge.target]
		if !sourceKnown || !targetKnown {
			v.add("error", "R15", fmt.Sprintf("Flow references a missing node (source '%s', target '%s').", edge.source, edge.target), nil, edge)
		}
	}
	outgoing, incoming := map[string][]*processGraphEdge{}, map[string][]*processGraphEdge{}
	for _, name := range order {
		outgoing[name], incoming[name] = []*processGraphEdge{}, []*processGraphEdge{}
	}
	for index := range connected {
		edge := &connected[index]
		_, sourceKnown := byName[edge.source]
		_, targetKnown := byName[edge.target]
		if sourceKnown && targetKnown {
			outgoing[edge.source] = append(outgoing[edge.source], edge)
			incoming[edge.target] = append(incoming[edge.target], edge)
		}
	}
	starts := []processGraphNode{}
	for _, node := range named {
		if processGraphRoleOf(node) == processGraphRoleStart {
			starts = append(starts, node)
		}
	}
	v.checkStartCount(starts)
	for _, node := range named {
		role := processGraphRoleOf(node)
		eventType := processGraphTypeOf(node)
		outs, ins := outgoing[node.name], incoming[node.name]
		v.checkStartEndArity(node, role, outs, ins)
		v.checkGatewayAndFlowRules(node, eventType, role, outs, byName)
		v.checkAddDataChaining(node, outs, byName)
	}
	v.checkConditionalFlows(connected, byName)
	for index := range connected {
		edge := &connected[index]
		if edge.source == edge.target {
			v.add("error", "R15", fmt.Sprintf("Flow connects '%s' to itself. To repeat an element, route the flow back through a "+
				"gateway that decides whether to repeat it.", edge.source), processGraphText(edge.source), edge)
		}
	}
	v.checkParallelJoinDeadlock(named, incoming, outgoing)
	v.checkReachability(named, starts, outgoing, incoming)
	hasErrors := false
	for _, finding := range v.findings {
		if finding.Severity == "error" {
			hasErrors = true
		}
	}
	return hasErrors, v.findings
}

// nameTheNameless gives an unnamed element a placeholder (reported as UNNAMED) and a blank flow endpoint a
// name no element plausibly has, so every rule after it sees names only. Null entries are dropped.
func (v *processGraphValidator) nameTheNameless(nodes []*ProcessGraphNode, edges []*ProcessGraphEdge) ([]processGraphNode, []processGraphEdge) {
	named := []processGraphNode{}
	unnamed := 0
	for _, node := range nodes {
		if node == nil {
			continue
		}
		if node.Name != nil && strings.TrimSpace(*node.Name) != "" {
			named = append(named, processGraphNode{name: *node.Name, typ: node.Type})
			continue
		}
		unnamed++
		placeholder := fmt.Sprintf("(unnamed element %d)", unnamed)
		v.add("error", "UNNAMED", fmt.Sprintf("An element has no name (%s). Every element needs a name: flows reference their "+
			"endpoints by name, so an unnamed element cannot be connected to anything.", placeholder), processGraphText(placeholder), nil)
		named = append(named, processGraphNode{name: placeholder, typ: node.Type})
	}
	connected := []processGraphEdge{}
	for index, edge := range edges {
		if edge == nil {
			continue
		}
		source, target := "(missing source)", "(missing target)"
		if edge.Source != nil && strings.TrimSpace(*edge.Source) != "" {
			source = *edge.Source
		}
		if edge.Target != nil && strings.TrimSpace(*edge.Target) != "" {
			target = *edge.Target
		}
		item := processGraphEdge{source: source, target: target, kind: edge.FlowKind, condition: edge.Condition, results: edge.Results}
		condition := "\x01"
		if edge.Condition != nil {
			condition = "\x02" + *edge.Condition
		}
		item.key = fmt.Sprintf("%q|%q|%d|%q", source, target, edge.FlowKind, condition)
		if edge.Results != nil {
			item.key += fmt.Sprintf("|results#%d", index)
		}
		connected = append(connected, item)
	}
	return named, connected
}

func processGraphDeref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// checkStartCount is R3: at least one start, at most one simple start.
func (v *processGraphValidator) checkStartCount(starts []processGraphNode) {
	if len(starts) == 0 {
		v.add("error", "R3", "Process has no start event.", nil, nil)
		return
	}
	simple := 0
	for _, node := range starts {
		if processGraphTypeOf(node) != processGraphStartEvent {
			continue
		}
		simple++
		if simple > 1 {
			v.add("error", "R3", fmt.Sprintf("Process has more than one simple start event ('%s'); only one is allowed — "+
				"signal, timer and message starts may be several.", node.name), processGraphText(node.name), nil)
		}
	}
}

// checkStartEndArity is R1 and R2.
func (v *processGraphValidator) checkStartEndArity(node processGraphNode, role processGraphRole, outs, ins []*processGraphEdge) {
	name := processGraphText(node.name)
	if role == processGraphRoleStart {
		if len(ins) > 0 {
			v.add("error", "R1", fmt.Sprintf("Start event '%s' must not have an incoming flow.", node.name), name, nil)
		}
		if len(outs) != 1 {
			v.add("error", "R1", fmt.Sprintf("Start event '%s' must have exactly one outgoing flow.", node.name), name, nil)
		}
	}
	if role == processGraphRoleEnd {
		if len(outs) > 0 {
			v.add("error", "R2", fmt.Sprintf("End event '%s' must not have an outgoing flow.", node.name), name, nil)
		}
		if len(ins) == 0 {
			v.add("error", "R2", fmt.Sprintf("End event '%s' must have at least one incoming flow.", node.name), name, nil)
		}
	}
}

func processGraphCount(edges []*processGraphEdge, kind ProcessGraphFlowKind) int {
	count := 0
	for _, edge := range edges {
		if edge.kind == kind {
			count++
		}
	}
	return count
}

// checkGatewayAndFlowRules is R11, R10, R14, R7/R9, R12 and R18 for one element.
func (v *processGraphValidator) checkGatewayAndFlowRules(node processGraphNode, eventType processGraphEventType, role processGraphRole,
	outs []*processGraphEdge, byName map[string]processGraphNode) {
	name := processGraphText(node.name)
	if eventType == processGraphParallel || eventType == processGraphEventBased {
		for _, edge := range outs {
			if edge.kind != ProcessGraphSequence {
				v.add("error", "R11", fmt.Sprintf("Gateway '%s' (parallel/event-based) must use plain sequence flows only.", node.name), name, nil)
				break
			}
		}
	}
	if eventType == processGraphEventBased {
		for _, edge := range outs {
			if target, ok := byName[edge.target]; ok && processGraphRoleOf(target) != processGraphRoleIntermediate {
				v.add("error", "R10", fmt.Sprintf("Event-based gateway '%s' outgoing must lead to an intermediate catch event; '%s' is not.", node.name, edge.target), name, edge)
			}
		}
	}
	v.checkDefaultFlowRules(node, eventType, outs, byName)
	if role != processGraphRoleGateway && processGraphCount(outs, ProcessGraphSequence) > 1 {
		v.add("warning", "R12", fmt.Sprintf("Element '%s' has multiple outgoing sequence flows (implicit parallel split) — confirm intent.", node.name), name, nil)
	}
	if processGraphCount(outs, ProcessGraphConditional) == 0 {
		return
	}
	unconditional := []*processGraphEdge{}
	for _, edge := range outs {
		if edge.kind != ProcessGraphConditional {
			unconditional = append(unconditional, edge)
		}
	}
	if len(unconditional) < 2 {
		return
	}
	offenders := make([]string, len(unconditional))
	for index, edge := range unconditional {
		offenders[index] = fmt.Sprintf("'%s' -> '%s'", node.name, edge.target)
	}
	v.add("error", "R18", fmt.Sprintf("Element '%s' branches on a condition while carrying %d flows that have "+
		"none (%s). Only one of those is the fallback — the platform starts the other one as well, beside "+
		"whichever branch the condition chose. Marking one of them 'default' does not settle which: "+
		"the runtime never reads that marker (GetIsDefSequenceFlow matches every non-conditional flow, "+
		"default and plain alike) and drops one by declaration order. Give the extra flow a condition, "+
		"or remove it.", node.name, len(unconditional), strings.Join(offenders, ", ")), name, nil)
}

// checkDefaultFlowRules is R14 (both arity rules) and the or-gateway fallback warnings R7/R9.
func (v *processGraphValidator) checkDefaultFlowRules(node processGraphNode, eventType processGraphEventType, outs []*processGraphEdge,
	byName map[string]processGraphNode) {
	name := processGraphText(node.name)
	defaults := processGraphCount(outs, ProcessGraphDefault)
	hasDefault := defaults > 0
	hasConditional := processGraphCount(outs, ProcessGraphConditional) > 0
	plainToGateway := false
	for _, edge := range outs {
		if target, ok := byName[edge.target]; edge.kind == ProcessGraphSequence && ok && processGraphRoleOf(target) == processGraphRoleGateway {
			plainToGateway = true
		}
	}
	if hasDefault && !hasConditional && len(outs) > 1 && defaults == 1 && !plainToGateway {
		v.add("error", "R14", fmt.Sprintf("Default flow from '%s' requires at least one sibling conditional flow.", node.name), name, nil)
	}
	if defaults > 1 {
		v.add("error", "R14", fmt.Sprintf("Element '%s' has %d default flows; only one branch can be the one "+
			"taken when no condition matched.", node.name, defaults), name, nil)
	}
	if (eventType != processGraphExclusive && eventType != processGraphInclusive) || len(outs) <= 1 {
		return
	}
	rule := "R9"
	if eventType == processGraphExclusive {
		rule = "R7"
	}
	if processGraphCount(outs, ProcessGraphSequence) > 0 {
		if hasDefault {
			v.add("warning", rule, fmt.Sprintf("Diverging gateway '%s' has a plain sequence flow beside a flow already marked "+
				"'default'. The runtime does not prefer the marked one: GetIsDefSequenceFlow matches "+
				"every non-conditional flow and drops exactly ONE of them by declaration order, so "+
				"which branch is the fallback is decided by flow order and nothing on the diagram "+
				"states it. Give this flow a condition, or remove it. Do NOT mark it 'default' as "+
				"well - two defaults on one element are an error.", node.name), name, nil)
		} else {
			v.add("warning", rule, fmt.Sprintf("Diverging gateway '%s' has a plain sequence flow. At run time it is taken as "+
				"the default branch; say so explicitly with kind 'default', or give it a condition, "+
				"so the diagram states which branch is the fallback.", node.name), name, nil)
		}
	} else if !hasDefault {
		v.add("warning", rule, fmt.Sprintf("Diverging gateway '%s' has no default flow: if no condition matches at run time the "+
			"instance stops there and the process log reads \"None of the conditions were met after the "+
			"element '%s'\". Add a default flow, or confirm the conditions cover every case.", node.name, node.name), name, nil)
	}
}

func processGraphNormalizedType(value *string) string {
	return strings.ToLower(strings.TrimSpace(processGraphDeref(value)))
}

// checkAddDataChaining is R17: Add data yields only the new Id, so a following activity needs a Read data.
func (v *processGraphValidator) checkAddDataChaining(node processGraphNode, outs []*processGraphEdge, byName map[string]processGraphNode) {
	if processGraphNormalizedType(node.typ) != "adddatausertask" {
		return
	}
	for _, edge := range outs {
		target, ok := byName[edge.target]
		targetType := processGraphNormalizedType(target.typ)
		if ok && processGraphRoleOf(target) == processGraphRoleActivity && targetType != "readdatausertask" && targetType != "readdata" {
			v.add("warning", "R17", fmt.Sprintf("Add data '%s' outputs only the new Id; chain a Read data before '%s' consumes other fields.",
				node.name, edge.target), processGraphText(edge.target), edge)
		}
	}
}

// checkConditionalFlows is R19/R20 over every flow carrying results, then R13 per conditional flow.
func (v *processGraphValidator) checkConditionalFlows(edges []processGraphEdge, byName map[string]processGraphNode) {
	for index := range edges {
		edge := &edges[index]
		if len(edge.results) == 0 {
			continue
		}
		source := processGraphText(edge.source)
		if edge.kind != ProcessGraphConditional {
			v.add("error", "R19", fmt.Sprintf("Flow '%s' -> '%s' carries 'results' but is not a conditional flow. "+
				"A result selection IS the branch's predicate, so only a conditional flow can hold one - "+
				"the build refuses this outright. Set 'flow-kind' to 'conditional', or drop 'results'.", edge.source, edge.target), source, edge)
			continue
		}
		if edge.condition != nil && strings.TrimSpace(*edge.condition) != "" {
			v.add("error", "R20", fmt.Sprintf("Conditional flow '%s' -> '%s' carries both a condition and "+
				"'results'. A branch decides one way or the other: the platform reads the selection and "+
				"never the expression, so the condition would be stored and never evaluated. The build "+
				"refuses it - keep 'results' and drop the condition, or the other way round.", edge.source, edge.target), source, edge)
		}
	}
	for index := range edges {
		edge := &edges[index]
		if edge.kind != ProcessGraphConditional {
			continue
		}
		source := processGraphText(edge.source)
		if node, ok := byName[edge.source]; ok {
			if role := processGraphRoleOf(node); role != processGraphRoleGateway && role != processGraphRoleActivity {
				v.add("warning", "R13", fmt.Sprintf("Conditional flow leaves '%s', which is neither a gateway nor an activity. "+
					"The designer cannot draw that connection, though four shipped flows have it and run.", edge.source), source, edge)
			}
		}
		if edge.condition != nil && strings.TrimSpace(*edge.condition) == "" {
			v.add("error", "R13", fmt.Sprintf("Conditional flow '%s' -> '%s' has an empty condition. The BUILD "+
				"path refuses it, and reached any other way the platform stores it as the literal "+
				"'true' - a branch that always fires. Give it a condition, or pass 'true' explicitly "+
				"if a branch that always fires is what you mean.", edge.source, edge.target), source, edge)
		} else if edge.condition == nil && len(edge.results) == 0 {
			v.add("warning", "R13", fmt.Sprintf("Conditional flow '%s' -> '%s' carries no condition. That is fine "+
				"for checking a graph's shape, but the BUILD path refuses it - give it a condition "+
				"before you build, or make the flow 'sequence'. Unless the branch is decided by an "+
				"activity RESULT rather than by text, in which case neither fix applies and neither is "+
				"wanted: pass the selection as 'results' on this edge and the warning goes away. 337 "+
				"shipped flows are that shape, and describe-business-process reports them with "+
				"branchesOnActivityResult and results.", edge.source, edge.target), source, edge)
		}
	}
}

// checkParallelJoinDeadlock is R8: a parallel join two of whose incoming branches leave one choosing element
// by different flows waits forever.
func (v *processGraphValidator) checkParallelJoinDeadlock(nodes []processGraphNode, incoming, outgoing map[string][]*processGraphEdge) {
	choosing := []string{}
	seen := map[string]bool{}
	for _, node := range nodes {
		eventType := processGraphTypeOf(node)
		chooses := eventType == processGraphExclusive || eventType == processGraphInclusive || processGraphCount(outgoing[node.name], ProcessGraphConditional) > 0
		if chooses && !seen[node.name] {
			seen[node.name] = true
			choosing = append(choosing, node.name)
		}
	}
	for _, node := range nodes {
		if processGraphTypeOf(node) != processGraphParallel {
			continue
		}
		ins := incoming[node.name]
		if len(ins) < 2 || len(choosing) == 0 {
			continue
		}
		branches := make([]map[string]map[string]bool, len(ins))
		for index, seed := range ins {
			branches[index] = processGraphWalkBySource(seed, incoming)
		}
		for _, split := range choosing {
			if processGraphDiverges(split, branches) {
				v.add("warning", "R8", fmt.Sprintf("Parallel join '%s' waits for every incoming branch, but two of them leave "+
					"'%s' by different flows, and that element takes only one of them. If that is the "+
					"shape you meant, the instance will hang in Running with no error - use an exclusive "+
					"gateway to merge instead.", node.name, split), processGraphText(node.name), nil)
				break
			}
		}
	}
}

// processGraphWalkBySource is one branch's backward walk from its inbound edge, the walked edges grouped by
// their source element.
func processGraphWalkBySource(seed *processGraphEdge, incoming map[string][]*processGraphEdge) map[string]map[string]bool {
	bySource := map[string]map[string]bool{}
	add := func(edge *processGraphEdge) {
		if bySource[edge.source] == nil {
			bySource[edge.source] = map[string]bool{}
		}
		bySource[edge.source][edge.key] = true
	}
	add(seed)
	visited := map[string]bool{seed.source: true}
	queue := []string{seed.source}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for _, edge := range incoming[current] {
			add(edge)
			if !visited[edge.source] {
				visited[edge.source] = true
				queue = append(queue, edge.source)
			}
		}
	}
	return bySource
}

// processGraphDiverges reports two branches that reach the element through disjoint outgoing flows.
func processGraphDiverges(element string, branches []map[string]map[string]bool) bool {
	atElement := []map[string]bool{}
	for _, branch := range branches {
		if walked, ok := branch[element]; ok {
			atElement = append(atElement, walked)
		}
	}
	for left := 0; left < len(atElement); left++ {
		for right := left + 1; right < len(atElement); right++ {
			overlaps := false
			for key := range atElement[left] {
				if atElement[right][key] {
					overlaps = true
					break
				}
			}
			if !overlaps {
				return true
			}
		}
	}
	return false
}

// checkReachability is R15: every element is reachable from a start and can reach an end.
func (v *processGraphValidator) checkReachability(nodes, starts []processGraphNode, outgoing, incoming map[string][]*processGraphEdge) {
	if len(starts) == 0 || len(nodes) == 0 {
		return
	}
	forward := map[string]bool{}
	queue := []string{}
	for _, node := range starts {
		if !forward[node.name] {
			forward[node.name] = true
			queue = append(queue, node.name)
		}
	}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for _, edge := range outgoing[current] {
			if !forward[edge.target] {
				forward[edge.target] = true
				queue = append(queue, edge.target)
			}
		}
	}
	backward := map[string]bool{}
	queue = queue[:0]
	for _, node := range nodes {
		if processGraphRoleOf(node) == processGraphRoleEnd && !backward[node.name] {
			backward[node.name] = true
			queue = append(queue, node.name)
		}
	}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for _, edge := range incoming[current] {
			if !backward[edge.source] {
				backward[edge.source] = true
				queue = append(queue, edge.source)
			}
		}
	}
	for _, node := range nodes {
		role := processGraphRoleOf(node)
		if role != processGraphRoleStart && !forward[node.name] {
			v.add("error", "R15", fmt.Sprintf("Element '%s' is not reachable from the start event.", node.name), processGraphText(node.name), nil)
		} else if role != processGraphRoleEnd && !backward[node.name] {
			v.add("error", "R15", fmt.Sprintf("Element '%s' cannot reach an end event.", node.name), processGraphText(node.name), nil)
		}
	}
}
