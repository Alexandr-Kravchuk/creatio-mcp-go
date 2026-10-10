package creatio

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func pkgWriteFastRestart(t *testing.T) {
	t.Helper()
	attempts, retry, initial, poll := pkgWriteRestartAttempts, pkgWriteRestartRetryDelay, pkgWriteReadinessInitialDelay, pkgWriteReadinessPollInterval
	pkgWriteRestartRetryDelay, pkgWriteReadinessInitialDelay, pkgWriteReadinessPollInterval = time.Millisecond, time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() {
		pkgWriteRestartAttempts, pkgWriteRestartRetryDelay, pkgWriteReadinessInitialDelay, pkgWriteReadinessPollInterval = attempts, retry, initial, poll
	})
}

type pkgWriteRestartServer struct {
	mu        sync.Mutex
	calls     []string
	restart   []int // statuses answered to the restart request, in turn; then 200
	pings     []int // statuses answered to the health ping, in turn; then 200
	selects   []string
	loginCode int
	down      bool // the health ping never answers 2xx
}

func (s *pkgWriteRestartServer) next(list *[]int) int {
	if len(*list) == 0 {
		return 200
	}
	status := (*list)[0]
	*list = (*list)[1:]
	return status
}

func (s *pkgWriteRestartServer) start(t *testing.T, netCore bool) *Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		body, _ := io.ReadAll(r.Body)
		s.calls = append(s.calls, r.Method+" "+r.URL.Path+" "+string(body))
		switch {
		case strings.HasSuffix(r.URL.Path, "/Login"):
			_, _ = io.WriteString(w, `{"Code":`+map[bool]string{true: "1", false: "0"}[s.loginCode != 0]+`}`)
		case strings.HasSuffix(r.URL.Path, "/UnloadAppDomain"), strings.HasSuffix(r.URL.Path, "/RestartApp"):
			w.WriteHeader(s.next(&s.restart))
		case strings.HasSuffix(r.URL.Path, "/HealthCheck/Ping") && s.down:
			w.WriteHeader(503)
		case strings.HasSuffix(r.URL.Path, "/HealthCheck/Ping"):
			w.WriteHeader(s.next(&s.pings))
		case strings.HasSuffix(r.URL.Path, "/SelectQuery"):
			answer := `{"rows":[]}`
			if len(s.selects) > 0 {
				answer, s.selects = s.selects[0], s.selects[1:]
			}
			_, _ = io.WriteString(w, answer)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(server.Close)
	client, err := NewClient(Config{BaseURL: server.URL, Login: "example-user", Password: "replace-me", IsNetCore: netCore})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func (s *pkgWriteRestartServer) recorded() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

func TestPkgWriteRestartRequestRoutesAndRetries(t *testing.T) {
	pkgWriteFastRestart(t)
	framework := &pkgWriteRestartServer{restart: []int{500}}
	if result := framework.start(t, false).RestartApplication(context.Background()); result.ExitCode != 0 ||
		result.Messages[0].MessageType != "Info" || result.Messages[0].Value != "Done restart-web-app" {
		t.Fatalf("framework = %+v", result)
	}
	calls := framework.recorded()
	if len(calls) != 3 || calls[1] != "POST /0/ServiceModel/AppInstallerService.svc/UnloadAppDomain {}" || calls[2] != calls[1] {
		t.Fatalf("framework calls = %q", calls)
	}
	netCore := &pkgWriteRestartServer{}
	if result := netCore.start(t, true).RestartApplication(context.Background()); result.ExitCode != 0 {
		t.Fatalf("netcore = %+v", result)
	}
	if calls := netCore.recorded(); calls[1] != "POST /ServiceModel/AppInstallerService.svc/RestartApp {}" {
		t.Fatalf("netcore calls = %q", calls)
	}
	failing := &pkgWriteRestartServer{restart: []int{500, 500, 500}}
	result := failing.start(t, false).RestartApplication(context.Background())
	if result.ExitCode != 1 || result.Messages[0].MessageType != "Error" || !strings.Contains(result.Messages[0].Value, "HTTP 500") {
		t.Fatalf("failing = %+v", result)
	}
}

func TestPkgWriteReadinessNeedsAnAuthenticatedAnswer(t *testing.T) {
	pkgWriteFastRestart(t)
	server := &pkgWriteRestartServer{pings: []int{503, 302}, selects: []string{`{"Code":1}`, `{"rows":[{"Id":"x"}]}`}}
	client := server.start(t, false)
	if !client.WaitForReadiness(context.Background(), 5*time.Second) {
		t.Fatalf("not ready: %q", server.recorded())
	}
	var pings, selects int
	for _, call := range server.recorded() {
		if strings.HasPrefix(call, "GET /0/api/HealthCheck/Ping") {
			pings++
		}
		if strings.Contains(call, "/0/DataService/json/SyncReply/SelectQuery") {
			selects++
			if !strings.Contains(call, `"rootSchemaName":"SysSettings"`) {
				t.Fatalf("select = %s", call)
			}
		}
	}
	if pings != 4 || selects != 2 {
		t.Fatalf("pings=%d selects=%d calls=%q", pings, selects, server.recorded())
	}

	down := &pkgWriteRestartServer{down: true}
	started := time.Now()
	if down.start(t, false).WaitForReadiness(context.Background(), time.Second) || time.Since(started) > 5*time.Second {
		t.Fatal("an application that never answers must time out")
	}

	rejected := &pkgWriteRestartServer{loginCode: 1}
	if rejected.start(t, false).WaitForReadiness(context.Background(), 5*time.Second) {
		t.Fatal("rejected credentials must not count as ready")
	}
	var logins int
	for _, call := range rejected.recorded() {
		if strings.HasSuffix(strings.Fields(call)[1], "/Login") {
			logins++
		}
	}
	if logins < 2 || logins > 4 {
		t.Fatalf("two rejections end the wait; logins = %d", logins)
	}
}
