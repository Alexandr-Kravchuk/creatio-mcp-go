package creatio

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

const (
	defaultODataTop = 25
	maxODataTop     = 100
)

var odataIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ODataReadRequest is the intentionally small, safe subset of clio's odata-read contract.
// Filters, expands and mutations are not silently approximated: they will be added only with their
// own wire evidence. This surface supports bounded projections and stable paging.
type ODataReadRequest struct {
	Entity  string   `json:"entity"`
	Select  []string `json:"select,omitempty"`
	OrderBy string   `json:"orderBy,omitempty"`
	Top     *int     `json:"top,omitempty"`
	Skip    *int     `json:"skip,omitempty"`
	Count   bool     `json:"count,omitempty"`
}

// ODataReadResult exposes the OData v4 collection payload without a vendor object model.
type ODataReadResult struct {
	Rows  []map[string]any `json:"rows"`
	Count *int             `json:"count,omitempty"`
}

// ODataRead queries an OData v4 entity set after authenticating through the same forms/OAuth paths
// as ListApps. The OData route follows ServiceUrlBuilder too: legacy .NET Framework instances use
// /0/odata while .NET Core instances use /odata.
func (c *Client) ODataRead(ctx context.Context, input ODataReadRequest) (ODataReadResult, error) {
	if err := input.validate(); err != nil {
		return ODataReadResult{}, err
	}
	if c.config.ClientID != "" {
		if err := c.acquireToken(ctx); err != nil {
			return ODataReadResult{}, err
		}
	} else if err := c.formsLogin(ctx); err != nil {
		return ODataReadResult{}, err
	}

	query := url.Values{}
	if len(input.Select) > 0 {
		query.Set("$select", strings.Join(input.Select, ","))
	}
	if input.OrderBy != "" {
		query.Set("$orderby", input.OrderBy)
	}
	if input.Skip != nil {
		query.Set("$skip", fmt.Sprintf("%d", *input.Skip))
	}
	if input.Count {
		query.Set("$count", "true")
	}
	top := defaultODataTop
	if input.Top != nil {
		top = *input.Top
	}
	query.Set("$top", fmt.Sprintf("%d", top))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.serviceURL("odata/"+input.Entity)+"?"+query.Encode(), nil)
	if err != nil {
		return ODataReadResult{}, fmt.Errorf("build OData request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if csrf := c.csrfToken(); csrf != "" {
		req.Header.Set("BPMCSRF", csrf)
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	response, err := c.http.Do(req)
	if err != nil {
		return ODataReadResult{}, fmt.Errorf("OData transport failure: %w", err)
	}
	payload, readErr := readResponse(response)
	if readErr != nil {
		return ODataReadResult{}, fmt.Errorf("OData response: %w", readErr)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return ODataReadResult{}, fmt.Errorf("OData returned HTTP %d", response.StatusCode)
	}
	if looksLikeHTML(payload) {
		return ODataReadResult{}, fmt.Errorf("OData returned HTML instead of JSON; authentication or routing failed")
	}
	var wire struct {
		Value json.RawMessage `json:"value"`
		Count *int            `json:"@odata.count"`
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(payload, &wire); err != nil {
		return ODataReadResult{}, fmt.Errorf("OData returned invalid JSON: %w", err)
	}
	if len(wire.Error) != 0 && !bytes.Equal(wire.Error, []byte("null")) {
		return ODataReadResult{}, fmt.Errorf("OData returned an error envelope")
	}
	if wire.Value == nil {
		return ODataReadResult{}, fmt.Errorf("OData response has no value collection")
	}
	var rows []map[string]any
	if err := json.Unmarshal(wire.Value, &rows); err != nil {
		return ODataReadResult{}, fmt.Errorf("OData value is not a collection: %w", err)
	}
	return ODataReadResult{Rows: rows, Count: wire.Count}, nil
}

func (input ODataReadRequest) validate() error {
	if !odataIdentifier.MatchString(input.Entity) {
		return fmt.Errorf("entity must be an OData entity set name containing only letters, digits, and underscores")
	}
	if input.Top != nil && (*input.Top < 1 || *input.Top > maxODataTop) {
		return fmt.Errorf("top must be between 1 and %d", maxODataTop)
	}
	if input.Skip != nil && *input.Skip < 0 {
		return fmt.Errorf("skip must be zero or greater")
	}
	for _, member := range input.Select {
		if !validMemberPath(member) {
			return fmt.Errorf("select contains an invalid OData member path")
		}
	}
	if input.OrderBy != "" && !validOrderBy(input.OrderBy) {
		return fmt.Errorf("orderBy must be a member path optionally followed by asc or desc")
	}
	return nil
}

func validMemberPath(path string) bool {
	segments := strings.Split(path, "/")
	return path != "" && strings.TrimSpace(path) == path && allIdentifiers(segments)
}

func validOrderBy(orderBy string) bool {
	parts := strings.Fields(orderBy)
	return len(parts) >= 1 && len(parts) <= 2 && validMemberPath(parts[0]) &&
		(len(parts) == 1 || strings.EqualFold(parts[1], "asc") || strings.EqualFold(parts[1], "desc"))
}

func allIdentifiers(parts []string) bool {
	for _, part := range parts {
		if !odataIdentifier.MatchString(part) {
			return false
		}
	}
	return true
}
