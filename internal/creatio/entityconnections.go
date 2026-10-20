package creatio

// get-entity-connections and update-entity-connections (clio 8.1.0.142 EntityConnectionsService,
// EntityConnectionsReader and EntityConnectionsAddonMetadata): an object's Connections, the ordered lookup
// columns the Freedom UI crt.Connections component shows, read through CrtUIv2's EntityConnectionsService and
// written as the object's EntityConnections add-on in one package.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/redact"
)

const (
	entityConnectionsAddonName       = "EntityConnections"
	EntityConnectionsNameRequired    = "entity-schema-name is required."
	EntityConnectionsPackageRequired = "package-name is required."
	EntityConnectionsListRequired    = "connections is required: pass the full desired ordered list of column names (an empty list removes every connection)."

	entityConnectionsLayerMismatch = "The effective list does not match the EntityConnections add-on content (a column the add-on removes is still " +
		"listed). The server probably does not apply the add-on — the UseConnectionsComponent feature may be off on this environment."
	entityConnectionsOrderMismatch = "The effective list does not start with the EntityConnections add-on items of this package. An add-on layer " +
		"of another package (outside this package's parents) may place or remove connections, or the server does not apply the add-on " +
		"because the UseConnectionsComponent feature is off on this environment."
	entityConnectionsReadBackFailed  = "Connections were saved, but reading them back failed: "
	entityConnectionsOrderUnverified = "Connections were saved, but the written order could not be verified: the list was read back from the " +
		"EntityConnection table, which does not reflect the EntityConnections add-on."
)

// EntityConnectionsResponse is clio's EntityConnectionsResponse; null members are omitted.
type EntityConnectionsResponse struct {
	Success          bool                           `json:"success"`
	Error            string                         `json:"error,omitempty"`
	Changed          *bool                          `json:"changed,omitempty"`
	EntitySchemaName string                         `json:"entitySchemaName,omitempty"`
	EntitySchemaUID  string                         `json:"entitySchemaUId,omitempty"`
	PackageName      string                         `json:"packageName,omitempty"`
	Connections      []EntityConnectionEntry        `json:"connections,omitempty"`
	PackageLayer     *EntityConnectionsPackageLayer `json:"packageLayer,omitempty"`
	Warnings         []string                       `json:"warnings,omitempty"`
}

// EntityConnectionEntry is EntityConnectionDto.
type EntityConnectionEntry struct {
	ColumnName          *string `json:"columnName,omitempty"`
	ColumnUID           string  `json:"columnUId"`
	Caption             *string `json:"caption,omitempty"`
	ReferenceSchemaName *string `json:"referenceSchemaName,omitempty"`
	Position            int     `json:"position"`
}

// EntityConnectionsPackageLayer is EntityConnectionsPackageLayerDto.
type EntityConnectionsPackageLayer struct {
	Items   []string `json:"items"`
	Removed []string `json:"removed"`
}

// EntityConnectionsFailure is EntityConnectionsResponse.Fail.
func EntityConnectionsFailure(message string) EntityConnectionsResponse {
	return EntityConnectionsResponse{Error: message}
}

// MarshalJSON keeps an empty connections list in a successful answer (clio writes [] there) and omits it in a failure.
func (r EntityConnectionsResponse) MarshalJSON() ([]byte, error) {
	type plain EntityConnectionsResponse
	if !r.Success {
		return json.Marshal(plain(r))
	}
	type shown struct {
		Success          bool                           `json:"success"`
		Error            string                         `json:"error,omitempty"`
		Changed          *bool                          `json:"changed,omitempty"`
		EntitySchemaName string                         `json:"entitySchemaName,omitempty"`
		EntitySchemaUID  string                         `json:"entitySchemaUId,omitempty"`
		PackageName      string                         `json:"packageName,omitempty"`
		Connections      []EntityConnectionEntry        `json:"connections"`
		PackageLayer     *EntityConnectionsPackageLayer `json:"packageLayer,omitempty"`
		Warnings         []string                       `json:"warnings,omitempty"`
	}
	connections := r.Connections
	if connections == nil {
		connections = []EntityConnectionEntry{}
	}
	return json.Marshal(shown{r.Success, r.Error, r.Changed, r.EntitySchemaName, r.EntitySchemaUID, r.PackageName, connections, r.PackageLayer, r.Warnings})
}

// ---- add-on content (EntityConnectionsAddonMetadata) ----

type connectionsContent struct {
	items, removed []string
	other          *jnode
}

func connectionsFindProperty(parent *jnode, key string) *jnode {
	return processDescribeProperty(parent, key)
}

func connectionsColumnUID(entry *jnode) (string, bool) {
	value := connectionsFindProperty(entry, "ColumnUId")
	if !value.isString() {
		return "", false
	}
	uid, ok := DataWriteParseGUID(value.text)
	return uid, ok && uid != emptyGUID
}

func connectionsPosition(entry *jnode) float64 {
	value := connectionsFindProperty(entry, "Position")
	if value == nil {
		return 0
	}
	switch value.kind {
	case jkInteger, jkFloat:
		number, _ := strconv.ParseFloat(value.text, 64)
		return number
	case jkString:
		if number, err := strconv.ParseFloat(strings.TrimSpace(value.text), 64); err == nil {
			return number
		}
	}
	return 0
}

func connectionsArray(raw *jnode, key string) []*jnode {
	node := connectionsFindProperty(raw, key)
	if !node.isArray() {
		return nil
	}
	objects := []*jnode{}
	for _, item := range node.items {
		if item.isObject() {
			objects = append(objects, item)
		}
	}
	return objects
}

// parseConnectionsContent is Parse: items ordered by Position (stable), removals, and every other property kept.
func parseConnectionsContent(metaData string) (connectionsContent, error) {
	if strings.TrimSpace(metaData) == "" {
		return connectionsContent{items: []string{}, removed: []string{}, other: newObject()}, nil
	}
	raw, err := parseJNode([]byte(metaData))
	if err != nil {
		return connectionsContent{}, errors.New("The EntityConnections add-on metadata is not valid JSON: " + err.Error())
	}
	if !raw.isObject() {
		return connectionsContent{}, errors.New("The EntityConnections add-on metadata is not a JSON object.")
	}
	type positioned struct {
		uid      string
		position float64
	}
	entries := []positioned{}
	for _, entry := range connectionsArray(raw, "items") {
		if uid, ok := connectionsColumnUID(entry); ok {
			entries = append(entries, positioned{uid, connectionsPosition(entry)})
		}
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].position < entries[j].position })
	content := connectionsContent{items: []string{}, removed: []string{}, other: newObject()}
	for _, entry := range entries {
		content.items = append(content.items, entry.uid)
	}
	for _, entry := range connectionsArray(raw, "removed") {
		if uid, ok := connectionsColumnUID(entry); ok {
			content.removed = append(content.removed, uid)
		}
	}
	for _, key := range raw.keys {
		if strings.EqualFold(key, "items") || strings.EqualFold(key, "removed") {
			continue
		}
		content.other.set(key, raw.props[key].clone())
	}
	return content, nil
}

func serializeConnectionsContent(content connectionsContent) string {
	result := content.other.clone()
	if result == nil {
		result = newObject()
	}
	items := newArray()
	for position, uid := range content.items {
		item := newObject()
		item.set("UId", newString(uid))
		item.set("ColumnUId", newString(uid))
		item.set("Position", newInt(position))
		items.items = append(items.items, item)
	}
	removed := newArray()
	for _, uid := range content.removed {
		item := newObject()
		item.set("UId", newString(uid))
		item.set("ColumnUId", newString(uid))
		removed.items = append(removed.items, item)
	}
	result.set("items", items)
	result.set("removed", removed)
	return string(result.stjJSON())
}

// withoutColumns is WithoutColumns; the bool says whether anything was dropped.
func (c connectionsContent) withoutColumns(columns map[string]bool) (connectionsContent, bool) {
	if len(columns) == 0 {
		return c, false
	}
	items, removed := []string{}, []string{}
	for _, uid := range c.items {
		if !columns[uid] {
			items = append(items, uid)
		}
	}
	for _, uid := range c.removed {
		if !columns[uid] {
			removed = append(removed, uid)
		}
	}
	if len(items) == len(c.items) && len(removed) == len(c.removed) {
		return c, false
	}
	return connectionsContent{items: items, removed: removed, other: c.other}, true
}

func sameUIDs(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

// buildConnectionsContent is Build: the content that makes the effective list the desired one, or false when
// they already match.
func buildConnectionsContent(base connectionsContent, effective, desired []string) (connectionsContent, bool) {
	if sameUIDs(effective, desired) {
		return connectionsContent{}, false
	}
	desiredSet := map[string]bool{}
	for _, uid := range desired {
		desiredSet[uid] = true
	}
	removed := []string{}
	seen := map[string]bool{}
	for _, uid := range append(append([]string{}, base.removed...), effective...) {
		if desiredSet[uid] || seen[uid] {
			continue
		}
		seen[uid] = true
		removed = append(removed, uid)
	}
	itemSet := map[string]bool{}
	for _, uid := range base.items {
		itemSet[uid] = true
	}
	rest := []string{}
	for _, uid := range effective {
		if !itemSet[uid] {
			rest = append(rest, uid)
		}
	}
	count := connectionsItemCount(desired, rest, itemSet)
	return connectionsContent{items: append([]string{}, desired[:count]...), removed: removed, other: base.other}, true
}

func connectionsItemCount(desired, rest []string, itemSet map[string]bool) int {
	restSet := map[string]bool{}
	for _, uid := range rest {
		restSet[uid] = true
	}
	lower := 0
	for index, uid := range desired {
		if itemSet[uid] || !restSet[uid] {
			lower = index + 1
		}
	}
	for count := lower; count < len(desired); count++ {
		tail := desired[count:]
		tailSet := map[string]bool{}
		for _, uid := range tail {
			tailSet[uid] = true
		}
		ordered := []string{}
		for _, uid := range rest {
			if tailSet[uid] {
				ordered = append(ordered, uid)
			}
		}
		if sameUIDs(tail, ordered) {
			return count
		}
	}
	return len(desired)
}

// ---- the object as the package sees it ----

type connectionsColumn struct {
	uid, name     string
	caption       *string
	dataValueType *int
	reference     *string
}

type connectionsTarget struct {
	name, uid, parentUID string
	packageID            string
	columns              []connectionsColumn // own, then inherited
}

type connectionsSnapshot struct {
	columnUIDs    []string
	tableFallback bool
	warning       string
}

type connectionsState struct {
	target    connectionsTarget
	schema    *jnode
	schemaUID string
	content   connectionsContent
	snapshot  connectionsSnapshot
}

// connectionsDataValueTypes is EntitySchemaDesignerSupport.SupportedDataValueTypes in its declaration order.
var connectionsDataValueTypes = []struct {
	name  string
	value int
}{
	{"guid", 0}, {"text", 1}, {"text50", 27}, {"text250", 28}, {"textUnlimited", 29}, {"text500", 30}, {"phoneNumber", 42},
	{"webLink", 44}, {"email", 45}, {"richText", 43}, {"binary", 13}, {"image", 14}, {"imageLookup", 16}, {"file", 25},
	{"secureText", 24}, {"integer", 4}, {"datetime", 7}, {"lookup", 10}, {"boolean", 12}, {"color", 18}, {"decimal0", 47},
	{"decimal1", 31}, {"decimal2", 32}, {"decimal3", 33}, {"decimal4", 34}, {"decimal8", 40}, {"currency0", 48},
	{"currency1", 49}, {"currency2", 6}, {"currency3", 50},
}

func connectionsDescribeType(dataValueType *int) string {
	if dataValueType == nil {
		return "unknown type"
	}
	for _, known := range connectionsDataValueTypes {
		if known.value == *dataValueType {
			return known.name
		}
	}
	return fmt.Sprintf("data value type %d", *dataValueType)
}

// entityConnectionsPackageUID is PageSchemaMetadataHelper.QueryPackageUId with its failure texts.
func (c *Client) entityConnectionsPackageUID(ctx context.Context, packageName string) (string, error) {
	query := newObject()
	query.set("rootSchemaName", newString("SysPackage"))
	query.set("operationType", newInt(0))
	filters := newObject()
	filters.set("filterType", newInt(6))
	filters.set("logicalOperation", newInt(0))
	filters.set("isEnabled", newBool(true))
	items := newObject()
	items.set("byName", connectionsEqFilter("Name", 1, packageName))
	filters.set("items", items)
	query.set("filters", filters)
	columns := newObject()
	columnItems := newObject()
	uid := newObject()
	expression := newObject()
	expression.set("expressionType", newInt(0))
	expression.set("columnPath", newString("UId"))
	uid.set("expression", expression)
	columnItems.set("UId", uid)
	columns.set("items", columnItems)
	query.set("columns", columns)
	query.set("rowCount", newInt(1))
	rows, success, failure := c.entityConnectionsSelect(ctx, query)
	if failure != "" {
		return "", errors.New(failure)
	}
	if !success {
		return "", errors.New("Failed to query SysPackage")
	}
	if len(rows) == 0 {
		return "", fmt.Errorf("Package '%s' not found in the target environment.", packageName)
	}
	value := processDescribeProperty(rows[0], "UId").tokenString()
	if value == nil || strings.TrimSpace(*value) == "" {
		return "", fmt.Errorf("Package '%s' has no UId in the SysPackage response.", packageName)
	}
	return *value, nil
}

func connectionsEqFilter(column string, dataValueType int, value string) *jnode {
	filter := newObject()
	filter.set("filterType", newInt(1))
	filter.set("comparisonType", newInt(3))
	filter.set("isEnabled", newBool(true))
	left := newObject()
	left.set("expressionType", newInt(0))
	left.set("columnPath", newString(column))
	filter.set("leftExpression", left)
	right := newObject()
	right.set("expressionType", newInt(2))
	parameter := newObject()
	parameter.set("dataValueType", newInt(dataValueType))
	parameter.set("value", newString(value))
	right.set("parameter", parameter)
	filter.set("rightExpression", right)
	return filter
}

// entityConnectionsSelect is PageSchemaMetadataHelper.ExecuteSelectQuery: rows and the success flag, or the
// failure text of a transport fault, an empty body or a body that is not JSON.
func (c *Client) entityConnectionsSelect(ctx context.Context, query *jnode) ([]*jnode, bool, string) {
	route := "DataService/json/SyncReply/SelectQuery"
	address := c.serviceURL(route)
	response, err := c.serviceRequest(ctx, serviceCall{Route: route, Body: query.newtonsoftJSON(), Timeout: 100 * time.Second, Limit: maxResponseBytes})
	if err != nil {
		fault := c.rightsTransportFault(err, 100*time.Second)
		if rightsIsTimeout(fault) {
			return nil, false, "SelectQuery timed out before the environment answered (URL: " + address + "). Detail: " + fault.Error() +
				". This says nothing about the requested schema or package — retry the request, and if it persists check the environment " +
				"health (healthcheck) and whether the target instance is still responding."
		}
		if rightsIsConnectionFailure(err) {
			return nil, false, "SelectQuery could not be completed against the environment (URL: " + address + "). Transport error: " +
				connectionsTransportDetail(fault) + ". This says nothing about the requested schema or package — verify that the environment " +
				"is registered with valid credentials (reg-web-app, then healthcheck), that the site is running and reachable, and retry the request."
		}
		return nil, false, err.Error()
	}
	text := strings.TrimPrefix(string(response.payload), "\uFEFF")
	if strings.TrimSpace(text) == "" {
		return nil, false, schemaWriteEmptyBodyMessage("SelectQuery", address)
	}
	node, parseErr := parseJNode([]byte(text))
	if parseErr != nil || !node.isObject() {
		if rightsLooksLikeMarkup(text) {
			return nil, false, schemaWriteMarkupMessage("SelectQuery", address)
		}
		reason := "Unexpected JSON token."
		if parseErr != nil {
			reason = parseErr.Error()
		}
		return nil, false, fmt.Sprintf("SelectQuery returned an unparseable response. URL: %s Parser error: %s. Response preview: %s",
			address, reason, schemaWritePreview(text))
	}
	success := node.get("success")
	if success == nil || success.kind != jkBool || !success.flag {
		return nil, false, ""
	}
	rows := node.get("rows")
	if !rows.isArray() {
		return []*jnode{}, true, ""
	}
	return rows.items, true, ""
}

// connectionsTransportDetail is how Creatio.Client words a request it could not send.
func connectionsTransportDetail(fault error) string {
	if strings.HasPrefix(fault.Error(), "Connection refused (") {
		return "Cannot connect to the application: " + fault.Error() + " (WebException: ConnectFailure). Make sure the site is running and accessible."
	}
	return fault.Error()
}

// entityConnectionsTarget is EntityAddonTargetResolver.Resolve: the package, and the object as it sees it.
func (c *Client) entityConnectionsTarget(ctx context.Context, packageName, entitySchemaName string) (connectionsTarget, error) {
	packageUID, err := c.entityConnectionsPackageUID(ctx, packageName)
	if err != nil {
		return connectionsTarget{}, err
	}
	packageID, ok := DataWriteParseGUID(packageUID)
	if !ok {
		return connectionsTarget{}, fmt.Errorf("Resolved package '%s' UId '%s' is not a valid GUID.", packageName, packageUID)
	}
	body, _ := json.Marshal(map[string]any{"name": strings.TrimSpace(entitySchemaName), "packageUId": packageID,
		"useFullHierarchy": true, "cultures": []string{businessRuleDefaultCulture}})
	payload, err := c.postCreatioServiceJSON(ctx, "ServiceModel/EntitySchemaDesignerService.svc/GetSchemaDesignItem", body, 45*time.Second, maxResponseBytes)
	if err != nil {
		return connectionsTarget{}, err
	}
	if strings.TrimSpace(string(payload)) == "" {
		return connectionsTarget{}, errors.New("GetSchemaDesignItem returned an empty response.")
	}
	root, err := parseJNode(payload)
	if err != nil || !root.isObject() {
		return connectionsTarget{}, fmt.Errorf("GetSchemaDesignItem returned invalid JSON: %v", err)
	}
	if success := processDescribeProperty(root, "success"); success == nil || success.kind != jkBool || !success.flag {
		if message := processDescribeProperty(processDescribeProperty(root, "errorInfo"), "message"); message.isString() && strings.TrimSpace(message.text) != "" {
			return connectionsTarget{}, errors.New(message.text)
		}
		return connectionsTarget{}, errors.New("GetSchemaDesignItem failed.")
	}
	schema := processDescribeProperty(root, "schema")
	if !schema.isObject() {
		return connectionsTarget{}, fmt.Errorf("Object (entity schema) '%s' not found in package '%s'. The object must be visible from that "+
			"package — if it lives elsewhere, add a package dependency.", entitySchemaName, packageName)
	}
	target := connectionsTarget{name: rightsStr(processDescribeProperty(schema, "name")), packageID: packageID,
		uid: guidOrEmpty(rightsStr(processDescribeProperty(schema, "uId"))), parentUID: emptyGUID}
	if parent := processDescribeProperty(schema, "parentSchema"); parent.isObject() {
		target.parentUID = guidOrEmpty(rightsStr(processDescribeProperty(parent, "uId")))
	}
	for _, key := range []string{"columns", "inheritedColumns"} {
		list := processDescribeProperty(schema, key)
		if !list.isArray() {
			continue
		}
		for _, item := range list.items {
			if !item.isObject() {
				continue
			}
			column := connectionsColumn{uid: guidOrEmpty(rightsStr(processDescribeProperty(item, "uId"))),
				name: rightsStr(processDescribeProperty(item, "name"))}
			if value, ok := rightsInt(processDescribeProperty(item, "type")); ok {
				column.dataValueType = &value
			}
			if reference := processDescribeProperty(item, "referenceSchema"); reference.isObject() {
				if name := processDescribeProperty(reference, "name"); name.isString() {
					text := name.text
					column.reference = &text
				}
			}
			column.caption = connectionsCaption(processDescribeProperty(item, "caption"))
			target.columns = append(target.columns, column)
		}
	}
	return target, nil
}

// connectionsCaption is GetLocalizableValue for the en-US culture: that value, else the first.
func connectionsCaption(values *jnode) *string {
	if !values.isArray() || len(values.items) == 0 {
		return nil
	}
	pick := func(entry *jnode) *string {
		if value := processDescribeProperty(entry, "value"); value.isString() {
			text := value.text
			return &text
		}
		return nil
	}
	for _, entry := range values.items {
		if strings.EqualFold(rightsStr(processDescribeProperty(entry, "cultureName")), businessRuleDefaultCulture) {
			return pick(entry)
		}
	}
	return pick(values.items[0])
}

// readConnectionsState is ReadState.
func (c *Client) readConnectionsState(ctx context.Context, packageName, entitySchemaName string) (connectionsState, error) {
	target, err := c.entityConnectionsTarget(ctx, packageName, entitySchemaName)
	if err != nil {
		return connectionsState{}, err
	}
	schema, err := c.relatedAddonSchema(ctx, map[string]any{
		"addonName": entityConnectionsAddonName, "targetSchemaUId": target.uid, "targetParentSchemaUId": target.parentUID,
		"targetPackageUId": target.packageID, "targetSchemaManagerName": "EntitySchemaManager", "useFullHierarchy": true,
	})
	if err != nil {
		return connectionsState{}, err
	}
	schemaUID := ""
	for _, key := range schema.keys {
		if value := schema.props[key]; strings.EqualFold(key, "targetSchemaUId") && value.isString() {
			if uid, ok := DataWriteParseGUID(value.text); ok && uid != emptyGUID {
				schemaUID = uid
				break
			}
		}
	}
	if schemaUID == "" {
		return connectionsState{}, errors.New("The EntityConnections add-on response is missing a valid targetSchemaUId.")
	}
	metaData := ""
	if value := processDescribeProperty(schema, "metaData"); value.isString() {
		metaData = value.text
	}
	content, err := parseConnectionsContent(metaData)
	if err != nil {
		return connectionsState{}, err
	}
	snapshot, err := c.readConnectionsSnapshot(ctx, schemaUID)
	if err != nil {
		return connectionsState{}, err
	}
	return connectionsState{target: target, schema: schema, schemaUID: schemaUID, content: content, snapshot: snapshot}, nil
}

// readConnectionsSnapshot is EntityConnectionsReader.Read: the service's merged list, or the EntityConnection
// table rows with a warning when the service cannot be used.
func (c *Client) readConnectionsSnapshot(ctx context.Context, schemaUID string) (connectionsSnapshot, error) {
	uids, serviceError := c.readConnectionsFromService(ctx, schemaUID)
	if serviceError == "" {
		return connectionsSnapshot{columnUIDs: uids}, nil
	}
	rows, tableError := c.readConnectionsTable(ctx, schemaUID)
	if tableError != "" {
		return connectionsSnapshot{}, fmt.Errorf("Could not read the connections of the object. EntityConnectionsService: %s EntityConnection table: %s",
			serviceError, tableError)
	}
	return connectionsSnapshot{columnUIDs: rows, tableFallback: true, warning: "EntityConnectionsService.GetConnections is unavailable (" +
		serviceError + "); the list was read from the EntityConnection table, so the EntityConnections add-on items and removals are NOT " +
		"reflected in it."}, nil
}

func (c *Client) readConnectionsFromService(ctx context.Context, schemaUID string) ([]string, string) {
	body, _ := json.Marshal(map[string]string{"entitySchemaUId": schemaUID})
	response, err := c.serviceRequest(ctx, serviceCall{Route: "rest/EntityConnectionsService/GetConnections", Body: body, Timeout: 100 * time.Second,
		Limit: maxResponseBytes})
	if err != nil {
		return nil, redact.Text(c.rightsTransportFault(err, 100*time.Second).Error())
	}
	text := strings.TrimPrefix(string(response.payload), "\uFEFF")
	if strings.TrimSpace(text) == "" {
		return nil, "the service returned an empty response"
	}
	var decoded struct {
		Success   bool `json:"success"`
		ErrorInfo *struct {
			Message *string `json:"message"`
		} `json:"errorInfo"`
		Connections []*struct {
			ColumnUID *string `json:"columnUId"`
		} `json:"connections"`
	}
	if err := json.Unmarshal([]byte(text), &decoded); err != nil {
		return nil, "the service did not return JSON (the endpoint may be missing on this environment)"
	}
	if strings.TrimSpace(text) == "null" || !decoded.Success || decoded.Connections == nil {
		if decoded.ErrorInfo != nil && decoded.ErrorInfo.Message != nil {
			return nil, *decoded.ErrorInfo.Message
		}
		return nil, "the service returned no connections"
	}
	uids := []string{}
	seen := map[string]bool{}
	for _, connection := range decoded.Connections {
		uid, ok := "", false
		if connection != nil && connection.ColumnUID != nil {
			uid, ok = DataWriteParseGUID(*connection.ColumnUID)
		}
		if !ok || uid == emptyGUID {
			return nil, "the service returned a connection with an invalid column UId"
		}
		if !seen[uid] {
			seen[uid] = true
			uids = append(uids, uid)
		}
	}
	return uids, ""
}

// readConnectionsTable is PageSchemaMetadataHelper.QueryEntityConnectionColumnUIds.
func (c *Client) readConnectionsTable(ctx context.Context, schemaUID string) ([]string, string) {
	query := newObject()
	query.set("rootSchemaName", newString("EntityConnection"))
	query.set("operationType", newInt(0))
	filters := newObject()
	filters.set("filterType", newInt(6))
	filters.set("logicalOperation", newInt(0))
	filters.set("isEnabled", newBool(true))
	items := newObject()
	items.set("bySchema", connectionsEqFilter("SysEntitySchemaUId", 0, schemaUID))
	filters.set("items", items)
	query.set("filters", filters)
	columnItems := newObject()
	for _, column := range []struct {
		name     string
		position int
	}{{"ColumnUId", -1}, {"Position", 0}, {"Id", 1}} {
		item := newObject()
		if column.position >= 0 {
			item.set("orderDirection", newInt(1))
			item.set("orderPosition", newInt(column.position))
		}
		expression := newObject()
		expression.set("expressionType", newInt(0))
		expression.set("columnPath", newString(column.name))
		item.set("expression", expression)
		columnItems.set(column.name, item)
	}
	columns := newObject()
	columns.set("items", columnItems)
	query.set("columns", columns)
	query.set("rowCount", newInt(-1))
	rows, success, failure := c.entityConnectionsSelect(ctx, query)
	if failure != "" {
		return nil, failure
	}
	if !success {
		return nil, "Failed to query EntityConnection"
	}
	uids := []string{}
	seen := map[string]bool{}
	for _, row := range rows {
		text := processDescribeProperty(row, "ColumnUId").tokenString()
		if text == nil {
			continue
		}
		if uid, ok := DataWriteParseGUID(*text); ok && uid != emptyGUID && !seen[uid] {
			seen[uid] = true
			uids = append(uids, uid)
		}
	}
	return uids, ""
}

// ---- the service (EntityConnectionsService) ----

// GetEntityConnections runs get-entity-connections.
func (c *Client) GetEntityConnections(ctx context.Context, entitySchemaName, packageName string) EntityConnectionsResponse {
	if strings.TrimSpace(entitySchemaName) == "" {
		return EntityConnectionsFailure(EntityConnectionsNameRequired)
	}
	if strings.TrimSpace(packageName) == "" {
		return EntityConnectionsFailure(EntityConnectionsPackageRequired)
	}
	state, err := c.readConnectionsState(ctx, packageName, entitySchemaName)
	if err != nil {
		return EntityConnectionsFailure(err.Error())
	}
	return connectionsResult(entitySchemaName, packageName, state, nil, nil)
}

// UpdateEntityConnections runs update-entity-connections with the full desired list (nil: not given).
func (c *Client) UpdateEntityConnections(ctx context.Context, entitySchemaName, packageName string, connections []*string) EntityConnectionsResponse {
	if strings.TrimSpace(entitySchemaName) == "" {
		return EntityConnectionsFailure(EntityConnectionsNameRequired)
	}
	if strings.TrimSpace(packageName) == "" {
		return EntityConnectionsFailure(EntityConnectionsPackageRequired)
	}
	if connections == nil {
		return EntityConnectionsFailure(EntityConnectionsListRequired)
	}
	names := make([]string, len(connections))
	for index, name := range connections {
		if name == nil || strings.TrimSpace(*name) == "" {
			return EntityConnectionsFailure("connections must not contain an empty column name.")
		}
		names[index] = strings.TrimSpace(*name)
	}
	if repeated := connectionsRepeated(names); len(repeated) > 0 {
		return EntityConnectionsFailure(fmt.Sprintf("connections lists a column more than once: %s. Each column can be one connection.",
			strings.Join(repeated, ", ")))
	}
	state, err := c.readConnectionsState(ctx, packageName, entitySchemaName)
	if err != nil {
		return EntityConnectionsFailure(err.Error())
	}
	desired, err := connectionsDesiredColumns(names, state.target)
	if err != nil {
		return EntityConnectionsFailure(err.Error())
	}
	if state.snapshot.tableFallback {
		return EntityConnectionsFailure("Connections were not changed: " + state.snapshot.warning +
			" Writing the add-on from that list could drop changes made by other packages.")
	}
	known := connectionsKnown(state.target)
	unresolvable := map[string]bool{}
	for _, uid := range state.snapshot.columnUIDs {
		if !known[uid] {
			unresolvable[uid] = true
		}
	}
	deleted := map[string]bool{}
	for _, uid := range append(append([]string{}, state.content.items...), state.content.removed...) {
		if !known[uid] && !unresolvable[uid] {
			deleted[uid] = true
		}
	}
	base, cleaned := state.content.withoutColumns(deleted)
	effective := []string{}
	for _, uid := range state.snapshot.columnUIDs {
		if !unresolvable[uid] {
			effective = append(effective, uid)
		}
	}
	if !connectionsRemovalsReflected(base, effective) {
		return EntityConnectionsFailure("Connections were not changed: " + entityConnectionsLayerMismatch)
	}
	metadata, build := buildConnectionsContent(base, effective, desired)
	if !build {
		if !cleaned {
			unchanged := false
			return connectionsResult(entitySchemaName, packageName, state, nil, &unchanged)
		}
		metadata = base
	}
	if err := c.relatedAddonSave(ctx, state.schema, serializeConnectionsContent(metadata)); err != nil {
		return EntityConnectionsFailure(err.Error())
	}
	warnings := []string{}
	if warning := c.relatedAddonResetCache(ctx); strings.TrimSpace(warning) != "" {
		warnings = append(warnings, warning)
	}
	changed := true
	after, err := c.readConnectionsState(ctx, packageName, entitySchemaName)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return EntityConnectionsFailure(err.Error())
		}
		warnings = append(warnings, entityConnectionsReadBackFailed+err.Error())
		written := append([]string{}, desired...)
		for _, uid := range state.snapshot.columnUIDs {
			if unresolvable[uid] {
				written = append(written, uid)
			}
		}
		state.content = metadata
		state.snapshot = connectionsSnapshot{columnUIDs: written}
		return connectionsResult(entitySchemaName, packageName, state, warnings, &changed)
	}
	if after.snapshot.tableFallback {
		warnings = append(warnings, entityConnectionsOrderUnverified)
	} else {
		knownAfter := connectionsKnown(after.target)
		listed := []string{}
		for _, uid := range after.snapshot.columnUIDs {
			if knownAfter[uid] {
				listed = append(listed, uid)
			}
		}
		if !sameUIDs(listed, desired) {
			warnings = append(warnings, "The add-on was saved, but the effective list differs from the requested one. A column removed by "+
				"another package layer cannot be brought back from this package (removals of all layers are united), and the server may not "+
				"apply the add-on when the UseConnectionsComponent feature is off.")
		}
	}
	return connectionsResult(entitySchemaName, packageName, after, warnings, &changed)
}

func connectionsRepeated(names []string) []string {
	counts := map[string]int{}
	order := []string{}
	first := map[string]string{}
	for _, name := range names {
		key := strings.ToUpper(name)
		if counts[key] == 0 {
			order = append(order, key)
			first[key] = name
		}
		counts[key]++
	}
	repeated := []string{}
	for _, key := range order {
		if counts[key] > 1 {
			repeated = append(repeated, first[key])
		}
	}
	return repeated
}

func connectionsKnown(target connectionsTarget) map[string]bool {
	known := map[string]bool{}
	for _, column := range target.columns {
		known[column.uid] = true
	}
	return known
}

func connectionsDesiredColumns(names []string, target connectionsTarget) ([]string, error) {
	byName := map[string]connectionsColumn{}
	for _, column := range target.columns {
		if strings.TrimSpace(column.name) == "" {
			continue
		}
		if _, exists := byName[strings.ToUpper(column.name)]; !exists {
			byName[strings.ToUpper(column.name)] = column
		}
	}
	unknown, notLookup, result := []string{}, []string{}, []string{}
	for _, name := range names {
		column, ok := byName[strings.ToUpper(name)]
		if !ok {
			unknown = append(unknown, "'"+name+"'")
			continue
		}
		if column.dataValueType == nil || *column.dataValueType != 10 {
			notLookup = append(notLookup, fmt.Sprintf("'%s' (%s)", column.name, connectionsDescribeType(column.dataValueType)))
			continue
		}
		result = append(result, column.uid)
	}
	errorsText := []string{}
	if len(unknown) > 0 {
		errorsText = append(errorsText, fmt.Sprintf("Object '%s' has no column %s.", target.name, strings.Join(unknown, ", ")))
	}
	if len(notLookup) > 0 {
		errorsText = append(errorsText, "Only lookup columns can be connections; not a lookup: "+strings.Join(notLookup, ", ")+".")
	}
	if len(errorsText) > 0 {
		return nil, errors.New("Connections were not changed. " + strings.Join(errorsText, " "))
	}
	return result, nil
}

func connectionsRemovalsReflected(content connectionsContent, effective []string) bool {
	removed := map[string]bool{}
	for _, uid := range content.removed {
		removed[uid] = true
	}
	for _, uid := range effective {
		if removed[uid] {
			return false
		}
	}
	return true
}

func connectionsItemsLeading(content connectionsContent, effective []string) bool {
	removed := map[string]bool{}
	for _, uid := range content.removed {
		removed[uid] = true
	}
	items := []string{}
	for _, uid := range content.items {
		if !removed[uid] {
			items = append(items, uid)
		}
	}
	return len(effective) >= len(items) && sameUIDs(effective[:len(items)], items)
}

// connectionsResult is ToResult.
func connectionsResult(entitySchemaName, packageName string, state connectionsState, extraWarnings []string, changed *bool) EntityConnectionsResponse {
	byUID := map[string]connectionsColumn{}
	for _, column := range state.target.columns {
		if _, exists := byUID[column.uid]; !exists {
			byUID[column.uid] = column
		}
	}
	entries := []EntityConnectionEntry{}
	unresolvable := []string{}
	for position, uid := range state.snapshot.columnUIDs {
		entry := EntityConnectionEntry{ColumnUID: uid, Position: position}
		if column, ok := byUID[uid]; ok {
			name := column.name
			entry.ColumnName = &name
			entry.Caption = column.caption
			entry.ReferenceSchemaName = column.reference
		} else {
			unresolvable = append(unresolvable, uid)
		}
		entries = append(entries, entry)
	}
	nameOf := func(uid string) string {
		if column, ok := byUID[uid]; ok && strings.TrimSpace(column.name) != "" {
			return column.name
		}
		return uid
	}
	warnings := []string{}
	add := func(warning string) {
		if strings.TrimSpace(warning) == "" {
			return
		}
		for _, existing := range warnings {
			if existing == warning {
				return
			}
		}
		warnings = append(warnings, warning)
	}
	add(state.snapshot.warning)
	if len(unresolvable) > 0 {
		warnings = append(warnings, fmt.Sprintf("%d connection(s) point to columns not visible from package '%s'; they are kept as they are "+
			"and must not be sent back: %s.", len(unresolvable), packageName, strings.Join(unresolvable, ", ")))
	}
	if !state.snapshot.tableFallback {
		if !connectionsRemovalsReflected(state.content, state.snapshot.columnUIDs) {
			warnings = append(warnings, entityConnectionsLayerMismatch)
		} else if !connectionsItemsLeading(state.content, state.snapshot.columnUIDs) {
			warnings = append(warnings, entityConnectionsOrderMismatch)
		}
	}
	for _, warning := range extraWarnings {
		add(warning)
	}
	layer := &EntityConnectionsPackageLayer{Items: []string{}, Removed: []string{}}
	for _, uid := range state.content.items {
		layer.Items = append(layer.Items, nameOf(uid))
	}
	for _, uid := range state.content.removed {
		layer.Removed = append(layer.Removed, nameOf(uid))
	}
	response := EntityConnectionsResponse{Success: true, Changed: changed, EntitySchemaName: entitySchemaName, EntitySchemaUID: state.schemaUID,
		PackageName: packageName, Connections: entries, PackageLayer: layer}
	if len(warnings) > 0 {
		response.Warnings = warnings
	}
	return response
}
