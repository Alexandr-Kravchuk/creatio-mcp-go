package creatio

// create-data-binding-db, upsert-data-binding-row-db and remove-data-binding-row-db: clio's DataBindingDbService
// (DataBindingDbCommand.cs) over PackageDataBindingWriter. Each command writes the rows through DataService
// Insert/Update/DeleteQuery and re-saves the package's SysPackageSchemaData registration through
// SchemaDataDesignerService.SaveSchema (or DeletePackageSchemaDataRequest when no row is left). The answer is
// clio's command log: a file-system-development-mode warning after a save, one line per created or skipped
// row, then "Done"; any failure is one Error line with exit code 1.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	dataWriteBindingInsertRoute       = "DataService/json/SyncReply/InsertQuery"
	dataWriteBindingUpdateRoute       = "DataService/json/SyncReply/UpdateQuery"
	dataWriteBindingDeleteRoute       = "DataService/json/SyncReply/DeleteQuery"
	dataWriteBindingSaveRoute         = "ServiceModel/SchemaDataDesignerService.svc/SaveSchema"
	dataWriteBindingDeleteDataRoute   = "DataService/json/SyncReply/DeletePackageSchemaDataRequest"
	dataWriteBindingFileDesignRoute   = "ServiceModel/WorkspaceExplorerService.svc/GetIsFileDesignMode"
	dataWriteBindingColorType         = 18
	dataWriteBindingTimeout           = 120 * time.Second
	dataWriteBindingFileDesignTimeout = 10 * time.Second
)

// DataWriteBindingDbRequest carries the arguments of the three DB-first binding tools.
type DataWriteBindingDbRequest struct {
	PackageName string
	SchemaName  string
	BindingName string
	RowsJSON    string
	ValuesJSON  string
	KeyValue    string
}

// dataWriteBindingColumn is clio's DataBindingSchemaColumn as the DB-first path reads it.
type dataWriteBindingColumn struct {
	UID           string
	Name          string
	DataValueType int
}

// dataWriteBindingSchema is clio's DataBindingDbSchema: the runtime entity schema, its columns ordered by name.
type dataWriteBindingSchema struct {
	UID     string
	Name    string
	Columns []dataWriteBindingColumn
}

type dataWriteBindingPackage struct{ UID, Name string }

// dataWriteBindingRow is one row as clio keeps it: a JsonObject turned into an insertion-ordered dictionary
// (keys compared case-sensitively, a JSON null held as a nil node).
type dataWriteBindingRow struct {
	keys   []string
	values map[string]*jnode
}

func (r *dataWriteBindingRow) get(key string) (*jnode, bool) {
	value, ok := r.values[key]
	return value, ok
}

func (r *dataWriteBindingRow) set(key string, value *jnode) {
	if _, ok := r.values[key]; !ok {
		r.keys = append(r.keys, key)
	}
	r.values[key] = value
}

func dataWriteBindingRowOf(object *jnode) *dataWriteBindingRow {
	row := &dataWriteBindingRow{values: map[string]*jnode{}}
	for _, key := range object.keys {
		value := object.props[key]
		if value != nil && value.kind == jkNull {
			value = nil
		}
		row.set(key, value)
	}
	return row
}

// dataWriteBindingRun holds one command's client and log; the file design mode state is read once per run,
// as PackageDataBindingWriter (transient, one per command) does.
type dataWriteBindingRun struct {
	ctx            context.Context
	client         *Client
	messages       []LogMessage
	fileDesignRead bool
	fileDesign     *bool
}

func (r *dataWriteBindingRun) log(kind, text string) {
	r.messages = append(r.messages, LogMessage{MessageType: kind, Value: text})
}

func (r *dataWriteBindingRun) result(err error) CommandResult {
	if err != nil {
		r.log("Error", err.Error())
		return CommandResult{ExitCode: 1, Messages: r.messages}
	}
	r.log("Info", "Done")
	return CommandResult{ExitCode: 0, Messages: r.messages}
}

// CreateDataBindingDb is CreateDataBindingDbCommand.Execute over DataBindingDbService.CreateBinding.
func (c *Client) CreateDataBindingDb(ctx context.Context, request DataWriteBindingDbRequest) CommandResult {
	run := &dataWriteBindingRun{ctx: ctx, client: c}
	created, skipped, err := run.createBinding(request)
	if err != nil {
		return run.result(err)
	}
	for _, row := range created {
		run.log("Info", fmt.Sprintf("Created row: %s (%s)", row.id, dataWriteBindingPreview(row.row)))
	}
	for _, row := range skipped {
		run.log("Info", fmt.Sprintf("Skipped existing row: %s (%s)", row.id, dataWriteBindingPreview(row.row)))
	}
	return run.result(nil)
}

// UpsertDataBindingRowDb is UpsertDataBindingRowDbCommand.Execute over DataBindingDbService.UpsertRow.
func (c *Client) UpsertDataBindingRowDb(ctx context.Context, request DataWriteBindingDbRequest) CommandResult {
	run := &dataWriteBindingRun{ctx: ctx, client: c}
	return run.result(run.upsertRow(request))
}

// RemoveDataBindingRowDb is RemoveDataBindingRowDbCommand.Execute over DataBindingDbService.RemoveRow.
func (c *Client) RemoveDataBindingRowDb(ctx context.Context, request DataWriteBindingDbRequest) CommandResult {
	run := &dataWriteBindingRun{ctx: ctx, client: c}
	return run.result(run.removeRow(request))
}

type dataWriteBindingOutcome struct {
	id  string
	row *dataWriteBindingRow
}

// dataWriteBindingPreview is the "key=value, ..." part of clio's created/skipped lines: every value but Id,
// as JsonNode.ToString renders it.
func dataWriteBindingPreview(row *dataWriteBindingRow) string {
	parts := []string{}
	for _, key := range row.keys {
		if strings.EqualFold(key, "Id") {
			continue
		}
		parts = append(parts, key+"="+dataWriteNodeText(row.values[key]))
	}
	return strings.Join(parts, ", ")
}

// dataWriteNodeText is JsonNode?.ToString() ?? "": a string value unquoted, a container as indented JSON,
// another value as its JSON text, a null as the empty string.
func dataWriteNodeText(node *jnode) string {
	if node == nil || node.kind == jkNull {
		return ""
	}
	switch node.kind {
	case jkString:
		return node.text
	case jkObject, jkArray:
		return string(node.stjIndentedJSON())
	}
	return string(node.stjJSON())
}

func (r *dataWriteBindingRun) createBinding(request DataWriteBindingDbRequest) ([]dataWriteBindingOutcome, []dataWriteBindingOutcome, error) {
	pkg, err := r.resolvePackage(request.PackageName)
	if err != nil {
		return nil, nil, err
	}
	bindingName := strings.TrimSpace(request.BindingName)
	if bindingName == "" {
		bindingName = request.SchemaName
	}
	schema, err := r.fetchSchema(request.SchemaName)
	if err != nil {
		return nil, nil, err
	}
	rows, err := dataWriteBindingParseRows(request.RowsJSON)
	if err != nil {
		return nil, nil, err
	}
	if err := dataWriteBindingValidateRequested(schema, rows); err != nil {
		return nil, nil, err
	}
	existingUID, _, found, err := r.findBinding(pkg.UID, bindingName)
	if err != nil {
		return nil, nil, err
	}
	var existingRows []*dataWriteBindingRow
	if found {
		if existingRows, err = r.boundRows(existingUID); err != nil {
			return nil, nil, err
		}
	}
	boundIDs, err := dataWriteBindingBoundIDs(existingRows)
	if err != nil {
		return nil, nil, err
	}
	nameToID := map[string]string{}
	if dataWriteBindingShouldFetchNames(schema, rows) {
		if nameToID, err = r.existingNames(request.SchemaName); err != nil {
			return nil, nil, err
		}
	}
	var created, skipped []dataWriteBindingOutcome
	for _, row := range rows {
		rowID := dataWriteBindingEnsureID(row)
		var rowName *string
		if node, ok := row.get("Name"); ok {
			if node != nil {
				text := dataWriteNodeText(node)
				rowName = &text
			}
		}
		if rowName != nil {
			if existingID, ok := nameToID[strings.ToUpper(*rowName)]; ok {
				boundIDs = dataWriteBindingAddID(boundIDs, existingID)
				copied := &dataWriteBindingRow{values: map[string]*jnode{}}
				for _, key := range row.keys {
					copied.set(key, row.values[key])
				}
				copied.set("Id", newString(existingID))
				skipped = append(skipped, dataWriteBindingOutcome{id: existingID, row: copied})
				continue
			}
		}
		exists, err := r.rowExists(request.SchemaName, rowID)
		if err != nil {
			return nil, nil, err
		}
		if !exists {
			if err := r.insertRow(request.SchemaName, row, schema.Columns); err != nil {
				return nil, nil, err
			}
		}
		if rowName != nil {
			nameToID[strings.ToUpper(*rowName)] = rowID
		}
		boundIDs = dataWriteBindingAddID(boundIDs, rowID)
		if exists {
			skipped = append(skipped, dataWriteBindingOutcome{id: rowID, row: row})
		} else {
			created = append(created, dataWriteBindingOutcome{id: rowID, row: row})
		}
	}
	projection, err := dataWriteBindingProjection(schema, existingRows, rows)
	if err != nil {
		return nil, nil, err
	}
	var existing *string
	if found {
		existing = &existingUID
	}
	if err := r.saveBinding(pkg, bindingName, request.SchemaName, projection, boundIDs, existing); err != nil {
		return nil, nil, err
	}
	return created, skipped, nil
}

func (r *dataWriteBindingRun) upsertRow(request DataWriteBindingDbRequest) error {
	pkg, err := r.resolvePackage(request.PackageName)
	if err != nil {
		return err
	}
	bindingUID, entity, err := r.lookupBinding(pkg.UID, request.BindingName)
	if err != nil {
		return err
	}
	schema, err := r.fetchSchema(entity)
	if err != nil {
		return err
	}
	values, err := dataWriteBindingParseValues(request.ValuesJSON)
	if err != nil {
		return err
	}
	if err := dataWriteBindingValidateRequested(schema, []*dataWriteBindingRow{values}); err != nil {
		return err
	}
	rowID := dataWriteBindingEnsureID(values)
	existingRows, err := r.boundRows(bindingUID)
	if err != nil {
		return err
	}
	ids, err := dataWriteBindingBoundIDs(existingRows)
	if err != nil {
		return err
	}
	alreadyBound := dataWriteBindingContains(ids, rowID)
	update := alreadyBound
	if !update {
		if update, err = r.rowExists(entity, rowID); err != nil {
			return err
		}
	}
	if update {
		err = r.updateRow(entity, rowID, values, schema.Columns)
	} else {
		err = r.insertRow(entity, values, schema.Columns)
	}
	if err != nil {
		return err
	}
	if !alreadyBound {
		ids = append(ids, rowID)
	}
	projection, err := dataWriteBindingProjection(schema, existingRows, []*dataWriteBindingRow{values})
	if err != nil {
		return err
	}
	return r.saveBinding(pkg, request.BindingName, schema.Name, projection, ids, &bindingUID)
}

func (r *dataWriteBindingRun) removeRow(request DataWriteBindingDbRequest) error {
	pkg, err := r.resolvePackage(request.PackageName)
	if err != nil {
		return err
	}
	bindingUID, entity, err := r.lookupBinding(pkg.UID, request.BindingName)
	if err != nil {
		return err
	}
	rows, err := r.boundRows(bindingUID)
	if err != nil {
		return err
	}
	ids, err := dataWriteBindingBoundIDs(rows)
	if err != nil {
		return err
	}
	if !dataWriteBindingContains(ids, request.KeyValue) {
		return fmt.Errorf("Row with key '%s' was not found in binding '%s'.", request.KeyValue, request.BindingName)
	}
	if err := r.deleteRow(entity, request.KeyValue); err != nil {
		return err
	}
	remainingIDs := []string{}
	for _, id := range ids {
		if !strings.EqualFold(id, request.KeyValue) {
			remainingIDs = append(remainingIDs, id)
		}
	}
	remainingRows := []*dataWriteBindingRow{}
	for _, row := range rows {
		if id, ok := row.get("Id"); ok && strings.EqualFold(dataWriteNodeText(id), request.KeyValue) {
			continue
		}
		remainingRows = append(remainingRows, row)
	}
	if len(remainingIDs) == 0 {
		return r.deleteBinding(pkg, request.BindingName)
	}
	schema, err := r.fetchSchema(entity)
	if err != nil {
		return err
	}
	projection, err := dataWriteBindingProjection(schema, remainingRows)
	if err != nil {
		return err
	}
	return r.saveBinding(pkg, request.BindingName, entity, projection, remainingIDs, &bindingUID)
}

// ---------------------------------------------------------------------------------------------- remote reads

// resolvePackage is PackageDataBindingWriter.ResolvePackage: a named package, locked or not.
func (r *dataWriteBindingRun) resolvePackage(packageName string) (dataWriteBindingPackage, error) {
	if strings.TrimSpace(packageName) == "" {
		return dataWriteBindingPackage{}, errors.New("Package name is required to write a package data binding.")
	}
	name := strings.TrimSpace(packageName)
	rows, err := r.client.selectTargetPackages(r.ctx, nil)
	if err != nil {
		return dataWriteBindingPackage{}, errors.New("The environment could not be asked which package to deliver the data into: " + err.Error())
	}
	for _, row := range rows {
		if !strings.EqualFold(row.Name, name) {
			continue
		}
		uid, ok := parseGUID(row.UID)
		if !ok || uid == emptyGUID {
			return dataWriteBindingPackage{}, fmt.Errorf("Package '%s' has no usable UId in the environment, so package data cannot be addressed to it. Name another package (see list-packages for the available names).", row.Name)
		}
		return dataWriteBindingPackage{UID: uid, Name: row.Name}, nil
	}
	return dataWriteBindingPackage{}, fmt.Errorf("Package '%s' was not found in the environment. Check the name against list-packages.", name)
}

// fetchSchema is DataBindingSchemaClient.Fetch over RuntimeEntitySchemaReader.GetByName.
func (r *dataWriteBindingRun) fetchSchema(schemaName string) (dataWriteBindingSchema, error) {
	if strings.TrimSpace(schemaName) == "" {
		return dataWriteBindingSchema{}, errors.New("Schema name is required.")
	}
	body := toJNode(orderedFields{{"Name", schemaName}}).stjJSON()
	payload, err := r.client.postDataServiceJSON(r.ctx, "RuntimeEntitySchemaRequest", body, dataWriteBindingTimeout, maxResponseBytes)
	if err != nil {
		return dataWriteBindingSchema{}, err
	}
	var response struct {
		Success bool `json:"success"`
		Schema  *struct {
			UID              string `json:"uId"`
			Name             string `json:"name"`
			PrimaryColumnUID string `json:"primaryColumnUId"`
			Columns          *struct {
				Items map[string]struct {
					UID           string `json:"uId"`
					Name          string `json:"name"`
					DataValueType int    `json:"dataValueType"`
				} `json:"items"`
			} `json:"columns"`
		} `json:"schema"`
		ErrorInfo *struct {
			Message *string `json:"message"`
		} `json:"errorInfo"`
	}
	if err := json.Unmarshal(payload, &response); err != nil {
		return dataWriteBindingSchema{}, fmt.Errorf("RuntimeEntitySchemaRequest returned invalid JSON: %w", err)
	}
	if !response.Success || response.Schema == nil {
		if response.ErrorInfo != nil && response.ErrorInfo.Message != nil {
			return dataWriteBindingSchema{}, errors.New(*response.ErrorInfo.Message)
		}
		return dataWriteBindingSchema{}, fmt.Errorf("Runtime schema '%s' was not returned by Creatio.", schemaName)
	}
	schema := dataWriteBindingSchema{Name: response.Schema.Name}
	if strings.TrimSpace(schema.Name) == "" {
		schema.Name = strings.TrimSpace(schemaName)
	}
	schema.UID, _ = parseGUID(response.Schema.UID)
	if schema.UID == "" {
		schema.UID = emptyGUID
	}
	if response.Schema.Columns != nil {
		for _, column := range response.Schema.Columns.Items {
			uid, _ := parseGUID(column.UID)
			if uid == "" {
				uid = emptyGUID
			}
			schema.Columns = append(schema.Columns, dataWriteBindingColumn{UID: uid, Name: column.Name, DataValueType: column.DataValueType})
		}
	}
	dataWriteBindingSortColumns(schema.Columns)
	if primary, ok := parseGUID(response.Schema.PrimaryColumnUID); !ok || primary == emptyGUID {
		return dataWriteBindingSchema{}, fmt.Errorf("Schema '%s' does not expose a primary column.", schemaName)
	}
	return schema, nil
}

// dataWriteBindingSortColumns orders columns by name as StringComparer.OrdinalIgnoreCase does (a stable sort,
// as LINQ OrderBy is).
func dataWriteBindingSortColumns(columns []dataWriteBindingColumn) {
	for i := 1; i < len(columns); i++ {
		for j := i; j > 0 && compareOrdinalIgnoreCase(columns[j].Name, columns[j-1].Name) < 0; j-- {
			columns[j], columns[j-1] = columns[j-1], columns[j]
		}
	}
}

// findBinding is PackageDataBindingWriter.FindBinding: the binding's UId and entity schema name, found=false
// when the package has no binding of that name.
func (r *dataWriteBindingRun) findBinding(packageUID, bindingName string) (string, string, bool, error) {
	rows, err := r.client.selectRows(r.ctx, buildSelectQuery("SysPackageSchemaData",
		map[string]string{"UId": "UId", "EntitySchemaName": "SysSchema.Name"},
		map[string]any{"filter0": comparisonFilter("Name", bindingName, 1, 3), "filter1": comparisonFilter("SysPackage.UId", packageUID, 0, 3)}, 10000))
	if err != nil {
		return "", "", false, err
	}
	if len(rows) > 1 {
		return "", "", false, fmt.Errorf("Package data binding '%s' has multiple registrations in package '%s'.", bindingName, packageUID)
	}
	if len(rows) == 0 {
		return "", "", false, nil
	}
	uid, ok := parseGUID(rowString(rows[0], "UId"))
	if !ok || uid == emptyGUID {
		return "", "", false, fmt.Errorf("Package data binding '%s' in package '%s' carries an unusable UId '%s', so a re-save could not update it in place.", bindingName, packageUID, rowString(rows[0], "UId"))
	}
	return uid, rowString(rows[0], "EntitySchemaName"), true, nil
}

// lookupBinding is DataBindingDbService.LookupBindingInfo: the binding must exist; its entity schema falls
// back to the binding name.
func (r *dataWriteBindingRun) lookupBinding(packageUID, bindingName string) (string, string, error) {
	uid, entity, found, err := r.findBinding(packageUID, bindingName)
	if err != nil {
		return "", "", err
	}
	if !found {
		return "", "", fmt.Errorf("Binding '%s' was not found in the remote environment.", bindingName)
	}
	if strings.TrimSpace(entity) == "" {
		entity = bindingName
	}
	return uid, entity, nil
}

// boundRows is DataBindingDbService.FetchBoundRows.
func (r *dataWriteBindingRun) boundRows(bindingUID string) ([]*dataWriteBindingRow, error) {
	nodes, err := r.client.dataBindingRows(r.ctx, bindingUID)
	if err != nil {
		return nil, err
	}
	rows := make([]*dataWriteBindingRow, 0, len(nodes))
	for _, node := range nodes {
		rows = append(rows, dataWriteBindingRowOf(node))
	}
	return rows, nil
}

// existingNames is FetchExistingEntityNameToId: Name to Id over the whole entity, the first Id of a name
// (compared ignoring case) kept. Keys are upper-cased for the case-insensitive lookup.
func (r *dataWriteBindingRun) existingNames(schemaName string) (map[string]string, error) {
	rows, err := r.client.selectRows(r.ctx, buildSelectQuery(schemaName, map[string]string{"Name": "Name", "Id": "Id"}, nil, 10000))
	if err != nil {
		return nil, err
	}
	result := map[string]string{}
	for _, row := range rows {
		name, id := rowString(row, "Name"), rowString(row, "Id")
		if strings.TrimSpace(name) == "" || strings.TrimSpace(id) == "" {
			continue
		}
		if _, ok := result[strings.ToUpper(name)]; !ok {
			result[strings.ToUpper(name)] = id
		}
	}
	return result, nil
}

// rowExists is RowExistsInTable over PackageDataBindingWriter.RowExists: false for an id that is not a GUID.
func (r *dataWriteBindingRun) rowExists(schemaName, rowID string) (bool, error) {
	id, ok := parseGUID(rowID)
	if !ok {
		return false, nil
	}
	rows, err := r.client.selectRows(r.ctx, buildSelectQuery(schemaName, map[string]string{"Id": "Id"},
		map[string]any{"filter0": comparisonFilter("Id", id, 0, 3)}, 10000))
	if err != nil {
		return false, err
	}
	for _, row := range rows {
		if strings.TrimSpace(rowString(row, "Id")) != "" {
			return true, nil
		}
	}
	return false, nil
}

// ---------------------------------------------------------------------------------------------- remote writes

func (r *dataWriteBindingRun) dataServiceWrite(route, operation string, body []byte) error {
	payload, err := r.client.callService(r.ctx, serviceCall{Route: route, Body: body, Timeout: dataWriteBindingTimeout, Label: operation})
	if err != nil {
		return err
	}
	return schemaWriteEntThrowIfUnsuccessful(payload, operation)
}

// dataWriteBindingParameter is one columnValues item: the column's insert type and its wire value.
func dataWriteBindingParameter(name string, value *jnode, columns []dataWriteBindingColumn) (orderedFields, error) {
	dataValueType := dataWriteBindingInsertType(name, columns)
	var wire any
	if dataValueType == dataWriteBindingColorType {
		text, err := dataWriteBindingColor(name, value)
		if err != nil {
			return nil, err
		}
		wire = text
	} else {
		wire = dataWriteNodeText(value)
	}
	return orderedFields{{"expressionType", 2}, {"parameter", orderedFields{{"dataValueType", dataValueType}, {"value", wire}}}}, nil
}

// insertRow is InsertEntityRow: every value of the row, Id included.
func (r *dataWriteBindingRun) insertRow(schemaName string, row *dataWriteBindingRow, columns []dataWriteBindingColumn) error {
	items := orderedFields{}
	for _, key := range row.keys {
		parameter, err := dataWriteBindingParameter(key, row.values[key], columns)
		if err != nil {
			return err
		}
		items = append(items, field{key, parameter})
	}
	body := toJNode(orderedFields{{"rootSchemaName", schemaName}, {"columnValues", orderedFields{{"items", items}}}}).stjJSON()
	return r.dataServiceWrite(dataWriteBindingInsertRoute, "InsertQuery", body)
}

// updateRow is UpdateEntityRow: every value but Id, by primary key; nothing is sent when no value is left.
func (r *dataWriteBindingRun) updateRow(schemaName, rowID string, row *dataWriteBindingRow, columns []dataWriteBindingColumn) error {
	items := orderedFields{}
	for _, key := range row.keys {
		if strings.EqualFold(key, "Id") {
			continue
		}
		parameter, err := dataWriteBindingParameter(key, row.values[key], columns)
		if err != nil {
			return err
		}
		items = append(items, field{key, parameter})
	}
	if len(items) == 0 {
		return nil
	}
	body := toJNode(orderedFields{{"rootSchemaName", schemaName}, {"columnValues", orderedFields{{"items", items}}},
		{"filters", dataWriteBindingKeyFilter(rowID)}}).stjJSON()
	return r.dataServiceWrite(dataWriteBindingUpdateRoute, "UpdateQuery", body)
}

// deleteRow is DeleteEntityRow: a DeleteQuery by primary key.
func (r *dataWriteBindingRun) deleteRow(schemaName, keyValue string) error {
	body := toJNode(orderedFields{{"__type", "Terrasoft.Nui.ServiceModel.DataContract.DeleteQuery"}, {"rootSchemaName", schemaName},
		{"filters", orderedFields{{"isEnabled", true}, {"filterType", 6}, {"logicalOperation", 0}, {"trimDateTimeParameterToDate", false},
			{"items", orderedFields{{"primaryFilter", dataWriteBindingKeyComparison(keyValue)}}}}}}).stjJSON()
	return r.dataServiceWrite(dataWriteBindingDeleteRoute, "DeleteQuery", body)
}

func dataWriteBindingKeyComparison(keyValue string) orderedFields {
	return orderedFields{{"filterType", 1}, {"comparisonType", 3}, {"isEnabled", true}, {"trimDateTimeParameterToDate", false},
		{"leftExpression", orderedFields{{"expressionType", 0}, {"columnPath", "Id"}}},
		{"rightExpression", orderedFields{{"expressionType", 2}, {"parameter", orderedFields{{"dataValueType", 0}, {"value", keyValue}}}}}}
}

func dataWriteBindingKeyFilter(keyValue string) orderedFields {
	return orderedFields{{"filterType", 6}, {"isEnabled", true}, {"trimDateTimeParameterToDate", false}, {"logicalOperation", 0},
		{"items", orderedFields{{"primaryFilter", dataWriteBindingKeyComparison(keyValue)}}}}
}

// saveBinding is PackageDataBindingWriter.SaveBinding: SaveSchema with the projected columns and the bound ids,
// then the file system development mode warning.
func (r *dataWriteBindingRun) saveBinding(pkg dataWriteBindingPackage, bindingName, entitySchemaName string, schema dataWriteBindingSchema,
	boundIDs []string, existingUID *string) error {
	uid := newGUID()
	if existingUID != nil {
		uid = *existingUID
	}
	columns := []any{}
	hasKey := false
	for _, column := range schema.Columns {
		typeUID, err := dataWriteBindingTypeUID(column)
		if err != nil {
			return err
		}
		isKey := strings.EqualFold(column.Name, "Id")
		hasKey = hasKey || isKey
		columns = append(columns, orderedFields{{"id", newGUID()}, {"uId", column.UID}, {"isForceUpdate", false}, {"isKey", isKey},
			{"name", column.Name}, {"caption", column.Name}, {"dataValueTypeUId", typeUID}})
	}
	if !hasKey {
		return fmt.Errorf("Binding '%s' would deliver schema '%s' with no key column, so the install target would match every row of the entity instead of the delivered one. Include the Id column in the delivered set, or supply a column policy naming the natural key.", bindingName, entitySchemaName)
	}
	ids := []any{}
	for _, id := range boundIDs {
		ids = append(ids, id)
	}
	body := toJNode(orderedFields{{"uId", uid}, {"name", bindingName}, {"package", orderedFields{{"uId", pkg.UID}, {"name", pkg.Name}}},
		{"entitySchemaUId", schema.UID}, {"entitySchemaName", entitySchemaName}, {"installType", 0},
		{"columns", columns}, {"boundRecordIds", ids}}).stjJSON()
	if err := r.dataServiceWrite(dataWriteBindingSaveRoute, "SaveSchema", body); err != nil {
		return err
	}
	change := "created in"
	if existingUID != nil {
		change = "re-saved in"
	}
	r.warnPackageFolder(pkg.Name, bindingName, change)
	return nil
}

// deleteBinding is PackageDataBindingWriter.DeleteBinding: the registration only, the rows stay.
func (r *dataWriteBindingRun) deleteBinding(pkg dataWriteBindingPackage, bindingName string) error {
	body := toJNode(orderedFields{{"packageUId", pkg.UID}, {"packageSchemaDataName", bindingName}}).stjJSON()
	if err := r.dataServiceWrite(dataWriteBindingDeleteDataRoute, "DeletePackageSchemaDataRequest", body); err != nil {
		return err
	}
	r.warnPackageFolder(pkg.Name, bindingName, "removed from")
	return nil
}

// warnPackageFolder is WarnIfPackageFolderIsNotUpdated: a warning unless file system development mode is
// known to be off. The state is read once per command; any failure to read it is "unknown".
func (r *dataWriteBindingRun) warnPackageFolder(packageName, bindingName, change string) {
	if !r.fileDesignRead {
		r.fileDesign = r.readFileDesignMode()
		r.fileDesignRead = true
	}
	if r.fileDesign != nil && !*r.fileDesign {
		return
	}
	r.log("Warning", dataWriteBindingFolderWarning(r.fileDesign, packageName, bindingName, change))
}

func (r *dataWriteBindingRun) readFileDesignMode() *bool {
	payload, err := r.client.callService(r.ctx, serviceCall{Route: dataWriteBindingFileDesignRoute, Body: []byte{}, Timeout: dataWriteBindingFileDesignTimeout})
	if err != nil {
		return nil
	}
	var response struct {
		Success bool `json:"success"`
		Value   bool `json:"value"`
	}
	if json.Unmarshal(payload, &response) != nil || !response.Success {
		return nil
	}
	return &response.Value
}

// dataWriteBindingFolderWarning is PackageDataBindingWriter.BuildPackageFolderWarning.
func dataWriteBindingFolderWarning(enabled *bool, packageName, bindingName, change string) string {
	folder := fmt.Sprintf("Pkg/%s/Data/%s", packageName, bindingName)
	remedy := "To write the database state to disk, run pkg-to-file-system for this environment " +
		"(CLI: clio pkg-to-file-system -e <environment>; MCP: clio-run with command pkg-to-file-system). It rewrites " +
		"every package folder on disk from the database, except client modules and C# source code, and " +
		"removes on-disk items the database does not have, so commit or copy on-disk work that is not in " +
		"the database yet before running it. Run it before the next pkg-to-db, which " +
		"makes the database match the files on disk."
	if enabled == nil {
		return fmt.Sprintf("Data binding '%s' of package '%s' was %s the database, but clio could not read whether file system "+
			"development mode is enabled on this environment. If it is, the package folder on disk (%s) was not updated. %s",
			bindingName, packageName, change, folder, remedy)
	}
	consequence := "a commit of that package still ships the removed binding"
	switch change {
	case "created in":
		consequence = "a commit of that package ships it without this binding"
	case "re-saved in":
		consequence = "a commit of that package carries the binding as the last export wrote it"
	}
	return fmt.Sprintf("File system development mode is enabled: data binding '%s' of package '%s' was %s the database only. "+
		"The package folder on disk (%s) was not updated by this change, so %s. %s", bindingName, packageName, change, folder, consequence, remedy)
}

// ---------------------------------------------------------------------------------------------- local rules

// dataWriteBindingTypeUID is ResolveBindingDataTypeValueUId over DataValueTypeMap.FromRuntimeValueType.
func dataWriteBindingTypeUID(column dataWriteBindingColumn) (string, error) {
	uid, ok := schemaWriteEntBindingTypes[column.DataValueType]
	if !ok {
		return "", fmt.Errorf("Column '%s' uses unsupported runtime dataValueType '%d' for DB-first binding generation.", column.Name, column.DataValueType)
	}
	return uid, nil
}

// dataWriteBindingInsertType is ResolveInsertDataValueType: the column's runtime type (text subtypes as 1),
// 1 for a name the schema does not have.
func dataWriteBindingInsertType(name string, columns []dataWriteBindingColumn) int {
	dataValueType := 1
	for _, column := range columns {
		if strings.EqualFold(column.Name, name) {
			dataValueType = column.DataValueType
			break
		}
	}
	if dataValueType >= 26 && dataValueType <= 30 {
		return 1
	}
	return dataValueType
}

// dataWriteBindingColor is ConvertColorValue: a JSON string as it is, a null kept null, anything else refused.
func dataWriteBindingColor(name string, value *jnode) (*string, error) {
	if value == nil || value.kind == jkNull {
		return nil, nil
	}
	if value.kind == jkString {
		text := value.text
		return &text, nil
	}
	return nil, fmt.Errorf("Column '%s' value '%s' is not valid for data type 'Color'.", name, string(value.stjJSON()))
}

// dataWriteBindingValidateRequested is ValidateRequestedBindingColumnsSupported: every requested column the
// schema has must have a binding type, and a Color value must be a hex string or null.
func dataWriteBindingValidateRequested(schema dataWriteBindingSchema, rows []*dataWriteBindingRow) error {
	for _, row := range rows {
		for _, key := range row.keys {
			if strings.TrimSpace(key) == "" {
				continue
			}
			for _, column := range schema.Columns {
				if !strings.EqualFold(column.Name, key) {
					continue
				}
				if _, err := dataWriteBindingTypeUID(column); err != nil {
					return err
				}
				if column.DataValueType == dataWriteBindingColorType {
					if _, err := dataWriteBindingColor(key, row.values[key]); err != nil {
						return err
					}
				}
				break
			}
		}
	}
	return nil
}

// dataWriteBindingProjection is BuildBindingSchemaProjection: the runtime columns named by any row (and Id).
func dataWriteBindingProjection(schema dataWriteBindingSchema, rowSets ...[]*dataWriteBindingRow) (dataWriteBindingSchema, error) {
	referenced := map[string]bool{"ID": true}
	for _, rows := range rowSets {
		for _, row := range rows {
			for _, key := range row.keys {
				if strings.TrimSpace(key) != "" {
					referenced[strings.ToUpper(key)] = true
				}
			}
		}
	}
	projection := dataWriteBindingSchema{UID: schema.UID, Name: schema.Name}
	for _, column := range schema.Columns {
		if referenced[strings.ToUpper(column.Name)] {
			if _, err := dataWriteBindingTypeUID(column); err != nil {
				return dataWriteBindingSchema{}, err
			}
			projection.Columns = append(projection.Columns, column)
		}
	}
	return projection, nil
}

func dataWriteBindingShouldFetchNames(schema dataWriteBindingSchema, rows []*dataWriteBindingRow) bool {
	if len(rows) == 0 {
		return false
	}
	hasName := false
	for _, column := range schema.Columns {
		if strings.EqualFold(column.Name, "Name") {
			hasName = true
		}
	}
	if !hasName {
		return false
	}
	for _, row := range rows {
		if _, ok := row.get("Name"); ok {
			return true
		}
	}
	return false
}

// dataWriteBindingEnsureID is EnsureRowId: the row's own non-blank Id (case-sensitive key), or a new GUID
// stored under "Id".
func dataWriteBindingEnsureID(row *dataWriteBindingRow) string {
	if node, ok := row.get("Id"); ok && node != nil {
		if text := dataWriteNodeText(node); strings.TrimSpace(text) != "" {
			return text
		}
	}
	id := newGUID()
	row.set("Id", newString(id))
	return id
}

// dataWriteBindingBoundIDs is ExtractBoundRecordIds: each bound row's non-blank string Id.
func dataWriteBindingBoundIDs(rows []*dataWriteBindingRow) ([]string, error) {
	ids := []string{}
	for _, row := range rows {
		node, ok := row.get("Id")
		if !ok || node == nil {
			continue
		}
		if node.kind != jkString {
			return nil, fmt.Errorf("The requested operation requires an element of type 'String', but the target element has type '%s'.", dataWriteSTJKind(node))
		}
		if strings.TrimSpace(node.text) != "" {
			ids = append(ids, node.text)
		}
	}
	return ids, nil
}

func dataWriteBindingContains(ids []string, id string) bool {
	for _, item := range ids {
		if strings.EqualFold(item, id) {
			return true
		}
	}
	return false
}

func dataWriteBindingAddID(ids []string, id string) []string {
	if dataWriteBindingContains(ids, id) {
		return ids
	}
	return append(ids, id)
}

// dataWriteBindingParseRows is ParseRowsJson: nil for blank text; only items carrying a "values" object count.
func dataWriteBindingParseRows(text string) ([]*dataWriteBindingRow, error) {
	if strings.TrimSpace(text) == "" {
		return nil, nil
	}
	node, err := parseJNode([]byte(text))
	if err != nil {
		return nil, errors.New("--rows must contain valid JSON. " + dataWriteSTJNodeParseError(text))
	}
	if !node.isArray() {
		return nil, errors.New("--rows must be a JSON array.")
	}
	rows := []*dataWriteBindingRow{}
	for _, item := range node.items {
		if values := item.get("values"); values.isObject() {
			rows = append(rows, dataWriteBindingRowOf(values))
		}
	}
	return rows, nil
}

// dataWriteBindingParseValues is ParseValues.
func dataWriteBindingParseValues(text string) (*dataWriteBindingRow, error) {
	if strings.TrimSpace(text) == "" {
		return nil, errors.New("--values is required.")
	}
	node, err := parseJNode([]byte(text))
	if err != nil {
		return nil, errors.New("--values must contain valid JSON. " + dataWriteSTJNodeParseError(text))
	}
	if !node.isObject() {
		return nil, errors.New("--values must be a JSON object keyed by column name.")
	}
	return dataWriteBindingRowOf(node), nil
}

// dataWriteSTJNodeParseError approximates the JsonException text of JsonNode.Parse for the usual invalid
// inputs: a bare word (an invalid literal) and a first character that cannot start a JSON value. Other
// malformed shapes get a generic sentence.
func dataWriteSTJNodeParseError(text string) string {
	trimmed := strings.TrimLeft(text, " \t\r\n")
	if trimmed == "" {
		return dataWriteSTJParseError(text)
	}
	lead := text[:len(text)-len(trimmed)]
	line := strings.Count(lead, "\n")
	column := len(lead) - (strings.LastIndex(lead, "\n") + 1)
	for _, literal := range []string{"true", "false", "null"} {
		if trimmed[0] != literal[0] {
			continue
		}
		mismatch := 0
		for mismatch < len(literal) && mismatch < len(trimmed) && trimmed[mismatch] == literal[mismatch] {
			mismatch++
		}
		if mismatch == len(literal) {
			break
		}
		return fmt.Sprintf("'%s' is an invalid JSON literal. Expected the literal '%s'. LineNumber: %d | BytePositionInLine: %d.",
			strings.TrimRight(trimmed, " \t\r\n"), literal, line, column+mismatch)
	}
	return dataWriteSTJParseError(text)
}
