package creatio

import (
	"context"
	"fmt"
	"strings"
)

// ProcessPageButton is one page button that can complete a Pre-configured page process element.
type ProcessPageButton struct {
	Name     string   `json:"name"`
	Caption  string   `json:"caption"`
	Event    string   `json:"event"`
	Requests []string `json:"requests"`
}

// ProcessPageDataSource is one page-scoped entity data source of a page.
type ProcessPageDataSource struct {
	Name             string `json:"name"`
	EntitySchemaName string `json:"entitySchemaName"`
}

// ProcessPageFactsResponse is clio's get-process-page-facts envelope.
type ProcessPageFactsResponse struct {
	Success                    bool                    `json:"success"`
	SchemaName                 string                  `json:"schema-name,omitempty"`
	CompletingButtonCandidates []ProcessPageButton     `json:"completingButtonCandidates,omitzero"`
	DataSources                []ProcessPageDataSource `json:"dataSources,omitzero"`
	Error                      string                  `json:"error,omitempty"`
	Warnings                   []string                `json:"warnings,omitzero"`
}

const processPageDefaultCulture = "en-US"

// processPageCompletingRequests mirrors the process designer's allow-list of page-completing requests.
var processPageCompletingRequests = []string{"crt.SaveRecordRequest", "crt.ClosePageRequest", "crt.CancelRecordChangesRequest"}

// GetProcessPageFacts reads a Freedom UI page through the get-page merge (no files are written) and projects
// the facts a Pre-configured page element needs: the completing-button candidates and the page-scoped entity
// data sources. The rules are clio's ProcessPageFactsProjection, transcribed from the process designer.
func (c *Client) GetProcessPageFacts(ctx context.Context, schemaName, culture string) ProcessPageFactsResponse {
	if strings.TrimSpace(schemaName) == "" {
		return ProcessPageFactsResponse{Error: "schema-name is required."}
	}
	page := c.GetPage(ctx, PageGetRequest{SchemaName: schemaName})
	if !page.Success || page.bundle == nil {
		message := page.Error
		if message == "" {
			message = fmt.Sprintf("Page '%s' could not be read.", schemaName)
		}
		return ProcessPageFactsResponse{SchemaName: schemaName, Error: message}
	}
	if pageType := processPageType(page); pageType != "web" {
		reason := "it could not be positively identified as one — a Classic UI page reads back this way"
		if pageType == "mobile" {
			reason = "it is a MOBILE page"
		}
		return ProcessPageFactsResponse{SchemaName: schemaName, Error: fmt.Sprintf("Page '%s' is not a Freedom UI web page (%s), so it has no "+
			"completing-button candidates to report. A Classic UI page completes through its own page-designer buttons instead.", schemaName, reason)}
	}
	buttons, dataSources, err := processPageProject(page.bundle, culture)
	if err != nil {
		return ProcessPageFactsResponse{SchemaName: schemaName,
			Error: fmt.Sprintf("Page '%s' was read but its merged bundle could not be projected: %v", schemaName, err)}
	}
	candidates := []ProcessPageButton{}
	for _, button := range buttons {
		if processPageIsCandidate(button) {
			candidates = append(candidates, button)
		}
	}
	name := schemaName
	if page.fullPage != nil && page.fullPage.SchemaName != "" {
		name = page.fullPage.SchemaName
	}
	response := ProcessPageFactsResponse{Success: true, SchemaName: name, CompletingButtonCandidates: candidates, DataSources: dataSources}
	if len(candidates) == 0 {
		response.Warnings = []string{fmt.Sprintf("No completing-button candidates were found on '%s'. Either the page genuinely has no buttons, "+
			"or the merged bundle's shape was not recognised — verify in the page designer before building a Pre-configured page element on it, "+
			"because an element without a completing button can never finish at run time.", schemaName)}
	}
	return response
}

// processPageType is clio's ResolvePageType: the label, then a present numeric type (a positive non-web
// identification), and only when the numeric is absent the body.
func processPageType(page PageGetResult) string {
	if page.fullPage != nil {
		if page.fullPage.SchemaType == "web" || page.fullPage.SchemaType == "mobile" {
			return page.fullPage.SchemaType
		}
		if page.fullPage.SchemaTypeValue != nil {
			return "unknown"
		}
	}
	body := strings.TrimSpace(page.rawBody)
	if body != "" && strings.HasPrefix(body, "{") {
		return "mobile"
	}
	if strings.Contains(page.rawBody, "viewConfigDiff") {
		return "web"
	}
	return "unknown"
}

// processPageProject walks the merged view config for crt.Button nodes and the model config for page-scoped
// entity data sources. A node shape Newtonsoft could not cast is reported as a projection failure.
func processPageProject(bundle *jnode, culture string) (buttons []ProcessPageButton, dataSources []ProcessPageDataSource, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			fault, ok := recovered.(applierFault)
			if !ok {
				panic(recovered)
			}
			err = fmt.Errorf("%s", fault.message)
		}
	}()
	if strings.TrimSpace(culture) == "" {
		culture = processPageDefaultCulture
	}
	var resourceStrings *jnode
	if resources := bundle.get("resources"); resources.isObject() {
		resourceStrings = resources.get("strings")
	}
	collected := []ProcessPageButton{}
	processPageCollectButtons(bundle.get("viewConfig"), resourceStrings, culture, &collected)
	var sources *jnode
	if model := bundle.get("modelConfig"); model.isObject() {
		sources = model.get("dataSources")
	}
	return processPageDistinct(collected), processPageDataSources(sources), nil
}

func processPageIsCandidate(button ProcessPageButton) bool {
	if len(button.Requests) == 0 {
		return true
	}
	for _, request := range button.Requests {
		for _, completing := range processPageCompletingRequests {
			if request == completing {
				return true
			}
		}
	}
	return false
}

// processPageDistinct collapses buttons sharing a name, keeping the most informative entry: a candidate beats
// a non-candidate, and one naming its requests beats one with none; the first occurrence wins ties.
func processPageDistinct(buttons []ProcessPageButton) []ProcessPageButton {
	index := map[string]int{}
	distinct := []ProcessPageButton{}
	rank := func(button ProcessPageButton) int {
		value := 0
		if processPageIsCandidate(button) {
			value += 2
		}
		if len(button.Requests) > 0 {
			value++
		}
		return value
	}
	for _, button := range buttons {
		position, seen := index[button.Name]
		if !seen {
			index[button.Name] = len(distinct)
			distinct = append(distinct, button)
			continue
		}
		if rank(button) > rank(distinct[position]) {
			distinct[position] = button
		}
	}
	return distinct
}

func processPageCollectButtons(node, resourceStrings *jnode, culture string, collected *[]ProcessPageButton) {
	switch {
	case node.isArray():
		for _, item := range node.items {
			processPageCollectButtons(item, resourceStrings, culture, collected)
		}
	case node.isObject():
		if processPageScalar(node.get("type")) == "crt.Button" {
			processPageAppendButton(node, resourceStrings, culture, collected)
		}
		for _, key := range node.keys {
			processPageCollectButtons(node.props[key], resourceStrings, culture, collected)
		}
	}
}

func processPageAppendButton(element, resourceStrings *jnode, culture string, collected *[]ProcessPageButton) {
	caption := processPageCaption(element.get("caption"), resourceStrings, culture)
	if processPageScalar(element.get("clickMode")) == "menu" {
		processPageAppendMenuItems(element.get("menuItems"), resourceStrings, culture, caption, collected)
		return
	}
	name := processPageScalar(element.get("name"))
	if strings.TrimSpace(name) == "" {
		return
	}
	*collected = append(*collected, processPageButton(name, caption, element.get("clicked")))
}

// processPageAppendMenuItems expands a menu into its leaf items, composing "parent | item" captions as the
// designer does.
func processPageAppendMenuItems(menuItems, resourceStrings *jnode, culture, parentCaption string, collected *[]ProcessPageButton) {
	if !menuItems.isArray() {
		return
	}
	for _, item := range menuItems.items {
		if !item.isObject() {
			continue
		}
		caption := parentCaption + " | " + processPageCaption(item.get("caption"), resourceStrings, culture)
		if nested := item.get("items"); nested.isArray() && len(nested.items) > 0 {
			processPageAppendMenuItems(nested, resourceStrings, culture, caption, collected)
			continue
		}
		if name := processPageScalar(item.get("name")); strings.TrimSpace(name) != "" {
			*collected = append(*collected, processPageButton(name, caption, item.get("clicked")))
		}
	}
}

func processPageButton(name, caption string, clicked *jnode) ProcessPageButton {
	requests := []string{}
	if clicked.isObject() {
		if request := processPageScalar(clicked.get("request")); strings.TrimSpace(request) != "" {
			requests = append(requests, request)
		}
	}
	return ProcessPageButton{Name: name, Caption: caption + " | " + name, Event: "clicked", Requests: requests}
}

// processPageCaption resolves #ResourceString(Key)# and $Resources.Strings.Key against the merged resource
// strings, falling back to the raw text so a caption is never empty.
func processPageCaption(caption, resourceStrings *jnode, culture string) string {
	if caption == nil || caption.kind != jkString || strings.TrimSpace(caption.text) == "" {
		return ""
	}
	raw := caption.text
	key := ""
	switch {
	case strings.HasPrefix(raw, "#ResourceString(") && strings.HasSuffix(raw, ")#") && len(raw) >= len("#ResourceString()#"):
		key = raw[len("#ResourceString(") : len(raw)-len(")#")]
	case strings.HasPrefix(raw, "$Resources.Strings."):
		key = raw[len("$Resources.Strings."):]
	}
	if strings.TrimSpace(key) == "" || !resourceStrings.isObject() {
		return raw
	}
	localized := resourceStrings.get(key)
	if !localized.isObject() {
		return raw
	}
	for _, candidate := range []*jnode{localized.get(culture), localized.get(processPageDefaultCulture)} {
		if value := candidate.stringValue(); value != nil {
			return *value
		}
	}
	if len(localized.keys) > 0 {
		if value := localized.props[localized.keys[0]].stringValue(); value != nil {
			return *value
		}
	}
	return raw
}

// processPageDataSources keeps only page-scoped crt.EntityDataSource entries, as the designer's card does.
func processPageDataSources(sources *jnode) []ProcessPageDataSource {
	collected := []ProcessPageDataSource{}
	if !sources.isObject() {
		return collected
	}
	for _, name := range sources.keys {
		source := sources.props[name]
		if !source.isObject() || processPageScalar(source.get("scope")) != "page" || processPageScalar(source.get("type")) != "crt.EntityDataSource" {
			continue
		}
		config := source.get("config")
		if !config.isObject() {
			continue
		}
		entity := processPageScalar(config.get("entitySchemaName"))
		if strings.TrimSpace(entity) == "" {
			continue
		}
		collected = append(collected, ProcessPageDataSource{Name: name, EntitySchemaName: entity})
	}
	return collected
}

// processPageScalar is JToken.Value<string>() with nil and null read as "".
func processPageScalar(node *jnode) string {
	if node == nil || node.kind == jkNull {
		return ""
	}
	return node.str()
}
