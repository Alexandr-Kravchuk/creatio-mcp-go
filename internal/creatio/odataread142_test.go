package creatio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestODataReadRequestPathMatchesClio(t *testing.T) {
	top, skip := 2, 0
	args := ODataReadArgs{Entity: " Contact ", OrderBy: " Name desc, CreatedOn ", Top: &top, Skip: &skip, Count: true,
		Filters: &ODataFilters{
			All: []*ODataFilterCondition{
				{Field: "Account/Id", Value: json.RawMessage(`"a0000000-0000-0000-0000-000000000001"`)},
				{Field: "Name", Op: "contains", Value: json.RawMessage(`"O'Neil & co"`)},
			},
			Any: []*ODataFilterCondition{
				{Field: "Id", In: json.RawMessage(`["c0000000-0000-0000-0000-000000000001","c0000000-0000-0000-0000-000000000002"]`)},
				{Field: "Email", Op: "ne", Value: json.RawMessage(`null`)},
			},
		}}
	// The decoded query string clio 8.1.0.142 sent for these arguments (t21b fake-stand log).
	want := "odata/Contact?$filter=(Account/Id eq a0000000-0000-0000-0000-000000000001 and contains(Name,'O''Neil & co')) and " +
		"((Id eq c0000000-0000-0000-0000-000000000001 or Id eq c0000000-0000-0000-0000-000000000002) or Email ne null)" +
		"&$select=Id,Name&$expand=Account&$orderby=Name desc, CreatedOn&$skip=0&$count=true&$top=2"
	path := odataReadRequestPath(args, []string{"Id", "Name"}, []string{"Account"})
	if !strings.Contains(path, "%20") || strings.Contains(path, " ") {
		t.Fatalf("path is not escaped as Uri.EscapeDataString escapes: %s", path)
	}
	if decoded := odataUnescape(path); decoded != want {
		t.Fatalf("path = %s\nwant   %s", decoded, want)
	}
	if got := odataEscapeDataString("a b/'~é"); got != "a%20b%2F%27~%C3%A9" {
		t.Fatalf("escape = %s", got)
	}
	if got := odataReadRequestPath(ODataReadArgs{Entity: "Contact"}, nil, nil); got != "odata/Contact?$top=25" {
		t.Fatalf("default = %s", got)
	}
}

func odataUnescape(text string) string {
	var builder strings.Builder
	for index := 0; index < len(text); index++ {
		if text[index] == '%' && index+2 < len(text) {
			var b byte
			for _, c := range text[index+1 : index+3] {
				b <<= 4
				switch {
				case c >= '0' && c <= '9':
					b |= byte(c - '0')
				case c >= 'A' && c <= 'F':
					b |= byte(c - 'A' + 10)
				}
			}
			builder.WriteByte(b)
			index += 2
			continue
		}
		builder.WriteByte(text[index])
	}
	return builder.String()
}

func TestODataReadValidationTexts(t *testing.T) {
	intPtr := func(value int) *int { return &value }
	cases := []struct {
		args ODataReadArgs
		want string
	}{
		{ODataReadArgs{Entity: "Contact", Extension: map[string]json.RawMessage{"filter": json.RawMessage(`"x"`)}},
			"Argument 'filter' is unsupported because raw filter strings are not accepted."},
		{ODataReadArgs{Entity: "Contact", Extension: map[string]json.RawMessage{"orderBy": json.RawMessage(`"x"`)}}, "Rename: 'orderBy' -> 'order-by'."},
		{ODataReadArgs{Entity: "Contact", Skip: intPtr(-1)}, "skip must be zero or greater (got -1)."},
		{ODataReadArgs{Entity: "Contact", Select: json.RawMessage(`["Id",5]`)}, `select must be an array of column names or a comma-separated string, for example ["Id","Name"] or "Id,Name".`},
		{ODataReadArgs{Entity: "Contact", Select: json.RawMessage(`"Id,Na me"`)}, "select[1] must be an OData member path"},
		{ODataReadArgs{Entity: "Contact", OrderBy: "Name up"}, "order-by must be a comma-separated list"},
		{ODataReadArgs{Entity: "Contact", FiltersProvided: true}, "filters must be a structured object containing at least one condition in all or any; null is not supported."},
		{ODataReadArgs{Entity: "Contact", Filters: &ODataFilters{}, FiltersProvided: true}, "filters must contain at least one condition in all or any."},
		{ODataReadArgs{Entity: "Contact", Filters: &ODataFilters{All: []*ODataFilterCondition{nil}}}, "filters.all[0] must be a filter condition object; null is not supported."},
		{ODataReadArgs{Entity: "Contact", Filters: &ODataFilters{Any: []*ODataFilterCondition{{Field: "Name", Value: json.RawMessage(`1`), In: json.RawMessage(`[1]`)}}}},
			"filters.any[0] must provide exactly one of value or in."},
		{ODataReadArgs{Entity: "Contact", Filters: &ODataFilters{All: []*ODataFilterCondition{{Field: "Name", Op: "like", Value: json.RawMessage(`1`)}}}},
			"filters.all[0].op must be one of: eq, ne, gt, ge, lt, le, contains, startswith, endswith (got like)."},
		{ODataReadArgs{Entity: "Con tact"}, "entity must be a valid OData entity set name (letters, digits, underscore)."},
		{ODataReadArgs{Entity: "Contact", Top: intPtr(0)}, "top must be between 1 and 100 (got 0). Omit top to use the default of 25."},
	}
	for _, c := range cases {
		failure, _, _ := ODataReadPrepare(c.args, false)
		if failure == nil || !strings.HasPrefix(*failure.Error, c.want) || *failure.ErrorCode != "argument" {
			t.Fatalf("%+v: %+v", c.args, failure)
		}
	}
	failure, _, _ := ODataReadPrepare(ODataReadArgs{Entity: "Contact", Top: intPtr(101)}, true)
	if failure.Entity == nil || *failure.Entity != "Contact" {
		t.Fatalf("a top refusal names the accepted entity: %+v", failure)
	}
}

// A canned OData endpoint for the read paths: the classification and the file twin.
func odataTestClient(t *testing.T, bodies map[string]string) *Client {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/AuthService.svc/Login") {
			http.SetCookie(w, &http.Cookie{Name: ".ASPXAUTH", Value: "x"})
			http.SetCookie(w, &http.Cookie{Name: "BPMCSRF", Value: "y"})
			_, _ = w.Write([]byte(`{"Code":0}`))
			return
		}
		for prefix, body := range bodies {
			if strings.Contains(r.URL.Path+"?"+r.URL.RawQuery, prefix) {
				_, _ = w.Write([]byte(body))
				return
			}
		}
		w.WriteHeader(404)
	}))
	t.Cleanup(server.Close)
	client, err := NewClient(Config{BaseURL: server.URL, Login: "example-user", Password: "replace-me"})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestODataReadClassifiesAsClio(t *testing.T) {
	client := odataTestClient(t, map[string]string{
		"/odata/Contact":   `{"@odata.context":"http://h/odata/$metadata#Contact","@odata.count":3,"value":[{"Id":"1","Name":"A"}],"@odata.nextLink":"n"}`,
		"/odata/Single":    `{"@odata.context":"http://h/odata/$metadata#single/$entity","Id":"1"}`,
		"/odata/Wrong":     `{"@odata.context":"http://h/odata/$metadata#Account","value":[]}`,
		"/odata/Html":      `<!DOCTYPE html><html><title>404 - File or directory not found.</title></html>`,
		"/odata/Routing":   `{"Message":"No HTTP resource was found that matches the request URI 'x'."}`,
		"/odata/Invalid":   `{"error":{"code":"","message":"The query specified in the URI is not valid. Could not find a property named 'Nmae' on type 'Terrasoft.Configuration.OData.Invalid'."}}`,
		"/odata/$metadata": `<edmx:Edmx xmlns:edmx="x"><Schema><EntityType Name="Invalid"><Property Name="Name"/><Property Name="Id"/></EntityType></Schema></edmx:Edmx>`,
	})
	read := func(entity string, count bool, selectColumns []string) ODataReadResponse {
		args := ODataReadArgs{Entity: entity, Count: count}
		if selectColumns != nil {
			args.Select, _ = json.Marshal(selectColumns)
		}
		return client.ODataRead142(context.Background(), args, selectColumns, nil)
	}
	if got := read("Contact", true, nil); !got.Success || *got.Count != 1 || *got.TotalCount != 3 || *got.NextLink != "n" ||
		string(got.Value) != `[{"Id":"1","Name":"A"}]` {
		t.Fatalf("collection = %+v", got)
	}
	if got := read("Single", false, nil); !got.Success || *got.Count != 1 || !strings.HasSuffix(string(got.Value), `"Id":"1"}`) {
		t.Fatalf("single = %+v", got)
	}
	if got := read("Wrong", false, nil); got.Success || *got.ErrorCode != "non-json-response" {
		t.Fatalf("wrong set = %+v", got)
	}
	if got := read("Html", false, nil); *got.ErrorCode != "entity-not-found" || *got.StatusCode != 404 ||
		!strings.HasPrefix(*got.Error, "Entity 'Html' is not exposed over OData on this environment (HTTP 404).") {
		t.Fatalf("html = %+v", got)
	}
	if got := read("Routing", false, nil); *got.ErrorCode != "entity-not-found" || !strings.Contains(*got.Error, "asynchronous OData rebuild") {
		t.Fatalf("routing = %+v", got)
	}
	got := read("Invalid", false, []string{"Nmae", "Id"})
	if *got.ErrorCode != "invalid-query" || len(got.UnknownMembers) != 1 || got.UnknownMembers[0].Name != "Nmae" ||
		got.UnknownMembers[0].Suggestions[0] != "Name" || *got.ValidColumnCount != 2 ||
		!strings.Contains(*got.Error, "From the error payload (validated identifiers only): unknown property 'Nmae' on 'Invalid'.") ||
		!strings.Contains(*got.Error, "Names this request sent - select: Nmae, Id.") ||
		!strings.Contains(*got.Error, "Checked against the OData metadata of Invalid: 1 name(s) cannot be used as sent: select 'Nmae' (not on the type; closest: Name). Invalid has 2 columns.") {
		t.Fatalf("invalid = %s", *got.Error)
	}
	if got := read("Contact", false, nil); got.Success != true {
		t.Fatalf("contact = %+v", got)
	}
}

func TestODataReadToFileWritesTheResponseBytes(t *testing.T) {
	t.Chdir(t.TempDir())
	body := `{"@odata.context":"http://h/odata/$metadata#Contact","value":[{"@odata.etag":"e","Id":"1","Name":"Ä"},{"Id":"2","Name" : null},5]}`
	client := odataTestClient(t, map[string]string{"/odata/Contact": body})
	path, _ := ToFileResolve("out/contacts.json")
	got := client.ODataReadToFile(context.Background(), ODataReadArgs{Entity: "Contact"}, nil, nil, path)
	if !got.Success || *got.RowCount != 2 || *got.Count != 3 || *got.OutputFile != path {
		t.Fatalf("summary = %+v", got)
	}
	sizes, _ := json.Marshal(got.ColumnSizes)
	if string(sizes) != `{"Id":6,"Name":8}` {
		t.Fatalf("column sizes = %s", sizes)
	}
	if content, _ := os.ReadFile(path); string(content) != body {
		t.Fatalf("file = %s", content)
	}
	again := client.ODataReadToFile(context.Background(), ODataReadArgs{Entity: "Contact"}, nil, nil, path)
	if again.Success || *again.ErrorCode != "argument" || !strings.Contains(*again.Error, "already exists; refusing to overwrite it") {
		t.Fatalf("second write = %+v", again)
	}
	// A rejected body writes nothing.
	other, _ := ToFileResolve("proxy.json")
	client = odataTestClient(t, map[string]string{"/odata/Contact": `{"value":[{"detail":"marker"}]}`})
	if got := client.ODataReadToFile(context.Background(), ODataReadArgs{Entity: "Contact"}, nil, nil, other); got.Success || *got.ErrorCode != "non-json-response" {
		t.Fatalf("proxy = %+v", got)
	}
	if _, err := os.Stat(other); !os.IsNotExist(err) {
		t.Fatal("a rejected body was written")
	}
}

// chunkedReader hands out at most size bytes per Read, as a transport splits a body.
type chunkedReader struct {
	data []byte
	size int
}

func (r *chunkedReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	n := copy(p[:min(len(p), r.size)], r.data)
	r.data = r.data[n:]
	return n, nil
}

// Creatio.Client checks the ceiling once per read of its 81920-byte buffer, so the size clio reports is the
// bytes read up to and including the read that crossed it.
func TestODataReadBoundedReportsTheSizeClioReports(t *testing.T) {
	body, observed, err := odataReadBounded(&chunkedReader{data: make([]byte, 200), size: 30}, 100)
	if !errors.Is(err, errResponseTooLarge) || observed != 120 || len(body) != 90 {
		t.Fatalf("chunks of 30: observed = %d, body = %d, err = %v", observed, len(body), err)
	}
	_, observed, err = odataReadBounded(bytes.NewReader(make([]byte, 300000)), 200000)
	if !errors.Is(err, errResponseTooLarge) || observed != 3*odataReadCopyBufferBytes {
		t.Fatalf("full buffers: observed = %d, err = %v", observed, err)
	}
	body, _, err = odataReadBounded(&chunkedReader{data: make([]byte, 100), size: 7}, 100)
	if err != nil || len(body) != 100 {
		t.Fatalf("a body of exactly the ceiling: body = %d, err = %v", len(body), err)
	}
}

func TestODataReadInlineIsNotCappedAtFourMiB(t *testing.T) {
	name := strings.Repeat("x", 5<<20)
	client := odataTestClient(t, map[string]string{
		"/odata/Contact": `{"@odata.context":"http://h/odata/$metadata#Contact","value":[{"Id":"1","Name":"` + name + `"}]}`,
	})
	got := client.ODataRead142(context.Background(), ODataReadArgs{Entity: "Contact"}, nil, nil)
	if !got.Success || *got.Count != 1 || len(got.Value) < 5<<20 {
		t.Fatalf("5 MiB read: success = %v, error = %v", got.Success, got.Error)
	}
}

func TestODataReadToFileRefusesPastSixtyFourMiBWithTheObservedSize(t *testing.T) {
	t.Chdir(t.TempDir())
	client := odataTestClient(t, map[string]string{"/odata/Contact": strings.Repeat(" ", ODataReadMaxFileBytes+1)})
	path, _ := ToFileResolve("big.json")
	got := client.ODataReadToFile(context.Background(), ODataReadArgs{Entity: "Contact"}, nil, nil, path)
	if got.Success || *got.ErrorCode != "transport" {
		t.Fatalf("refusal = %+v", got)
	}
	var observed int64
	if _, err := fmt.Sscanf(*got.Error, "OData response is at least %d bytes", &observed); err != nil ||
		observed <= ODataReadMaxFileBytes || observed > ODataReadMaxFileBytes+odataReadCopyBufferBytes ||
		!strings.HasSuffix(*got.Error, "exceeds the 67108864-byte limit for one call. Narrow the query with select, or page it with top and skip.") {
		t.Fatalf("error = %s", *got.Error)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("a refused body was written")
	}
}
