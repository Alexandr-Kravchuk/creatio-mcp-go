package creatio

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	pageSchemaTypeWeb    = 9
	pageSchemaTypeMobile = 10

	dashboardTemplateName = "BaseDashboardTemplate"
	dashboardTemplateUID  = "eb4d4a67-25d8-fcfa-7851-c4c91efb7b9c"
	desktopTemplateName   = "CentralAreaDesktopTemplate"
	desktopTemplateUID    = "fbc98c89-0691-479c-bc25-59c11ac2365f"
	desktopGroupName      = "Desktop"
)

type PageTemplateItem struct {
	UID        string `json:"uId"`
	Name       string `json:"name"`
	Title      string `json:"title"`
	GroupName  string `json:"groupName"`
	SchemaType int    `json:"schemaType"`
}

// PageTemplateListResult keeps count on failure (0), as clio's envelope does.
type PageTemplateListResult struct {
	Success bool               `json:"success"`
	Count   int                `json:"count"`
	Items   []PageTemplateItem `json:"items,omitzero"`
	Error   string             `json:"error,omitempty"`
}

// ParsePageSchemaType accepts clio's schema-type spellings. An empty value means both catalogs.
func ParsePageSchemaType(value string) (int, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "web", "freedomuipage", "page", "9":
		return pageSchemaTypeWeb, nil
	case "mobile", "mobilepage", "10":
		return pageSchemaTypeMobile, nil
	}
	return 0, fmt.Errorf("Unknown schema-type '%s'. Use 'web' or 'mobile'.", value)
}

// ListPageTemplates reads schema.template.api for the web catalog, then the mobile one, and adds the two web
// templates clio injects because the endpoint omits them (dashboard, desktop).
func (c *Client) ListPageTemplates(ctx context.Context, schemaType string) PageTemplateListResult {
	types := []int{pageSchemaTypeWeb, pageSchemaTypeMobile}
	if strings.TrimSpace(schemaType) != "" {
		parsed, err := ParsePageSchemaType(schemaType)
		if err != nil {
			return PageTemplateListResult{Error: err.Error()}
		}
		types = []int{parsed}
	}
	items := []PageTemplateItem{}
	for _, schemaType := range types {
		loaded, err := c.loadPageTemplates(ctx, schemaType)
		if err != nil {
			return PageTemplateListResult{Error: err.Error()}
		}
		items = append(items, loaded...)
	}
	return PageTemplateListResult{Success: true, Count: len(items), Items: items}
}

func (c *Client) loadPageTemplates(ctx context.Context, schemaType int) ([]PageTemplateItem, error) {
	route := "rest/schema.template.api/templates"
	payload, err := c.getSchemaTemplateJSON(ctx, route, schemaType)
	if err != nil {
		return nil, err
	}
	var response map[string]json.RawMessage
	if err := json.Unmarshal(payload, &response); err != nil || response == nil {
		if looksLikeHTML(payload) {
			return nil, fmt.Errorf("Page template catalog endpoint did not return JSON (%s). The server returned an HTML response, likely a login page or a Creatio version without /rest/schema.template.api/templates (Creatio 7.18+).", route)
		}
		snippet := string(payload)
		if strings.TrimSpace(snippet) == "" {
			snippet = "<empty body>"
		} else if len([]rune(snippet)) > 200 {
			snippet = string([]rune(snippet)[:200]) + "…"
		}
		return nil, fmt.Errorf("Page template catalog endpoint returned an unparseable response (%s). Response preview: %s", route, snippet)
	}
	var success bool
	_ = json.Unmarshal(response["success"], &success)
	if !success {
		return nil, fmt.Errorf("%s", pageTemplateFailureDetail(response["errorInfo"]))
	}
	var rawItems []map[string]json.RawMessage
	if err := json.Unmarshal(response["items"], &rawItems); err != nil || rawItems == nil {
		// clio caches an empty catalog and skips the injections when items is not an array.
		return []PageTemplateItem{}, nil
	}
	items := make([]PageTemplateItem, 0, len(rawItems)+2)
	for _, item := range rawItems {
		if item == nil {
			continue
		}
		items = append(items, PageTemplateItem{
			UID: pageTemplateText(item["uId"]), Name: pageTemplateText(item["name"]), Title: pageTemplateText(item["title"]),
			GroupName: pageTemplateText(item["groupName"]), SchemaType: schemaType,
		})
	}
	if schemaType != pageSchemaTypeWeb {
		return items, nil
	}
	if !hasPageTemplate(items, dashboardTemplateName) {
		// The platform's dashboard query lists a dashboard only when its schema group is exactly DashboardPage.
		items = append(items, PageTemplateItem{UID: dashboardTemplateUID, Name: dashboardTemplateName, Title: "Dashboard",
			GroupName: "DashboardPage", SchemaType: pageSchemaTypeWeb})
	}
	desktopFound := false
	for index := range items {
		if strings.EqualFold(items[index].Name, desktopTemplateName) {
			// Only group Desktop registers a new schema in the desktop selector, so clio forces it.
			items[index].GroupName = desktopGroupName
			desktopFound = true
			break
		}
	}
	if !desktopFound {
		items = append(items, PageTemplateItem{UID: desktopTemplateUID, Name: desktopTemplateName, Title: "Desktop",
			GroupName: desktopGroupName, SchemaType: pageSchemaTypeWeb})
	}
	return items, nil
}

func hasPageTemplate(items []PageTemplateItem, name string) bool {
	for _, item := range items {
		if strings.EqualFold(item.Name, name) {
			return true
		}
	}
	return false
}

// pageTemplateText renders a JSON value the way Newtonsoft's JToken.ToString does for scalars: strings
// unquoted, null and a missing value empty, anything else as its JSON text.
func pageTemplateText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	return string(raw)
}

func pageTemplateFailureDetail(errorInfo json.RawMessage) string {
	var info map[string]json.RawMessage
	if err := json.Unmarshal(errorInfo, &info); err == nil && info != nil {
		if message, ok := info["message"]; ok && string(message) != "null" {
			return pageTemplateText(message)
		}
	}
	if len(errorInfo) > 0 && string(errorInfo) != "null" {
		return pageTemplateText(errorInfo)
	}
	return "Failed to load page template catalog"
}

// getSchemaTemplateJSON issues the authenticated GET of the read-only template catalog. Only that fixed route is
// accepted, so a tool argument cannot select another endpoint.
func (c *Client) getSchemaTemplateJSON(ctx context.Context, route string, schemaType int) ([]byte, error) {
	if route != "rest/schema.template.api/templates" {
		return nil, fmt.Errorf("unsupported schema template route")
	}
	requestCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	response, _, err := c.doAuthenticated(requestCtx, c.requestClient(), func() (*http.Request, error) {
		request, err := http.NewRequestWithContext(requestCtx, http.MethodGet,
			fmt.Sprintf("%s?schemaType=%d", c.serviceURL(route), schemaType), nil)
		if err != nil {
			return nil, fmt.Errorf("build page template request: %w", err)
		}
		request.Header.Set("Accept", "application/json")
		return request, nil
	})
	if err != nil {
		if isAuthenticationError(err) {
			return nil, err
		}
		if isTransportError(err) {
			return nil, fmt.Errorf("Creatio service %s transport failure: %w", route, err)
		}
		return nil, err
	}
	payload, err := readResponseLimit(response, maxResponseBytes)
	if err != nil {
		return nil, fmt.Errorf("Creatio service %s response: %w", route, err)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("Creatio service %s returned HTTP %d", route, response.StatusCode)
	}
	return payload, nil
}
