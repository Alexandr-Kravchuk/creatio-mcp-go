package creatio

// clio 8.1.0.142's RecordDeletionService and DeleteRefusalText: reads why a record may not be deleted - whether
// it is visible, which rows reference it (SysEntitySchemaReference metadata, audit columns excluded) and
// whether a live process element holds it - and words a refused delete as one explanation.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf16"
)

const (
	recordDeletionMaxReferencePairs = 400
	recordDeletionMaxIDsPerPair     = 20
	recordDeletionMaxMetadataRows   = 5000
	recordDeletionMaxParentDepth    = 12
	recordDeletionGUIDType          = 0
	recordDeletionTextType          = 1
	recordDeletionProcessColumn     = "ProcessElementId"

	referencingCascade = "cascade"
	referencingOwned   = "owned"
	referencingLinked  = "linked"
)

var (
	recordDeletionLookupBases  = map[string]bool{"BaseLookup": true, "BaseCodeLookup": true, "BaseImageLookup": true, "BaseCodeImageLookup": true}
	recordDeletionAuditColumns = map[string]bool{"CreatedBy": true, "ModifiedBy": true}
	recordDeletionEntityName   = regexp.MustCompile(`\A[A-Za-z_][A-Za-z0-9_]{0,127}\z`)
)

// ReferencingRows is clio's ReferencingRows: rows of one entity that reference a record through one column.
type ReferencingRows struct {
	Entity string   `json:"entity"`
	Column string   `json:"column"`
	IDs    []string `json:"ids"`
	More   bool     `json:"more"`
	Kind   string   `json:"kind"`
}

type recordDeleteOutcome struct {
	success      bool
	err          string
	serverDetail string
	attempted    bool
	received     bool
}

type recordDeleteDiagnosis struct {
	visible          *bool
	processElementID string
	referencing      []ReferencingRows
	complete         bool
	unchecked        []string
}

func (d *recordDeleteDiagnosis) referencingList() []ReferencingRows {
	if d.referencing == nil {
		return []ReferencingRows{}
	}
	return d.referencing
}

type recordDeletionSchema struct {
	uid       string
	parentUID string
	isDBView  bool
	isVirtual bool
	columns   map[string]recordDeletionColumn
}

type recordDeletionColumn struct {
	isCascade, isRequired bool
}

type recordDeletionPairs struct {
	pairs     [][2]string
	truncated bool
}

// recordDeletionService serves one tool call: a cascade diagnoses many rows of the same entities.
type recordDeletionService struct {
	ctx     context.Context
	client  *Client
	lookups map[string]bool
	pairs   map[string]recordDeletionPairs
	schemas map[string]*recordDeletionSchema
}

func newRecordDeletionService(ctx context.Context, client *Client) *recordDeletionService {
	return &recordDeletionService{ctx: ctx, client: client, lookups: map[string]bool{}, pairs: map[string]recordDeletionPairs{},
		schemas: map[string]*recordDeletionSchema{}}
}

// ---- the ESQ shapes clio's ClassicEntitySchemaQuery builds ----

func esqColumn(path string) map[string]any {
	return map[string]any{"expression": map[string]any{"expressionType": 0, "columnPath": path}}
}

func esqEq(column, value string, dataValueType int) map[string]any {
	return map[string]any{"filterType": 1, "comparisonType": 3, "isEnabled": true,
		"leftExpression":  map[string]any{"expressionType": 0, "columnPath": column},
		"rightExpression": map[string]any{"expressionType": 2, "parameter": map[string]any{"dataValueType": dataValueType, "value": value}}}
}

func esqGroup(logical int, items map[string]any) map[string]any {
	return map[string]any{"filterType": 6, "logicalOperation": logical, "isEnabled": true, "items": items}
}

func esqQuery(root string, columns map[string]any, filters map[string]any, rowCount int) map[string]any {
	return map[string]any{"rootSchemaName": root, "operationType": 0, "columns": map[string]any{"items": columns},
		"filters": filters, "rowCount": rowCount}
}

// esqSelect posts one SelectQuery with no retry and reads its rows the way DataServiceSelectResponse.ReadRows
// does: an explicit failure, a non-empty errorInfo or a missing rows token is an error.
func (c *Client) esqSelect(ctx context.Context, query map[string]any, timeout time.Duration) ([]map[string]json.RawMessage, error) {
	body, err := json.Marshal(query)
	if err != nil {
		return nil, err
	}
	requestCtx, cancel := context.WithTimeout(ctx, max(time.Millisecond, timeout))
	defer cancel()
	payload, err := c.postDataServiceJSON(requestCtx, "SelectQuery", body, max(time.Millisecond, timeout), maxResponseBytes)
	if err != nil {
		return nil, err
	}
	var envelope struct {
		Success        *bool                        `json:"success"`
		ErrorInfo      map[string]json.RawMessage   `json:"errorInfo"`
		ResponseStatus map[string]json.RawMessage   `json:"responseStatus"`
		Rows           []map[string]json.RawMessage `json:"rows"`
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(payload, &probe); err != nil {
		return nil, err
	}
	_ = json.Unmarshal(payload, &envelope)
	errorCode := ""
	if raw, ok := envelope.ResponseStatus["ErrorCode"]; ok {
		_ = json.Unmarshal(raw, &errorCode)
	}
	if (envelope.Success != nil && !*envelope.Success) || len(envelope.ErrorInfo) > 0 || errorCode != "" {
		message := ""
		if raw, ok := envelope.ErrorInfo["message"]; ok {
			_ = json.Unmarshal(raw, &message)
		}
		if message == "" {
			if raw, ok := envelope.ResponseStatus["Message"]; ok {
				_ = json.Unmarshal(raw, &message)
			}
		}
		if message == "" {
			message = "Creatio DataService returned a failure response with no rows"
		}
		return nil, errors.New("SelectQuery failed: " + message)
	}
	if rows, ok := probe["rows"]; !ok || string(rows) == "null" {
		return nil, errors.New("SelectQuery failed: Creatio DataService returned a response with no rows and no explicit success signal")
	}
	return envelope.Rows, nil
}

// recordDeletionLookupValue is RunRecordProvenance.LookupValue: a lookup cell's id, or a plain id, never Guid.Empty.
func recordDeletionLookupValue(raw json.RawMessage) string {
	var text string
	var lookup map[string]json.RawMessage
	if json.Unmarshal(raw, &lookup) == nil && lookup != nil {
		text = rowText(lookup, "value")
	} else {
		text = rowText(map[string]json.RawMessage{"v": raw}, "v")
	}
	id, ok := parseGUID(text)
	if !ok || id == emptyGUID {
		return ""
	}
	return id
}

func (s *recordDeletionService) selectRows(query map[string]any, timeout time.Duration) ([]map[string]json.RawMessage, error) {
	return s.client.esqSelect(s.ctx, query, timeout)
}

// schema is the runtime schema read by name, cached per call; nil when it could not be read.
func (s *recordDeletionService) schema(entity string, timeout time.Duration) *recordDeletionSchema {
	if cached, ok := s.schemas[entity]; ok {
		return cached
	}
	var result *recordDeletionSchema
	body, _ := json.Marshal(map[string]string{"Name": entity})
	requestCtx, cancel := context.WithTimeout(s.ctx, max(time.Millisecond, timeout))
	payload, err := s.client.postDataServiceJSON(requestCtx, "RuntimeEntitySchemaRequest", body, max(time.Millisecond, timeout), maxResponseBytes)
	cancel()
	if err == nil {
		var response struct {
			Success bool `json:"success"`
			Schema  *struct {
				UID       string `json:"uId"`
				ParentUID string `json:"parentUId"`
				IsDBView  bool   `json:"isDBView"`
				IsVirtual bool   `json:"isVirtual"`
				Columns   *struct {
					Items map[string]struct {
						Name       string `json:"name"`
						IsRequired bool   `json:"isRequired"`
						IsCascade  bool   `json:"isCascade"`
					} `json:"items"`
				} `json:"columns"`
			} `json:"schema"`
		}
		if json.Unmarshal(payload, &response) == nil && response.Success && response.Schema != nil {
			result = &recordDeletionSchema{uid: response.Schema.UID, isDBView: response.Schema.IsDBView, isVirtual: response.Schema.IsVirtual,
				columns: map[string]recordDeletionColumn{}}
			if parent, ok := parseGUID(response.Schema.ParentUID); ok && parent != emptyGUID {
				result.parentUID = parent
			}
			if response.Schema.Columns != nil {
				for _, column := range response.Schema.Columns.Items {
					result.columns[column.Name] = recordDeletionColumn{isCascade: column.IsCascade, isRequired: column.IsRequired}
				}
			}
		}
	}
	s.schemas[entity] = result
	return result
}

// exists reads whether the record exists and this user can see it; nil when it could not be read.
func (s *recordDeletionService) exists(entity, id string, timeout time.Duration) *bool {
	rows, err := s.selectRows(esqQuery(entity, map[string]any{"Id": esqColumn("Id")},
		esqGroup(0, map[string]any{"byId": esqEq("Id", id, recordDeletionGUIDType)}), 1), timeout)
	if err != nil {
		return nil
	}
	found := len(rows) > 0
	return &found
}

// isLookup: registered in SysLookup or derived from a base lookup schema. Not knowing counts as a lookup: a
// cascade refused wrongly costs a call, a lookup cascaded costs data.
func (s *recordDeletionService) isLookup(entity string, timeout time.Duration) bool {
	if known, ok := s.lookups[strings.ToLower(entity)]; ok {
		return known
	}
	lookup := true
	if schema := s.schema(entity, timeout); schema != nil {
		rows, err := s.selectRows(esqQuery("SysLookup", map[string]any{"Id": esqColumn("Id")},
			esqGroup(0, map[string]any{"bySchema": esqEq("SysEntitySchemaUId", schema.uid, recordDeletionGUIDType)}), 1), timeout)
		if err == nil {
			lookup = len(rows) > 0
			if !lookup && schema.parentUID != "" {
				lookup = s.isLookupBase(schema.parentUID, timeout)
			}
		}
	}
	s.lookups[strings.ToLower(entity)] = lookup
	return lookup
}

// isLookupBase walks the parent chain up to BaseEntity; anything unreadable or cyclic counts as a lookup.
func (s *recordDeletionService) isLookupBase(parentUID string, timeout time.Duration) bool {
	seen := map[string]bool{}
	current := parentUID
	for depth := 0; depth < recordDeletionMaxParentDepth; depth++ {
		if current == "" || current == emptyGUID {
			return false
		}
		if seen[current] {
			return true
		}
		seen[current] = true
		rows, err := s.selectRows(esqQuery("SysSchema", map[string]any{"UId": esqColumn("UId"), "Name": esqColumn("Name")},
			esqGroup(0, map[string]any{"byUId": inFilter("UId", []string{current}, 0)}), 1), timeout)
		if err != nil {
			return true
		}
		name := ""
		for _, row := range rows {
			if value := rowText(row, "Name"); value != "" {
				name = value
				break
			}
		}
		if name == "" || recordDeletionLookupBases[name] {
			return true
		}
		if name == "BaseEntity" {
			return false
		}
		schema := s.schema(name, timeout)
		if schema == nil {
			return true
		}
		current = schema.parentUID
	}
	return true
}

// diagnose finds why a record may not be deleted. No request starts after stopAt, and each is bounded by
// what is left before it; the result then says it is partial.
func (s *recordDeletionService) diagnose(entity, id string, timeout time.Duration, stopAt time.Time) *recordDeleteDiagnosis {
	budget := func() time.Duration {
		left := time.Until(stopAt)
		return max(time.Millisecond, min(left, max(time.Millisecond, timeout)))
	}
	timeIsUp := func() bool { return !time.Now().Before(stopAt) }
	if timeIsUp() {
		return &recordDeleteDiagnosis{referencing: []ReferencingRows{}}
	}
	diagnosis := &recordDeleteDiagnosis{referencing: []ReferencingRows{}, complete: true}
	diagnosis.visible, diagnosis.processElementID = s.readVisibility(entity, id, budget())
	cached, ok := s.pairs[entity]
	if !ok {
		if timeIsUp() {
			diagnosis.complete = false
			return diagnosis
		}
		pairs, truncated, err := s.readReferencePairs(entity, budget())
		if err != nil {
			diagnosis.complete = false
			return diagnosis
		}
		cached = recordDeletionPairs{pairs: pairs, truncated: truncated}
		s.pairs[entity] = cached
	}
	if cached.truncated {
		diagnosis.complete = false
	}
	pairs := cached.pairs
	if len(pairs) > recordDeletionMaxReferencePairs {
		diagnosis.complete = false
		pairs = pairs[:recordDeletionMaxReferencePairs]
	}
	// One select per referencing entity, its referencing columns OR-ed; pairs are sorted by entity.
	for start := 0; start < len(pairs); {
		end := start
		for end < len(pairs) && pairs[end][0] == pairs[start][0] {
			end++
		}
		if timeIsUp() {
			diagnosis.complete = false
			break
		}
		columns := make([]string, 0, end-start)
		for _, pair := range pairs[start:end] {
			columns = append(columns, pair[1])
		}
		s.readReferencingEntity(pairs[start][0], columns, id, budget, timeIsUp, diagnosis)
		start = end
	}
	return diagnosis
}

func (s *recordDeletionService) readReferencingEntity(entity string, columns []string, id string, budget func() time.Duration,
	timeIsUp func() bool, diagnosis *recordDeleteDiagnosis) {
	if len(columns) > 1 && s.tryReadGrouped(entity, columns, id, budget, diagnosis) {
		return
	}
	for _, column := range columns {
		if timeIsUp() {
			diagnosis.complete = false
			return
		}
		rows, readable := s.readReferencingRows(entity, column, id, budget())
		if !readable {
			diagnosis.unchecked = append(diagnosis.unchecked, entity+"."+column)
			diagnosis.complete = false
		}
		if rows != nil {
			diagnosis.referencing = append(diagnosis.referencing, *rows)
		}
	}
}

// tryReadGrouped is false when the grouped select failed or filled its cap.
func (s *recordDeletionService) tryReadGrouped(entity string, columns []string, id string, budget func() time.Duration,
	diagnosis *recordDeleteDiagnosis) bool {
	limit := (recordDeletionMaxIDsPerPair + 1) * len(columns)
	selected := map[string]any{"Id": esqColumn("Id")}
	anyOf := map[string]any{}
	for i, column := range columns {
		selected[column] = esqColumn(column)
		anyOf[fmt.Sprintf("ref%d", i)] = esqEq(column, id, recordDeletionGUIDType)
	}
	rows, err := s.selectRows(esqQuery(entity, selected, esqGroup(0, map[string]any{"any": esqGroup(1, anyOf)}), limit), budget())
	if err != nil || len(rows) >= limit {
		return false
	}
	target := id
	if parsed, ok := parseGUID(id); ok {
		target = parsed
	}
	for _, column := range columns {
		ids := []string{}
		for _, row := range rows {
			if strings.EqualFold(recordDeletionLookupValue(row[column]), target) {
				if raw, ok := row["Id"]; ok && string(raw) != "null" {
					ids = append(ids, rowText(row, "Id"))
				}
			}
		}
		if len(ids) == 0 {
			continue
		}
		if found := s.classify(entity, column, ids, budget()); found != nil {
			diagnosis.referencing = append(diagnosis.referencing, *found)
		}
	}
	return true
}

// readVisibility reads the record's own row (absent: missing or not readable) and, when the entity has it,
// the ProcessElementId of a live element that holds it.
func (s *recordDeletionService) readVisibility(entity, id string, timeout time.Duration) (*bool, string) {
	schema := s.schema(entity, timeout)
	hasProcessElement := false
	if schema != nil {
		_, hasProcessElement = schema.columns[recordDeletionProcessColumn]
	}
	columns := map[string]any{"Id": esqColumn("Id")}
	if hasProcessElement {
		columns[recordDeletionProcessColumn] = esqColumn(recordDeletionProcessColumn)
	}
	rows, err := s.selectRows(esqQuery(entity, columns, esqGroup(0, map[string]any{"byId": esqEq("Id", id, recordDeletionGUIDType)}), 1), timeout)
	if err != nil {
		return nil, ""
	}
	visible := len(rows) > 0
	element := ""
	if visible {
		if elementID, ok := parseGUID(rowText(rows[0], recordDeletionProcessColumn)); ok && elementID != emptyGUID && s.isLiveProcessElement(elementID, timeout) {
			element = elementID
		}
	}
	return &visible, element
}

// isLiveProcessElement: a completed element's data row is deleted; an unreadable table counts as live.
func (s *recordDeletionService) isLiveProcessElement(elementID string, timeout time.Duration) bool {
	rows, err := s.selectRows(esqQuery("SysProcessElementData", map[string]any{"Id": esqColumn("Id")},
		esqGroup(0, map[string]any{"byId": esqEq("Id", elementID, recordDeletionGUIDType)}), 1), timeout)
	return err != nil || len(rows) > 0
}

// readReferencePairs: distinct (schema, column) pairs whose lookup targets the entity, sorted ordinally.
func (s *recordDeletionService) readReferencePairs(entity string, timeout time.Duration) ([][2]string, bool, error) {
	rows, err := s.selectRows(esqQuery("SysEntitySchemaReference",
		map[string]any{"SchemaName": esqColumn("SysSchema.Name"), "ColumnName": esqColumn("ColumnName")},
		esqGroup(0, map[string]any{"target": esqEq("ReferenceSchema.Name", entity, recordDeletionTextType)}), recordDeletionMaxMetadataRows), timeout)
	if err != nil {
		return nil, false, err
	}
	seen := map[[2]string]bool{}
	pairs := [][2]string{}
	for _, row := range rows {
		pair := [2]string{rowText(row, "SchemaName"), rowText(row, "ColumnName")}
		if _, ok := row["SchemaName"]; !ok {
			continue
		}
		if !recordDeletionEntityName.MatchString(pair[0]) || !recordDeletionEntityName.MatchString(pair[1]) || recordDeletionAuditColumns[pair[1]] || seen[pair] {
			continue
		}
		seen[pair] = true
		pairs = append(pairs, pair)
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i][0] != pairs[j][0] {
			return pairs[i][0] < pairs[j][0]
		}
		return pairs[i][1] < pairs[j][1]
	})
	return pairs, len(rows) >= recordDeletionMaxMetadataRows, nil
}

// readReferencingRows: readable is false when the column could not be checked (a timeout, a rights refusal,
// a schema with no table of its own); a virtual schema or a DB view cannot block a delete.
func (s *recordDeletionService) readReferencingRows(entity, column, id string, timeout time.Duration) (*ReferencingRows, bool) {
	rows, err := s.selectRows(esqQuery(entity, map[string]any{"Id": esqColumn("Id")},
		esqGroup(0, map[string]any{"ref": esqEq(column, id, recordDeletionGUIDType)}), recordDeletionMaxIDsPerPair+1), timeout)
	if err != nil {
		failed := s.schema(entity, timeout)
		return nil, failed != nil && (failed.isVirtual || failed.isDBView)
	}
	if len(rows) == 0 {
		return nil, true
	}
	ids := []string{}
	for _, row := range rows {
		if raw, ok := row["Id"]; ok && string(raw) != "null" {
			ids = append(ids, rowText(row, "Id"))
		}
	}
	return s.classify(entity, column, ids, timeout), true
}

// classify: cascade when the platform deletes the row with the record, owned when its lookup is required,
// linked otherwise. A DB view cannot block a delete, so it is not reported.
func (s *recordDeletionService) classify(entity, column string, ids []string, timeout time.Duration) *ReferencingRows {
	schema := s.schema(entity, timeout)
	if schema != nil && schema.isDBView {
		return nil
	}
	kind := referencingLinked
	if schema != nil {
		if lookup, ok := schema.columns[column]; ok {
			if lookup.isCascade {
				kind = referencingCascade
			} else if lookup.isRequired {
				kind = referencingOwned
			}
		}
	}
	listed := ids
	if len(listed) > recordDeletionMaxIDsPerPair {
		listed = listed[:recordDeletionMaxIDsPerPair]
	}
	return &ReferencingRows{Entity: entity, Column: column, IDs: append([]string{}, listed...), More: len(ids) > recordDeletionMaxIDsPerPair, Kind: kind}
}

// recordDeletionFirstLine is ProcessRunMonitor.FirstLine.
func recordDeletionFirstLine(text string) string {
	for _, line := range strings.FieldsFunc(text, func(r rune) bool { return r == '\r' || r == '\n' }) {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// recordDeletionInnerMessage is RecordDeletionService.ReadInnerMessage: the deepest innererror.message of an
// OData error body, fenced. Creatio puts the reason there and only "An error has occurred." in the headline.
func recordDeletionInnerMessage(response string) string {
	if dataWriteBlank(response) {
		return ""
	}
	var root map[string]json.RawMessage
	if json.Unmarshal([]byte(response), &root) != nil {
		return ""
	}
	var current map[string]json.RawMessage
	if json.Unmarshal(root["error"], &current) != nil || current == nil {
		return ""
	}
	inner := ""
	for depth := 0; depth < 5; depth++ {
		next, ok := current["innererror"]
		if !ok {
			next, ok = current["internalexception"]
		}
		if !ok {
			break
		}
		var object map[string]json.RawMessage
		if json.Unmarshal(next, &object) != nil || object == nil {
			break
		}
		var message string
		if raw, has := object["message"]; has && json.Unmarshal(raw, &message) == nil && strings.TrimSpace(message) != "" {
			inner = message
		}
		current = object
	}
	if inner == "" {
		return ""
	}
	line := recordDeletionFirstLine(inner)
	if units := utf16.Encode([]rune(line)); len(units) > 500 {
		line = string(utf16.Decode(units[:500]))
	}
	if fenced := DataWriteUntrusted(line); fenced != nil {
		return *fenced
	}
	return ""
}

// explainDeleteRefusal is DeleteRefusalText.Explain: never empty, because a refusal must always be explained.
func explainDeleteRefusal(entity, id string, outcome recordDeleteOutcome, diagnosis *recordDeleteDiagnosis) string {
	parts := []string{}
	if outcome.serverDetail != "" {
		parts = append(parts, fmt.Sprintf("The server's own reason: %s.", outcome.serverDetail))
	}
	notVisible := diagnosis != nil && diagnosis.visible != nil && !*diagnosis.visible
	if notVisible {
		parts = append(parts, fmt.Sprintf("%s(%s) is not visible to this user: it does not exist, was already deleted, or the user "+
			"has no read access to it.", entity, id))
	}
	if diagnosis != nil && diagnosis.processElementID != "" {
		parts = append(parts, fmt.Sprintf("It is bound to process element %s (ProcessElementId): a process run waiting on it - for "+
			"example a Perform task that created this activity - keeps it from being deleted. Complete the task or cancel that run "+
			"first; run-process with cleanup does this for the records a run created.", diagnosis.processElementID))
	}
	blocking := []string{}
	if diagnosis != nil {
		for _, rows := range diagnosis.referencing {
			if rows.Kind == referencingCascade {
				continue
			}
			more := ""
			if rows.More {
				more = "+"
			}
			blocking = append(blocking, fmt.Sprintf("%s.%s x%d%s (%s)", rows.Entity, rows.Column, len(rows.IDs), more, rows.Kind))
		}
	}
	if len(blocking) > 0 {
		parts = append(parts, "Rows that still reference it: "+strings.Join(blocking, "; ")+". Owned rows (a required lookup) go "+
			"with cascade: true - call with dry-run: true first to list them; linked rows (an optional lookup) are never deleted by "+
			"cascade: re-point or clear that lookup, or delete them separately if the user asks.")
	}
	incomplete := diagnosis != nil && !diagnosis.complete
	if incomplete {
		listed := ""
		if len(diagnosis.unchecked) > 0 {
			shown := diagnosis.unchecked
			if len(shown) > 10 {
				shown = shown[:10]
			}
			listed = " (" + strings.Join(shown, ", ") + ")"
		}
		parts = append(parts, "Not every referencing table could be checked"+listed+", so other referencing rows may exist.")
	}
	nothingFound := len(blocking) == 0 && (diagnosis == nil || diagnosis.processElementID == "") && !notVisible
	if len(parts) == 0 || (nothingFound && !incomplete) {
		tail := " - the server's reason above says which."
		if outcome.serverDetail == "" {
			tail = "; the server gave no reason."
		}
		parts = append(parts, "No referencing rows and no process binding were found, so the refusal comes from the user's delete "+
			"rights on this object or record, or from an entity event handler that refuses it"+tail)
	}
	return strings.Join(parts, " ")
}
