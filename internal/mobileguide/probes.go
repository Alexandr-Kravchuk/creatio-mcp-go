package mobileguide

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/redact"
)

// The HTTP halves of the four best-effort probes MobilePageConversionGuideTool runs before the engine:
// MobileSectionRegistrationProbe, PageBusinessRuleProbe, MobileActionTargetProbe and ExistingMobilePageProbe
// (with SchemaNameResolver and DefaultPageAddonReader). Every read is a GET or a read-only POST; a failure is
// reported in the probe's own result and never stops the guide.

// ProbeClient is what the probes read from the environment (clio's IApplicationClient, IServiceUrlBuilder,
// IAddonSchemaDesignerClient and IPageBusinessRuleSchemaProvider for one environment).
type ProbeClient interface {
	// ODataGet is IApplicationClient.ExecuteGetRequest(urlBuilder.Build(path), 30 s): the body whatever the status.
	ODataGet(ctx context.Context, path string) ([]byte, error)
	// Select posts one DataService SelectQuery and returns its rows (DataServiceSelectResponse.ReadRows).
	Select(ctx context.Context, query map[string]any) ([]map[string]json.RawMessage, error)
	// AddonSchema is AddonSchemaDesignerClient.GetSchema.
	AddonSchema(ctx context.Context, request map[string]any) (*AddonSchema, error)
	// PageRuleContext is PageBusinessRuleSchemaProvider.GetSchema: the page uId and its parent uId as seen from
	// the package ("" when the hierarchy has one entry).
	PageRuleContext(ctx context.Context, pageSchemaName, packageUID string) (schemaUID, parentUID string, err error)
}

// AddonSchema is the part of AddonSchemaDto the probes read.
type AddonSchema struct {
	MetaData  string
	Resources []AddonResource
}

const (
	emptyGUIDText               = "00000000-0000-0000-0000-000000000000"
	kindWebPage                 = "web-page"
	kindEntityDefaultMobilePage = "entity-default-mobile-page"
	kindSection                 = "section"
	rowsPerNameHeadroom         = 4
	maxEntityAddonProbes        = 32
	inFilterChunkSize           = 400
	maxTargetWalkDepth          = 128
	entitySchemaManagerName     = "EntitySchemaManager"
	clientUnitSchemaManagerName = "ClientUnitSchemaManager"
	businessRuleAddonName       = "BusinessRule"
	mobileRelatedPageAddonName  = "MobileRelatedPage"
	relatedPageAddonName        = "RelatedPage"
)

// parseGUID is Guid.TryParse followed by ToString(): the N, D, B and P formats, written lower case with hyphens.
func parseGUID(value string) (string, bool) {
	value = dotnetTrim(value)
	if len(value) == 38 && ((value[0] == '{' && value[37] == '}') || (value[0] == '(' && value[37] == ')')) {
		value = value[1:37]
	}
	digits := value
	if len(value) == 36 {
		if value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
			return "", false
		}
		digits = value[:8] + value[9:13] + value[14:18] + value[19:23] + value[24:]
	}
	if len(digits) != 32 {
		return "", false
	}
	if _, err := hex.DecodeString(digits); err != nil {
		return "", false
	}
	lower := strings.ToLower(digits)
	return lower[:8] + "-" + lower[8:12] + "-" + lower[12:16] + "-" + lower[16:20] + "-" + lower[20:], true
}

// rowCell is JToken.ToString() of a DataService cell (null and absent as nil).
func rowCell(row map[string]json.RawMessage, key string) *string {
	raw, ok := row[key]
	if !ok || strings.TrimSpace(string(raw)) == "null" {
		return nil
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return &text
	}
	value := strings.TrimSpace(string(raw))
	return &value
}

// elementStr is MobileSectionRegistrationProbe.Str: JsonElement.ToString() of a non-null property.
func elementStr(element map[string]json.RawMessage, property string) *string {
	raw, ok := element[property]
	if !ok || strings.TrimSpace(string(raw)) == "null" {
		return nil
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return &text
	}
	value := strings.TrimSpace(string(raw))
	switch value {
	case "true":
		value = "True"
	case "false":
		value = "False"
	}
	return &value
}

func describeFailure(what string, err error) string {
	if err == nil {
		return what + "."
	}
	detail := redact.Text(err.Error())
	if detail == "" {
		return what + "."
	}
	return what + ": " + detail + "."
}

// ---- MobileSectionRegistrationProbe ----

func manualEditPageActionOrSkip(isFormPage bool) string {
	if isFormPage {
		return "Edit page: after creating the mobile form page, register it as the object's default MOBILE edit page with create-related-page-addon (schema-type=mobile, a single is-default page → the MobileRelatedPage add-on)."
	}
	return "No additional section registration action is required for this page."
}

// odataPath is urlBuilder.Build("odata/{entity}?{query}") as .NET's Uri sends it: spaces percent-encoded.
func odataPath(entity, query string) string {
	return "odata/" + entity + "?" + strings.ReplaceAll(query, " ", "%20")
}

func odataRows(ctx context.Context, client ProbeClient, entity, query string) ([]map[string]json.RawMessage, error) {
	payload, err := client.ODataGet(ctx, odataPath(entity, query))
	if err != nil {
		return nil, err
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(payload, &root); err != nil {
		var any json.RawMessage
		if json.Unmarshal(payload, &any) != nil {
			return nil, err
		}
		return []map[string]json.RawMessage{}, nil
	}
	value, ok := root["value"]
	trimmed := strings.TrimSpace(string(value))
	if !ok || !strings.HasPrefix(trimmed, "[") {
		return []map[string]json.RawMessage{}, nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(value, &items); err != nil {
		return nil, err
	}
	rows := make([]map[string]json.RawMessage, 0, len(items))
	for _, item := range items {
		var row map[string]json.RawMessage
		if json.Unmarshal(item, &row) != nil || row == nil {
			// JsonElement.TryGetProperty on a non-object throws.
			return nil, errors.New("The requested operation requires an element of type 'Object', but the target element has a different type.")
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// ProbeSectionRegistration is MobileSectionRegistrationProbe.Probe.
func ProbeSectionRegistration(ctx context.Context, client ProbeClient, pageSchemaUID *string, isFormPage bool) *SectionRegistrationInfo {
	notProbed := func(note string) *SectionRegistrationInfo {
		return &SectionRegistrationInfo{IsFormPage: isFormPage, Note: strp(note),
			RegistrationActions: []string{manualEditPageActionOrSkip(isFormPage)}}
	}
	if client == nil || pageSchemaUID == nil || isBlank(*pageSchemaUID) {
		return notProbed("Section registration was not probed (missing environment client or page schema UId).")
	}
	pageUID, ok := parseGUID(*pageSchemaUID)
	if !ok {
		return notProbed("Section registration was not probed (page schema UId is not a valid GUID).")
	}
	failed := func() *SectionRegistrationInfo {
		return notProbed("The environment could not be queried for section registration, so none of these " +
			"flags was established. Verify section / workplace registration manually.")
	}
	modules, err := odataRows(ctx, client, "SysModule", "$select=Id,Code,Caption,SectionSchemaUId,SysPageSchemaUId,MobileSectionSchemaUId&$filter="+
		fmt.Sprintf("SectionSchemaUId eq %s or SysPageSchemaUId eq %s", pageUID, pageUID))
	if err != nil {
		return failed()
	}
	if len(modules) == 0 {
		action := "The source page is not a registered section — no SysModule registration is needed (or register it as a new section manually)."
		if isFormPage {
			action = manualEditPageActionOrSkip(true)
		}
		return &SectionRegistrationInfo{SourcePageIsSection: ptr(false), IsFormPage: isFormPage, ProbeOk: true,
			Note: strp("The source page is not registered as a section in SysModule."), RegistrationActions: []string{action}}
	}
	module := modules[0]
	sysModuleID := elementStr(module, "Id")
	mobileUID := elementStr(module, "MobileSectionSchemaUId")
	mobileRegistered := mobileUID != nil && !isBlank(*mobileUID) && !strings.EqualFold(*mobileUID, emptyGUIDText)
	currentWorkplaceIDs := newSet(true)
	links, err := odataRows(ctx, client, "SysModuleInWorkplace", "$select=SysWorkplaceId&$filter=SysModuleId eq "+deref(sysModuleID))
	if err != nil {
		return failed()
	}
	for _, link := range links {
		if id := elementStr(link, "SysWorkplaceId"); id != nil && !isBlank(*id) {
			currentWorkplaceIDs.add(*id)
		}
	}
	mobileClientTypeID := resolveMobileClientTypeID(ctx, client)
	current, availableMobile := []*WorkplaceInfo{}, []*WorkplaceInfo{}
	workplaces, err := odataRows(ctx, client, "SysWorkplace", "$select=Id,Name,SysApplicationClientTypeId&$top=100")
	if err != nil {
		return failed()
	}
	for _, workplace := range workplaces {
		id := elementStr(workplace, "Id")
		clientTypeID := elementStr(workplace, "SysApplicationClientTypeId")
		isMobile := mobileClientTypeID != nil && clientTypeID != nil && strings.EqualFold(*clientTypeID, *mobileClientTypeID)
		contains := id != nil && currentWorkplaceIDs.contains(*id)
		info := &WorkplaceInfo{ID: id, Name: elementStr(workplace, "Name"), IsMobile: isMobile, ContainsSection: contains}
		if contains {
			current = append(current, info)
		}
		if isMobile {
			availableMobile = append(availableMobile, info)
		}
	}
	var mobileSchema *string
	if mobileRegistered {
		mobileSchema = mobileUID
	}
	code := elementStr(module, "Code")
	return &SectionRegistrationInfo{SourcePageIsSection: ptr(true), SysModuleID: sysModuleID, SectionCode: code,
		SectionCaption: elementStr(module, "Caption"), MobileSectionSchemaUID: mobileSchema, MobileSectionRegistered: ptr(mobileRegistered),
		IsFormPage: isFormPage, CurrentWorkplaces: current, AvailableMobileWorkplaces: availableMobile, ProbeOk: true,
		RegistrationActions: sectionActions(deref(sysModuleID), deref(code), mobileRegistered, availableMobile, isFormPage)}
}

func sectionActions(sysModuleID, sectionCode string, mobileRegistered bool, availableMobile []*WorkplaceInfo, isFormPage bool) []string {
	var actions []string
	if mobileRegistered {
		actions = append(actions, fmt.Sprintf("Section '%s' already has a mobile page (SysModule.MobileSectionSchemaUId is set); updating it would replace the current mobile section page.", sectionCode))
	} else {
		actions = append(actions, fmt.Sprintf("After creating the mobile list page, set SysModule.MobileSectionSchemaUId = <new mobile page schema UId> on row '%s' (id %s) with odata-update (confirm=true).", sectionCode, sysModuleID))
	}
	if len(availableMobile) > 0 {
		var names []string
		for _, workplace := range availableMobile {
			if workplace.Name != nil && !isBlank(*workplace.Name) && len(names) < 5 {
				names = append(names, *workplace.Name)
			}
		}
		actions = append(actions, fmt.Sprintf("Add the section to a mobile workplace (e.g. %s) with odata-create SysModuleInWorkplace {SysModuleId, SysWorkplaceId, Position}, or create a new mobile workplace — the user decides.", strings.Join(names, ", ")))
	} else {
		actions = append(actions, "No mobile workplace was found. Create one with odata-create SysWorkplace (Mobile client type), then add the section with odata-create SysModuleInWorkplace — the user decides.")
	}
	if isFormPage {
		actions = append(actions, manualEditPageActionOrSkip(true))
	}
	return actions
}

func resolveMobileClientTypeID(ctx context.Context, client ProbeClient) *string {
	types, err := odataRows(ctx, client, "SysApplicationClientType", "$select=Id,Name&$top=50")
	if err != nil {
		return nil
	}
	for _, clientType := range types {
		if name := elementStr(clientType, "Name"); name != nil && !isBlank(*name) && strings.Contains(strings.ToLower(*name), "mobile") {
			return elementStr(clientType, "Id")
		}
	}
	return nil
}

// ---- PageBusinessRuleProbe ----

// ProbePageBusinessRules is PageBusinessRuleProbe.Probe.
func ProbePageBusinessRules(ctx context.Context, client ProbeClient, pageSchemaName, packageUID *string) *PageBusinessRuleProbeResult {
	if client == nil || pageSchemaName == nil || isBlank(*pageSchemaName) {
		return &PageBusinessRuleProbeResult{Note: strp("Page-level business rules were not probed (missing environment client or page schema name).")}
	}
	packageGUID, ok := "", false
	if packageUID != nil && !isBlank(*packageUID) {
		packageGUID, ok = parseGUID(*packageUID)
	}
	if !ok {
		return &PageBusinessRuleProbeResult{Note: strp("Page-level business rules were not probed (the source page package could not be resolved).")}
	}
	failure := func(err error) *PageBusinessRuleProbeResult {
		return &PageBusinessRuleProbeResult{Note: strp("Could not read page-level business rules (" + redact.Text(err.Error()) + "). " +
			"Review and recreate them manually.")}
	}
	schemaUID, parentUID, err := client.PageRuleContext(ctx, *pageSchemaName, packageGUID)
	if err != nil {
		return failure(err)
	}
	target, ok := parseGUID(schemaUID)
	if !ok {
		return failure(errors.New("Unrecognized Guid format."))
	}
	parent := emptyGUIDText
	if !isBlank(parentUID) {
		if parent, ok = parseGUID(parentUID); !ok {
			return failure(errors.New("Unrecognized Guid format."))
		}
	}
	schema, err := client.AddonSchema(ctx, map[string]any{"addonName": businessRuleAddonName, "targetSchemaUId": target,
		"targetParentSchemaUId": parent, "targetPackageUId": packageGUID, "targetSchemaManagerName": clientUnitSchemaManagerName,
		"useFullHierarchy": true})
	if err != nil {
		return failure(err)
	}
	metaData, resources := "", []AddonResource(nil)
	if schema != nil {
		metaData, resources = schema.MetaData, schema.Resources
	}
	rules := ParsePageBusinessRules(metaData, resources)
	result := &PageBusinessRuleProbeResult{ProbeOk: true, Rules: rules}
	if len(rules) == 0 {
		result.Note = strp("No page-level business rules found on the source page.")
	}
	return result
}

// ---- MobileActionTargetProbe ----

type actionTargetOccurrence struct {
	kind, target string
}

func isRecognizedTargetKind(kind *string) bool {
	return kind != nil && (strings.EqualFold(*kind, kindWebPage) || strings.EqualFold(*kind, kindEntityDefaultMobilePage))
}

func buildTargetedRequestMap(rules *Rules) *dict[*RequestMappingRule] {
	result := newCI[*RequestMappingRule]()
	if rules == nil {
		return result
	}
	for _, rule := range rules.Requests {
		if rule != nil && rule.Web != nil && !isBlank(*rule.Web) && rule.TargetParam != nil && !isBlank(*rule.TargetParam) &&
			isRecognizedTargetKind(rule.TargetKind) {
			result.set(*rule.Web, rule)
		}
	}
	return result
}

func collectActionTargets(viewConfig *jn, targeted *dict[*RequestMappingRule]) []actionTargetOccurrence {
	found := []actionTargetOccurrence{}
	if viewConfig == nil || !viewConfig.isArr() || targeted.len() == 0 {
		return found
	}
	for _, node := range viewConfig.arr {
		walkForTargets(node, targeted, &found, 0)
	}
	return found
}

func walkForTargets(node *jn, targeted *dict[*RequestMappingRule], found *[]actionTargetOccurrence, depth int) {
	if depth > maxTargetWalkDepth || node == nil {
		return
	}
	if node.isArr() {
		for _, item := range node.arr {
			walkForTargets(item, targeted, found, depth+1)
		}
		return
	}
	if !node.isObj() {
		return
	}
	for _, key := range node.keyList() {
		value := node.get(key)
		if value.isObj() {
			if request := relatedStr(value, "request"); request != nil && !isBlank(*request) {
				recordOccurrence(*request, value, targeted, found)
				continue
			}
		}
		walkForTargets(value, targeted, found, depth+1)
	}
}

func recordOccurrence(request string, binding *jn, targeted *dict[*RequestMappingRule], found *[]actionTargetOccurrence) {
	rule, ok := targeted.get(request)
	if !ok {
		return
	}
	target := ""
	if parameters := binding.get("params"); parameters.isObj() {
		if value := relatedStr(parameters, *rule.TargetParam); value != nil {
			target = dotnetTrim(*value)
		}
	}
	if target == "" || strings.HasPrefix(target, "$") {
		return
	}
	*found = append(*found, actionTargetOccurrence{kind: *rule.TargetKind, target: target})
}

func distinctTargetsOfKind(occurrences []actionTargetOccurrence, kind string) []string {
	seen := newSet(true)
	result := []string{}
	for _, occurrence := range occurrences {
		if strings.EqualFold(occurrence.kind, kind) && seen.add(occurrence.target) {
			result = append(result, occurrence.target)
		}
	}
	return result
}

func recordTarget(into *dict[*ActionTargetResolution], kind, target string, state int, candidate *string) {
	into.set(targetKey(&kind, &target), &ActionTargetResolution{Kind: strp(kind), Target: strp(target), State: state,
		ResolvedCandidateSchemaName: candidate})
}

// ProbeActionTargets is MobileActionTargetProbe.Probe.
func ProbeActionTargets(ctx context.Context, client ProbeClient, viewConfig *jn, rules *Rules, modelConfig *jn, pagePackageUID *string) *ActionTargetsProbeResult {
	targeted := buildTargetedRequestMap(rules)
	if targeted.len() == 0 {
		return &ActionTargetsProbeResult{Note: strp("Action targets were not verified (the conversion rules declare no request navigation targets).")}
	}
	sourceEntities := collectSourceEntityNames(modelConfig)
	all := collectActionTargets(viewConfig, targeted)
	occurrences := all
	if sourceEntities.len() > 0 {
		occurrences = []actionTargetOccurrence{}
		for _, occurrence := range all {
			if !strings.EqualFold(occurrence.kind, kindEntityDefaultMobilePage) || !sourceEntities.contains(occurrence.target) {
				occurrences = append(occurrences, occurrence)
			}
		}
	}
	if len(occurrences) == 0 {
		return &ActionTargetsProbeResult{ProbeOk: true}
	}
	resolutions := newCI[*ActionTargetResolution]()
	for _, webPage := range distinctTargetsOfKind(occurrences, kindWebPage) {
		recordTarget(resolutions, kindWebPage, webPage, TargetMissing, nil)
	}
	entityTargets := distinctTargetsOfKind(occurrences, kindEntityDefaultMobilePage)
	if len(entityTargets) == 0 {
		return &ActionTargetsProbeResult{ProbeOk: true, TargetsByKey: resolutions}
	}
	if client == nil {
		return &ActionTargetsProbeResult{Note: strp("Object action targets were not verified (missing environment client)."), TargetsByKey: resolutions}
	}
	if ctx.Err() != nil {
		return &ActionTargetsProbeResult{Note: strp("Object action targets were not verified (the check was cancelled before it completed)."),
			TargetsByKey: resolutions}
	}
	answered, note, err := resolveEntityTargets(ctx, client, entityTargets, pagePackageUID, resolutions)
	if err != nil {
		if ctx.Err() != nil {
			return &ActionTargetsProbeResult{Note: strp("Object action targets were not verified (the check was cancelled before it completed)."),
				TargetsByKey: resolutions}
		}
		return &ActionTargetsProbeResult{Note: strp(describeFailure("Could not verify object action targets", err) +
			" Check each action's target object manually."), TargetsByKey: resolutions}
	}
	return &ActionTargetsProbeResult{ProbeOk: answered, Note: note, TargetsByKey: resolutions}
}

type entitySchemaRows struct {
	uidByName, packageUIDByName *dict[string]
	seen                        *strSet
}

// readEntitySchemaRows is MobileActionTargetProbe.ReadEntitySchemaRows: the base (ExtendParent = false) row of
// each named object, in chunks of 400 names with four rows of headroom per name.
func readEntitySchemaRows(ctx context.Context, client ProbeClient, names []string, into entitySchemaRows) (bool, error) {
	truncated := false
	for start := 0; start < len(names); start += inFilterChunkSize {
		chunk := names[start:min(len(names), start+inFilterChunkSize)]
		requested := len(chunk) * rowsPerNameHeadroom
		query := esqQuery("SysSchema", map[string]any{"Name": esqColumn("Name"), "UId": esqColumn("UId"),
			"PackageUId": esqColumn("SysPackage.UId"),
			"ExtendParent": map[string]any{"expression": map[string]any{"expressionType": 0, "columnPath": "ExtendParent"},
				"orderDirection": 1, "orderPosition": 0}},
			esqGroup(map[string]any{"byName": esqIn("Name", chunk, 1), "byManager": esqEq("ManagerName", entitySchemaManagerName, 1)}), requested)
		rows, err := client.Select(ctx, query)
		if err != nil {
			return truncated, err
		}
		truncated = truncated || len(rows) >= requested
		for _, row := range rows {
			name := rowCell(row, "Name")
			if name == nil || isBlank(*name) {
				continue
			}
			into.seen.add(*name)
			uid := rowCell(row, "UId")
			if extendParent, ok := rowBool(row, "ExtendParent"); ok && !extendParent && uid != nil && !isBlank(*uid) {
				into.uidByName.set(*name, *uid)
				if packageUID := rowCell(row, "PackageUId"); packageUID != nil && !isBlank(*packageUID) {
					into.packageUIDByName.set(*name, *packageUID)
				}
			}
		}
	}
	return truncated, nil
}

// rowBool is JToken.Value<bool?>(): a boolean, or a string/number Json.NET converts.
func rowBool(row map[string]json.RawMessage, key string) (bool, bool) {
	raw, ok := row[key]
	if !ok {
		return false, false
	}
	var value bool
	if json.Unmarshal(raw, &value) == nil {
		return value, true
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		switch strings.ToLower(dotnetTrim(text)) {
		case "true":
			return true, true
		case "false":
			return false, true
		}
	}
	var number float64
	if json.Unmarshal(raw, &number) == nil {
		return number != 0, true
	}
	return false, false
}

func resolveEntityTargets(ctx context.Context, client ProbeClient, names []string, pagePackageUID *string,
	into *dict[*ActionTargetResolution]) (bool, *string, error) {
	fallbackPackageUID, ok := "", false
	if pagePackageUID != nil {
		fallbackPackageUID, ok = parseGUID(*pagePackageUID)
	}
	if !ok {
		for _, name := range names {
			recordTarget(into, kindEntityDefaultMobilePage, name, TargetUnknown, nil)
		}
		return false, strp("Object action targets were not verified (the source page package could not be resolved)."), nil
	}
	rows := entitySchemaRows{uidByName: newCI[string](), packageUIDByName: newCI[string](), seen: newSet(true)}
	truncated, err := readEntitySchemaRows(ctx, client, names, rows)
	if err != nil {
		return false, nil, err
	}
	type probeItem struct{ name, entityUID, packageUID string }
	var toProbe []probeItem
	for _, name := range names {
		entityUID, found := rows.uidByName.get(name)
		if !found {
			state := TargetMissing
			if rows.seen.contains(name) || truncated {
				state = TargetUnknown
			}
			recordTarget(into, kindEntityDefaultMobilePage, name, state, nil)
			continue
		}
		packageUID := fallbackPackageUID
		if raw, has := rows.packageUIDByName.get(name); has {
			if parsed, valid := parseGUID(raw); valid {
				packageUID = parsed
			}
		}
		toProbe = append(toProbe, probeItem{name, entityUID, packageUID})
	}
	budgetExhausted := len(toProbe) > maxEntityAddonProbes
	if budgetExhausted {
		for _, item := range toProbe[maxEntityAddonProbes:] {
			recordTarget(into, kindEntityDefaultMobilePage, item.name, TargetUnknown, nil)
		}
		toProbe = toProbe[:maxEntityAddonProbes]
	}
	// clio runs these reads in parallel (at most eight); the outcomes are indexed, so a sequential run gives
	// the same answer.
	failures := &candidateFailures{}
	type pendingCandidate struct{ name, pageUID string }
	var pending []pendingCandidate
	for _, item := range toProbe {
		if err := ctx.Err(); err != nil {
			return false, nil, err
		}
		state := readMobileState(ctx, client, item.entityUID, item.packageUID)
		var candidate *string
		var candidateFailure error
		if state == TargetMissing {
			candidate, candidateFailure = readWebDefaultPageUID(ctx, client, item.entityUID, item.packageUID)
		}
		if candidateFailure != nil {
			failures.record(1, candidateFailure)
		}
		if candidate != nil {
			pending = append(pending, pendingCandidate{item.name, *candidate})
		}
		recordTarget(into, kindEntityDefaultMobilePage, item.name, state, nil)
	}
	if len(pending) > 0 {
		var uids []string
		seen := map[string]bool{}
		for _, candidate := range pending {
			if !seen[candidate.pageUID] {
				seen[candidate.pageUID] = true
				uids = append(uids, candidate.pageUID)
			}
		}
		resolved, err := resolveSchemaNames(ctx, client, uids)
		if err != nil {
			failures.record(len(pending), err)
		} else {
			for _, candidate := range pending {
				result := resolved[candidate.pageUID]
				switch result.status {
				case nameRowMissing:
					failures.record(1, fmt.Errorf("Page schema '%s' could not be resolved to a name.", candidate.pageUID))
				case nameResolved:
					recordTarget(into, kindEntityDefaultMobilePage, candidate.name, TargetMissing, strp(result.name))
				}
			}
		}
	}
	return true, failures.note(budgetExhausted), nil
}

type candidateFailures struct {
	count int
	first error
}

func (f *candidateFailures) record(count int, err error) {
	f.count += count
	if f.first == nil {
		f.first = err
	}
}

func (f *candidateFailures) note(budgetExhausted bool) *string {
	var candidateNote *string
	if f.count > 0 {
		candidateNote = strp(describeFailure(fmt.Sprintf("Could not resolve a candidate web page for %d object(s)", f.count), f.first))
	}
	if !budgetExhausted {
		return candidateNote
	}
	budgetNote := fmt.Sprintf("Only the first %d object targets were checked; the rest are reported as unverified. Check them manually.",
		maxEntityAddonProbes)
	if candidateNote != nil {
		budgetNote += " " + *candidateNote
	}
	return &budgetNote
}

func relatedPageRequest(addonName, entityUID, packageUID string) map[string]any {
	return map[string]any{"addonName": addonName, "targetSchemaUId": entityUID, "targetParentSchemaUId": emptyGUIDText,
		"targetPackageUId": packageUID, "targetSchemaManagerName": entitySchemaManagerName, "useFullHierarchy": true}
}

// readMobileState is DefaultPageAddonReader.ReadMobileState.
func readMobileState(ctx context.Context, client ProbeClient, entitySchemaUID, packageUID string) int {
	entityUID, ok := parseGUID(entitySchemaUID)
	if !ok {
		return TargetUnknown
	}
	schema, err := client.AddonSchema(ctx, relatedPageRequest(mobileRelatedPageAddonName, entityUID, packageUID))
	if err != nil {
		return TargetUnknown
	}
	metaData := ""
	if schema != nil {
		metaData = schema.MetaData
	}
	return classifyRelatedPageMetadata(metaData)
}

// readWebDefaultPageUID is DefaultPageAddonReader.ReadWebDefaultPageUId.
func readWebDefaultPageUID(ctx context.Context, client ProbeClient, entitySchemaUID, packageUID string) (*string, error) {
	entityUID, ok := parseGUID(entitySchemaUID)
	if !ok {
		return nil, nil
	}
	schema, err := client.AddonSchema(ctx, relatedPageRequest(relatedPageAddonName, entityUID, packageUID))
	if err != nil {
		return nil, err
	}
	metaData := ""
	if schema != nil {
		metaData = schema.MetaData
	}
	pageSchemaUID := extractDefaultPageSchemaUID(metaData)
	if pageSchemaUID == nil || isBlank(*pageSchemaUID) {
		return nil, nil
	}
	pageUID, ok := parseGUID(*pageSchemaUID)
	if !ok {
		return nil, fmt.Errorf("Page schema '%s' could not be resolved to a name.", *pageSchemaUID)
	}
	return &pageUID, nil
}

// ---- SchemaNameResolver ----

const (
	nameRowMissing = iota
	nameInvalid
	nameResolved
)

type nameResult struct {
	status int
	name   string
}

// resolveSchemaNames is SchemaNameResolver.ResolveNames over lower-case D-format uIds.
func resolveSchemaNames(ctx context.Context, client ProbeClient, uids []string) (map[string]nameResult, error) {
	results := map[string]nameResult{}
	nameByUID := map[string]*string{}
	for start := 0; start < len(uids); start += inFilterChunkSize {
		chunk := uids[start:min(len(uids), start+inFilterChunkSize)]
		query := esqQuery("SysSchema", map[string]any{"UId": esqColumn("UId"), "Name": esqColumn("Name")},
			esqGroup(map[string]any{"byUId": esqIn("UId", chunk, 0)}), len(chunk)*rowsPerNameHeadroom)
		rows, err := client.Select(ctx, query)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if uid := rowCell(row, "UId"); uid != nil {
				if parsed, ok := parseGUID(*uid); ok {
					nameByUID[parsed] = rowCell(row, "Name")
				}
			}
		}
	}
	for _, uid := range uids {
		name, ok := nameByUID[uid]
		switch {
		case !ok:
			results[uid] = nameResult{status: nameRowMissing}
		case name != nil && *name != "" && isValidSchemaName(*name):
			results[uid] = nameResult{status: nameResolved, name: *name}
		default:
			results[uid] = nameResult{status: nameInvalid}
		}
	}
	return results, nil
}

func resolveSchemaName(ctx context.Context, client ProbeClient, uid string) nameResult {
	results, err := resolveSchemaNames(ctx, client, []string{uid})
	if err != nil {
		return nameResult{status: nameRowMissing}
	}
	if result, ok := results[uid]; ok {
		return result
	}
	return nameResult{status: nameRowMissing}
}

// ---- ExistingMobilePageProbe ----

// ProbeExistingMobilePages is ExistingMobilePageProbe.Probe.
func ProbeExistingMobilePages(ctx context.Context, client ProbeClient, section *SectionRegistrationInfo, isFormPage bool,
	modelConfig *jn, pagePackageUID *string, targetName string) []*ExistingMobilePageInfo {
	matches := []*ExistingMobilePageInfo{}
	if section != nil && section.MobileSectionRegistered != nil && *section.MobileSectionRegistered &&
		section.MobileSectionSchemaUID != nil && len(*section.MobileSectionSchemaUID) > 0 {
		if match := probeSectionMobilePage(ctx, client, *section.MobileSectionSchemaUID); match != nil && !strings.EqualFold(deref(match.SchemaName), targetName) {
			matches = append(matches, match)
		}
	}
	if isFormPage {
		for _, entity := range collectSourceEntityNames(modelConfig).keys() {
			if match := probeEntityDefaultMobilePage(ctx, client, entity, pagePackageUID); match != nil && !strings.EqualFold(deref(match.SchemaName), targetName) {
				matches = append(matches, match)
			}
		}
	}
	return matches
}

func probeSectionMobilePage(ctx context.Context, client ProbeClient, sectionSchemaUID string) *ExistingMobilePageInfo {
	sectionUID, ok := parseGUID(sectionSchemaUID)
	if client == nil || !ok || ctx.Err() != nil {
		return nil
	}
	if result := resolveSchemaName(ctx, client, sectionUID); result.status == nameResolved {
		return &ExistingMobilePageInfo{SchemaName: strp(result.name), SchemaUID: strp(sectionSchemaUID), Source: strp(kindSection)}
	}
	return nil
}

func probeEntityDefaultMobilePage(ctx context.Context, client ProbeClient, entity string, pagePackageUID *string) *ExistingMobilePageInfo {
	fallbackPackageUID, ok := "", false
	if pagePackageUID != nil {
		fallbackPackageUID, ok = parseGUID(*pagePackageUID)
	}
	if client == nil || isBlank(entity) || !ok || ctx.Err() != nil {
		return nil
	}
	rows := entitySchemaRows{uidByName: newCI[string](), packageUIDByName: newCI[string](), seen: newSet(true)}
	if _, err := readEntitySchemaRows(ctx, client, []string{entity}, rows); err != nil {
		return nil
	}
	rawEntityUID, found := rows.uidByName.get(entity)
	if !found {
		return nil
	}
	entityUID, ok := parseGUID(rawEntityUID)
	if !ok {
		return nil
	}
	packageUID := fallbackPackageUID
	if raw, has := rows.packageUIDByName.get(entity); has {
		if parsed, valid := parseGUID(raw); valid {
			packageUID = parsed
		}
	}
	schema, err := client.AddonSchema(ctx, relatedPageRequest(mobileRelatedPageAddonName, entityUID, packageUID))
	if err != nil {
		return nil
	}
	metaData := ""
	if schema != nil {
		metaData = schema.MetaData
	}
	rawPageUID := extractDefaultPageSchemaUID(metaData)
	if rawPageUID == nil || isBlank(*rawPageUID) {
		return nil
	}
	pageUID, ok := parseGUID(*rawPageUID)
	if !ok {
		return nil
	}
	if result := resolveSchemaName(ctx, client, pageUID); result.status == nameResolved {
		return &ExistingMobilePageInfo{SchemaName: strp(result.name), SchemaUID: rawPageUID, Source: strp(kindEntityDefaultMobilePage)}
	}
	return nil
}

// ---- the SelectQuery shapes of ClassicEntitySchemaQuery ----

func esqColumn(path string) map[string]any {
	return map[string]any{"expression": map[string]any{"expressionType": 0, "columnPath": path}}
}

func esqEq(column, value string, dataValueType int) map[string]any {
	return map[string]any{"filterType": 1, "comparisonType": 3, "isEnabled": true,
		"leftExpression":  map[string]any{"expressionType": 0, "columnPath": column},
		"rightExpression": map[string]any{"expressionType": 2, "parameter": map[string]any{"dataValueType": dataValueType, "value": value}}}
}

func esqIn(column string, values []string, dataValueType int) map[string]any {
	expressions := make([]any, 0, len(values))
	for _, value := range values {
		expressions = append(expressions, map[string]any{"expressionType": 2, "parameter": map[string]any{"dataValueType": dataValueType, "value": value}})
	}
	return map[string]any{"filterType": 4, "comparisonType": 3, "isEnabled": true,
		"leftExpression": map[string]any{"expressionType": 0, "columnPath": column}, "rightExpressions": expressions}
}

func esqGroup(items map[string]any) map[string]any {
	return map[string]any{"filterType": 6, "logicalOperation": 0, "isEnabled": true, "items": items}
}

func esqQuery(root string, columns, filters map[string]any, rowCount int) map[string]any {
	return map[string]any{"rootSchemaName": root, "operationType": 0, "columns": map[string]any{"items": columns},
		"filters": filters, "rowCount": rowCount}
}
