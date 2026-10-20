package creatio

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestESQPrepareRefusesAsClio142(t *testing.T) {
	name := "env"
	cases := []struct {
		label     string
		query     string
		extension map[string]json.RawMessage
		prefix    string
		suggested string
	}{
		{"write SQL", `"delete from Contact"`, nil, "execute-esq is read-only: it runs a SELECT as a SelectQuery JSON object", ""},
		{"SQL translated", `"SELECT TOP 7 [Id], Name FROM dbo.Contact WHERE Name = 'O''Neil'"`, nil,
			"query holds SQL text, but execute-esq runs a SelectQuery JSON object, never SQL; nothing was executed. The equivalent query is returned in 'suggested-query'",
			`{"rootSchemaName":"Contact","operationType":0,"allColumns":false,"isDistinct":false,"ignoreDisplayValues":false,"rowCount":7,"rowsOffset":-1,"isPageable":false,` +
				`"columns":{"items":{"Id":{"expression":{"expressionType":0,"columnPath":"Id"},"orderDirection":0,"orderPosition":-1,"isVisible":true},` +
				`"Name":{"expression":{"expressionType":0,"columnPath":"Name"},"orderDirection":0,"orderPosition":-1,"isVisible":true}}},` +
				`"filters":{"filterType":6,"isEnabled":true,"trimDateTimeParameterToDate":false,"logicalOperation":0,"items":{"filter0":{"filterType":1,"comparisonType":3,` +
				`"isEnabled":true,"trimDateTimeParameterToDate":false,"leftExpression":{"expressionType":0,"columnPath":"Name"},"rightExpression":{"expressionType":2,` +
				`"parameter":{"value":"O'Neil","dataValueType":1}}}}}}`},
		{"SQL with a number filter", `"select Id from Contact where Age = 5"`, nil, "query holds SQL text, but execute-esq runs a SelectQuery JSON object, never SQL; nothing was executed. The filter on 'Age' compares with a number or a date", ""},
		{"SELECT *", `"select * from Contact"`, nil, "query holds SQL text, but execute-esq runs a SelectQuery JSON object, never SQL; nothing was executed. 'SELECT *' has no safe SelectQuery equivalent", ""},
		{"join", `"select Id from Contact join Account"`, nil, "query holds SQL text, but execute-esq runs a SelectQuery JSON object, never SQL; nothing was executed. This statement is not a single-table SELECT", ""},
		{"flat call", ``, map[string]json.RawMessage{"schema-name": json.RawMessage(`"Contact"`), "columns": json.RawMessage(`["Id"]`)},
			"query is missing: the schema, columns and filters belong inside the 'query' SelectQuery object", "rowCount"},
		{"columns array", `{"rootSchemaName":"Contact","columns":["Id"]}`, nil,
			`query is not a valid SelectQuery: 'columns' must be an object {"items":{"<alias>":{"expression":{"expressionType":0,"columnPath":"<column>"}}}}, not an array.`, "rowCount"},
		{"where", `{"rootSchemaName":"Contact","where":{}}`, nil, "query is not a valid SelectQuery: 'where' is not a SelectQuery member.", ""},
		{"rowCount string", `{"rootSchemaName":"Contact","rowCount":"5","isDistinct":true}`, nil,
			"query is not a valid SelectQuery: 'rowCount' must be a whole number, not a string. Creatio would ignore those parts and run the rest, so nothing was executed. It also carries parts a translation would drop or misread", ""},
		{"top-level part", `{"rootSchemaName":"Contact"}`, map[string]json.RawMessage{"filters": json.RawMessage(`{}`)}, "query parts were also sent at the top level ('filters')", ""},
		{"no root", `{"columns":{"items":{}}}`, nil, "query must be a SelectQuery object with a non-empty 'rootSchemaName'.", ""},
		{"number", `5`, nil, "query must be a JSON SelectQuery object.", ""},
		{"string not object", `"[1]"`, nil, "query string must contain a JSON SelectQuery object.", ""},
	}
	for _, c := range cases {
		_, failure := ESQPrepare(json.RawMessage(c.query), c.extension, &name)
		if failure == nil || failure.Success || !strings.HasPrefix(*failure.Error, c.prefix) || failure.Hint == nil {
			t.Fatalf("%s: %+v", c.label, failure)
		}
		if !strings.HasSuffix(*failure.Error, `"columns":{"items":{"Id":{"expression":{"expressionType":0,"columnPath":"Id"}}}}}}}`) {
			t.Fatalf("%s: the shape example is missing: %s", c.label, *failure.Error)
		}
		if c.suggested != "" && !strings.Contains(string(failure.SuggestedQuery), c.suggested) && !esqSameJSON(failure.SuggestedQuery, c.suggested) {
			t.Fatalf("%s: suggested = %s", c.label, failure.SuggestedQuery)
		}
		if c.suggested == "" && failure.SuggestedQuery != nil {
			t.Fatalf("%s: unexpected suggestion %s", c.label, failure.SuggestedQuery)
		}
	}
	blank := " "
	if _, failure := ESQPrepare(json.RawMessage(`{"rootSchemaName":"Contact"}`), nil, &blank); failure == nil || *failure.Error != "environment-name is required." {
		t.Fatalf("blank environment = %+v", failure)
	}
	prepared, failure := ESQPrepare(json.RawMessage(`{"rootSchemaName":"Contact","newMember":1}`), nil, &name)
	if failure != nil || len(prepared.warnings) != 1 || !strings.HasPrefix(prepared.warnings[0], "query carries 'newMember', which clio does not know") {
		t.Fatalf("warning = %+v %+v", prepared, failure)
	}
}

func esqSameJSON(left json.RawMessage, right string) bool {
	var a, b any
	return json.Unmarshal(left, &a) == nil && json.Unmarshal([]byte(right), &b) == nil && reflect.DeepEqual(a, b)
}

func TestESQExecuteReadsTheResponseAsClio142(t *testing.T) {
	body := `{"success":true,"rows":[]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/AuthService.svc/Login") {
			http.SetCookie(w, &http.Cookie{Name: "BPMCSRF", Value: "y"})
			_, _ = w.Write([]byte(`{"Code":0}`))
			return
		}
		w.WriteHeader(500)
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()
	client, _ := NewClient(Config{BaseURL: server.URL, Login: "example-user", Password: "replace-me"})
	name := "env"
	run := func(responseBody, query string, limit int, twin bool) ExecuteEsqResponse {
		body = responseBody
		prepared, failure := ESQPrepare(json.RawMessage(query), nil, &name)
		if failure != nil {
			t.Fatalf("prepare: %+v", failure)
		}
		return client.ESQExecute(context.Background(), prepared, nil, limit, twin)
	}
	const query = `{"rootSchemaName":"Contact","columns":{"items":{"Id":{},"Name":{}}}}`
	if got := run(`{"rows": [ {"Id":"1","Name":"A"} ],"success":true}`, query, ESQMaxResponseBytes, true); !got.Success || *got.Count != 1 || string(got.Rows) != `[ {"Id":"1","Name":"A"} ]` {
		t.Fatalf("rows = %+v", got)
	}
	if got := run(`{"rows":[{"Id":"1"}],"success":true}`, query, ESQMaxResponseBytes, true); got.Success ||
		*got.Error != "unknown column 'Name' on schema 'Contact': the requested column was not returned by the SelectQuery, which means its columnPath did not resolve. Verify the column path against the schema (get-entity-schema-properties / find-entity-schema) and read the 'esq' guidance for the columns shape." {
		t.Fatalf("unknown column = %+v", got)
	}
	if got := run(` {"success":true} `, query, ESQMaxResponseBytes, true); !got.Success || got.Count != nil || string(got.Rows) != `{"success":true}` {
		t.Fatalf("no rows array = %+v", got)
	}
	if got := run(`{"responseStatus":{"ErrorCode":"E","Message":"m"},"success":false}`, query, ESQMaxResponseBytes, true); *got.Error != "E: m" || got.Hint == nil {
		t.Fatalf("responseStatus = %+v", got)
	}
	if got := run(`[1]`, query, ESQMaxResponseBytes, true); !strings.HasPrefix(*got.Error,
		"Failed to parse SelectQuery response: The requested operation requires an element of type 'Object', but the target element has type 'Array'. | Response: [1]") {
		t.Fatalf("array = %+v", got)
	}
	big := `{"success":true,"rows":[{"Id":"` + strings.Repeat("x", 300) + `"}]}`
	if got := run(big, query, 100, true); *got.ErrorClass != "result-too-large" || !strings.HasSuffix(*got.Error,
		" When every row is needed, run the same query with execute-esq-to-file, which writes the rows to a local file.") {
		t.Fatalf("too large inline = %+v", got)
	}
	if got := run(big, query, 100, false); strings.Contains(*got.Error, "execute-esq-to-file") ||
		*got.Error != "SelectQuery result exceeds the 100 UTF-8 byte limit. Select explicit columns instead of allColumns, lower rowCount, or page with rowsOffset, then retry." {
		t.Fatalf("too large twin = %+v", got)
	}
}
