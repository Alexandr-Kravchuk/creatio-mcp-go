package creatio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"
)

// SequenceContextSection is one bounded section of sequence discovery: complete, truncated, missing or failed.
type SequenceContextSection struct {
	State string  `json:"state"`
	Data  any     `json:"data"`
	Error *string `json:"error,omitempty"`
}

// SequenceContextSections keeps the sections in the order clio adds them.
type SequenceContextSections struct {
	keys   []string
	values map[string]SequenceContextSection
}

func (s *SequenceContextSections) set(key string, section SequenceContextSection) {
	if s.values == nil {
		s.values = map[string]SequenceContextSection{}
	}
	if _, exists := s.values[key]; !exists {
		s.keys = append(s.keys, key)
	}
	s.values[key] = section
}

// Get returns a section and whether it was reported.
func (s *SequenceContextSections) Get(key string) (SequenceContextSection, bool) {
	section, ok := s.values[key]
	return section, ok
}

func (s *SequenceContextSections) MarshalJSON() ([]byte, error) {
	var buffer bytes.Buffer
	buffer.WriteByte('{')
	for index, key := range s.keys {
		if index > 0 {
			buffer.WriteByte(',')
		}
		encodedKey, _ := json.Marshal(key)
		buffer.Write(encodedKey)
		buffer.WriteByte(':')
		encoded, err := json.Marshal(s.values[key])
		if err != nil {
			return nil, err
		}
		buffer.Write(encoded)
	}
	buffer.WriteByte('}')
	return buffer.Bytes(), nil
}

// SequenceContextResult is clio's get-sequence-context answer.
type SequenceContextResult struct {
	Success      bool                     `json:"success"`
	Availability string                   `json:"availability"`
	Sections     *SequenceContextSections `json:"sections"`
	Limitations  []string                 `json:"limitations"`
}

type sequenceContextColumn struct {
	Name      string  `json:"name"`
	Type      string  `json:"type"`
	Required  bool    `json:"required"`
	Reference *string `json:"reference,omitempty"`
}

const (
	sequenceContextRowLimit       = 100
	sequenceContextResponseBudget = 200_000
	sequenceContextComplete       = "complete"
	sequenceContextFailed         = "failed"
	sequenceContextMissing        = "missing"
	sequenceContextTruncated      = "truncated"
	sequenceContextSchedule       = "DeliverySchedule"
)

var sequenceContextSchemas = []string{"Sequence", "SequenceStep", "SequenceParticipant", "SequenceRuleset", sequenceContextSchedule, "DeliveryScheduleSlot"}

var sequenceContextChoiceSchemas = []string{
	"SequenceStatus", "SequenceType", "SeqIntervalType", "SeqRecipientReplyBehavior", "SequenceStepType", "SequenceStepPostponeTimeUnits", "SequenceAudienceStage",
	"SeqParticipantStatus", "SeqEnrollmentEligibility", "SeqEmailMode", "SeqEmailThreadingMode",
	"ActivityPriority", "SequenceStepAction", "DayOfWeek", "SequenceRuleset", sequenceContextSchedule,
}

var sequenceContextLimitations = []string{
	"Metadata presence does not prove permission to activate, enroll or send email.",
	"Required fields describe schema metadata, not dynamic rules or defaults.",
	"Choices are capped at 100 rows; use targeted execute-esq reads for truncated sections.",
	"Use native sequence enrollment; do not create Active participants or their activities manually.",
}

// ErrSequenceIDEmpty is clio's refusal of the all-zero sequence id.
var ErrSequenceIDEmpty = errors.New("sequence-id must be a non-empty UUID.")

// GetSequenceContext reads the effective sequence schemas, the bounded lookup choices and, for a sequence id,
// that definition with its steps and prerequisites, all through read-only DataService queries. Every section
// states whether it is complete; clio's 100-row and 200 000-byte budgets apply. sequenceID is lower-case "D".
func (c *Client) GetSequenceContext(ctx context.Context, sequenceID *string) (SequenceContextResult, error) {
	if sequenceID != nil && *sequenceID == emptyGUID {
		return SequenceContextResult{}, ErrSequenceIDEmpty
	}
	sections := &SequenceContextSections{}
	catalog, catalogRows := c.sequenceContextRows(ctx, "SysSchema", []string{"Id"}, "Name", "Sequence", false, 1)
	if catalog.State == sequenceContextTruncated {
		catalog.State = sequenceContextComplete
	}
	sections.set("schema-presence", catalog)
	if catalog.State == sequenceContextFailed {
		return sequenceContextResult("unknown", sections), nil
	}
	if len(catalogRows) == 0 {
		return sequenceContextResult("absent", sections), nil
	}
	for _, schema := range sequenceContextSchemas {
		sections.set("schema:"+schema, c.sequenceContextSchema(ctx, schema))
	}
	for _, schema := range sequenceContextChoiceSchemas {
		section, _ := c.sequenceContextRows(ctx, schema, []string{"Id", "Name"}, "", "", false, sequenceContextRowLimit)
		sections.set("choices:"+schema, section)
	}
	if sequenceID != nil {
		sequence, records := c.sequenceContextRows(ctx, "Sequence", []string{"Id", "Name", "Status", "Ruleset", sequenceContextSchedule}, "Id", *sequenceID, true, 1)
		sections.set("sequence", sequence)
		steps, _ := c.sequenceContextRows(ctx, "SequenceStep", []string{"Id", "Name", "Index", "Type", "Postpone", "Measurement"}, "Sequence", *sequenceID, true, sequenceContextRowLimit)
		sections.set("steps", steps)
		sequenceContextRequireRows(sections, "steps", "No visible sequence steps were found.")
		c.sequenceContextPrerequisites(ctx, sections, sequence, records)
	}
	result := sequenceContextResult("present", sections)
	if encoded, err := json.Marshal(result); err == nil && len(encoded) > sequenceContextResponseBudget {
		budget := &SequenceContextSections{}
		message := "Context exceeds the response budget; use targeted schema and ESQ reads."
		budget.set("context", SequenceContextSection{State: sequenceContextFailed, Error: &message})
		return SequenceContextResult{Success: false, Availability: "present", Sections: budget, Limitations: sequenceContextLimitations}, nil
	}
	return result, nil
}

func sequenceContextResult(availability string, sections *SequenceContextSections) SequenceContextResult {
	success := availability == "present"
	for _, key := range sections.keys {
		if sections.values[key].State != sequenceContextComplete {
			success = false
		}
	}
	return SequenceContextResult{Success: success, Availability: availability, Sections: sections, Limitations: sequenceContextLimitations}
}

func sequenceContextFailure(message string) SequenceContextSection {
	return SequenceContextSection{State: sequenceContextFailed, Error: &message}
}

// sequenceContextSchema reports a schema's effective columns, capped at 100, from the merged runtime schema.
func (c *Client) sequenceContextSchema(ctx context.Context, schema string) SequenceContextSection {
	properties, err := c.GetEntitySchemaProperties(ctx, EntitySchemaPropertiesRequest{SchemaName: schema})
	if err != nil {
		return sequenceContextFailure(err.Error())
	}
	if len(properties.Columns) == 0 {
		return sequenceContextFailure("Effective schema columns are unavailable.")
	}
	state := sequenceContextComplete
	columns := properties.Columns
	if len(columns) > sequenceContextRowLimit {
		state, columns = sequenceContextTruncated, columns[:sequenceContextRowLimit]
	}
	data := make([]sequenceContextColumn, 0, len(columns))
	for _, column := range columns {
		data = append(data, sequenceContextColumn{Name: column.Name, Type: column.Type, Required: column.Required, Reference: column.ReferenceSchemaName})
	}
	return SequenceContextSection{State: state, Data: data}
}

// sequenceContextRows is clio's ReadRows: one SelectQuery for limit+1 rows, refused when the answer exceeds
// the byte budget, is not a successful rows envelope, or omits a requested column. Rows are kept verbatim.
func (c *Client) sequenceContextRows(ctx context.Context, schema string, columns []string, filterColumn, filterValue string, guidFilter bool, limit int) (SequenceContextSection, []json.RawMessage) {
	paths := make(map[string]string, len(columns))
	for _, column := range columns {
		paths[column] = column
	}
	filters := map[string]any{}
	if filterColumn != "" {
		dataValueType := 1
		if guidFilter {
			dataValueType = 0
		}
		filters["filter0"] = comparisonFilter(filterColumn, filterValue, dataValueType, 3)
	}
	query := buildSelectQuery(schema, paths, filters, limit+1)
	if schema == "SequenceStep" {
		items := query["columns"].(map[string]any)["items"].(map[string]any)
		index := items["Index"].(map[string]any)
		index["orderDirection"], index["orderPosition"] = 1, 0
	}
	body, err := json.Marshal(query)
	if err != nil {
		return sequenceContextFailure(err.Error()), nil
	}
	payload, err := c.postDataServiceJSON(ctx, "SelectQuery", body, 10*time.Second, maxResponseBytes)
	if err != nil {
		return sequenceContextFailure(err.Error()), nil
	}
	if len(payload) > sequenceContextResponseBudget {
		return sequenceContextFailure("DataService response exceeds the response budget."), nil
	}
	var response struct {
		Success json.RawMessage   `json:"success"`
		Rows    []json.RawMessage `json:"rows"`
	}
	var shape map[string]json.RawMessage
	if json.Unmarshal(payload, &shape) != nil || json.Unmarshal(payload, &response) != nil ||
		string(response.Success) != "true" || response.Rows == nil || !bytes.HasPrefix(bytes.TrimSpace(shape["rows"]), []byte("[")) {
		return sequenceContextFailure("DataService read failed. Check schema availability and read permissions; do not infer absence."), nil
	}
	rows := response.Rows
	if len(rows) > limit {
		rows = rows[:limit]
	}
	for _, row := range rows {
		var fields map[string]json.RawMessage
		if json.Unmarshal(row, &fields) != nil {
			return sequenceContextFailure("DataService omitted a requested field; check effective schema compatibility."), nil
		}
		for _, column := range columns {
			if _, ok := fields[column]; !ok {
				return sequenceContextFailure("DataService omitted a requested field; check effective schema compatibility."), nil
			}
		}
	}
	state := sequenceContextComplete
	if len(response.Rows) > limit {
		state = sequenceContextTruncated
	}
	return SequenceContextSection{State: state, Data: rows}, rows
}

func sequenceContextRequireRows(sections *SequenceContextSections, key, message string) {
	section := sections.values[key]
	if rows, ok := section.Data.([]json.RawMessage); ok && section.State == sequenceContextComplete && len(rows) == 0 {
		section.State, section.Error = sequenceContextMissing, &message
		sections.set(key, section)
	}
}

// sequenceContextPrerequisites is clio's InspectPrerequisites: a sequence needs a ruleset and a delivery
// schedule, and the schedule needs delivery slots.
func (c *Client) sequenceContextPrerequisites(ctx context.Context, sections *SequenceContextSections, sequence SequenceContextSection, records []json.RawMessage) {
	if _, ok := sequence.Data.([]json.RawMessage); !ok {
		return
	}
	if len(records) == 0 {
		message := "Sequence not found or not visible to the current user."
		sections.set("sequence", SequenceContextSection{State: sequenceContextMissing, Data: records, Error: &message})
		return
	}
	var record map[string]json.RawMessage
	_ = json.Unmarshal(records[0], &record)
	for _, field := range []string{"Ruleset", sequenceContextSchedule} {
		reference := sequenceContextReference(record[field])
		if reference == "" || reference == emptyGUID {
			sections.set("prerequisite:"+field, sequenceContextFailureState(sequenceContextMissing, field+" must be configured."))
			continue
		}
		if field == "Ruleset" {
			ruleset, _ := c.sequenceContextRows(ctx, "SequenceRuleset", []string{"Id", "Name", "EnrollmentEligibility", "MaxActiveParticipantsPerUser",
				"MaxAddsPerUserPer24Hours", "ParticAddedToSequence", "ParticFirstOutreachCompleted", "ParticReplies", "ParticEmailBounces", "ParticOptsOut"},
				"Id", reference, true, 1)
			sections.set("selected-ruleset", ruleset)
			sequenceContextRequireRows(sections, "selected-ruleset", "Selected ruleset was not found or is not visible.")
			continue
		}
		schedule, _ := c.sequenceContextRows(ctx, sequenceContextSchedule, []string{"Id", "Name", "DefaultTimeZone", "UseProspectTimezone", "UseSenderTimezone", "HolidaysByCountry"},
			"Id", reference, true, 1)
		sections.set("selected-schedule", schedule)
		sequenceContextRequireRows(sections, "selected-schedule", "Selected schedule was not found or is not visible.")
		slots, slotRows := c.sequenceContextRows(ctx, "DeliveryScheduleSlot", []string{"Id", "DayOfWeek", "TimeFrom", "TimeTo"}, sequenceContextSchedule, reference, true, sequenceContextRowLimit)
		sections.set("schedule-slots", slots)
		if _, ok := slots.Data.([]json.RawMessage); ok && len(slotRows) == 0 {
			message := "Configure delivery slots."
			sections.set("schedule-slots", SequenceContextSection{State: sequenceContextMissing, Data: []json.RawMessage{}, Error: &message})
		}
	}
}

func sequenceContextFailureState(state, message string) SequenceContextSection {
	return SequenceContextSection{State: state, Error: &message}
}

// sequenceContextReference reads a lookup cell, either a bare GUID string or a {value, displayValue} envelope.
func sequenceContextReference(raw json.RawMessage) string {
	var envelope struct {
		Value json.RawMessage `json:"value"`
	}
	if json.Unmarshal(raw, &envelope) == nil && envelope.Value != nil {
		raw = envelope.Value
	}
	var text string
	if json.Unmarshal(raw, &text) != nil {
		return ""
	}
	return normalizeGUID(text)
}
