package creatio

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// dataWriteSQLRows is the answer clio 8.1.0.134 rendered into testdata/sql-console-table.txt (captured
// against a fake Creatio answering ExecuteSqlScript with these rows).
const dataWriteSQLRows = `[{"A":1,"LongText":"abcdefghij klmnopqrstuvwxyzabcdefghij klmnopqrstuvwxyz0123456789 and more words here to wrap again past forty","N":null,"B":true,"E":1e+20,"W":"日本語テキスト","S":"  padded  "},` +
	`{"A":22,"LongText":"x","N":"y","B":false,"E":1e-05,"W":"","S":"noSpacesAtAllInThisVeryLongTokenThatKeepsGoingAndGoing"}]`

func dataWriteSQLServer(t *testing.T, answer string, requests *int) *Client {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ServiceModel/AuthService.svc/Login" {
			io.WriteString(w, `{"Code":0}`)
			return
		}
		*requests++
		if r.Method != "POST" || r.URL.Path != "/0/rest/CreatioApiGateway/ExecuteSqlScript" {
			t.Errorf("request %s %s", r.Method, r.URL.Path)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["script"] == "" {
			t.Errorf("body %#v %v", body, err)
		}
		quoted, _ := json.Marshal(answer)
		w.Write(quoted)
	}))
	t.Cleanup(server.Close)
	client, _ := NewClient(Config{BaseURL: server.URL, Login: "example-user", Password: "replace-me"})
	return client
}

func TestDataWriteSQLTableMatchesClioConsoleTable(t *testing.T) {
	requests := 0
	client := dataWriteSQLServer(t, dataWriteSQLRows, &requests)
	want, err := os.ReadFile("testdata/sql-console-table.txt")
	if err != nil {
		t.Fatal(err)
	}
	got := client.ExecuteSQLScript(context.Background(), DataWriteSQLRequest{Script: "SELECT 1", View: "table"})
	if got.ExitCode != 0 || len(got.Messages) != 2 || got.Messages[0].MessageType != "None" || got.Messages[1].Value != "Done" {
		t.Fatalf("got=%#v", got)
	}
	if got.Messages[0].Value != strings.ReplaceAll(string(want), "\n", dataWriteNewline()) {
		t.Fatalf("table differs:\n%s\nwant:\n%s", got.Messages[0].Value, want)
	}
}

func TestDataWriteSQLFilesAndCounts(t *testing.T) {
	requests := 0
	client := dataWriteSQLServer(t, dataWriteSQLRows, &requests)
	dir := t.TempDir()
	csv := filepath.Join(dir, "rows.csv")
	got := client.ExecuteSQLScript(context.Background(), DataWriteSQLRequest{Script: "SELECT 1", View: "csv", Destination: &csv, Silent: true})
	if got.ExitCode != 0 || len(got.Messages) != 2 || got.Messages[0].Value != "Results saved to: "+csv {
		t.Fatalf("csv got=%#v", got)
	}
	content, _ := os.ReadFile(csv)
	if lines := strings.Split(string(content), dataWriteNewline()); lines[0] != "A;LongText;N;B;E;W;S" ||
		lines[2] != "22;x;y;False;1E-05;;noSpacesAtAllInThisVeryLongTokenThatKeepsGoingAndGoing" {
		t.Fatalf("csv content %q", content)
	}
	xlsx := filepath.Join(dir, "rows.xlsx")
	if got := client.ExecuteSQLScript(context.Background(), DataWriteSQLRequest{Script: "SELECT 1", View: "xlsx", Destination: &xlsx, Silent: true}); got.ExitCode != 0 {
		t.Fatalf("xlsx got=%#v", got)
	}

	count := dataWriteSQLServer(t, "3", &requests)
	if got := count.ExecuteSQLScript(context.Background(), DataWriteSQLRequest{Script: "UPDATE", View: "table"}); got.Messages[0].Value != "(3 rows affected)" {
		t.Fatalf("count got=%#v", got)
	}
	if got := count.ExecuteSQLScript(context.Background(), DataWriteSQLRequest{Script: "UPDATE", View: "table", Destination: &csv}); got.ExitCode != 1 ||
		got.Messages[0].Value != "Use -v json to export an affected-row count." {
		t.Fatalf("count export got=%#v", got)
	}
}

func TestDataWriteSQLGatewayErrorAndFileInput(t *testing.T) {
	requests := 0
	client := dataWriteSQLServer(t, "ExecuteSQL ERROR: denied", &requests)
	if got := client.ExecuteSQLScript(context.Background(), DataWriteSQLRequest{Script: "SELECT 1", View: "json"}); got.ExitCode != 1 || got.Messages[0].Value != "denied" {
		t.Fatalf("got=%#v", got)
	}
	input := filepath.Join(t.TempDir(), "in.sql")
	os.WriteFile(input, []byte("SELECT 1\nFROM X\n"), 0o600)
	got := client.ExecuteSQLScript(context.Background(), DataWriteSQLRequest{File: input, View: "json"})
	if got.ExitCode != 1 || got.Messages[0].MessageType != "None" || got.Messages[0].Value != "SELECT 1\nFROM X\n" {
		t.Fatalf("file got=%#v", got)
	}
	missing := filepath.Join(filepath.Dir(input), "nope.sql")
	if got := client.ExecuteSQLScript(context.Background(), DataWriteSQLRequest{File: missing, View: "json"}); got.Messages[0].Value != "Could not find file '"+missing+"'." {
		t.Fatalf("missing got=%#v", got)
	}
	if requests != 2 {
		t.Fatalf("requests=%d", requests)
	}
}
