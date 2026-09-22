package creatio

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
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
		c.serviceURL("DataService/json/SyncReply/"+endpoint), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build DataService request: %w", err)
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
		return nil, fmt.Errorf("DataService %s transport failure: %w", endpoint, err)
	}
	payload, err := readResponseLimit(response, responseLimit)
	if err != nil {
		return nil, fmt.Errorf("DataService %s response: %w", endpoint, err)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("DataService %s returned HTTP %d", endpoint, response.StatusCode)
	}
	if looksLikeHTML(payload) {
		return nil, fmt.Errorf("DataService %s returned HTML instead of JSON; authentication or routing failed", endpoint)
	}
	return payload, nil
}
