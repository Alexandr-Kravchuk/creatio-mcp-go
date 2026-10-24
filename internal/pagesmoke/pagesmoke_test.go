package pagesmoke

import (
	"strings"
	"testing"
	"time"
)

func TestNewtonsoftTimestampDropsTrailingZeros(t *testing.T) {
	at := time.Date(2026, 10, 23, 15, 39, 50, 869071000, time.UTC)
	if got := newtonsoftTimestamp(at); got != "2026-10-23T15:39:50.869071+00:00" {
		t.Fatalf("got %s", got)
	}
	if got := newtonsoftTimestamp(at.Truncate(time.Second)); got != "2026-10-23T15:39:50+00:00" {
		t.Fatalf("got %s", got)
	}
}

func TestPipeMessageReaderSplitsAndReplacesOversizedReplies(t *testing.T) {
	big := `{"id":7,"result":{"value":"` + strings.Repeat("x", maxMessageBytes) + `"}}`
	reader := &pipeMessageReader{from: strings.NewReader(`{"id":1}` + "\x00" + big + "\x00" + `{"method":"m"}` + "\x00")}
	for _, want := range []string{`{"id":1}`, `{"id":7,"clio.replyTooLarge":true}`, `{"method":"m"}`} {
		got, err := reader.next()
		if err != nil || got != want {
			t.Fatalf("got %q, %v; want %q", got, err, want)
		}
	}
	if _, err := reader.next(); err == nil {
		t.Fatal("want EOF")
	}
}

func TestFitKeepsTheReplyUnderTheBound(t *testing.T) {
	result := Result{Success: true, Ready: true, ConsoleErrors: []PageConsoleError{}, Warnings: []string{}}
	for index := 0; index < 40; index++ {
		value := strings.Repeat("v", 60)
		name := strings.Repeat("n", 300)
		result.Fields = append(result.Fields, PageField{Name: &name, Value: &value})
		result.Buttons = append(result.Buttons, PageButton{Name: &name})
	}
	fitted := fit(result)
	if size := replySize(fitted); size > maxReplyBytes-envelopeReserve {
		t.Fatalf("size %d", size)
	}
	if fitted.FieldsOmitted == 0 || !strings.Contains(strings.Join(fitted.Warnings, " "), "Lists cut to keep the reply under 14 KB") {
		t.Fatalf("warnings %v, fields omitted %d", fitted.Warnings, fitted.FieldsOmitted)
	}
}

func TestSessionCacheKeyIsClios(t *testing.T) {
	service := sessionService{env: envFor("http://127.0.0.1:9", "example-user", "replace-me"), clioHome: "/h"}
	path := service.cachePath()
	if !strings.HasPrefix(path, "/h/sessions/127.0.0.1-9_") || !strings.HasSuffix(path, ".storageState.json") {
		t.Fatalf("path %s", path)
	}
}

func TestSessionExpiredResponse(t *testing.T) {
	for body, want := range map[string]bool{
		"\uFEFF <html><a href='/Login/NuiLogin.aspx'>": true,
		`{"Message":"Authentication failed."}`:         true,
		`{"Message":"Authentication failed. Again"}`:   false,
		"<html><crt-root></crt-root></html>":           false,
	} {
		if got := isSessionExpiredResponse(body); got != want {
			t.Errorf("%q: got %v", body, got)
		}
	}
}
