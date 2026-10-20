package creatio

import (
	"encoding/json"
	"errors"
	"net/url"
	"syscall"
	"testing"
	"time"
)

func TestTriggerSmokeSettledAndParked(t *testing.T) {
	userTask, script := "ProcessSchemaUserTask", "ScriptTask"
	parked := SmokeProcessRun{Status: "running", WaitingOn: []WaitingElement{{Type: &userTask}}}
	busy := SmokeProcessRun{Status: "running", WaitingOn: []WaitingElement{{Type: &script}}}
	done := SmokeProcessRun{Status: "completed", WaitingOn: []WaitingElement{}}
	if !triggerSmokeIsParked(parked) || triggerSmokeIsParked(busy) || triggerSmokeIsParked(done) {
		t.Fatal("parked detection")
	}
	cases := []struct {
		runs    []SmokeProcessRun
		quiet   time.Duration
		waited  time.Duration
		timeout int
		want    bool
	}{
		{nil, time.Hour, time.Hour, 15, false},
		{[]SmokeProcessRun{done, parked}, 3 * time.Second, 6 * time.Second, 15, true},
		{[]SmokeProcessRun{done}, 2 * time.Second, 6 * time.Second, 15, false},
		{[]SmokeProcessRun{done}, 3 * time.Second, 5 * time.Second, 15, false},
		{[]SmokeProcessRun{done}, 3 * time.Second, 2 * time.Second, 2, true},
		{[]SmokeProcessRun{busy}, time.Hour, time.Hour, 15, false},
	}
	for index, c := range cases {
		if got := triggerSmokeSettled(c.runs, c.quiet, c.waited, c.timeout); got != c.want {
			t.Errorf("case %d: settled = %v, want %v", index, got, c.want)
		}
	}
}

func TestTriggerSmokeResponseKeepsClioLists(t *testing.T) {
	encoded, _ := json.Marshal(TriggerSmokeResponse{Processes: []SmokeProcessRun{}, Created: []CreatedRecord{}, CorrelationID: "c"})
	if string(encoded) != `{"success":false,"processes":[],"created":[],"correlation-id":"c"}` {
		t.Fatalf("encoded = %s", encoded)
	}
	failure, _ := json.Marshal(TriggerSmokeFailure("bad"))
	var decoded map[string]any
	_ = json.Unmarshal(failure, &decoded)
	if decoded["error"] != "bad" || decoded["processes"] != nil || decoded["correlation-id"] == "" {
		t.Fatalf("failure = %s", failure)
	}
}

func TestClioConnectionText(t *testing.T) {
	refused := &url.Error{Op: "Post", URL: "http://127.0.0.1:9/0/DataService/json/SyncReply/SelectQuery", Err: syscall.ECONNREFUSED}
	if got := clioConnectionText(refused); got != "Connection refused (127.0.0.1:9)" {
		t.Fatalf("got %q", got)
	}
	if got := clioConnectionText(errors.New("other")); got != "other" {
		t.Fatalf("got %q", got)
	}
}
