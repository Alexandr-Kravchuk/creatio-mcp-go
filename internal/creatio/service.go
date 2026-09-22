package creatio

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// postDataServiceJSON posts a caller-built JSON request to a fixed DataService endpoint. The
// caller supplies an explicit response budget because schema reads and ESQ results have different
// output limits. Authentication and CSRF handling are shared with the existing direct HTTP client.
func (c *Client) postDataServiceJSON(ctx context.Context, endpoint string, body []byte, timeout time.Duration, responseLimit int) ([]byte, error) {
	if endpoint == "" || strings.Contains(endpoint, "..") || strings.ContainsAny(endpoint, "/\\?#") {
		return nil, fmt.Errorf("invalid DataService endpoint")
	}
	return c.postCreatioJSON(ctx, "DataService/json/SyncReply/"+endpoint, body, timeout, responseLimit)
}

// postCreatioServiceJSON posts to a fixed non-DataService platform route. It is deliberately
// limited to ServiceModel routes so callers cannot turn a tool argument into an arbitrary URL.
func (c *Client) postCreatioServiceJSON(ctx context.Context, route string, body []byte, timeout time.Duration, responseLimit int) ([]byte, error) {
	if !strings.HasPrefix(route, "ServiceModel/") || strings.Contains(route, "..") || strings.ContainsAny(route, "\\?#") {
		return nil, fmt.Errorf("invalid Creatio service route")
	}
	return c.postCreatioJSON(ctx, route, body, timeout, responseLimit)
}

// getClioGateJSON is restricted to the two read-only package-file endpoints. ClioGate is an
// installed Creatio package, not a Go/.NET client dependency; these calls require that package on
// the target environment.
func (c *Client) getClioGateJSON(ctx context.Context, route string, query url.Values, timeout time.Duration, responseLimit int) ([]byte, error) {
	switch route {
	case "rest/CreatioApiGateway/GetPackageFilesDirectoryContent", "rest/CreatioApiGateway/GetPackageFileContent":
	default:
		return nil, fmt.Errorf("unsupported ClioGate read route")
	}
	if timeout <= 0 {
		timeout = c.http.Timeout
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if c.config.ClientID != "" {
		if err := c.acquireToken(requestCtx); err != nil {
			return nil, err
		}
	} else if err := c.formsLogin(requestCtx); err != nil {
		return nil, err
	}

	requestURL := c.serviceURL(route)
	if encoded := query.Encode(); encoded != "" {
		requestURL += "?" + encoded
	}
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build ClioGate request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	if csrf := c.csrfToken(); csrf != "" {
		request.Header.Set("BPMCSRF", csrf)
	}
	if token := c.bearerToken(); token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{Transport: c.http.Transport, Jar: c.http.Jar, CheckRedirect: c.http.CheckRedirect}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("ClioGate %s transport failure: %w", route, err)
	}
	payload, err := readResponseLimit(response, responseLimit)
	if err != nil {
		return nil, fmt.Errorf("ClioGate %s response: %w", route, err)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("ClioGate %s returned HTTP %d", route, response.StatusCode)
	}
	if looksLikeHTML(payload) {
		return nil, fmt.Errorf("ClioGate could not complete %s; the package or file may not exist, or the installed gate may be stale", route)
	}
	return payload, nil
}

func (c *Client) postCreatioJSON(ctx context.Context, route string, body []byte, timeout time.Duration, responseLimit int) ([]byte, error) {
	if timeout <= 0 {
		timeout = c.http.Timeout
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if c.config.ClientID != "" {
		if err := c.acquireToken(requestCtx); err != nil {
			return nil, err
		}
	} else if err := c.formsLogin(requestCtx); err != nil {
		return nil, err
	}

	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost,
		c.serviceURL(route), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build Creatio service request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	if csrf := c.csrfToken(); csrf != "" {
		request.Header.Set("BPMCSRF", csrf)
	}
	if token := c.bearerToken(); token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}

	// The configured Client has a 45-second timeout. For endpoints with a caller-selected deadline,
	// retain its transport/cookie jar but let the request context carry the requested timeout.
	client := &http.Client{Transport: c.http.Transport, Jar: c.http.Jar, CheckRedirect: c.http.CheckRedirect}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("Creatio service %s transport failure: %w", route, err)
	}
	payload, err := readResponseLimit(response, responseLimit)
	if err != nil {
		return nil, fmt.Errorf("Creatio service %s response: %w", route, err)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("Creatio service %s returned HTTP %d", route, response.StatusCode)
	}
	if looksLikeHTML(payload) {
		return nil, fmt.Errorf("Creatio service %s returned HTML instead of JSON; authentication or routing failed", route)
	}
	return payload, nil
}
