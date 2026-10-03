package creatio

// The one authenticated path to Creatio's service routes (rest/..., ServiceModel/..., DataService/...).
// callService is what a tool uses: it authenticates (forms or OAuth, re-login once on a login page or
// 401/403), bounds the response size, and fails on a transport error, a non-2xx status or an HTML body
// (a login or error page instead of JSON). Routes are fixed strings in the tool's code; a tool argument
// never becomes a route.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// serviceCall is one request to a Creatio service route.
type serviceCall struct {
	// Method defaults to POST. A POST carries Body as JSON.
	Method string
	// Route is relative to the application root, e.g. "rest/RightsService/GetRecordRights".
	Route string
	Query url.Values
	Body  []byte
	// Timeout defaults to the client's 45 s; the request context carries it.
	Timeout time.Duration
	// Limit is the largest body accepted, in bytes; a larger one fails ("response exceeds safety byte limit").
	Limit int
	// Label names the service in failure texts; default "Creatio service".
	Label string
}

// serviceResponse is an answer whose status and content the caller judges itself.
type serviceResponse struct {
	status  int
	payload []byte
}

// validServiceRoute admits the three service roots and nothing that could leave them.
func validServiceRoute(route string) bool {
	if strings.Contains(route, "..") || strings.ContainsAny(route, "\\?#") {
		return false
	}
	for _, root := range []string{"rest/", "ServiceModel/", "DataService/"} {
		if strings.HasPrefix(route, root) && len(route) > len(root) {
			return true
		}
	}
	return false
}

// callService sends the call and returns the body of a 2xx JSON answer.
func (c *Client) callService(ctx context.Context, call serviceCall) ([]byte, error) {
	response, err := c.serviceRequest(ctx, call)
	if err != nil {
		return nil, err
	}
	label := call.label()
	if response.status < http.StatusOK || response.status >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("%s %s returned HTTP %d", label, call.Route, response.status)
	}
	if looksLikeHTML(response.payload) {
		return nil, fmt.Errorf("%s %s returned HTML instead of JSON; authentication or routing failed", label, call.Route)
	}
	return response.payload, nil
}

// serviceRequest sends the call and returns status and bounded body, whatever the status, for a service
// whose failures are read from the body (ClioGate, Data Forge).
func (c *Client) serviceRequest(ctx context.Context, call serviceCall) (serviceResponse, error) {
	if !validServiceRoute(call.Route) {
		return serviceResponse{}, fmt.Errorf("invalid Creatio service route")
	}
	method := call.Method
	if method == "" {
		method = http.MethodPost
	}
	timeout := call.Timeout
	if timeout <= 0 {
		timeout = c.http.Timeout
	}
	limit := call.Limit
	if limit <= 0 {
		limit = maxResponseBytes
	}
	label := call.label()
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// The configured Client has a 45-second timeout. For a caller-selected deadline, keep its transport and
	// redirect policy while the request context carries the requested timeout.
	response, _, err := c.doAuthenticated(requestCtx, c.requestClient(), func() (*http.Request, error) {
		requestURL := c.serviceURL(call.Route)
		if encoded := call.Query.Encode(); encoded != "" {
			requestURL += "?" + encoded
		}
		var body io.Reader
		if method != http.MethodGet {
			body = bytes.NewReader(call.Body)
		}
		request, err := http.NewRequestWithContext(requestCtx, method, requestURL, body)
		if err != nil {
			return nil, fmt.Errorf("build %s request: %w", label, err)
		}
		if method != http.MethodGet {
			request.Header.Set("Content-Type", "application/json")
		}
		request.Header.Set("Accept", "application/json")
		return request, nil
	})
	if err != nil {
		if isTransportError(err) {
			return serviceResponse{}, fmt.Errorf("%s %s transport failure: %w", label, call.Route, err)
		}
		return serviceResponse{}, err
	}
	payload, err := readResponseLimit(response, limit)
	if err != nil {
		return serviceResponse{}, fmt.Errorf("%s %s response: %w", label, call.Route, err)
	}
	return serviceResponse{status: response.StatusCode, payload: payload}, nil
}

func (call serviceCall) label() string {
	if call.Label == "" {
		return "Creatio service"
	}
	return call.Label
}

// postDataServiceJSON posts a caller-built JSON request to a fixed DataService endpoint. The caller supplies
// an explicit response budget because schema reads and ESQ results have different output limits.
func (c *Client) postDataServiceJSON(ctx context.Context, endpoint string, body []byte, timeout time.Duration, responseLimit int) ([]byte, error) {
	if endpoint == "" || strings.Contains(endpoint, "..") || strings.ContainsAny(endpoint, "/\\?#") {
		return nil, fmt.Errorf("invalid DataService endpoint")
	}
	return c.callService(ctx, serviceCall{Route: "DataService/json/SyncReply/" + endpoint, Body: body, Timeout: timeout, Limit: responseLimit})
}

// postCreatioServiceJSON posts to a fixed ServiceModel route.
func (c *Client) postCreatioServiceJSON(ctx context.Context, route string, body []byte, timeout time.Duration, responseLimit int) ([]byte, error) {
	if !strings.HasPrefix(route, "ServiceModel/") {
		return nil, fmt.Errorf("invalid Creatio service route")
	}
	return c.callService(ctx, serviceCall{Route: route, Body: body, Timeout: timeout, Limit: responseLimit})
}

// getClioGateJSON is restricted to the two read-only package-file endpoints. ClioGate is an installed
// Creatio package, not a Go/.NET client dependency; these calls require that package on the target.
func (c *Client) getClioGateJSON(ctx context.Context, route string, query url.Values, timeout time.Duration, responseLimit int) ([]byte, error) {
	switch route {
	case "rest/CreatioApiGateway/GetPackageFilesDirectoryContent", "rest/CreatioApiGateway/GetPackageFileContent":
	default:
		return nil, fmt.Errorf("unsupported ClioGate read route")
	}
	response, err := c.serviceRequest(ctx, serviceCall{Method: http.MethodGet, Route: route, Query: query,
		Timeout: timeout, Limit: responseLimit, Label: "ClioGate"})
	if err != nil {
		return nil, err
	}
	if response.status == http.StatusNotFound {
		return nil, fmt.Errorf("ClioGate %s returned HTTP 404: install or update the cliogate package to version %s or higher in the target environment (clio install-gate), then retry", route, minClioGateVersion)
	}
	// ClioGate answers a missing package or file with HTTP 400 or an HTML error page, never with a reason.
	if response.status == http.StatusBadRequest || response.status >= http.StatusInternalServerError || looksLikeHTML(response.payload) {
		return nil, fmt.Errorf("ClioGate could not complete %s (HTTP %d). The package or file may not exist; inspect the Creatio Error.log. If the installed gate artifacts are stale, run install-gate and retry", route, response.status)
	}
	if response.status < http.StatusOK || response.status >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("ClioGate %s returned HTTP %d", route, response.status)
	}
	return response.payload, nil
}
