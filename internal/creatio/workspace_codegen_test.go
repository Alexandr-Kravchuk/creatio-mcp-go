package creatio

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// codegenStand is a fake Creatio: the login, then route -> handler; it records every other request.
type codegenStand struct {
	mu       sync.Mutex
	requests []string
}

func (s *codegenStand) seen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.requests...)
}

func newCodegenStand(t *testing.T, handle func(w http.ResponseWriter, route, body string)) (*Client, *codegenStand, *httptest.Server) {
	t.Helper()
	stand := &codegenStand{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ServiceModel/AuthService.svc/Login" {
			_, _ = w.Write([]byte(`{"Code":0}`))
			return
		}
		raw, _ := io.ReadAll(r.Body)
		route := strings.TrimPrefix(r.URL.Path, "/0/")
		stand.mu.Lock()
		stand.requests = append(stand.requests, r.Method+" "+route+" "+string(raw))
		stand.mu.Unlock()
		handle(w, route, string(raw))
	}))
	t.Cleanup(server.Close)
	client, _ := NewClient(Config{BaseURL: server.URL, Login: "example-user", Password: "replace-me", IsNetCore: false})
	return client, stand, server
}

const gscWarnings = `{"message-type":"Warning","value":"This command may take a while to complete, please wait..."},` +
	`{"message-type":"Warning","value":"Clio will timeout if the operation takes more than 60 minutes. You can adjust the ` +
	`timeout using the --timeout option (CLI) or the 'timeout' argument (MCP)."}`

func TestGenerateSourceCodeRoutesEachModeAndPostsOnce(t *testing.T) {
	client, stand, _ := newCodegenStand(t, func(w http.ResponseWriter, _, _ string) {
		_, _ = w.Write([]byte(`{"success":true}`))
	})
	cases := []struct {
		request GenerateSourceCodeRequest
		route   string
	}{
		{GenerateSourceCodeRequest{}, "GenerateAllSchemasSources"},
		{GenerateSourceCodeRequest{Modified: true}, "GenerateModifiedSchemasSources"},
		{GenerateSourceCodeRequest{Required: true}, "GenerateRequiredSchemasSources"},
		{GenerateSourceCodeRequest{Background: true}, "GenerateAllSchemasSourcesInBackground"},
		{GenerateSourceCodeRequest{Background: true, Modified: true, Required: true}, "GenerateAllSchemasSourcesInBackground"},
		{GenerateSourceCodeRequest{Modified: true, Required: true}, "GenerateModifiedSchemasSources"},
	}
	for index, test := range cases {
		test.request.TimeoutMilliseconds = GenerateSourceCodeDefaultTimeout
		got := CommandResultText(client.GenerateSourceCode(context.Background(), test.request))
		want := `{"exit-code":0,"execution-log-messages":[` + gscWarnings + `,{"message-type":"Info","value":"Done generate-source-code"}]}`
		if got != want {
			t.Fatalf("%s: %s", test.route, got)
		}
		requests := stand.seen()
		if len(requests) != index+1 || requests[index] != "POST ServiceModel/WorkspaceExplorerService.svc/"+test.route+" " {
			t.Fatalf("%s: requests %q", test.route, requests)
		}
	}
}

func TestGenerateSourceCodeVerdicts(t *testing.T) {
	var body string
	var status int
	client, _, server := newCodegenStand(t, func(w http.ResponseWriter, _, _ string) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})
	endpoint := `{"message-type":"Error","value":"Endpoint: ` + server.URL + `/0/ServiceModel/WorkspaceExplorerService.svc/GenerateAllSchemasSources"}`
	full := func(text string) string {
		return endpoint + `,{"message-type":"Error","value":"Full response:"},{"message-type":"None","value":` + text + `}`
	}
	cases := []struct {
		status        int
		body, message string
		exit          int
	}{
		{500, `{"success":false,"errorInfo":{"errorCode":"X","message":"boom"}}`, `{"message-type":"Error","value":"X: boom"}`, 1},
		{200, `{"success":false}`, `{"message-type":"Error","value":": "}`, 1},
		{200, `{"Success":true}`, `{"message-type":"Error","value":": "}`, 1},
		{200, `{"success":true,"success":false}`, `{"message-type":"Error","value":": "}`, 1},
		{200, `{"success":true,"errorInfo":{"errorCode":"X"}}`, `{"message-type":"Info","value":"Done generate-source-code"}`, 0},
		{200, `<html><body>Error</body></html>`, `{"message-type":"Error","value":"Server returned non-JSON response (looks like HTML)."},` + endpoint, 1},
		{200, " \r\n", `{"message-type":"Error","value":"Empty response received from server."},` + endpoint, 1},
		{502, "Bad gateway", `{"message-type":"Error","value":"'B' is an invalid start of a value. Path: $ | LineNumber: 0 | BytePositionInLine: 0."},` + full(`"Bad gateway"`), 1},
		{200, "[1,2]", `{"message-type":"Error","value":"The JSON value could not be converted to Clio.Command.CreatioResponse. Path: $ | LineNumber: 0 | BytePositionInLine: 1."},` + full(`"[1,2]"`), 1},
		{200, "null", `{"message-type":"Error","value":"Object reference not set to an instance of an object."},` + full(`"null"`), 1},
		{200, `{"success":"yes"}`, `{"message-type":"Error","value":"The JSON value could not be converted to System.Boolean. Path: $.success | LineNumber: 0 | BytePositionInLine: 16."},` + full(`"{\"success\":\"yes\"}"`), 1},
		{200, "\u00a0{}", `{"message-type":"Error","value":"'0xC2' is an invalid start of a value. Path: $ | LineNumber: 0 | BytePositionInLine: 0."},` + full(`"\u00A0{}"`), 1},
	}
	for _, test := range cases {
		status, body = test.status, test.body
		got := CommandResultText(client.GenerateSourceCode(context.Background(), GenerateSourceCodeRequest{TimeoutMilliseconds: GenerateSourceCodeDefaultTimeout}))
		want := `{"exit-code":` + map[int]string{0: "0", 1: "1"}[test.exit] + `,"execution-log-messages":[` + gscWarnings + `,` + test.message + `]}`
		if got != want {
			t.Fatalf("body %q:\n got %s\nwant %s", test.body, got, want)
		}
	}
}

func TestStjCreatioResponseMeasuredTexts(t *testing.T) {
	cases := map[string]string{
		`"abc"`:                              "The JSON value could not be converted to Clio.Command.CreatioResponse. Path: $ | LineNumber: 0 | BytePositionInLine: 5.",
		`123`:                                "The JSON value could not be converted to Clio.Command.CreatioResponse. Path: $ | LineNumber: 0 | BytePositionInLine: 3.",
		`true`:                               "The JSON value could not be converted to Clio.Command.CreatioResponse. Path: $ | LineNumber: 0 | BytePositionInLine: 4.",
		`{"success":1}`:                      "The JSON value could not be converted to System.Boolean. Path: $.success | LineNumber: 0 | BytePositionInLine: 12.",
		"{\n  \"success\": null\n}":          "The JSON value could not be converted to System.Boolean. Path: $.success | LineNumber: 1 | BytePositionInLine: 17.",
		`{"success":true,"buildResult":1.5}`: "The JSON value could not be converted to System.Int32. Path: $.buildResult | LineNumber: 0 | BytePositionInLine: 33.",
		`{"errorInfo":"x","success":true}`:   "The JSON value could not be converted to Clio.Command.ErrorInfo. Path: $.errorInfo | LineNumber: 0 | BytePositionInLine: 16.",
		`{"success":false,"errorInfo":{"errorCode":5}}`:  "The JSON value could not be converted to System.String. Path: $.errorInfo.errorCode | LineNumber: 0 | BytePositionInLine: 43.",
		`{"success":true} x`:                             "'x' is invalid after a single JSON value. Expected end of data. Path: $ | LineNumber: 0 | BytePositionInLine: 17.",
		`{"success":true,}`:                              "The JSON object contains a trailing comma at the end which is not supported in this mode. Change the reader options. Path: $ | LineNumber: 0 | BytePositionInLine: 16.",
		`{"a":[1,],"success":true}`:                      "The JSON array contains a trailing comma at the end which is not supported in this mode. Change the reader options. Path: $.a | LineNumber: 0 | BytePositionInLine: 8.",
		`{"errorInfo":{"message":"m",},"success":false}`: "The JSON object contains a trailing comma at the end which is not supported in this mode. Change the reader options. Path: $.errorInfo | LineNumber: 0 | BytePositionInLine: 28.",
		`{"success":x}`:                                  "'x' is an invalid start of a value. Path: $.success | LineNumber: 0 | BytePositionInLine: 11.",
		`{"success":true`:                                "Expected depth to be zero at the end of the JSON payload. There is an open JSON object or array that should be closed. Path: $ | LineNumber: 0 | BytePositionInLine: 15.",
		`{"success" true}`:                               "'t' is invalid after a property name. Expected a ':'. Path: $ | LineNumber: 0 | BytePositionInLine: 11.",
		`{"a":{"b":tru},"success":true}`:                 `'tru},"success":true}' is an invalid JSON literal. Expected the literal 'true'. Path: $.a | LineNumber: 0 | BytePositionInLine: 13.`,
	}
	for body, want := range cases {
		if _, got := stjCreatioResponse([]byte(body)); got != want {
			t.Errorf("%s:\n got %s\nwant %s", body, got, want)
		}
	}
	verdict, failure := stjCreatioResponse([]byte(`{"success":false,"errors":[1,{"a":2}],"x":{"y":[true,null,-1.5e3]},"errorInfo":{"errorCode":null,"message":"\u0041\n"}}`))
	if failure != "" || verdict.success || verdict.errorCode != "" || verdict.message != "A\n" {
		t.Fatalf("verdict = %#v, failure = %q", verdict, failure)
	}
}

func TestGenerateSourceCodeMinutes(t *testing.T) {
	for milliseconds, want := range map[int]string{3_600_000: "60", 10_800_000: "180", 90_000: "1.5", 1_000: "0.02", 100: "0",
		7_500: "0.13", 60_000: "1", 135_000: "2.25", 1: "0"} {
		if got := GenerateSourceCodeMinutes(milliseconds); got != want {
			t.Errorf("%d ms: %q, want %q", milliseconds, got, want)
		}
	}
}

func TestGenerateSourceCodeTimeoutAndUnreachableSite(t *testing.T) {
	release := make(chan struct{})
	client, _, _ := newCodegenStand(t, func(w http.ResponseWriter, _, _ string) {
		<-release
		_, _ = w.Write([]byte(`{"success":true}`))
	})
	defer close(release)
	result := client.GenerateSourceCode(context.Background(), GenerateSourceCodeRequest{TimeoutMilliseconds: 50})
	if result.ExitCode != 1 || result.Messages[len(result.Messages)-1].Value != "A task was canceled." ||
		!strings.Contains(result.Messages[1].Value, "more than 0 minutes") {
		t.Fatalf("timeout result = %#v", result)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	dead, _ := NewClient(Config{BaseURL: "http://" + address, Login: "example-user", Password: "replace-me", IsNetCore: true})
	result = dead.GenerateSourceCode(context.Background(), GenerateSourceCodeRequest{TimeoutMilliseconds: GenerateSourceCodeDefaultTimeout})
	want := "Cannot connect to the application: Connection refused (" + address + ") (WebException: ConnectFailure). Make sure the site is running and accessible."
	if result.ExitCode != 1 || len(result.Messages) != 3 || result.Messages[2].Value != want {
		t.Fatalf("unreachable result = %#v", result)
	}
}

func TestEntityModelSchemasReadsListAndRuntimeSchemas(t *testing.T) {
	runtime := map[string]string{
		"A": `{"schema":{"description":{"en-US":"About A","uk-UA":"Про A"},"columns":{"Items":{
			"ae0e45ca-c495-4fe7-a39d-3ab7278e1617":{"name":"Id","dataValueType":0,"isRequired":true},
			"be0e45ca-c495-4fe7-a39d-3ab7278e1617":{"name":"B","dataValueType":10,"isRequired":"true","referenceSchemaName":"B","description":{"en-US":"Link"}},
			"ce0e45ca-c495-4fe7-a39d-3ab7278e1617":{"name":"N","dataValueType":"28","isRequired":0,"referenceSchemaName":null,"description":{"en-US":null}}}}}}`,
		"B": `{"schema":{"name":"B"}}`,
	}
	client, stand, _ := newCodegenStand(t, func(w http.ResponseWriter, route, body string) {
		if strings.HasSuffix(route, "EntitySchemaManagerRequest") {
			_, _ = w.Write([]byte(`{"collection":[{"name":"A"},{"name":"B"},{"name":"A","isVirtual":true}],"success":true}`))
			return
		}
		name := strings.TrimSuffix(strings.TrimPrefix(body, `{"Name" : "`), `"}`)
		_, _ = w.Write([]byte(runtime[name]))
	})
	schemas, lines, err := client.EntityModelSchemas(context.Background(), "en-US")
	if err != nil || len(schemas) != 2 || schemas[0].Name != "A" || schemas[1].Name != "B" {
		t.Fatalf("schemas = %#v, err = %v", schemas, err)
	}
	if len(lines) != 1 || lines[0] != (LogMessage{MessageType: "None", Value: "Schema B not found"}) {
		t.Fatalf("lines = %#v", lines)
	}
	a := schemas[0]
	if a.Description == nil || *a.Description != "About A" || len(a.Columns) != 3 {
		t.Fatalf("A = %#v", a)
	}
	link, text := a.Columns[1], a.Columns[2]
	if link.Name != "B" || link.DataValueType != 10 || !link.IsRequired || *link.ReferenceSchemaName != "B" || *link.Description != "Link" {
		t.Fatalf("link column = %#v", link)
	}
	if text.DataValueType != 28 || text.IsRequired || text.ReferenceSchemaName != nil || text.Description == nil || *text.Description != "" {
		t.Fatalf("text column = %#v", text)
	}
	bodies := map[string]bool{}
	for _, request := range stand.seen() {
		bodies[request] = true
	}
	for _, want := range []string{"POST DataService/json/SyncReply/EntitySchemaManagerRequest ",
		`POST DataService/json/SyncReply/RuntimeEntitySchemaRequest {"Name" : "A"}`,
		`POST DataService/json/SyncReply/RuntimeEntitySchemaRequest {"Name" : "B"}`} {
		if !bodies[want] {
			t.Fatalf("missing request %q in %q", want, stand.seen())
		}
	}
}

func TestEntityModelSchemasFailures(t *testing.T) {
	var list, runtime string
	client, _, _ := newCodegenStand(t, func(w http.ResponseWriter, route, _ string) {
		if strings.HasSuffix(route, "EntitySchemaManagerRequest") {
			_, _ = w.Write([]byte(list))
			return
		}
		_, _ = w.Write([]byte(runtime))
	})
	cases := []struct{ list, runtime, want string }{
		{`{"success":false}`, "", "Value cannot be null. (Parameter 'source')"},
		{"<html>Error</html>", "", "Unexpected character encountered while parsing value: <. Path '', line 0, position 0."},
		{"", "", "Object reference not set to an instance of an object."},
		{`{"collection":[{"name":null}]}`, "", "Value cannot be null. (Parameter 'key')"},
		{`{"collection":[{"name":"A"}]}`, "Service unavailable", "One or more errors occurred. (Failed to parse runtime schema 'A'. Response starts with: Service unavailable)"},
		{`{"collection":[{"name":"A"}]}`, `{"schema":{"columns":{"Items":null}}}`, "One or more errors occurred. (Object reference not set to an instance of an object.)"},
		{`{"collection":[{"name":"A"}]}`, `{"schema":{"columns":{"Items":{"ae0e45ca-c495-4fe7-a39d-3ab7278e1617":{"name":"X","dataValueType":1}}}}}`,
			"One or more errors occurred. (Value cannot be null. (Parameter 'value'))"},
		{`{"collection":[{"name":"A"}]}`, `{"schema":{"columns":{"Items":{"ae0e45ca-c495-4fe7-a39d-3ab7278e1617":{"name":"X","dataValueType":1,"isRequired":false},` +
			`"be0e45ca-c495-4fe7-a39d-3ab7278e1617":{"name":"X","dataValueType":1,"isRequired":false}}}}}`,
			"One or more errors occurred. (An item with the same key has already been added. Key: X)"},
		{`{"collection":[{"name":"A"}]}`, strings.Repeat("E", 250), "One or more errors occurred. (Failed to parse runtime schema 'A'. Response starts with: " + strings.Repeat("E", 200) + ")"},
	}
	for _, test := range cases {
		list, runtime = test.list, test.runtime
		if _, _, err := client.EntityModelSchemas(context.Background(), "en-US"); err == nil || err.Error() != test.want {
			t.Errorf("list %q runtime %q: %v", test.list, test.runtime, err)
		}
	}
}

func TestEntityModelSchemasRetriesARequestThatGotNoAnswer(t *testing.T) {
	previous := modelRequestDelay
	modelRequestDelay = time.Millisecond
	t.Cleanup(func() { modelRequestDelay = previous })
	var mu sync.Mutex
	attempts := 0
	client, _, _ := newCodegenStand(t, func(w http.ResponseWriter, route, _ string) {
		mu.Lock()
		attempts++
		current := attempts
		mu.Unlock()
		if current < 3 {
			connection, _, _ := w.(http.Hijacker).Hijack()
			_ = connection.Close()
			return
		}
		_, _ = w.Write([]byte(`{"collection":[]}`))
	})
	schemas, _, err := client.EntityModelSchemas(context.Background(), "en-US")
	if err != nil || len(schemas) != 0 || attempts != 3 {
		t.Fatalf("schemas = %#v, err = %v, attempts = %d", schemas, err, attempts)
	}
}

func TestCommandResultTextEscapesAsClio(t *testing.T) {
	got := CommandResultText(CommandResult{ExitCode: -1, Messages: []LogMessage{{MessageType: "None",
		Value: "<url> 'x' & \"q\" \\ \n\t\u00a0\u2028\u200b\U0001F600é"}}})
	want := `{"exit-code":-1,"execution-log-messages":[{"message-type":"None","value":"<url> 'x' & \"q\" \\ \n\t\u00A0\u2028\u200B\uD83D\uDE00é"}]}`
	if got != want {
		t.Fatalf("\n got %s\nwant %s", got, want)
	}
}
