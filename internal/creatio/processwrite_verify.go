package creatio

// The post-write configuration check of create-business-process and modify-business-process: clio's
// WarnOnDiscardedConfigurationBlocks with BlockExpectationIntent, BlockExpectationJson,
// BlockExpectationReporter and the AccessRights / Approval / Email / FlowLabel expectations. A
// CrtProcessBuilder that predates a block discards it and still answers success, so after a successful write
// that sent an accessRights, email or approval block, a flow label or (modify) a clearFilter, the process is
// read back once (DescribeProcess, one attempt, 5 s) and every block that did not land is reported as a
// warning. The read-back is a caveat: its failure never fails the write.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"
)

const (
	processWriteAccessRightsKey  = "accessRights"
	processWriteChangeRightsTask = "ChangeAdminRightsUserTask"
	processWriteRecordFilterName = "DataSourceFilters"
	processWriteUnresolvedReason = "the saved process does not report an element with that name or UId"
	processWriteMaxRenderedFlows = 10
	processWriteMaxRenderedText  = 200
)

// ---------------------------------------------------------------------------------- intent (JSON side)

// processWriteIntent is BlockExpectationIntent.
type processWriteIntent struct {
	configuredRights []string
	configuredEmail  []string
	filterTouched    []string
	templatedEmail   []string
}

func (i processWriteIntent) isEmpty() bool {
	return len(i.configuredRights) == 0 && len(i.configuredEmail) == 0 && len(i.filterTouched) == 0
}

// refilteredOnly is FilterTouched except ConfiguredRights (set semantics, case-insensitive).
func (i processWriteIntent) refilteredOnly() []string {
	result := []string{}
	for _, name := range i.filterTouched {
		if !processWriteContainsFold(i.configuredRights, name) && !processWriteContainsFold(result, name) {
			result = append(result, name)
		}
	}
	return result
}

func (i processWriteIntent) recordFilterSubjects() []string {
	if len(i.filterTouched) == 0 {
		return i.configuredRights
	}
	return processWriteDistinct(append(append([]string{}, i.configuredRights...), i.filterTouched...))
}

// processWriteParseBlocks is BlockExpectationJson.Parse: nil for blank or malformed text.
func processWriteParseBlocks(text string) *jnode {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	node, err := parseJNode([]byte(text))
	if err != nil {
		return nil
	}
	return node
}

// processWriteReadText is BlockExpectationJson.ReadText: a JSON string's value, else nothing.
func processWriteReadText(node *jnode) (string, bool) {
	if node == nil || node.kind != jkString {
		return "", false
	}
	return node.text, true
}

func processWriteAddName(names []string, node *jnode) []string {
	if name, ok := processWriteReadText(node); ok && strings.TrimSpace(name) != "" {
		names = append(names, name)
	}
	return names
}

func processWriteDistinct(names []string) []string {
	result := []string{}
	for _, name := range names {
		if !processWriteContainsFold(result, name) {
			result = append(result, name)
		}
	}
	return result
}

func processWriteItems(node *jnode) []*jnode {
	if !node.isArray() {
		return nil
	}
	return node.items
}

// processWriteElementsCarrying is ElementsCarrying: descriptor elements whose block is an object.
func processWriteElementsCarrying(descriptor *jnode, key string) []string {
	names := []string{}
	if !descriptor.isObject() {
		return names
	}
	for _, element := range processWriteItems(descriptor.get("elements")) {
		if element.isObject() && element.get(key).isObject() {
			names = processWriteAddName(names, element.get("name"))
		}
	}
	return names
}

// processWriteSetElementTargets is SetElementTargets: setElement operations whose elementUpdate carries it.
func processWriteSetElementTargets(operations *jnode, key string) []string {
	names := []string{}
	for _, operation := range processWriteItems(operations) {
		if operation.isObject() && operation.get("elementUpdate").isObject() && operation.get("elementUpdate").get(key).isObject() {
			names = processWriteAddName(names, operation.get("elementName"))
		}
	}
	return names
}

// processWriteAddElementTargets is AddElementTargets.
func processWriteAddElementTargets(operations *jnode, key string) []string {
	names := []string{}
	for _, operation := range processWriteItems(operations) {
		if added := operation.get("element"); added.isObject() && added.get(key).isObject() {
			names = processWriteAddName(names, added.get("name"))
		}
	}
	return names
}

// processWriteOperationTargets is OperationTargets for one op token.
func processWriteOperationTargets(operations *jnode, token string) []string {
	names := []string{}
	for _, operation := range processWriteItems(operations) {
		if op, ok := processWriteReadText(operation.get("op")); ok && strings.EqualFold(op, token) {
			names = processWriteAddName(names, operation.get("elementName"))
		}
	}
	return names
}

func processWriteIntentFromDescriptor(descriptor *jnode) processWriteIntent {
	return processWriteIntent{
		configuredRights: processWriteDistinct(processWriteElementsCarrying(descriptor, processWriteAccessRightsKey)),
		configuredEmail:  processWriteEmailFromDescriptor(descriptor),
		filterTouched:    []string{},
		templatedEmail:   processWriteTemplateElements(descriptor),
	}
}

func processWriteIntentFromOperations(operations *jnode) processWriteIntent {
	return processWriteIntent{
		configuredRights: processWriteDistinct(processWriteSetElementTargets(operations, processWriteAccessRightsKey)),
		configuredEmail:  processWriteEmailFromOperations(operations),
		filterTouched:    processWriteDistinct(processWriteOperationTargets(operations, "clearFilter")),
		templatedEmail:   processWriteTemplateElementsFromOperations(operations),
	}
}

// ---------------------------------------------------------------------------------- email (JSON side)

func processWriteEmailFromDescriptor(descriptor *jnode) []string {
	return processWriteElementsCarrying(descriptor, "email")
}

func processWriteEmailFromOperations(operations *jnode) []string {
	names := []string{}
	for _, operation := range processWriteItems(operations) {
		if !operation.isObject() {
			continue
		}
		if added := operation.get("element"); added.isObject() && added.get("email").isObject() {
			names = processWriteAddName(names, added.get("name"))
		}
		if update := operation.get("elementUpdate"); update.isObject() && update.get("email").isObject() {
			names = processWriteAddName(names, operation.get("elementName"))
		}
	}
	return names
}

// processWriteMacroBodyElements is MacroBodyElements: email bodies carrying [[param: or [[element:.
func processWriteMacroBodyElements(descriptor *jnode) []string {
	names := []string{}
	if !descriptor.isObject() {
		return names
	}
	for _, element := range processWriteItems(descriptor.get("elements")) {
		email := element.get("email")
		if !element.isObject() || !email.isObject() {
			continue
		}
		body, ok := processWriteReadText(email.get("body"))
		lower := strings.ToLower(body)
		if ok && body != "" && (strings.Contains(lower, "[[param:") || strings.Contains(lower, "[[element:")) {
			names = processWriteAddName(names, element.get("name"))
		}
	}
	return names
}

func processWriteCarriesTemplate(email *jnode) bool {
	if !email.isObject() {
		return false
	}
	template, ok := processWriteReadText(email.get("template"))
	return ok && strings.TrimSpace(template) != ""
}

func processWriteSwitchesToCustom(email *jnode) bool {
	if !email.isObject() {
		return false
	}
	if body, ok := processWriteReadText(email.get("body")); ok && strings.TrimSpace(body) != "" {
		return true
	}
	mode, ok := processWriteReadText(email.get("messageSource"))
	return ok && strings.EqualFold(strings.TrimSpace(mode), "custom")
}

func processWriteTemplateElements(descriptor *jnode) []string {
	names := []string{}
	if !descriptor.isObject() {
		return names
	}
	for _, element := range processWriteItems(descriptor.get("elements")) {
		if element.isObject() && processWriteCarriesTemplate(element.get("email")) {
			names = processWriteAddName(names, element.get("name"))
		}
	}
	return processWriteDistinct(names)
}

func processWriteTemplateElementsFromOperations(operations *jnode) []string {
	names := []string{}
	for _, operation := range processWriteItems(operations) {
		if !operation.isObject() {
			continue
		}
		var name string
		var nameOK bool
		var email *jnode
		if added := operation.get("element"); added.isObject() {
			name, nameOK = processWriteReadText(added.get("name"))
			email = added.get("email")
		} else if update := operation.get("elementUpdate"); update.isObject() {
			name, nameOK = processWriteReadText(operation.get("elementName"))
			email = update.get("email")
		}
		if !nameOK || strings.TrimSpace(name) == "" || !email.isObject() {
			continue
		}
		if processWriteCarriesTemplate(email) {
			names = append(names, name)
		} else if processWriteSwitchesToCustom(email) {
			kept := []string{}
			for _, existing := range names {
				if !strings.EqualFold(existing, name) {
					kept = append(kept, existing)
				}
			}
			names = kept
		}
	}
	return processWriteDistinct(names)
}

// ------------------------------------------------------------------------------- approval (JSON side)

// processWriteApprovalExpectation is ApprovalBlockExpectation.ApprovalExpectation.
type processWriteApprovalExpectation struct {
	element                                            string
	approver, approverTemplate, authorTemplate, author bool
	describeShaped                                     bool
}

func processWriteApprovalFromDescriptor(descriptor *jnode) []processWriteApprovalExpectation {
	expectations := []processWriteApprovalExpectation{}
	if !descriptor.isObject() {
		return expectations
	}
	for _, element := range processWriteItems(descriptor.get("elements")) {
		expectations = processWriteAddApproval(expectations, element.get("approval"), element.get("name"))
	}
	return expectations
}

func processWriteApprovalFromOperations(operations *jnode) []processWriteApprovalExpectation {
	expectations := []processWriteApprovalExpectation{}
	for _, operation := range processWriteItems(operations) {
		added := operation.get("element")
		expectations = processWriteAddApproval(expectations, added.get("approval"), added.get("name"))
		update := operation.get("elementUpdate")
		expectations = processWriteAddApproval(expectations, update.get("approval"), operation.get("elementName"))
	}
	return expectations
}

func processWriteAddApproval(expectations []processWriteApprovalExpectation, approval, nameNode *jnode) []processWriteApprovalExpectation {
	if !approval.isObject() {
		return expectations
	}
	name, ok := processWriteReadText(nameNode)
	if !ok || strings.TrimSpace(name) == "" {
		return expectations
	}
	hasTemplate := func(notification *jnode) bool {
		if !notification.isObject() {
			return false
		}
		template, ok := processWriteReadText(notification.get("emailTemplate"))
		return ok && strings.TrimSpace(template) != ""
	}
	scalar := func(node *jnode) bool {
		return node != nil && node.kind != jkObject && node.kind != jkArray && node.kind != jkNull
	}
	approverType := approval.get("approverType")
	author := approval.get("notifyAuthor")
	return append(expectations, processWriteApprovalExpectation{
		element:          name,
		approver:         approval.get("approver").isObject(),
		approverTemplate: hasTemplate(approval.get("notifyApprover")),
		authorTemplate:   hasTemplate(author),
		author:           author.isObject() && author.get("recipient").isObject(),
		describeShaped: (!approval.get("approver").isObject() && approverType != nil && approverType.kind != jkNull) ||
			scalar(approval.get("notifyApprover")) || scalar(author),
	})
}

// ------------------------------------------------------------------------------ flow labels (JSON side)

// processWriteFlowLabel is FlowLabelExpectation.FlowLabel.
type processWriteFlowLabel struct {
	source, target, label string
}

func processWriteFlowLabelsFromDescriptor(descriptor *jnode) []processWriteFlowLabel {
	if !descriptor.isObject() || !descriptor.get("flows").isArray() {
		return []processWriteFlowLabel{}
	}
	return processWriteCollectLabelled(descriptor.get("flows").items, false)
}

func processWriteFlowLabelsFromOperations(operations *jnode) []processWriteFlowLabel {
	if !operations.isArray() {
		return []processWriteFlowLabel{}
	}
	return processWriteCollectLabelled(operations.items, true)
}

// processWriteCollectLabelled is CollectLabelled: the last label written per endpoint pair, in the order
// the pairs were first seen; a removeFlow forgets the pair.
func processWriteCollectLabelled(entries []*jnode, filterByOp bool) []processWriteFlowLabel {
	type key struct{ source, target string }
	byEndpoints := map[key]processWriteFlowLabel{}
	order := []key{}
	fold := func(source, target string) key { return key{strings.ToLower(source), strings.ToLower(target)} }
	for _, candidate := range entries {
		if !candidate.isObject() {
			continue
		}
		if filterByOp {
			op, _ := processWriteReadText(candidate.get("op"))
			op = strings.TrimSpace(op)
			if strings.EqualFold(op, "removeFlow") {
				source, sourceOK := processWriteReadText(candidate.get("source"))
				target, targetOK := processWriteReadText(candidate.get("target"))
				if sourceOK && targetOK && strings.TrimSpace(source) != "" && strings.TrimSpace(target) != "" {
					pair := fold(strings.TrimSpace(source), strings.TrimSpace(target))
					if _, exists := byEndpoints[pair]; exists {
						delete(byEndpoints, pair)
						kept := order[:0]
						for _, existing := range order {
							if existing != pair {
								kept = append(kept, existing)
							}
						}
						order = kept
					}
				}
				continue
			}
			if !strings.EqualFold(op, "addFlow") && !strings.EqualFold(op, "setFlow") {
				continue
			}
		}
		label, labelOK := processWriteReadText(candidate.get("label"))
		source, sourceOK := processWriteReadText(candidate.get("source"))
		target, targetOK := processWriteReadText(candidate.get("target"))
		if !labelOK || !sourceOK || !targetOK || strings.TrimSpace(source) == "" || strings.TrimSpace(target) == "" {
			continue
		}
		flow := processWriteFlowLabel{source: strings.TrimSpace(source), target: strings.TrimSpace(target), label: processWriteAsStored(label)}
		pair := fold(flow.source, flow.target)
		if _, exists := byEndpoints[pair]; !exists {
			order = append(order, pair)
		}
		byEndpoints[pair] = flow
	}
	labels := make([]processWriteFlowLabel, 0, len(order))
	for _, pair := range order {
		labels = append(labels, byEndpoints[pair])
	}
	return labels
}

// processWriteAsStored is AsStored: lone surrogates and characters XML cannot store are dropped, the
// result trimmed.
func processWriteAsStored(label string) string {
	var kept strings.Builder
	for _, r := range label {
		if r == utf8RuneError || r == 0xFFFE || r == 0xFFFF || (unicode.IsControl(r) && r != '\t' && r != '\n' && r != '\r') {
			continue
		}
		kept.WriteRune(r)
	}
	return strings.TrimSpace(kept.String())
}

const utf8RuneError = '�'

// --------------------------------------------------------------------------------------- read-back

// processWriteDescribed is the part of DescribeProcessResult the expectations read.
type processWriteDescribed struct {
	elements    []*jnode
	hasElements bool
	flows       []*jnode
	hasFlows    bool
}

// processWriteDescribe is ServerProcessDescriber.Describe with bestEffort and no version facts: one
// DescribeProcess read; an error is the description clio reports.
func (c *Client) processWriteDescribe(ctx context.Context, name, uid string) (processWriteDescribed, error) {
	result, err := c.processWriteDescribeRaw(ctx, name, uid)
	if err != nil {
		return processWriteDescribed{}, err
	}
	return processWriteDescribedOf(result), nil
}

// processWriteDescribedOf is the part of a DescribeProcess result the expectations read.
func processWriteDescribedOf(result *jnode) processWriteDescribed {
	described := processWriteDescribed{}
	if elements := processWriteProperty(result, "elements"); elements.isArray() {
		described.elements, described.hasElements = elements.items, true
	}
	if flows := processWriteProperty(result, "flows"); flows.isArray() {
		described.flows, described.hasFlows = flows.items, true
	}
	return described
}

// processWriteDescribeRaw is the DescribeProcess read of ServerProcessDescriber.Describe with bestEffort (one
// attempt at 5 s, no culture, so the server resolves text in the profile culture): the server's result object.
func (c *Client) processWriteDescribeRaw(ctx context.Context, name, uid string) (*jnode, error) {
	identity := newObject()
	if strings.TrimSpace(uid) != "" {
		identity.set("uid", newString(strings.TrimSpace(uid)))
	} else {
		identity.set("name", newString(strings.TrimSpace(name)))
	}
	body := newObject()
	body.set("request", identity)
	payload, err := c.processWritePost(ctx, serviceCall{Route: processDescribeRoute, Body: body.stjJSON(), Timeout: 5 * time.Second,
		Limit: maxResponseBytes, Label: "ProcessDesignService"})
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(string(payload)) == "" {
		return nil, errors.New("empty response from the server")
	}
	envelope, err := parseJNode(payload)
	if err != nil {
		return nil, fmt.Errorf("could not parse server response: %s", processWriteDeserializeError(payload, err))
	}
	if envelope.kind != jkObject && envelope.kind != jkNull {
		return nil, errors.New("could not parse server response: The JSON value could not be converted to the describe result.")
	}
	result := processWriteProperty(envelope, "DescribeProcessResult")
	if result == nil || result.kind == jkNull {
		return nil, errors.New("unexpected server response shape")
	}
	if !result.isObject() {
		return nil, errors.New("could not parse server response: The JSON value could not be converted to the describe result.")
	}
	if success := processWriteProperty(result, "success"); success == nil || success.kind != jkBool || !success.flag {
		if message := processWriteProperty(result, "errorMessage"); message != nil && message.kind == jkString {
			return nil, errors.New(message.text)
		}
		return nil, errors.New("describe-business-process failed on the server")
	}
	return result, nil
}

func processWriteText(node *jnode, key string) string {
	value := processWriteProperty(node, key)
	if value == nil || value.kind != jkString {
		return ""
	}
	return value.text
}

// resolve is BlockExpectationJson.ResolveElement: by name or UId, case-insensitive, the first match.
func (d processWriteDescribed) resolve(name string) *jnode {
	for _, element := range d.elements {
		if element.isObject() && (strings.EqualFold(processWriteText(element, "name"), name) || strings.EqualFold(processWriteText(element, "uid"), name)) {
			return element
		}
	}
	return nil
}

// processWriteAccessRights is TryGetAccessRights: the element's accessRights entry, when present and not null.
func processWriteAccessRights(element *jnode) (*jnode, bool) {
	for _, key := range element.keys {
		if strings.EqualFold(key, processWriteAccessRightsKey) {
			value := element.props[key]
			return value, value != nil && value.kind != jkNull
		}
	}
	return nil, false
}

func processWriteUnresolved(d processWriteDescribed, expected []string) []string {
	if len(expected) == 0 {
		return []string{}
	}
	if !d.hasElements {
		return expected
	}
	unresolved := []string{}
	for _, name := range expected {
		if d.resolve(name) == nil {
			unresolved = append(unresolved, name)
		}
	}
	return unresolved
}

func processWriteRightsMissing(d processWriteDescribed, expected []string) []string {
	missing := []string{}
	if len(expected) == 0 || !d.hasElements {
		return missing
	}
	for _, name := range expected {
		if element := d.resolve(name); element != nil {
			if _, ok := processWriteAccessRights(element); !ok {
				missing = append(missing, name)
			}
		}
	}
	return missing
}

const (
	processWriteFilterNarrowing = iota
	processWriteFilterConditionless
	processWriteFilterUndecodable
	processWriteFilterAbsent
)

func processWriteNarrows(group *jnode) bool {
	if !group.isObject() {
		return false
	}
	if conditions := processWriteProperty(group, "conditions"); conditions.isArray() && len(conditions.items) > 0 {
		return true
	}
	for _, child := range processWriteItems(processWriteProperty(group, "groups")) {
		if processWriteNarrows(child) {
			return true
		}
	}
	return false
}

func processWriteClassifyFilter(element *jnode) int {
	if filter := processWriteProperty(element, "filter"); filter != nil && filter.kind != jkNull {
		if processWriteNarrows(filter) {
			return processWriteFilterNarrowing
		}
		return processWriteFilterConditionless
	}
	for _, parameter := range processWriteItems(processWriteProperty(element, "parameters")) {
		if strings.EqualFold(processWriteText(parameter, "name"), processWriteRecordFilterName) {
			if strings.TrimSpace(processWriteText(parameter, "value")) == "" {
				return processWriteFilterAbsent
			}
			return processWriteFilterUndecodable
		}
	}
	return processWriteFilterAbsent
}

type processWriteFilterless struct {
	name  string
	state int
}

func processWriteFilterlessElements(d processWriteDescribed, expected []string) []processWriteFilterless {
	result := []processWriteFilterless{}
	if len(expected) == 0 || !d.hasElements {
		return result
	}
	for _, name := range expected {
		element := d.resolve(name)
		if element == nil {
			continue
		}
		if _, ok := processWriteAccessRights(element); !ok {
			continue
		}
		if state := processWriteClassifyFilter(element); state != processWriteFilterNarrowing {
			result = append(result, processWriteFilterless{name: name, state: state})
		}
	}
	return result
}

func processWriteNoun(count int) string {
	if count == 1 {
		return "element"
	}
	return "elements"
}

func processWriteNoFilterWarning(d processWriteDescribed, expected []string) string {
	unfiltered := processWriteFilterlessElements(d, expected)
	if len(unfiltered) == 0 {
		return ""
	}
	names := func(state int) string {
		selected := []string{}
		for _, entry := range unfiltered {
			if entry.state == state {
				selected = append(selected, entry.name)
			}
		}
		return strings.Join(selected, "', '")
	}
	absent, conditionless, undecodable := names(processWriteFilterAbsent), names(processWriteFilterConditionless), names(processWriteFilterUndecodable)
	parts := []string{}
	if absent != "" {
		parts = append(parts, fmt.Sprintf("'%s' has NO record filter at all, so at run time it will apply the permission "+
			"change to EVERY record of the target object — not to none. The element's query runs with "+
			"record permissions DISABLED, so that is every row in the table, not the rows you can see", absent))
	}
	if conditionless != "" {
		parts = append(parts, fmt.Sprintf("'%s' has a record filter with NO conditions, so the runtime takes its "+
			"\"filters empty\" exit and changes nothing — the element is inert rather than wide", conditionless))
	}
	if undecodable != "" {
		parts = append(parts, fmt.Sprintf("'%s' HAS a stored record filter that this read-back could not decode (a "+
			"filter saved in the legacy designer format reads back empty) — it may be narrowing exactly "+
			"as intended, so verify it in the designer and do NOT replace it on the strength of this "+
			"message", undecodable))
	}
	remedies := []string{}
	if absent != "" {
		remedies = append(remedies, fmt.Sprintf("Set the filter you mean for '%s' BEFORE this process runs, with the "+
			"setFilter operation (to act on one record, filter Id against a process parameter or a "+
			"trigger output). Note that ANY change to the element's object clears its record filter, so "+
			"an ordinary retarget lands in exactly this state", absent))
	}
	if conditionless != "" {
		remedies = append(remedies, fmt.Sprintf("Give '%s' the conditions you mean - a conditionless filter is "+
			"refused at build by a current CrtProcessBuilder, so an element carrying one was configured "+
			"by an older package or in the designer - and do not report a grant or revoke as applied "+
			"until you have", conditionless))
	}
	if undecodable != "" {
		remedies = append(remedies, fmt.Sprintf("Confirm '%s' in the designer before reporting a grant or revoke as "+
			"applied, and do NOT replace that filter on the strength of this message", undecodable))
	}
	return "The 'accessRights' configuration was saved, but " + strings.Join(parts, "; and ") +
		". This happens silently, because the element has no output parameters" + ". " + strings.Join(remedies, ". ") + "."
}

func processWriteRightsWarning(missing []string) string {
	if len(missing) == 0 {
		return ""
	}
	return fmt.Sprintf("The operation reported success, but the saved process does NOT carry the 'accessRights' "+
		"configuration for the %s '%s' — the read-back shows no accessRights block. The usual "+
		"cause is a deployed CrtProcessBuilder that predates the Change access rights element: it has no "+
		"'accessRights' member and does not implement IExtensibleDataObject, so it discards the block instead "+
		"of rejecting it and still answers success. The element is therefore UNCONFIGURED and will grant and "+
		"revoke NOTHING when it runs, silently — it has no output parameters to report that. If you asked for "+
		"a REVOKE, treat those permissions as still in place and do not report the change as applied. Install "+
		"a package that supports the element (install-process-builder after user approval - it "+processWriteInstallCost+") and re-apply the block, or "+
		"configure the element in the designer. If re-applying reproduces this same warning, the archive "+
		"bundled with THIS clio does not carry the element either — and note that its VERSION cannot tell "+
		"you that: the bundled package satisfies this command's [RequiresPackage] floor while still "+
		"discarding the block, so the version precondition does not protect you here and this warning is "+
		"the only signal. Deploying a CrtProcessBuilder built from a source tree that contains the element "+
		"is then the only fix, and the block cannot be applied on this environment until you do.",
		processWriteNoun(len(missing)), strings.Join(missing, "', '"))
}

func processWriteAddElementWarning(ignored []string) string {
	if len(ignored) == 0 {
		return ""
	}
	return fmt.Sprintf("The 'accessRights' block sent with addElement for the %s '%s' was NOT applied: "+
		"addElement applies only the email and performer blocks, so the element was created without any "+
		"permission configuration and will grant and revoke nothing when it runs. Configure it with a "+
		"setElement operation carrying accessRights (you can put it in the same operations array).",
		processWriteNoun(len(ignored)), strings.Join(ignored, "', '"))
}

func processWriteUnreportableFilterWarning(d processWriteDescribed, touched []string) string {
	if len(touched) == 0 || !d.hasElements {
		return ""
	}
	unreportable := []string{}
	for _, name := range touched {
		element := d.resolve(name)
		if element == nil || !strings.EqualFold(processWriteText(element, "userTaskName"), processWriteChangeRightsTask) {
			continue
		}
		if _, ok := processWriteAccessRights(element); !ok {
			unreportable = append(unreportable, name)
		}
	}
	if len(unreportable) == 0 {
		return ""
	}
	return fmt.Sprintf("The record filter on the %s '%s' was "+
		"changed, but this environment's CrtProcessBuilder does not report the element's access-rights "+
		"state, so the change could NOT be verified here. If that element grants or revokes permissions, "+
		"check it in the designer before reporting the change as applied — clearing a record filter makes "+
		"the element act on EVERY record of its object.", processWriteNoun(len(unreportable)), strings.Join(unreportable, "', '"))
}

func processWriteUnreadableCount(block *jnode, property string) int {
	if !block.isObject() {
		return 0
	}
	value := block.get(property)
	if value == nil || value.kind != jkInteger {
		return 0
	}
	var count int
	if _, err := fmt.Sscan(value.text, &count); err != nil {
		return 0
	}
	return count
}

func processWriteLossyReadWarning(d processWriteDescribed, expected []string) string {
	if len(expected) == 0 || !d.hasElements {
		return ""
	}
	lossy := []string{}
	for _, name := range expected {
		element := d.resolve(name)
		if element == nil {
			continue
		}
		if block, ok := processWriteAccessRights(element); ok &&
			(processWriteUnreadableCount(block, "addUnreadable") != 0 || processWriteUnreadableCount(block, "removeUnreadable") != 0) {
			lossy = append(lossy, name)
		}
	}
	if len(lossy) == 0 {
		return ""
	}
	return fmt.Sprintf("The read-back of the %s '%s' could NOT report "+
		"every stored permission entry. Do NOT build a replacement collection from this description: a "+
		"supplied 'add' or 'remove' REPLACES the stored one, so every entry the read-back omitted would be "+
		"deleted. Omit the collection to keep what is stored, or inspect the element in the designer.",
		processWriteNoun(len(lossy)), strings.Join(lossy, "', '"))
}

func processWriteUnverifiedRights(names []string, reason string) string {
	if len(names) == 0 {
		return ""
	}
	return fmt.Sprintf("Could not verify that the 'accessRights' configuration for the %s '%s' landed: "+
		"%s. The operation itself succeeded, but this check is the only signal that the permissions "+
		"were actually written — the element has no output parameters. Re-read the process with "+
		"describe-business-process mode=full before reporting a grant or revoke as applied.",
		processWriteNoun(len(names)), strings.Join(names, "', '"), reason)
}

func processWriteUnverifiedFilter(names []string, reason string) string {
	if len(names) == 0 {
		return ""
	}
	return fmt.Sprintf("Could not read back the record filter this edit changed on the %s '%s': "+
		"%s. The operation itself succeeded. If any of those is a Change access rights element, "+
		"note that CLEARING its record filter makes it apply the permission change to EVERY record of "+
		"its object — re-read the process with describe-business-process mode=full (or element=<name>) before "+
		"reporting the change as "+
		"applied.", processWriteNoun(len(names)), strings.Join(names, "', '"), reason)
}

// email (described side)

func processWriteEmailOf(element *jnode) *jnode {
	email := processWriteProperty(element, "email")
	if !email.isObject() {
		return nil
	}
	return email
}

func processWriteEmailMissing(d processWriteDescribed, expected []string) []string {
	missing := []string{}
	if len(expected) == 0 || !d.hasElements {
		return missing
	}
	for _, name := range expected {
		if element := d.resolve(name); element != nil && processWriteEmailOf(element) == nil {
			missing = append(missing, name)
		}
	}
	return missing
}

func processWriteEmailWarning(missing []string) string {
	if len(missing) == 0 {
		return ""
	}
	return fmt.Sprintf("The operation reported success, but the saved process does NOT carry the 'email' configuration for "+
		"the %s '%s' — the read-back shows no email block. The usual cause is a deployed "+
		"CrtProcessBuilder that predates the sendEmail element: it has no 'email' member and does not "+
		"implement IExtensibleDataObject, so it discards the block instead of rejecting it and still answers "+
		"success. Either way the element is UNCONFIGURED (no sender, no recipients, no subject or body), so do "+
		"not report it as configured. Check the package version, install one that supports sendEmail "+
		"(install-process-builder after user approval - it "+processWriteInstallCost+") and re-apply the email block, or configure the element in the designer.",
		processWriteNoun(len(missing)), strings.Join(missing, "', '"))
}

func processWriteUnlandedTemplates(d processWriteDescribed, templates []string) []string {
	unlanded := []string{}
	if len(templates) == 0 || !d.hasElements {
		return unlanded
	}
	for _, name := range templates {
		element := d.resolve(name)
		if element == nil {
			continue
		}
		if email := processWriteEmailOf(element); email != nil && strings.TrimSpace(processWriteText(email, "template")) == "" {
			unlanded = append(unlanded, name)
		}
	}
	return unlanded
}

func processWriteTemplateWarning(unlanded []string) string {
	if len(unlanded) == 0 {
		return ""
	}
	return fmt.Sprintf("The operation reported success, but the Send email %s '%s' sent a 'template' that the read-back does NOT show "+
		"(describe reports no template on the element). The usual cause is a deployed "+
		"CrtProcessBuilder that predates the template message mode (ENG-95986): it has no 'template' member, so it "+
		"discards the field while answering success. The element is then in the WRONG mode — no mode at all, which "+
		"the platform runs as template mode with no template and fails with 'Localizable template not found', or a "+
		"custom message with no body when a subject travelled with the template. Do not report it as configured: "+
		"install a package that supports template mode (install-process-builder after user approval - it "+processWriteInstallCost+") and re-apply the email block, "+
		"or pick the template in the designer.", processWriteNoun(len(unlanded)), strings.Join(unlanded, "', '"))
}

func processWriteUnresolvedMacros(d processWriteDescribed, macroElements []string) []string {
	unresolved := []string{}
	if len(macroElements) == 0 || !d.hasElements {
		return unresolved
	}
	for _, name := range macroElements {
		element := d.resolve(name)
		if element == nil {
			continue
		}
		email := processWriteEmailOf(element)
		if email == nil {
			continue
		}
		if hasBody := processWriteProperty(email, "hasBody"); hasBody != nil && hasBody.kind == jkBool && hasBody.flag &&
			strings.TrimSpace(processWriteText(email, "body")) == "" {
			unresolved = append(unresolved, name)
		}
	}
	return unresolved
}

func processWriteMacroWarning(unresolved []string) string {
	if len(unresolved) == 0 {
		return ""
	}
	return fmt.Sprintf("The operation reported success, but the Send email body for the %s '%s' carried "+
		"process-macro placeholders ([[param:…]] / [[element:…]]) that the read-back shows did NOT resolve "+
		"(the element reports a body but describe returned none). The usual cause is a deployed CrtProcessBuilder "+
		"that predates the body-macro feature: it stores the HTML verbatim, so the literal placeholder text "+
		"would be emailed to the recipient. Install a package that supports body macros "+
		"(install-process-builder after user approval - it "+processWriteInstallCost+") and re-apply the body, or insert the process data through the "+
		"designer's Content designer.", processWriteNoun(len(unresolved)), strings.Join(unresolved, "', '"))
}

// approval (described side)

const (
	processWriteDropWhole = iota
	processWriteDropApprover
	processWriteDropTemplate
	processWriteDropRecipient
	processWriteDropShaped
)

type processWriteApprovalDrop struct {
	element string
	kind    int
}

func processWriteApprovalMissing(d processWriteDescribed, expected []processWriteApprovalExpectation) []processWriteApprovalDrop {
	drops := []processWriteApprovalDrop{}
	if len(expected) == 0 || !d.hasElements {
		return drops
	}
	for _, expectation := range expected {
		if expectation.describeShaped {
			drops = append(drops, processWriteApprovalDrop{expectation.element, processWriteDropShaped})
			continue
		}
		element := d.resolve(expectation.element)
		if element == nil {
			continue
		}
		approval := processWriteProperty(element, "approval")
		if !approval.isObject() {
			drops = append(drops, processWriteApprovalDrop{expectation.element, processWriteDropWhole})
			continue
		}
		blank := func(key string) bool { return strings.TrimSpace(processWriteText(approval, key)) == "" }
		if expectation.approver && blank("approverType") {
			drops = append(drops, processWriteApprovalDrop{expectation.element, processWriteDropApprover})
		}
		if expectation.approverTemplate && blank("approverEmailTemplate") {
			drops = append(drops, processWriteApprovalDrop{expectation.element, processWriteDropTemplate})
		}
		if expectation.authorTemplate && blank("authorEmailTemplate") {
			drops = append(drops, processWriteApprovalDrop{expectation.element, processWriteDropTemplate})
		}
		if expectation.author && blank("recipient") {
			drops = append(drops, processWriteApprovalDrop{expectation.element, processWriteDropRecipient})
		}
	}
	return drops
}

func processWriteApprovalWarning(missing []processWriteApprovalDrop) string {
	if len(missing) == 0 {
		return ""
	}
	named := func(kind int) []string {
		names := []string{}
		for _, drop := range missing {
			if drop.kind != kind {
				continue
			}
			seen := false
			for _, name := range names {
				if name == drop.element {
					seen = true
				}
			}
			if !seen {
				names = append(names, drop.element)
			}
		}
		return names
	}
	shaped, whole, approver, template, recipient := named(processWriteDropShaped), named(processWriteDropWhole),
		named(processWriteDropApprover), named(processWriteDropTemplate), named(processWriteDropRecipient)
	parts := []string{}
	if len(shaped) > 0 {
		parts = append(parts, fmt.Sprintf("The 'approval' block sent for the %s '%s' is in the shape "+
			"describe REPORTS, not the shape create/modify accepts. The read shape is flat "+
			"('approverType', 'approverEmployee', a boolean 'notifyApprover' with 'approverEmailTemplate' "+
			"beside it); the write shape is nested ('approver': {type, employee}, 'notifyApprover': "+
			"{emailTemplate}), and 'recordId' is an object on write and a string on read. The values a "+
			"describe returns are accepted — the SHAPE has to be translated. Flat members bind to nothing "+
			"and are dropped without an error while the operation still answers success, so translate the "+
			"block and re-apply it before reporting this element as configured.", processWriteNoun(len(shaped)), strings.Join(shaped, "', '")))
	}
	if len(whole) > 0 {
		parts = append(parts, fmt.Sprintf("The operation reported success, but the saved process does NOT carry the 'approval' "+
			"configuration for the %s '%s' — the "+
			"read-back shows no approval block. The usual cause is a deployed CrtProcessBuilder that predates "+
			"the Approval element: it has no 'approval' member and does not implement IExtensibleDataObject, "+
			"so it discards the block instead of rejecting it and still answers success. Either way the "+
			"element is UNCONFIGURED (no approval object, no record under approval, no notifications), so do "+
			"not report it as configured.", processWriteNoun(len(whole)), strings.Join(whole, "', '")))
	}
	if len(approver) > 0 {
		parts = append(parts, fmt.Sprintf("The operation reported success and the approval block came back, but WITHOUT the approver "+
			"that was sent, for the %s '%s'. The usual cause is a deployed CrtProcessBuilder that has "+
			"the Approval element but predates its 'approver' member, which it discards the same silent way. "+
			"The element therefore has NOBODY assigned to approve it: it saves and runs, and the approval it "+
			"raises cannot be acted on, so do not report it as configured.", processWriteNoun(len(approver)), strings.Join(approver, "', '")))
	}
	if len(template) > 0 {
		parts = append(parts, fmt.Sprintf("The operation reported success, but a notification switched ON came back with NO email "+
			"template stored, for the %s '%s'. The send then fails inside the runtime and is swallowed "+
			"because approval elements ignore email errors by default, so the element reports the "+
			"notification as configured and never sends. Do not report it as configured.", processWriteNoun(len(template)), strings.Join(template, "', '")))
	}
	if len(recipient) > 0 {
		parts = append(parts, fmt.Sprintf("The operation reported success, but the author notification came back with NO recipient "+
			"resolved, for the %s '%s'. \"Author\" resolves nobody on its own — the runtime "+
			"reads only the address that was written — so the notification is switched on and silently "+
			"sends to no one.", processWriteNoun(len(recipient)), strings.Join(recipient, "', '")))
	}
	for _, drop := range missing {
		if drop.kind != processWriteDropShaped {
			parts = append(parts, "Check the package version, install one that supports what you sent "+
				"(install-process-builder after user approval - it "+processWriteInstallCost+") and re-apply the approval block, or configure the element in the "+
				"designer.")
			break
		}
	}
	return strings.Join(parts, " ")
}

// flow labels (described side)

type processWriteLabelMiss struct {
	wanted processWriteFlowLabel
	drawn  string
}

// findFlow is FindFlow: exactly one described flow between the two names.
func (d processWriteDescribed) findFlow(wanted processWriteFlowLabel) *jnode {
	var found *jnode
	count := 0
	for _, flow := range d.flows {
		if strings.EqualFold(processWriteText(flow, "source"), wanted.source) && strings.EqualFold(processWriteText(flow, "target"), wanted.target) {
			count++
			found = flow
			if count == 2 {
				return nil
			}
		}
	}
	return found
}

func processWriteLabelsMissing(d processWriteDescribed, expected []processWriteFlowLabel) []processWriteLabelMiss {
	missing := []processWriteLabelMiss{}
	if len(expected) == 0 || !d.hasFlows {
		return missing
	}
	for _, wanted := range expected {
		flow := d.findFlow(wanted)
		if flow == nil {
			continue
		}
		drawn := strings.TrimSpace(processWriteText(flow, "label"))
		if wanted.label == "" {
			if drawn != "" {
				missing = append(missing, processWriteLabelMiss{wanted, drawn})
			}
			continue
		}
		if drawn != wanted.label {
			missing = append(missing, processWriteLabelMiss{wanted, drawn})
		}
	}
	return missing
}

func processWriteUIDAddressed(d processWriteDescribed, expected []processWriteFlowLabel) []processWriteFlowLabel {
	addressed := []processWriteFlowLabel{}
	for _, wanted := range expected {
		if processWriteParseGUID(wanted.source) == "" && processWriteParseGUID(wanted.target) == "" {
			continue
		}
		if !d.hasFlows || d.findFlow(wanted) == nil {
			addressed = append(addressed, wanted)
		}
	}
	return addressed
}

func processWriteFlowSubject(count int) string {
	if count == 1 {
		return "flow"
	}
	return "flows"
}

func processWriteJoinFlows(count int, render func(int) string) string {
	rendered := []string{}
	for index := 0; index < count && index < processWriteMaxRenderedFlows; index++ {
		rendered = append(rendered, render(index))
	}
	listed := strings.Join(rendered, ", ")
	if hidden := count - processWriteMaxRenderedFlows; hidden > 0 {
		return fmt.Sprintf("%s (+ %d more)", listed, hidden)
	}
	return listed
}

func processWriteEndpoints(flow processWriteFlowLabel) string {
	return processWriteSanitize(flow.source) + " -> " + processWriteSanitize(flow.target)
}

func processWriteDescribeFlows(flows []processWriteFlowLabel) string {
	return processWriteJoinFlows(len(flows), func(index int) string {
		return fmt.Sprintf("%s ('%s')", processWriteEndpoints(flows[index]), processWriteSanitize(flows[index].label))
	})
}

func processWriteUIDAddressedWarning(addressed []processWriteFlowLabel) string {
	if len(addressed) == 0 {
		return ""
	}
	return fmt.Sprintf("The operation reported success, but the diagram label on the %s "+
		"%s could NOT be verified: the flow was addressed by UId, and the "+
		"read-back reports a flow's endpoints as element NAMES, so there is nothing to compare. A "+
		"CrtProcessBuilder that PREDATES the flow label discards it silently, and this read-back "+
		"is the only signal that it did. Re-send the same operation naming the SOURCE and TARGET "+
		"elements, or re-read the process with describe-business-process mode=full, before reporting the label "+
		"as applied.", processWriteFlowSubject(len(addressed)), processWriteDescribeFlows(addressed))
}

func processWriteLabelsWarning(missing []processWriteLabelMiss) string {
	if len(missing) == 0 {
		return ""
	}
	var absent, different, notCleared []processWriteLabelMiss
	for _, miss := range missing {
		switch {
		case miss.drawn == "":
			absent = append(absent, miss)
		case miss.wanted.label == "":
			notCleared = append(notCleared, miss)
		default:
			different = append(different, miss)
		}
	}
	parts := []string{}
	if len(absent) > 0 {
		wanted := make([]processWriteFlowLabel, len(absent))
		for index, miss := range absent {
			wanted[index] = miss.wanted
		}
		parts = append(parts, fmt.Sprintf("The read-back shows no diagram label on the %s "+
			"%s. The usual cause is a deployed CrtProcessBuilder that PREDATES "+
			"the flow label: it declares no 'label' field on a flow and therefore discards it "+
			"silently. This is USUALLY a CAPABILITY gap rather than an old version number: clio normally "+
			"refuses an environment running a package older than the one it ships, so what is missing is "+
			"the member rather than a higher number. Reinstall the package clio ships "+
			"(install-process-builder after user approval - it "+processWriteInstallCost+") and "+
			"re-apply the labels, or "+
			"label the connectors in the process designer. Until then a decision with two branches "+
			"renders as two identical unlabelled arrows.", processWriteFlowSubject(len(absent)), processWriteDescribeFlows(wanted)))
	}
	if len(different) > 0 {
		parts = append(parts, fmt.Sprintf("The read-back shows DIFFERENT text on the %s "+
			"%s. The package version is NOT the cause here - one that "+
			"discards the field leaves nothing rather than something else - so the label reached the "+
			"server and came back changed: the process was edited elsewhere between the write and this "+
			"read, or the text was normalised on the way in. Re-read it with describe-business-process mode=full "+
			"and re-apply only if the drawn text is not what the diagram should say.", processWriteFlowSubject(len(different)),
			processWriteJoinFlows(len(different), func(index int) string {
				miss := different[index]
				return fmt.Sprintf("%s (asked for '%s', drawn '%s')", processWriteEndpoints(miss.wanted), processWriteSanitize(miss.wanted.label), processWriteSanitize(miss.drawn))
			})))
	}
	if len(notCleared) > 0 {
		parts = append(parts, fmt.Sprintf("The %s %s still carries a diagram "+
			"label after being asked to drop it, so the connector still reads the old text. Re-apply the "+
			"clear, or remove the label in the process designer. Updating the package will not help: one "+
			"that discards the field leaves the old label exactly where it was.", processWriteFlowSubject(len(notCleared)),
			processWriteJoinFlows(len(notCleared), func(index int) string {
				miss := notCleared[index]
				return fmt.Sprintf("%s (still drawn: '%s')", processWriteEndpoints(miss.wanted), processWriteSanitize(miss.drawn))
			})))
	}
	return "The operation reported success. " + strings.Join(parts, " ")
}

func processWriteUnverifiedLabels(expected []processWriteFlowLabel, reason string) string {
	if len(expected) == 0 {
		return ""
	}
	return fmt.Sprintf("Could not verify that the diagram label on the %s "+
		"%s landed: "+
		"%s. The operation itself succeeded. A CrtProcessBuilder that PREDATES the flow label "+
		"discards the field silently, and this read-back is the only signal that it did — re-read the "+
		"process with describe-business-process mode=full before reporting the labels as applied.",
		processWriteFlowSubject(len(expected)), processWriteDescribeFlows(expected), reason)
}

// processWriteSanitize is TextUtilities.SanitizeForDisplay(text, 200): control, format and line/paragraph
// separator characters become spaces, lone surrogates are dropped, and more than 200 UTF-16 units are cut
// with "...".
func processWriteSanitize(text string) string {
	if text == "" {
		return text
	}
	units := []uint16{}
	for _, r := range text {
		if len(units) > processWriteMaxRenderedText {
			break
		}
		if r == utf8RuneError {
			continue
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r) {
			r = ' '
		}
		units = append(units, utf16.Encode([]rune{r})...)
	}
	if len(units) <= processWriteMaxRenderedText {
		return string(utf16.Decode(units))
	}
	cut := processWriteMaxRenderedText
	if units[cut-1] >= 0xD800 && units[cut-1] <= 0xDBFF {
		cut--
	}
	return string(utf16.Decode(units[:cut])) + "..."
}

// ------------------------------------------------------------------------------------------ flows

// warnIf is BlockExpectationReporter.Warn: it writes a non-empty warning and says whether it did.
func (l *processWriteLog) warnIf(text string) bool {
	if text == "" {
		return false
	}
	l.warning(text)
	return true
}

// processWriteReportDescribed is BlockExpectationReporter.ReportDescribed.
func processWriteReportDescribed(log *processWriteLog, d processWriteDescribed, intent processWriteIntent) {
	log.warnIf(processWriteUnverifiedRights(processWriteUnresolved(d, intent.configuredRights), processWriteUnresolvedReason))
	log.warnIf(processWriteUnverifiedFilter(processWriteUnresolved(d, intent.refilteredOnly()), processWriteUnresolvedReason))
	log.warnIf(processWriteUnreportableFilterWarning(d, intent.filterTouched))
	log.warnIf(processWriteLossyReadWarning(d, intent.configuredRights))
	log.warnIf(processWriteNoFilterWarning(d, intent.recordFilterSubjects()))
	log.warnIf(processWriteRightsWarning(processWriteRightsMissing(d, intent.configuredRights)))
	log.warnIf(processWriteEmailWarning(processWriteEmailMissing(d, intent.configuredEmail)))
	log.warnIf(processWriteTemplateWarning(processWriteUnlandedTemplates(d, intent.templatedEmail)))
}

func processWriteReportLabels(log *processWriteLog, d processWriteDescribed, labels []processWriteFlowLabel) {
	log.warnIf(processWriteUIDAddressedWarning(processWriteUIDAddressed(d, labels)))
	log.warnIf(processWriteLabelsWarning(processWriteLabelsMissing(d, labels)))
}

// processWriteWarnUnverified is WarnAccessRightsUnverified plus WarnFlowLabelsUnverified: whether either wrote.
func processWriteWarnUnverified(log *processWriteLog, intent processWriteIntent, labels []processWriteFlowLabel, reason string) bool {
	rights := log.warnIf(processWriteUnverifiedRights(intent.configuredRights, reason))
	filters := log.warnIf(processWriteUnverifiedFilter(intent.refilteredOnly(), reason))
	flowLabels := log.warnIf(processWriteUnverifiedLabels(labels, reason))
	return rights || filters || flowLabels
}

// processWriteVerifyCreate is CreateBusinessProcessCommand.WarnOnDiscardedConfigurationBlocks (8.1.0.142): the
// read-back always runs, since it is also the post-write digest (ENG-103023), and it is handed to *saved as
// soon as it is taken; the guards still run only when the descriptor asked for them. A caller that no guard
// needs read back is not read back (ENG-103165). The result says whether the reply already explains why there
// is no read-back, so the digest does not say it twice.
func (c *Client) processWriteVerifyCreate(ctx context.Context, descriptorText, schemaName string, log *processWriteLog,
	saved **processWriteRead) bool {
	descriptor := processWriteParseBlocks(descriptorText)
	intent := processWriteIntentFromDescriptor(descriptor)
	approval := processWriteApprovalFromDescriptor(descriptor)
	labels := processWriteFlowLabelsFromDescriptor(descriptor)
	guarded := !(intent.isEmpty() && len(approval) == 0 && len(labels) == 0)
	if strings.TrimSpace(schemaName) == "" {
		return processWriteWarnUnverified(log, intent, labels, "the operation returned no process name to read back")
	}
	if !guarded && len(processWriteElementsCarrying(descriptor, processWriteSubProcessKey)) > 0 {
		log.info(processWriteCallerNotReadBack)
		return true
	}
	read := c.processWriteDigestRead(ctx, schemaName, "", true)
	*saved = &read
	if !guarded {
		return false
	}
	if read.err != nil {
		return processWriteWarnUnverified(log, intent, labels, read.err.Error())
	}
	described := processWriteDescribedOf(read.raw)
	processWriteReportDescribed(log, described, intent)
	log.warnIf(processWriteApprovalWarning(processWriteApprovalMissing(described, approval)))
	log.warnIf(processWriteMacroWarning(processWriteUnresolvedMacros(described, processWriteMacroBodyElements(descriptor))))
	processWriteReportLabels(log, described, labels)
	return false
}

// processWriteVerifyModify is ModifyBusinessProcessCommand.WarnOnDiscardedConfigurationBlocks (8.1.0.142), with
// the same read-back hand-out and result as processWriteVerifyCreate; before is the read taken before the edit.
func (c *Client) processWriteVerifyModify(ctx context.Context, request ProcessWriteModifyRequest, schemaName string, log *processWriteLog,
	before processWriteRead, saved **processWriteRead) bool {
	operations := processWriteParseBlocks(request.Operations)
	intent := processWriteIntentFromOperations(operations)
	approval := processWriteApprovalFromOperations(operations)
	labels := processWriteFlowLabelsFromOperations(operations)
	ignored := []string{}
	for _, name := range processWriteDistinct(processWriteAddElementTargets(operations, processWriteAccessRightsKey)) {
		if !processWriteContainsFold(intent.configuredRights, name) && !processWriteContainsFold(ignored, name) {
			ignored = append(ignored, name)
		}
	}
	log.warnIf(processWriteAddElementWarning(ignored))
	guarded := !(intent.isEmpty() && len(approval) == 0 && len(labels) == 0)
	code := schemaName
	if strings.TrimSpace(code) == "" {
		code = request.ProcessName
	}
	if strings.TrimSpace(code) == "" && strings.TrimSpace(request.ProcessUID) == "" {
		return processWriteWarnUnverified(log, intent, labels, "the edit returned no process identity to read back")
	}
	if !guarded {
		if reason := processWriteWhyAnEditIsNotReadBack(operations, before); reason != "" {
			log.info(reason)
			return true
		}
	}
	read := c.processWriteDigestRead(ctx, code, request.ProcessUID, true)
	*saved = &read
	if !guarded {
		return false
	}
	if read.err != nil {
		return processWriteWarnUnverified(log, intent, labels, read.err.Error())
	}
	described := processWriteDescribedOf(read.raw)
	processWriteReportDescribed(log, described, intent)
	log.warnIf(processWriteApprovalWarning(processWriteApprovalMissing(described, approval)))
	processWriteReportLabels(log, described, labels)
	return false
}
