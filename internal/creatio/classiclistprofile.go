package creatio

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// classicPageProfile is clio's ClassicListProfileResult: the columns of the view the section opens with, read
// from the saved grid profile, with where they came from.
type classicPageProfile struct {
	columns  []classicPageProfileColumn
	view     string
	viewType string
	scope    string
	notes    []string
}

type classicPageProfileColumn struct {
	path    string
	caption *string
}

// classicPageReadProfile reads the section's saved grid profile over the platform's QueryProfile route, as
// clio's ClassicListProfileReader does. Every failure degrades to "no columns" with a note, never to an error.
func (c *Client) classicPageReadProfile(ctx context.Context, section string) classicPageProfile {
	notes := []string{}
	viewName := classicPageDefaultView
	activeView, ok, reason := c.classicPageQueryProfile(ctx, section+"ActiveViewSettingsProfile")
	if !ok {
		notes = append(notes, "The section's active view could not be read, so the platform default view "+
			fmt.Sprintf("'%s' was assumed; the reported columns may belong to a different view than the ", classicPageDefaultView)+
			"one the section opens with"+classicPageDescribe(reason)+".")
	} else if name := strings.TrimSpace(classicPageTokenString(activeView, "activeViewName")); name != "" {
		viewName = name
	}
	gridKey := section + "GridSettings" + viewName
	profile, ok, reason := c.classicPageQueryProfile(ctx, gridKey)
	if !ok {
		notes = append(notes, "The saved grid profile could not be read — the QueryProfile request failed"+
			classicPageDescribe(reason)+" — so the answer falls back to the section's static declaration "+
			"and may be narrower than the set the list renders.")
		return classicPageProfile{notes: notes}
	}
	if len(profile) == 0 {
		return classicPageProfile{notes: notes}
	}
	before := len(notes)
	columns, viewType := classicPageProfileColumns(profile, &notes)
	if len(columns) == 0 {
		if len(notes) == before {
			notes = append(notes, "A saved profile exists for this list, but no column configuration could be read out of "+
				"it, so the answer falls back to the section's static declaration; the stored payload may use "+
				"a shape this reader does not understand.")
		}
		return classicPageProfile{view: viewName, notes: notes}
	}
	return classicPageProfile{columns: columns, view: viewName, viewType: viewType, scope: c.classicPageProfileScope(ctx, gridKey, &notes), notes: notes}
}

// classicPageQueryProfile posts one QueryProfile read. An empty body means nothing is stored for the key.
func (c *Client) classicPageQueryProfile(ctx context.Context, key string) (map[string]json.RawMessage, bool, string) {
	body, _ := json.Marshal(map[string]string{"key": key})
	response, err := c.postDataServiceJSON(ctx, "QueryProfile", body, 45*time.Second, maxResponseBytes)
	if err != nil {
		return nil, false, err.Error()
	}
	if strings.TrimSpace(string(response)) == "" {
		return nil, true, ""
	}
	var profile map[string]json.RawMessage
	if err := json.Unmarshal(response, &profile); err != nil || profile == nil {
		if err == nil {
			err = fmt.Errorf("QueryProfile did not return a JSON object")
		}
		return nil, false, err.Error()
	}
	return profile, true, ""
}

// classicPageProfileColumns reads the configuration the grid renders. DataGrid.isTiled is the authoritative
// flag; older profiles keep both configurations at the top level as arrays.
func classicPageProfileColumns(profile map[string]json.RawMessage, notes *[]string) ([]classicPageProfileColumn, string) {
	var dataGrid map[string]json.RawMessage
	if raw, ok := profile["DataGrid"]; ok && json.Unmarshal(raw, &dataGrid) == nil && dataGrid != nil {
		return classicPageGridConfigs(dataGrid, "tiledConfig", "listedConfig", classicPageModernItems, notes)
	}
	return classicPageGridConfigs(profile, "tiledColumnsConfig", "listedColumnsConfig", classicPageLegacyItems, notes)
}

func classicPageGridConfigs(container map[string]json.RawMessage, tiledProperty, listedProperty string,
	parseItems func(any) []classicPageProfileColumn, notes *[]string) ([]classicPageProfileColumn, string) {
	var isTiled bool
	_ = json.Unmarshal(container["isTiled"], &isTiled)
	activeType, fallbackType := "listed", "tiled"
	activeProperty, fallbackProperty := listedProperty, tiledProperty
	if isTiled {
		activeType, fallbackType = fallbackType, activeType
		activeProperty, fallbackProperty = fallbackProperty, activeProperty
	}
	if config, ok := classicPageEmbeddedJSON(container[activeProperty], activeProperty, notes); ok {
		if active := parseItems(config); len(active) > 0 {
			return active, activeType
		}
	}
	config, ok := classicPageEmbeddedJSON(container[fallbackProperty], fallbackProperty, notes)
	if !ok {
		return nil, ""
	}
	fallback := parseItems(config)
	if len(fallback) == 0 {
		return nil, ""
	}
	*notes = append(*notes, fmt.Sprintf("The saved profile's active '%s' configuration is empty, so the '%s' ", activeType, fallbackType)+
		"configuration was reported instead; the rendered set may differ from what the section opens with.")
	return fallback, fallbackType
}

// classicPageEmbeddedJSON reads a configuration that the profile stores either inline or as a JSON string.
// Newtonsoft's JToken.Parse is lenient (comments, single quotes, unquoted keys), so the lenient reader is the
// fallback for a string that is not strict JSON.
func classicPageEmbeddedJSON(raw json.RawMessage, property string, notes *[]string) (any, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, false
	}
	var text string
	if json.Unmarshal(raw, &text) != nil {
		value, err := parseOrderedJSON(raw)
		return value, err == nil
	}
	if strings.TrimSpace(text) == "" {
		return nil, false
	}
	parsed, err := lenientJSON(text)
	if err == nil {
		value, err := parseOrderedJSON(parsed)
		if err == nil {
			return value, true
		}
	}
	*notes = append(*notes, fmt.Sprintf("The saved profile's '%s' value is not valid JSON and was skipped.", property))
	return nil, false
}

func classicPageModernItems(config any) []classicPageProfileColumn {
	object, _ := config.(*orderedObject)
	if object == nil {
		return nil
	}
	items, _ := jsonArrayAt(object, "items")
	return classicPageDistinctColumns(items, func(item any) (string, *string) {
		return classicPageFirstString(item, "bindTo", "metaPath", "path", "columnName"), classicPageFirstStringPointer(item, "caption")
	})
}

func classicPageLegacyItems(config any) []classicPageProfileColumn {
	array, ok := config.([]any)
	if !ok {
		return nil
	}
	cells := array
	nested := false
	for _, item := range array {
		if _, isArray := item.([]any); isArray {
			nested = true
			break
		}
	}
	if nested {
		// A legacy tiled configuration nests one array per rendered row; flattening one level covers both.
		cells = []any{}
		for _, item := range array {
			if row, isArray := item.([]any); isArray {
				cells = append(cells, row...)
			} else {
				cells = append(cells, item)
			}
		}
	}
	return classicPageDistinctColumns(cells, func(cell any) (string, *string) {
		key := classicPageKeyCell(cell)
		path := classicPageFirstString(cell, "metaPath", "columnName", "bindTo")
		if path == "" {
			path = classicPageFirstString(key, "bindTo", "name")
		}
		caption := classicPageFirstStringPointer(cell, "caption")
		if caption == nil {
			caption = classicPageFirstStringPointer(key, "caption")
		}
		return path, caption
	})
}

func classicPageKeyCell(cell any) any {
	object, _ := cell.(*orderedObject)
	if object == nil {
		return nil
	}
	if key, ok := jsonArrayAt(object, "key"); ok && len(key) > 0 {
		return key[0]
	}
	return nil
}

func classicPageDistinctColumns(items []any, read func(any) (string, *string)) []classicPageProfileColumn {
	result := []classicPageProfileColumn{}
	seen := map[string]bool{}
	for _, item := range items {
		path, caption := read(item)
		if strings.TrimSpace(path) == "" || seen[strings.ToLower(path)] {
			continue
		}
		seen[strings.ToLower(path)] = true
		result = append(result, classicPageProfileColumn{path: path, caption: caption})
	}
	return result
}

// classicPageFirstString returns the first non-blank string property, trimmed; a property that is an object
// may nest the binding one level deeper as { bindTo: ... }.
func classicPageFirstString(source any, names ...string) string {
	object, _ := source.(*orderedObject)
	if object == nil {
		return ""
	}
	for _, name := range names {
		switch value := object.get(name).(type) {
		case string:
			if strings.TrimSpace(value) != "" {
				return strings.TrimSpace(value)
			}
		case *orderedObject:
			if bindTo := classicPageFirstString(value, "bindTo"); bindTo != "" {
				return bindTo
			}
		}
	}
	return ""
}

func classicPageFirstStringPointer(source any, names ...string) *string {
	if value := classicPageFirstString(source, names...); value != "" {
		return &value
	}
	return nil
}

// classicPageProfileScope classifies the grid-settings row: "user" when the calling user has a personal row,
// "shared" when only the system row exists, "unknown" when that could not be established.
func (c *Client) classicPageProfileScope(ctx context.Context, gridKey string, notes *[]string) string {
	contactID, reason := c.classicPageCurrentContactID(ctx)
	if strings.TrimSpace(contactID) == "" {
		*notes = append(*notes, "The current user's contact could not be read, so the profile could not be classified as "+
			"personal or shared"+classicPageDescribe(reason)+".")
		return "unknown"
	}
	rows, err := c.selectRows(ctx, buildSelectQuery("SysProfileData", map[string]string{"Id": "Id"}, map[string]any{
		"Key": comparisonFilter("Key", gridKey, 1, 3), "Contact.Id": comparisonFilter("Contact.Id", contactID, 0, 3),
	}, 1))
	if err != nil {
		*notes = append(*notes, fmt.Sprintf("The saved profile could not be classified as personal or shared (%s).", err.Error()))
		return "unknown"
	}
	if len(rows) > 0 {
		return "user"
	}
	return "shared"
}

func (c *Client) classicPageCurrentContactID(ctx context.Context) (string, string) {
	response, err := c.postCreatioServiceJSON(ctx, "ServiceModel/UserInfoService.svc/GetCurrentUserInfo", []byte("{}"), 45*time.Second, maxResponseBytes)
	if err != nil {
		return "", err.Error()
	}
	if strings.TrimSpace(string(response)) == "" {
		return "", ""
	}
	var decoded struct {
		UserInfo map[string]json.RawMessage `json:"userInfo"`
	}
	if err := json.Unmarshal(response, &decoded); err != nil {
		return "", err.Error()
	}
	return rowText(decoded.UserInfo, "contactId"), ""
}

func classicPageTokenString(values map[string]json.RawMessage, key string) string {
	if raw, ok := values[key]; !ok || string(raw) == "null" {
		return ""
	}
	return rowText(values, key)
}

func classicPageDescribe(reason string) string {
	if strings.TrimSpace(reason) == "" {
		return ""
	}
	return " (" + strings.TrimSpace(reason) + ")"
}
