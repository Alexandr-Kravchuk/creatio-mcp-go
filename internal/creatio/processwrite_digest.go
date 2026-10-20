package creatio

// The post-write digest of the process-designer write tools, as clio 8.1.0.142 reports it
// (ProcessWriteDigest and ProcessDigest.BuildChanges, ENG-103023): after create-business-process,
// modify-business-process, modify-business-process-as-new-version and set-active-business-process-version, one
// Info line {"summary":{...},"changes":{...}} - the summary describe-business-process mode=summary returns for
// the version the write produced, plus what the write changed - so the caller can confirm a write without
// reading the process back. Every failure here is a warning, never an exit code: the write already landed.

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const (
	// processWriteDigestBudget is ProcessVersionLibReader.BestEffortReadBudget: a version read on the success
	// path of a write gets 3 s, so a stalled process library costs a missing fact rather than ~10 s per read.
	processWriteDigestBudget = 3 * time.Second
	// processWriteMaxChangeEntries is ProcessDigest.MaxChangeEntries.
	processWriteMaxChangeEntries = 25
	processWriteSubProcessKey    = "subProcess"

	processWriteVersionStandingNotRead = "version standing not read; the summary and changes still confirm the write - no re-describe is needed"
	processWriteFactsNotRequested      = "the version facts were not requested for this read, so they were not established"
	processWriteCallerNotReadBack      = "No summary is returned for a process that calls another process: reading it back now would keep its " +
		"sub-process elements on the called processes' current parameters until it is saved again. Read it with " +
		"describe-business-process once the processes it calls are final."
	processWritePossibleCallerNotReadBack = "No summary is returned: the read before the edit failed, so whether this process calls another process is " +
		"unknown, and reading a process that does back now would keep its sub-process elements on the called " +
		"processes' current parameters until it is saved again. Read it with describe-business-process to confirm " +
		"the edit once the processes it calls are final."
)

// processWriteRead is one best-effort read of a process for the digest: raw is the server's DescribeProcess
// result (which the configuration guards read), graph the describe-business-process projection of it with the
// version facts overlaid. err is set when the read failed.
type processWriteRead struct {
	raw   *jnode
	graph *jnode
	err   error
}

// processWriteDigestRead is ProcessWriteDigest.Read: ServerProcessDescriber.Describe with bestEffort, in the
// profile culture (no culture is sent), with the version facts on the 3 s budget when asked for; without them
// the facts carry the "not requested" warning the describer assigns.
func (c *Client) processWriteDigestRead(ctx context.Context, name, uid string, includeFacts bool) processWriteRead {
	raw, err := c.processWriteDescribeRaw(ctx, name, uid)
	if err != nil {
		return processWriteRead{err: err}
	}
	facts := processDescribeFacts{Warning: processWriteFactsNotRequested}
	if includeFacts {
		schemaUID := ""
		if value := processDescribeProperty(raw, "schemaUId"); value != nil && value.kind == jkString {
			schemaUID = value.text
		}
		facts = c.processWriteFactsWithin(ctx, processWriteDigestBudget, func(ctx context.Context) processDescribeFacts {
			return c.processDescribeFactsForSchema(ctx, schemaUID)
		})
	}
	return processWriteRead{raw: raw, graph: processDescribeOutput(raw, facts)}
}

// processWriteReadVersionStanding is ProcessWriteDigest.ReadVersionStanding (ServerProcessDescriber.ReadVersionFacts):
// the version facts only, by UId or by name, on the 3 s budget; an error when neither the version nor the
// active version was established.
func (c *Client) processWriteReadVersionStanding(ctx context.Context, name, uid string) processWriteRead {
	var facts processDescribeFacts
	switch {
	case strings.TrimSpace(uid) != "":
		facts = c.processWriteFactsWithin(ctx, processWriteDigestBudget, func(ctx context.Context) processDescribeFacts {
			return c.processDescribeFactsForSchema(ctx, strings.TrimSpace(uid))
		})
	case strings.TrimSpace(name) != "":
		code := strings.TrimSpace(name)
		facts = c.processWriteFactsWithin(ctx, processWriteDigestBudget, func(ctx context.Context) processDescribeFacts {
			rows, err := c.processDescribeLibRows(ctx, "Name", code, 1, 1)
			if err != nil {
				return processDescribeNotEstablished("reading the process library failed: " + err.Error())
			}
			if len(rows) == 0 {
				return processDescribeNotEstablished(fmt.Sprintf("the process library has no row for schema '%s'", code))
			}
			return c.processDescribeFactsForRow(ctx, rows[0])
		})
	default:
		return processWriteRead{err: fmt.Errorf("the process identity could not be resolved")}
	}
	if facts.Version == nil && facts.ActiveVersionSchemaUID == "" {
		if facts.Warning != "" {
			return processWriteRead{err: fmt.Errorf("%s", facts.Warning)}
		}
		return processWriteRead{err: fmt.Errorf("the version facts were not established")}
	}
	identity := newObject()
	if strings.TrimSpace(name) != "" {
		identity.set("name", newString(name))
	}
	if strings.TrimSpace(uid) != "" {
		identity.set("schemaUId", newString(uid))
	}
	return processWriteRead{graph: processDescribeOutput(identity, facts)}
}

// processWriteFactsWithin is ProcessLibRead.WithinBudget for a version read: the read's answer when it finishes
// within budget, otherwise the "did not complete" warning (the read is abandoned).
func (c *Client) processWriteFactsWithin(ctx context.Context, budget time.Duration,
	read func(context.Context) processDescribeFacts) processDescribeFacts {
	readCtx, cancel := context.WithCancel(ctx)
	answer := make(chan processDescribeFacts, 1)
	go func() { answer <- read(readCtx) }()
	timer := time.NewTimer(budget)
	defer timer.Stop()
	select {
	case facts := <-answer:
		cancel()
		return facts
	case <-timer.C:
		cancel()
		return processDescribeNotEstablished(fmt.Sprintf("the process library read did not complete within %ss",
			processWriteSeconds(budget)))
	}
}

// processWriteSeconds formats a budget the way {TotalSeconds:0.##} does.
func processWriteSeconds(budget time.Duration) string {
	text := fmt.Sprintf("%.2f", budget.Seconds())
	text = strings.TrimRight(strings.TrimRight(text, "0"), ".")
	return text
}

// processWriteReportDigest is ProcessWriteDigest.Report. before nil leaves the change list out (a create has
// nothing to compare with); a failed before-read says so in changesUnavailable. versionFactsRead says whether
// after asked for the version standing; changesAgainst names what before is when it is not the same schema
// before the edit; readFailureReported is set when a guard already warned that this read-back failed.
func processWriteReportDigest(log *processWriteLog, after processWriteRead, before *processWriteRead, versionFactsRead bool,
	changesAgainst string, readFailureReported bool) {
	defer func() {
		if recovered := recover(); recovered != nil {
			log.warning(fmt.Sprintf("The write landed, but its summary could not be built: %v. "+
				"describe-business-process mode=summary confirms the saved state.", recovered))
		}
	}()
	if after.err != nil {
		if readFailureReported {
			return
		}
		log.warning(fmt.Sprintf("The write landed, but reading the process back for its summary failed: %s. "+
			"describe-business-process mode=summary confirms the saved state.", after.err.Error()))
		return
	}
	saved := after.graph.clone()
	processDescribeOmitDecodedFilters(saved)
	if !versionFactsRead {
		saved.remove("versionReadWarning")
	} else if warning := saved.get("versionReadWarning"); warning != nil && warning.kind == jkString && strings.TrimSpace(warning.text) != "" {
		saved.set("versionReadWarning", newString(fmt.Sprintf("%s (%s)", processWriteVersionStandingNotRead, warning.text)))
	}
	digest := newObject()
	digest.set("summary", processDescribeFitSummary(saved, processDescribeChecksum(saved), nil))
	if before != nil {
		if strings.TrimSpace(changesAgainst) != "" {
			digest.set("changesAgainst", newString(changesAgainst))
		}
		if before.err != nil {
			digest.set("changesUnavailable", newString("the read before the write failed: "+before.err.Error()))
		} else {
			previous := before.graph.clone()
			processDescribeOmitDecodedFilters(previous)
			digest.set("changes", processWriteBuildChanges(previous, saved))
		}
	}
	log.info(processDescribeWrite(digest))
}

// processWriteWhyAnEditIsNotReadBack is ProcessWriteDigest.WhyAnEditIsNotReadBack (ENG-103165): the reply line
// for an edit no guard reads back whose process calls another one (or may, when the read before the edit
// failed); "" when it is read back - it calls nothing, or it binds a process parameter.
func processWriteWhyAnEditIsNotReadBack(operations *jnode, before processWriteRead) string {
	if processWriteBindsAProcessParameter(operations) {
		return ""
	}
	var previous *jnode
	if before.err == nil {
		previous = before.graph
	}
	if processWriteEditedProcessCallsAnotherProcess(operations, previous) {
		return processWriteCallerNotReadBack
	}
	if before.err != nil {
		return processWritePossibleCallerNotReadBack
	}
	return ""
}

// processWriteBindsAProcessParameter is ProcessParameterBindingWarnings.BindsAProcessParameter: an addMapping
// whose mapping names a targetProcessParameter.
func processWriteBindsAProcessParameter(operations *jnode) bool {
	for _, operation := range processWriteItems(operations) {
		if !operation.isObject() {
			continue
		}
		op, ok := processWriteReadText(operation.get("op"))
		mapping := operation.get("mapping")
		if !ok || !strings.EqualFold(op, "addMapping") || !mapping.isObject() {
			continue
		}
		if target, found := processWriteReadText(mapping.get("targetProcessParameter")); found && strings.TrimSpace(target) != "" {
			return true
		}
	}
	return false
}

// processWriteEditedProcessCallsAnotherProcess is ProcessWriteDigest.EditedProcessCallsAnotherProcess: the edit
// configures a sub-process element, or the read before it shows one the edit does not remove.
func processWriteEditedProcessCallsAnotherProcess(operations *jnode, before *jnode) bool {
	if len(processWriteAddElementTargets(operations, processWriteSubProcessKey)) > 0 ||
		len(processWriteSetElementTargets(operations, processWriteSubProcessKey)) > 0 {
		return true
	}
	if before == nil {
		return false
	}
	elements := processDescribeObjects(before.get("elements"))
	if len(elements) == 0 {
		return false
	}
	removed := map[*jnode]bool{}
	for _, target := range processWriteOperationTargets(operations, "removeElement") {
		for _, element := range elements {
			if element.isObject() && (strings.EqualFold(processDescribeText(element, "name"), target) ||
				strings.EqualFold(processDescribeText(element, "uid"), target)) {
				removed[element] = true
				break
			}
		}
	}
	for _, element := range elements {
		if !element.isObject() || removed[element] {
			continue
		}
		if sub := element.get(processWriteSubProcessKey); (sub != nil && sub.kind != jkNull) ||
			strings.EqualFold(processDescribeText(element, "buildType"), "subprocess") {
			return true
		}
	}
	return false
}

// ---- ProcessDigest.BuildChanges ----

type processWriteKeyed struct {
	keys    []string
	entries map[string]processWriteKeyedEntry
}

type processWriteKeyedEntry struct{ label, content string }

func (k *processWriteKeyed) add(key, label, content string, overwrite bool) {
	if k.entries == nil {
		k.entries = map[string]processWriteKeyedEntry{}
	}
	if _, exists := k.entries[key]; exists {
		if overwrite {
			k.entries[key] = processWriteKeyedEntry{label, content}
		}
		return
	}
	k.keys = append(k.keys, key)
	k.entries[key] = processWriteKeyedEntry{label, content}
}

// processWriteBuildChanges is ProcessDigest.BuildChanges: elements, flows and process parameters added,
// removed and changed between two reads, layout ignored; a list with nothing in it is left out.
func processWriteBuildChanges(before, after *jnode) *jnode {
	changes := newObject()
	byName := func(graph *jnode, member string, ignored []string) processWriteKeyed {
		keyed := processWriteKeyed{}
		for _, item := range processDescribeObjects(graph.get(member)) {
			name := item.get("name")
			if !item.isObject() || name == nil || name.kind != jkString {
				continue
			}
			keyed.add(name.text, name.text, processWriteCanonical(item, ignored), false)
		}
		return keyed
	}
	processWriteDiff(changes, "elements", byName(before, "elements", processDescribeElementLayout),
		byName(after, "elements", processDescribeElementLayout))
	flowsBefore, flowsAfter := processWriteWithoutUnchangedFlows(processDescribeObjects(before.get("flows")),
		processDescribeObjects(after.get("flows")))
	processWriteDiff(changes, "flows", processWriteKeyedFlows(flowsBefore), processWriteKeyedFlows(flowsAfter))
	processWriteDiff(changes, "parameters", byName(before, "parameters", nil), byName(after, "parameters", nil))
	return changes
}

// processWriteCanonical is ProcessDigest.Canonical: the compact JSON without the ignored top-level members and
// without every uid.
func processWriteCanonical(item *jnode, ignored []string) string {
	copied := item.clone()
	if copied.isObject() {
		for _, member := range ignored {
			copied.remove(member)
		}
	}
	processDescribeStrip(copied, func(key string) bool { return key == "uid" })
	return processDescribeWrite(copied)
}

// processWriteWithoutUnchangedFlows pairs off flows whose content is identical in both reads, so removing one
// of two parallel flows between the same elements reports that flow as removed.
func processWriteWithoutUnchangedFlows(before, after []*jnode) ([]*jnode, []*jnode) {
	type pending struct {
		flow    *jnode
		content string
	}
	remaining := []pending{}
	for _, flow := range after {
		remaining = append(remaining, pending{flow, processWriteCanonical(flow, processDescribeFlowLayout)})
	}
	changedBefore := []*jnode{}
	for _, flow := range before {
		content := processWriteCanonical(flow, processDescribeFlowLayout)
		match := -1
		for index, candidate := range remaining {
			if candidate.content == content {
				match = index
				break
			}
		}
		if match >= 0 {
			remaining = append(remaining[:match], remaining[match+1:]...)
		} else {
			changedBefore = append(changedBefore, flow)
		}
	}
	changedAfter := make([]*jnode, 0, len(remaining))
	for _, candidate := range remaining {
		changedAfter = append(changedAfter, candidate.flow)
	}
	return changedBefore, changedAfter
}

// processWriteKeyedFlows keys flows by their endpoint pair, with an ordinal for the second flow between the
// same two elements.
func processWriteKeyedFlows(flows []*jnode) processWriteKeyed {
	keyed := processWriteKeyed{}
	seen := map[string]int{}
	for _, flow := range flows {
		pair := processDescribeText(flow, "source") + " -> " + processDescribeText(flow, "target")
		ordinal := 0
		if count, ok := seen[pair]; ok {
			ordinal = count + 1
		}
		seen[pair] = ordinal
		key := pair
		if ordinal != 0 {
			key = fmt.Sprintf("%s #%d", pair, ordinal+1)
		}
		keyed.add(key, processDescribeFlowLine(flow), processWriteCanonical(flow, processDescribeFlowLayout), true)
	}
	return keyed
}

func processWriteDiff(changes *jnode, family string, before, after processWriteKeyed) {
	added, removed, changed := []string{}, []string{}, []string{}
	for _, key := range after.keys {
		if _, ok := before.entries[key]; !ok {
			added = append(added, after.entries[key].label)
		}
	}
	for _, key := range before.keys {
		if _, ok := after.entries[key]; !ok {
			removed = append(removed, before.entries[key].label)
		}
	}
	for _, key := range after.keys {
		if old, ok := before.entries[key]; ok && old.content != after.entries[key].content {
			changed = append(changed, after.entries[key].label)
		}
	}
	processWriteAddList(changes, family+"Added", added)
	processWriteAddList(changes, family+"Removed", removed)
	processWriteAddList(changes, family+"Changed", changed)
}

// processWriteAddList is ProcessDigest.AddList: at most 25 entries, the rest counted in <list>Omitted.
func processWriteAddList(target *jnode, member string, values []string) {
	if len(values) == 0 {
		return
	}
	kept := values
	if len(kept) > processWriteMaxChangeEntries {
		kept = kept[:processWriteMaxChangeEntries]
	}
	list := newArray()
	for _, value := range kept {
		list.items = append(list.items, newString(value))
	}
	target.set(member, list)
	if len(values) > processWriteMaxChangeEntries {
		target.set(member+"Omitted", newInt(len(values)-processWriteMaxChangeEntries))
	}
}
