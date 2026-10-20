package creatio

// get-object-rights (clio's GetObjectRightsCommand with the MCP limits of GetObjectRightsTool): the operation
// layer and the record layer of an object and, optionally, of its own lookup objects. Facts only.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	objectRightsGuidancePointer = "What the rows mean for a scenario (external users, system operations, shared lookups): get-guidance object-rights."
	objectRightsPriorityRule    = "Rows are listed in priority order ([position], 0 is the highest). A user in several roles gets the operations " +
		"of the highest matching row; a row with no operations denies them."
)

// ObjectRightsGetRequest is GetObjectRightsArgs.
type ObjectRightsGetRequest struct {
	EntitySchemaName string
	Grantee          string
	Author           string
	IncludeConnected bool

	clock *rightsClock
}

type rightsRuleFilter struct{ author, grantee string }

func (f rightsRuleFilter) matches(rule recordRule) bool {
	return (f.author == "" || rule.authorID == f.author) && (f.grantee == "" || rule.granteeID == f.grantee)
}

// GetObjectRights runs get-object-rights: each request one attempt of at most 30 s, all within 90 s.
func (c *Client) GetObjectRights(ctx context.Context, input ObjectRightsGetRequest) ObjectRightsResponse {
	log := &rightsLog{}
	clock := realRightsClock()
	if input.clock != nil {
		clock = *input.clock
	}
	request := rightsRequest{timeout: 30 * time.Second, clock: clock, deadline: newRightsDeadline(90*time.Second, clock)}
	return log.response(c.getObjectRights(ctx, input, request, log))
}

func (c *Client) getObjectRights(ctx context.Context, input ObjectRightsGetRequest, request rightsRequest, log *rightsLog) int {
	named, ok := rightsReadObjectName(input.EntitySchemaName, log)
	if !ok {
		return 1
	}
	granteeFilter, ok := rightsUnitFilter(input.Grantee)
	if !ok {
		log.error("Error: --grantee must be a SysAdminUnit id (GUID).")
		return 1
	}
	authorFilter, ok := rightsUnitFilter(input.Author)
	if !ok {
		log.error("Error: --author must be a SysAdminUnit id (GUID).")
		return 1
	}
	ruleFilter := rightsRuleFilter{author: authorFilter, grantee: granteeFilter}
	reader := c.newObjectRightsReader()
	readRights := func(name string) objectRightsInfo { return reader.getObjectRights(ctx, name, request) }
	resolution, ok := rightsResolveObjectName(ctx, named, readRights, reader, request, log)
	if !ok {
		return 1
	}
	var rootName string
	var root objectRightsInfo
	byTitle := false
	switch {
	case len(resolution.titleMatches) == 0:
		rootName, root = resolution.code, resolution.byCode
	case len(resolution.titleMatches) > 1:
		log.error("Error: %s. Re-run with the exact code.", rightsNotACode(named, resolution.titleMatches))
		return 1
	default:
		rootName = resolution.titleMatches[0].name
		root = readRights(rootName)
		byTitle = true
	}
	connected, excluded, enumerationError, notListed := c.listConnected(ctx, input.IncludeConnected, rootName, root, request)
	header := "Object permissions for " + rightsFormatObject(rootName, root.caption)
	if input.IncludeConnected {
		header += " and its connected objects"
	}
	if granteeFilter != "" {
		header += " (grantee " + granteeFilter + ")"
	}
	if authorFilter != "" {
		header += " (rules of author " + authorFilter + ")"
	}
	log.info(header + ":")
	log.info("  " + objectRightsPriorityRule)
	log.info("  " + objectRightsGuidancePointer)
	if enumerationError != "" {
		log.warn("  Could not enumerate the connected objects of '%s' (%s); only the root object was read.", rootName, enumerationError)
	}
	for _, name := range excluded {
		log.warn("  %s: security/system object — not read as a connected object. Pass it as --entity-schema-name to read it.", name)
	}
	if byTitle {
		log.info("  %s, which is read.", rightsNotACode(named, []objectTitleMatch{{name: rootName, caption: root.caption}}))
	}
	rootRead := reportObjectTarget(rootName, root, true, granteeFilter, ruleFilter, log)
	if notListed != "" {
		log.warn(notListed)
	}
	for index, target := range connected {
		if request.deadline.spent() {
			log.warn("  Stopped: the read budget of %s s is spent. Not read: %s — read them one by one.",
				rightsSeconds(request.deadline.budget), strings.Join(connected[index:], ", "))
			break
		}
		info := readRights(target)
		reportObjectTarget(target, info, false, granteeFilter, ruleFilter, log)
		if info.timedOut && index < len(connected)-1 {
			log.warn("  Stopped after the read of %s timed out. Not read: %s — read them one by one.", target, strings.Join(connected[index+1:], ", "))
			break
		}
	}
	if !rootRead {
		return 1
	}
	if state := root.recordState(); state.administered || len(state.rules) > 0 {
		fact, counted := rightsDescribeRecordCount(ctx, reader, rootName, request)
		if counted {
			log.info("  %s: %s.", rootName, fact)
		} else {
			log.warn("  %s: %s.", rootName, fact)
		}
	}
	return 0
}

// listConnected is TryListConnected with ConnectedObjectsResolver: the root's own lookup objects (after the
// root), the security/system ones left out, or why they were not listed.
func (c *Client) listConnected(ctx context.Context, include bool, rootName string, root objectRightsInfo, request rightsRequest) (connected, excluded []string, enumerationError, notListed string) {
	if !include {
		return nil, nil, "", ""
	}
	if root.timedOut {
		return nil, nil, "", fmt.Sprintf("  Stopped after the read of %s timed out: its connected objects were not listed.", rootName)
	}
	timeout, err := request.next()
	if request.deadline.spent() || err != nil {
		return nil, nil, "", fmt.Sprintf("  Stopped: the read budget of %s s is spent: the connected objects of %s were not listed.",
			rightsSeconds(request.deadline.budget), rootName)
	}
	listCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	schema, err := c.GetEntitySchemaProperties(listCtx, EntitySchemaPropertiesRequest{SchemaName: rootName})
	if err != nil {
		// clio's runtime reader reports the service's own message; the Go reader prefixes it.
		failure := c.rightsTransportFault(err, timeout)
		return nil, nil, rightsDisplayError(strings.TrimPrefix(failure.Error(), "runtime schema read failed: ")), ""
	}
	seen := map[string]bool{}
	names := []string{}
	for _, column := range schema.Columns {
		if !strings.EqualFold(column.Source, "own") || column.ReferenceSchemaName == nil {
			continue
		}
		name, ok := rightsNormalizeSchemaName(*column.ReferenceSchemaName)
		if !ok || strings.EqualFold(name, rootName) || seen[strings.ToUpper(name)] {
			continue
		}
		seen[strings.ToUpper(name)] = true
		names = append(names, name)
	}
	sort.SliceStable(names, func(i, j int) bool { return compareOrdinalIgnoreCase(names[i], names[j]) < 0 })
	for _, name := range names {
		if rightsIsSecurityObject(name) {
			excluded = append(excluded, name)
		} else {
			connected = append(connected, name)
		}
	}
	return connected, excluded, "", ""
}

// reportObjectTarget is ReportTarget: one object, and whether it could be read.
func reportObjectTarget(name string, info objectRightsInfo, isRoot bool, granteeFilter string, ruleFilter rightsRuleFilter, log *rightsLog) bool {
	if !info.isRead() {
		if isRoot {
			log.error("  %s: %s.", name, info.failureReason())
		} else {
			log.warn("  %s: %s.", name, info.failureReason())
		}
		return false
	}
	reportOperationLayer(name, info, granteeFilter, log)
	reportRecordLayer(info.recordState(), ruleFilter, log)
	return true
}

func reportRecordLayer(state recordState, filter rightsRuleFilter, log *rightsLog) {
	if state.administered && len(state.rules) == 0 {
		log.info("    Record permissions: ON, with NO default record rules: every user sees only the records they create (and their " +
			"managers and holders of 'view any data' see them too).")
		return
	}
	if len(state.rules) == 0 {
		log.info("    Record permissions: OFF, no default record rules.")
		return
	}
	if state.administered {
		log.info("    Record permissions: ON. Default record rules (records created by author → rights of grantee; rules add up, their " +
			"order has no meaning):")
	} else {
		log.info("    Record permissions: OFF — record rights are not evaluated. Stored default record rules (not in effect while record " +
			"permissions are off; they come into effect when record permissions are turned on):")
	}
	shown := 0
	for _, rule := range state.rules {
		if filter.matches(rule) {
			shown++
		}
	}
	if shown == 0 {
		log.info("      no rule matches the filter (%d rule(s) in all).", len(state.rules))
		return
	}
	for _, rule := range state.rules {
		if filter.matches(rule) {
			log.info("      " + recordRuleText(rule, true))
		}
	}
}

func reportOperationLayer(name string, info objectRightsInfo, granteeFilter string, log *rightsLog) {
	label := name
	if rightsHasOwnTitle(name, info.caption) {
		label = rightsFormatObject(name, info.caption)
	}
	if !info.administeredOps {
		log.info("  %s: not administered by operation permissions (they are OFF) — available to all internal users.", label)
		if len(info.roles) > 0 {
			log.info("    Rows that apply if operation permissions are turned on:")
			reportRows(info.roles, "", log)
		}
		if !info.state().hasAllEmployeesRow() {
			log.info("    It has no 'All employees' row: set-object-rights --enable-operation-permissions adds one with " +
				"read/create/edit/delete below any stored rows, unless the grant is for All employees itself.")
		}
		return
	}
	if len(info.roles) == 0 {
		log.info("  %s: administered by operation permissions, with NO rows.", label)
		return
	}
	log.info("  %s: administered by operation permissions. Rows in priority order:", label)
	reportRows(info.roles, granteeFilter, log)
}

func reportRows(rows []roleRow, granteeFilter string, log *rightsLog) {
	if granteeFilter == "" {
		for _, row := range rows {
			log.info("    " + rightsFormatRow(row, true))
		}
		return
	}
	granteeRows := []roleRow{}
	for _, row := range rows {
		if row.granteeID == granteeFilter {
			granteeRows = append(granteeRows, row)
		}
	}
	if len(granteeRows) == 0 {
		log.info("    grantee %s has NO row (no operations granted).", granteeFilter)
		if len(rows) > 0 {
			log.info("    A new row would go below every row; these decide first for a user who is also in those roles:")
			for _, row := range rows {
				log.info("      " + rightsFormatRow(row, true))
			}
		}
		return
	}
	for _, row := range granteeRows {
		log.info("    " + rightsFormatRow(row, true))
	}
	above := []roleRow{}
	for _, row := range rows {
		if row.position < granteeRows[0].position {
			above = append(above, row)
		}
	}
	if len(above) > 0 {
		log.info("    Rows above it, which decide first for a user who is also in those roles:")
		for _, row := range above {
			log.info("      " + rightsFormatRow(row, true))
		}
	}
}

// ---- inputs shared by the four commands (ObjectRightsCommandInput) ----

// rightsReadObjectName is TryReadObjectName: required and trimmed.
func rightsReadObjectName(raw string, log *rightsLog) (string, bool) {
	named := strings.TrimSpace(raw)
	if named == "" {
		log.error("Error: --entity-schema-name is required.")
		return "", false
	}
	return named, true
}

// rightsReadSchemaName is TryReadSchemaName: required, trimmed and a plain schema identifier.
func rightsReadSchemaName(raw string, log *rightsLog) (string, bool) {
	if strings.TrimSpace(raw) == "" {
		log.error("Error: --entity-schema-name is required.")
		return "", false
	}
	name, ok := rightsNormalizeSchemaName(raw)
	if !ok {
		log.error("Error: --entity-schema-name '%s' is not a schema name (letters, digits and '_' only).", rightsDisplay(raw))
		return "", false
	}
	return name, true
}

// rightsUnitFilter reads an optional SysAdminUnit id: blank is "every role", a given one a non-empty GUID.
func rightsUnitFilter(raw string) (string, bool) {
	if strings.TrimSpace(raw) == "" {
		return "", true
	}
	id, ok := DataWriteParseGUID(raw)
	if !ok || id == emptyGUID {
		return "", false
	}
	return id, true
}

type rightsNameResolution struct {
	code         string
	byCode       objectRightsInfo
	titleMatches []objectTitleMatch
}

// rightsResolveObjectName is TryResolveObjectName: the code always wins; otherwise the name is looked up as a title.
func rightsResolveObjectName(ctx context.Context, named string, readByCode func(string) objectRightsInfo, reader *objectRightsReader,
	request rightsRequest, log *rightsLog) (rightsNameResolution, bool) {
	code, isCode := rightsNormalizeSchemaName(named)
	var byCode objectRightsInfo
	if isCode {
		byCode = readByCode(code)
		if byCode.found {
			return rightsNameResolution{code: code, byCode: byCode}, true
		}
	} else {
		code = ""
	}
	matches, err := reader.findObjectsByTitle(ctx, named, request)
	if err != nil {
		lookup := fmt.Sprintf("the lookup of the objects titled '%s' failed: %s", rightsDisplay(named), rightsDisplayFailure(err))
		if code == "" {
			log.error("Error: " + lookup)
		} else {
			log.error("Error: no object has the code '%s', and %s", code, lookup)
		}
		return rightsNameResolution{}, false
	}
	if code == "" && len(matches) == 0 {
		log.error("Error: --entity-schema-name '%s' is not an object code (letters, digits and '_' only), and no object has it as its title.",
			rightsDisplay(named))
		return rightsNameResolution{}, false
	}
	return rightsNameResolution{code: code, byCode: byCode, titleMatches: matches}, true
}

// rightsNotACode is NotACode: 'Creatio functionality' is not an object code: it is the title of Feature.
func rightsNotACode(named string, matches []objectTitleMatch) string {
	owner := matches[0].name
	if len(matches) != 1 {
		owner = fmt.Sprintf("%d objects: %s", len(matches), rightsFormatTitleMatches(matches))
	}
	return "'" + rightsDisplay(named) + "' is not an object code: it is the title of " + owner
}

// rightsResolveUnitName is TryResolveUnitName: the id must name an existing role or user.
func rightsResolveUnitName(ctx context.Context, reader *objectRightsReader, id, argument string, request rightsRequest, log *rightsLog) (string, bool) {
	name, err := reader.resolveGranteeName(ctx, id, request)
	if err != nil {
		log.error("Error: could not check %s %s: %s", argument, id, rightsDisplayFailure(err))
		return "", false
	}
	if name == nil {
		log.error("Error: %s %s was not found in SysAdminUnit. Nothing was changed — pass the id of an existing role or user.", argument, id)
		return "", false
	}
	return *name, true
}

// rightsHasTimeFor is HasTimeFor: whether the deadline leaves time for the step's requests.
func rightsHasTimeFor(request rightsRequest, requests int, schemaName, step, needs, outcome string, log *rightsLog) bool {
	if request.deadline == nil {
		return true
	}
	needed := time.Duration(requests) * request.timeout
	left := request.deadline.remaining()
	if left >= needed {
		return true
	}
	log.error("Error: '%s': the reads before the %s took most of the call's time limit of %s s: %s s are left, and %s up to %s s. The %s "+
		"was not sent — %s. Re-run the call.", schemaName, step, rightsSeconds(request.deadline.budget), rightsSeconds(left), needs,
		rightsSeconds(needed), step, outcome)
	return false
}

// rightsDescribeRecordCount is DescribeRecordCount: a failed count is reported, never thrown.
func rightsDescribeRecordCount(ctx context.Context, reader *objectRightsReader, schemaName string, request rightsRequest) (string, bool) {
	count, err := reader.countRecords(ctx, schemaName, request)
	if err != nil {
		return "existing records not counted (" + rightsDisplayFailure(err) + ")", false
	}
	return fmt.Sprintf("%d existing record(s), counted under the calling account", count), true
}
