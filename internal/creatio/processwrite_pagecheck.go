package creatio

// clio's ProcessPageFactsChecker: before a build or an edit is sent, every preconfiguredPage block that names
// buttons or data sources is checked against the page those names live on. A name the page does not carry
// refuses the call (it would build green and then leave the step waiting); a name that exists but is not a
// completing candidate, or a data source that is not page-scoped, is a warning. A page that cannot be read
// is not checked.

import (
	"context"
	"fmt"
	"strings"
)

// processWritePageFacts is the checker's PageFacts record.
type processWritePageFacts struct {
	allButtons       []string
	candidates       []string
	allDataSources   []string
	offeredNames     []string
	offeredEntities  map[string]string
	dataSourcesKnown bool
}

type processWriteNamedSource struct {
	name   string
	entity string
}

// processWriteCheckPages is CheckPreconfiguredPages: the refusal text ("" when none) and the warnings.
func (c *Client) processWriteCheckPages(ctx context.Context, payload *jnode) (string, []string) {
	warnings := []string{}
	if payload == nil {
		return "", warnings
	}
	factsByPage := map[string]*processWritePageFacts{}
	readPages := map[string]bool{}
	for _, block := range processWritePageBlocks(payload) {
		page := strings.TrimSpace(processWriteNodeString(block.get("page")))
		if page == "" {
			continue
		}
		buttons := processWriteBlockNames(block.get("buttons"))
		sources := processWriteBlockSources(block.get("dataSources"))
		if len(buttons) == 0 && len(sources) == 0 {
			continue
		}
		key := strings.ToLower(page)
		if !readPages[key] {
			factsByPage[key] = c.processWriteReadPageFacts(ctx, page)
			readPages[key] = true
		}
		facts := factsByPage[key]
		if facts == nil {
			continue
		}
		if refusal := processWriteCheckButtons(page, buttons, facts, &warnings); refusal != "" {
			return refusal, warnings
		}
		if refusal := processWriteCheckSources(page, sources, facts, &warnings); refusal != "" {
			return refusal, warnings
		}
	}
	return "", warnings
}

func processWriteCheckButtons(page string, names []string, facts *processWritePageFacts, warnings *[]string) string {
	for _, name := range names {
		if !processWriteContains(facts.allButtons, name) {
			available := "that page reports no buttons at all"
			if len(facts.allButtons) > 0 {
				available = "the page carries: " + processWriteQuoted(facts.allButtons)
			}
			return fmt.Sprintf("Page '%s' has no button named '%s'. A completing button is stored as the tag "+
				"'<name>_clicked' and the run time matches the pressed button against it, so a name the page does "+
				"not carry builds and saves green and then leaves the step waiting forever. Read the page's "+
				"candidates with get-process-page-facts and pass them unchanged (%s).", page, name, available)
		}
		if !processWriteContains(facts.candidates, name) {
			*warnings = append(*warnings, fmt.Sprintf("Button '%s' exists on page '%s' but is not among its "+
				"completing-button candidates — its handler issues a request that does not complete the "+
				"page. That is correct for a custom button which finishes the step in its own code; if "+
				"it does not, the step waits after the button is pressed.", name, page))
		}
	}
	return ""
}

func processWriteCheckSources(page string, sources []processWriteNamedSource, facts *processWritePageFacts, warnings *[]string) string {
	if !facts.dataSourcesKnown {
		return ""
	}
	for _, source := range sources {
		if !processWriteContains(facts.allDataSources, source.name) {
			available := "that page has no page-scoped entity data source, so pass no 'dataSources' at all"
			if len(facts.offeredNames) > 0 {
				available = "the page offers: " + processWriteQuoted(facts.offeredNames)
			}
			return fmt.Sprintf("Page '%s' has no data source named '%s'. The element stores it as the parameter "+
				"tag 'DataSource:<name>:PrimaryColumnValue', and when the completing button is pressed the run time "+
				"resolves that name against the page's own data sources to read the record id back. A name the page "+
				"does not declare resolves to nothing and the completion is abandoned there — silently: the page "+
				"stays open with no error and no validation message, and the process instance never leaves "+
				"'Running'. Read the page's data sources with get-process-page-facts (%s).", page, source.name, available)
		}
		entity, offered := facts.offeredEntities[source.name]
		if !offered {
			*warnings = append(*warnings, fmt.Sprintf("Data source '%s' is declared on page '%s' but is not one of "+
				"its page-scoped entity data sources — those are the ones the element's card offers, and the "+
				"only ones that yield an element parameter the page fills. The step can still complete; the "+
				"parameter generated for this source may never receive a value.", source.name, page))
			continue
		}
		if strings.TrimSpace(source.entity) != "" && source.entity != entity {
			*warnings = append(*warnings, fmt.Sprintf("Data source '%s' on page '%s' is over '%s', not "+
				"'%s'. The generated parameter is typed from the name you pass, "+
				"so the wrong entity resolves the wrong primary column and the record id never reaches the "+
				"process. Pass the entity get-process-page-facts reports.", source.name, page, entity, source.entity))
		}
	}
	return ""
}

// processWriteReadPageFacts is ReadPageFacts over ProcessPageFactsCommand.TryGetFacts; nil when the page
// cannot be read, is not a Freedom UI web page or cannot be projected.
func (c *Client) processWriteReadPageFacts(ctx context.Context, page string) (facts *processWritePageFacts) {
	defer func() {
		if recover() != nil {
			facts = nil
		}
	}()
	read := c.GetPage(ctx, PageGetRequest{SchemaName: page})
	if !read.Success || read.bundle == nil || processPageType(read) != "web" {
		return nil
	}
	buttons, dataSources, err := processPageProject(read.bundle, "")
	if err != nil {
		return nil
	}
	facts = &processWritePageFacts{offeredEntities: map[string]string{}, dataSourcesKnown: true, allDataSources: []string{}}
	for _, button := range buttons {
		if strings.TrimSpace(button.Name) == "" {
			continue
		}
		facts.allButtons = append(facts.allButtons, button.Name)
		if processPageIsCandidate(button) {
			facts.candidates = append(facts.candidates, button.Name)
		}
	}
	for _, source := range dataSources {
		if strings.TrimSpace(source.Name) == "" {
			continue
		}
		if _, seen := facts.offeredEntities[source.Name]; !seen {
			facts.offeredNames = append(facts.offeredNames, source.Name)
		}
		facts.offeredEntities[source.Name] = source.EntitySchemaName
	}
	if declared := read.bundle.get("modelConfig").get("dataSources"); declared.isObject() {
		for _, name := range declared.keys {
			if strings.TrimSpace(name) != "" {
				facts.allDataSources = append(facts.allDataSources, name)
			}
		}
	}
	return facts
}

// processWritePageBlocks is FindPreconfiguredPageBlocks: every object under a "preconfiguredPage" key, in
// document order, the block itself before anything nested in it.
func processWritePageBlocks(node *jnode) []*jnode {
	blocks := []*jnode{}
	if node == nil {
		return blocks
	}
	switch node.kind {
	case jkObject:
		for _, key := range node.keys {
			value := node.props[key]
			if value.isObject() && key == "preconfiguredPage" {
				blocks = append(blocks, value)
			}
			blocks = append(blocks, processWritePageBlocks(value)...)
		}
	case jkArray:
		for _, item := range node.items {
			blocks = append(blocks, processWritePageBlocks(item)...)
		}
	}
	return blocks
}

// processWriteNodeString is JsonNode.GetValue<string>() on an optional node: the string, or "" for an
// absent or null node. A value of another kind is what GetValue refuses; it is read as no name.
func processWriteNodeString(node *jnode) string {
	if node == nil || node.kind != jkString {
		return ""
	}
	return node.text
}

func processWriteBlockNames(items *jnode) []string {
	names := []string{}
	if !items.isArray() {
		return names
	}
	for _, item := range items.items {
		name := strings.TrimSpace(processWriteNodeString(item.get("name")))
		if name != "" {
			names = append(names, name)
		}
	}
	return names
}

func processWriteBlockSources(items *jnode) []processWriteNamedSource {
	sources := []processWriteNamedSource{}
	if !items.isArray() {
		return sources
	}
	for _, item := range items.items {
		if !item.isObject() {
			continue
		}
		name := strings.TrimSpace(processWriteNodeString(item.get("name")))
		if name == "" {
			continue
		}
		sources = append(sources, processWriteNamedSource{name: name, entity: strings.TrimSpace(processWriteNodeString(item.get("entitySchemaName")))})
	}
	return sources
}

func processWriteContains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func processWriteQuoted(values []string) string {
	quoted := make([]string, len(values))
	for index, value := range values {
		quoted[index] = "'" + value + "'"
	}
	return strings.Join(quoted, ", ")
}
