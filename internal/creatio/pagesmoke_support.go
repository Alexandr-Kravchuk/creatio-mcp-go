package creatio

// What page-smoke (internal/pagesmoke) reads from a resolved client: the connection settings its browser
// session logs in with, the created-record watcher it diffs around a click (clio's CreatedRecordWatcher, shared
// with run-process and trigger-smoke), and clio's two JSON spellings of an answer.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// PageSmokeEnvironment is the part of clio's EnvironmentSettings page-smoke reads.
type PageSmokeEnvironment struct {
	URI       string
	Login     string
	Password  string
	ClientID  string
	IsNetCore bool
}

// PageSmokeEnvironment returns the connection the client was resolved for.
func (c *Client) PageSmokeEnvironment() PageSmokeEnvironment {
	return PageSmokeEnvironment{URI: c.config.BaseURL, Login: c.config.Login, Password: c.config.Password,
		ClientID: c.config.ClientID, IsNetCore: c.config.IsNetCore}
}

// PageSmokeTransport is the client's HTTP transport (nil: the default one), so the browser session's own login
// takes the same route as every other call.
func (c *Client) PageSmokeTransport() http.RoundTripper { return c.http.Transport }

// PageSmokeWatch is CreatedRecordWatcher's baseline of the entities page-smoke diffs around a click.
type PageSmokeWatch struct {
	watcher  *createdRecordWatcher
	baseline *watchBaseline
}

// PageSmokeCaptureWatch is CreatedRecordWatcher.Capture. The reads finish even when ctx is cancelled: the
// record a click created must still be reported.
func (c *Client) PageSmokeCaptureWatch(ctx context.Context, entities []string, timeout time.Duration, stopAt time.Time) *PageSmokeWatch {
	watcher := &createdRecordWatcher{c: c, ctx: context.WithoutCancel(ctx)}
	return &PageSmokeWatch{watcher: watcher, baseline: watcher.capture(entities, timeout, stopAt)}
}

// Collect is CreatedRecordWatcher.Collect: the records that appeared since the baseline, and the warnings.
func (w *PageSmokeWatch) Collect(timeout time.Duration, stopAt time.Time) ([]CreatedRecord, []string) {
	outcome := w.watcher.collect(w.baseline, timeout, stopAt)
	return outcome.created, outcome.warnings
}

// ClioTransportText is the message .NET's HttpRequestException carries for a request that got no answer: a
// refused connection or an unknown host in the socket stack's words with the endpoint ("Connection refused
// (127.0.0.1:9)"); any other failure keeps Go's text.
func ClioTransportText(err error) string {
	if fault := newConnectFault(err); fault != nil {
		return fault.Error()
	}
	return err.Error()
}

// ClioMcpResultText renders a value the way clio's MCP server writes a tool result (McpResultJsonEncoder:
// System.Text.Json's relaxed escaping, Format characters escaped), keeping the member order and the numbers
// json.Marshal produced.
func ClioMcpResultText(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	node, err := parseJNode(encoded)
	if err != nil {
		return "", err
	}
	var buffer bytes.Buffer
	writeMcpResultJSON(&buffer, node)
	return buffer.String(), nil
}

// ClioDefaultJSONLength is the length in bytes of value written by System.Text.Json with its default options
// and encoder, which escapes ', +, &, <, >, ` and every non-ASCII character (JsonSerializer.SerializeToUtf8Bytes).
func ClioDefaultJSONLength(value any) int {
	text, err := ClioJSONText(value)
	if err != nil {
		return 0
	}
	return len(text)
}
