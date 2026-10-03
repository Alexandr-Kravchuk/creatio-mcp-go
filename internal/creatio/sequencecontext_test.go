package creatio

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

const sequenceTestID = "55555555-5555-5555-5555-555555555555"
const sequenceTestRuleset = "66666666-6666-6666-6666-666666666666"

func TestGetSequenceContextInspectsADefinition(t *testing.T) {
	client, requests := groupIServer(t, func(request groupIRequest) (int, string) {
		switch {
		case request.Path == "/0/DataService/json/SyncReply/RuntimeEntitySchemaRequest":
			return 200, `{"success":true,"schema":{"name":"X","columns":{"items":{"b":{"name":"Ruleset","dataValueType":10,"isRequired":true,"referenceSchemaName":"SequenceRuleset"},"a":{"name":"Id","dataValueType":0,"isRequired":true}}}}}`
		case request.Root == "SysSchema":
			return 200, `{"success":true,"rows":[{"Id":"77777777-7777-7777-7777-777777777777"}]}`
		case request.Root == "Sequence":
			return 200, `{"success":true,"rows":[{"Id":"` + sequenceTestID + `","Name":"Outreach","Status":{"value":"s","displayValue":"Draft"},` +
				`"Ruleset":{"value":"` + sequenceTestRuleset + `","displayValue":"Default"},"DeliverySchedule":{"value":"","displayValue":""}}]}`
		case request.Root == "SequenceStep":
			return 200, `{"success":true,"rows":[]}`
		case request.Root == "SequenceRuleset" && request.Filters["Id"] == sequenceTestRuleset:
			return 200, `{"success":true,"rows":[{"Id":"` + sequenceTestRuleset + `","Name":"Default","EnrollmentEligibility":null,"MaxActiveParticipantsPerUser":5,` +
				`"MaxAddsPerUserPer24Hours":5,"ParticAddedToSequence":true,"ParticFirstOutreachCompleted":true,"ParticReplies":true,"ParticEmailBounces":true,"ParticOptsOut":true}]}`
		case request.Root == "SequenceStatus":
			return 200, `{"success":false,"errorInfo":{"message":"denied"}}`
		}
		return 200, `{"success":true,"rows":[{"Name":"Only name"}]}`
	})
	result, err := client.GetSequenceContext(context.Background(), stringPointer(sequenceTestID))
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(result)
	text := string(encoded)
	for _, fragment := range []string{
		`"success":false,"availability":"present"`,
		`"schema-presence":{"state":"complete","data":[{"Id":"77777777-7777-7777-7777-777777777777"}]}`,
		`"schema:Sequence":{"state":"complete","data":[{"name":"Id","type":"Guid","required":true},{"name":"Ruleset","type":"Lookup","required":true,"reference":"SequenceRuleset"}]}`,
		`"choices:SequenceStatus":{"state":"failed","data":null,"error":"DataService read failed. Check schema availability and read permissions; do not infer absence."}`,
		`"choices:SequenceType":{"state":"failed","data":null,"error":"DataService omitted a requested field; check effective schema compatibility."}`,
		`"steps":{"state":"missing","data":[],"error":"No visible sequence steps were found."}`,
		`"selected-ruleset":{"state":"complete","data":[{"Id":"` + sequenceTestRuleset + `","Name":"Default","EnrollmentEligibility":null,`,
		`"prerequisite:DeliverySchedule":{"state":"missing","data":null,"error":"DeliverySchedule must be configured."}`,
	} {
		if !strings.Contains(text, fragment) {
			t.Errorf("result lacks %s\n%s", fragment, text)
		}
	}
	if strings.Index(text, `"schema-presence"`) > strings.Index(text, `"schema:Sequence"`) || strings.Index(text, `"steps"`) > strings.Index(text, `"selected-ruleset"`) {
		t.Errorf("sections are not in clio's order: %s", text)
	}
	for _, request := range *requests {
		if request.Root == "SequenceStep" {
			if !strings.Contains(request.Body, `"Index":{"expression":{"columnPath":"Index","expressionType":0},"isVisible":true,"orderDirection":1,"orderPosition":0}`) ||
				!strings.Contains(request.Body, `"rowCount":101`) || request.Filters["Sequence"] != sequenceTestID {
				t.Errorf("steps query = %s", request.Body)
			}
		}
		if request.Root == "SysSchema" && (request.Filters["Name"] != "Sequence" || !strings.Contains(request.Body, `"rowCount":2`)) {
			t.Errorf("presence query = %s", request.Body)
		}
	}
}

func TestGetSequenceContextStopsWhenPresenceCannotBeRead(t *testing.T) {
	client, requests := groupIServer(t, func(request groupIRequest) (int, string) {
		return 200, `{"success":false}`
	})
	result, err := client.GetSequenceContext(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Success || result.Availability != "unknown" || len(result.Sections.keys) != 1 || len(*requests) != 1 {
		t.Fatalf("result = %#v", result)
	}
	if _, err := client.GetSequenceContext(context.Background(), stringPointer(emptyGUID)); err != ErrSequenceIDEmpty {
		t.Fatalf("empty id error = %v", err)
	}
}
