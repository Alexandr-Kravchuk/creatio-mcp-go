package creatio

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// classicPageSectionNames is clio's ClassicSectionSchemaResolver: the entity's base SysSchema UId, the
// SysModule rows that bind it, then the section schema names of those rows. The second value is the failure.
func (c *Client) classicPageSectionNames(ctx context.Context, entity string) ([]string, string) {
	entityRows, err := c.selectRows(ctx, entityQuery("SysSchema", map[string]string{"UId": "UId", "ExtendParent": "ExtendParent"},
		map[string]any{"byName": eqFilter("Name", entity, 1), "byManager": eqFilter("ManagerName", "EntitySchemaManager", 1)}, entityRowCount))
	if err != nil {
		return []string{}, err.Error()
	}
	entityUID, failure := resolveEntityUID(entity, entityRows)
	if entityUID == "" {
		return []string{}, failure
	}
	moduleRows, err := c.selectRows(ctx, entityQuery("SysModule", map[string]string{"SectionSchemaUId": "SectionSchemaUId"},
		map[string]any{"byEntity": eqFilter("SysModuleEntity.SysEntitySchemaUId", entityUID, 0)}, sectionRowCount))
	if err != nil {
		return []string{}, err.Error()
	}
	sectionUIDs := []string{}
	for _, row := range moduleRows {
		sectionUIDs = append(sectionUIDs, rowText(row, "SectionSchemaUId"))
	}
	sectionUIDs = distinctGUIDText(sectionUIDs)
	if len(sectionUIDs) == 0 {
		return []string{}, ""
	}
	nameByUID, err := c.classicPageSchemaNames(ctx, sectionUIDs)
	if err != nil {
		return []string{}, err.Error()
	}
	names := []string{}
	for _, uid := range sectionUIDs {
		if name, ok := nameByUID[strings.ToLower(uid)]; ok {
			names = append(names, name)
		}
	}
	return classicPageDistinct(names), ""
}

// classicPageSchemaNames maps lower-case schema UIds to names, in chunked In-filter reads.
func (c *Client) classicPageSchemaNames(ctx context.Context, uids []string) (map[string]string, error) {
	names := map[string]string{}
	for offset := 0; offset < len(uids); offset += lookupIDsPerQuery {
		chunk := uids[offset:min(offset+lookupIDsPerQuery, len(uids))]
		rows, err := c.selectRows(ctx, entityQuery("SysSchema", map[string]string{"UId": "UId", "Name": "Name"},
			map[string]any{"byUId": inFilter("UId", chunk, 0)}, len(chunk)))
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			uid, name := rowText(row, "UId"), rowText(row, "Name")
			if strings.TrimSpace(uid) != "" && strings.TrimSpace(name) != "" {
				names[strings.ToLower(uid)] = name
			}
		}
	}
	return names, nil
}

type classicPageChildPage struct {
	entityName, schemaName string
	isMiniPage             bool
}

type classicPageChildLookup struct {
	pages    []classicPageChildPage
	warnings []string
	resolved []string
}

// classicPageChildPages is clio's ClassicDetailEditPageResolver: the SysModuleEdit card and add mini page of
// each entity, in DataService row order.
func (c *Client) classicPageChildPages(ctx context.Context, entityNames []string) (classicPageChildLookup, error) {
	lookup := classicPageChildLookup{}
	names := []string{}
	for _, name := range classicPageDistinct(entityNames) {
		if strings.TrimSpace(name) != "" {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return lookup, nil
	}
	entityRows := []map[string]json.RawMessage{}
	for offset := 0; offset < len(names); offset += lookupIDsPerQuery {
		chunk := names[offset:min(offset+lookupIDsPerQuery, len(names))]
		rows, err := c.selectRows(ctx, entityQuery("SysSchema", map[string]string{"Name": "Name", "UId": "UId", "ExtendParent": "ExtendParent"},
			map[string]any{"byName": inFilter("Name", chunk, 1), "byManager": eqFilter("ManagerName", "EntitySchemaManager", 1)},
			len(chunk)*entityRowCount))
		if err != nil {
			return lookup, err
		}
		entityRows = append(entityRows, rows...)
	}
	if len(entityRows) >= len(names)*entityRowCount {
		lookup.warnings = append(lookup.warnings, fmt.Sprintf("Detail-entity lookup reached the rowCount cap (%d); some detail entities may be unresolved.", len(names)*entityRowCount))
	}
	// Group rows by entity name in first-seen order, then resolve each group's base row.
	groupOrder := []string{}
	groups := map[string][]map[string]json.RawMessage{}
	groupName := map[string]string{}
	for _, row := range entityRows {
		name := rowText(row, "Name")
		key := strings.ToLower(name)
		if _, seen := groups[key]; !seen {
			groupOrder = append(groupOrder, key)
			groupName[key] = name
		}
		groups[key] = append(groups[key], row)
	}
	uidByEntity := map[string]string{}
	entityOrder := []string{}
	for _, key := range groupOrder {
		if strings.TrimSpace(groupName[key]) == "" {
			continue
		}
		if uid, _ := resolveEntityUID(groupName[key], groups[key]); uid != "" {
			uidByEntity[key] = uid
			entityOrder = append(entityOrder, groupName[key])
		}
	}
	unresolved := []string{}
	for _, name := range names {
		if _, ok := uidByEntity[strings.ToLower(name)]; !ok {
			unresolved = append(unresolved, name)
		}
	}
	if len(unresolved) > 0 {
		noun := "entities"
		if len(unresolved) == 1 {
			noun = "entity"
		}
		lookup.warnings = append(lookup.warnings, "No entity metadata resolved for detail "+noun+" "+strings.Join(unresolved, ", ")+
			"; their child pages could not be looked up, which is NOT the same as 'they have none'.")
	}
	lookup.resolved = entityOrder
	if len(uidByEntity) == 0 {
		return lookup, nil
	}
	entityUIDs := []string{}
	entityByUID := map[string]string{}
	for _, name := range entityOrder {
		uid := uidByEntity[strings.ToLower(name)]
		entityByUID[strings.ToLower(uid)] = name
		entityUIDs = append(entityUIDs, uid)
	}
	entityUIDs = classicPageDistinct(entityUIDs)
	editRows := []map[string]json.RawMessage{}
	for offset := 0; offset < len(entityUIDs); offset += lookupIDsPerQuery {
		chunk := entityUIDs[offset:min(offset+lookupIDsPerQuery, len(entityUIDs))]
		rows, err := c.selectRows(ctx, entityQuery("SysModuleEdit", map[string]string{
			"SysEntitySchemaUId": "SysModuleEntity.SysEntitySchemaUId", "CardSchemaUId": "CardSchemaUId", "MiniPageSchemaUId": "MiniPageSchemaUId",
		}, map[string]any{"byEntity": inFilter("SysModuleEntity.SysEntitySchemaUId", chunk, 0)}, len(chunk)*classicPageEditRowsPerEnt))
		if err != nil {
			return lookup, err
		}
		editRows = append(editRows, rows...)
	}
	if editCap := len(entityUIDs) * classicPageEditRowsPerEnt; len(editRows) >= editCap {
		lookup.warnings = append(lookup.warnings, fmt.Sprintf("Child-page lookup reached the rowCount cap (%d); the child-page list may be truncated.", editCap))
	}
	if len(editRows) == 0 {
		return lookup, nil
	}
	pageUIDs := []string{}
	for _, row := range editRows {
		pageUIDs = append(pageUIDs, rowText(row, "CardSchemaUId"), rowText(row, "MiniPageSchemaUId"))
	}
	nameByUID, err := c.classicPageSchemaNames(ctx, distinctGUIDText(pageUIDs))
	if err != nil {
		return lookup, err
	}
	seen := map[string]bool{}
	for _, row := range editRows {
		entityName, ok := entityByUID[strings.ToLower(rowText(row, "SysEntitySchemaUId"))]
		if !ok || !classicPageRealReference(rowText(row, "SysEntitySchemaUId")) {
			continue
		}
		for _, column := range []struct {
			name string
			mini bool
		}{{"CardSchemaUId", false}, {"MiniPageSchemaUId", true}} {
			uid := rowText(row, column.name)
			schemaName, known := nameByUID[strings.ToLower(uid)]
			if !classicPageRealReference(uid) || !known {
				continue
			}
			if key := strings.ToLower(entityName + "|" + schemaName); !seen[key] {
				seen[key] = true
				lookup.pages = append(lookup.pages, classicPageChildPage{entityName: entityName, schemaName: schemaName, isMiniPage: column.mini})
			}
		}
	}
	return lookup, nil
}

func classicPageRealReference(uid string) bool {
	return strings.TrimSpace(uid) != "" && !strings.EqualFold(uid, emptyGUID)
}

// classicPageEnumNames are the enum tables echoed from the stand's own sysenums.js.
var classicPageEnumNames = []string{"ViewItemType", "ContentType", "DataValueType"}

var classicPageBlockedMembers = classicPageOrdinalSet("__proto__", "constructor", "prototype", "toString", "toLocaleString",
	"valueOf", "hasOwnProperty", "isPrototypeOf", "propertyIsEnumerable")

func classicPageOrdinalSet(values ...string) map[string]bool {
	set := map[string]bool{}
	for _, value := range values {
		set[value] = true
	}
	return set
}

var (
	classicPageContentHash = regexp.MustCompile(`(?i)((?:/0)?)/core/([0-9a-f]{32}|hash)/`)
	// clio's member pattern is (?<![\w$])name\s*:\s*(-?\d+)\s*(?=,|\}|$); the look-behind and look-ahead are
	// checked by hand in classicPageEnumMembers, because Go's regexp has neither.
	classicPageEnumMember = regexp.MustCompile(`([A-Za-z_$][\w$]*)\s*:\s*(-?\d+)\s*`)
)

// enumVocabulary reads the stand's sysenums.js the way clio's ClassicEnumVocabularyResolver does: the login
// page names the content-hash path of the static files, and the three enum tables are parsed from the file.
func (r *classicPageRun) enumVocabulary(ctx context.Context) *jnode {
	vocabulary := newObject()
	enums, warnings := r.client.classicPageEnumTables(ctx)
	for _, warning := range warnings {
		r.warn(warning)
	}
	for _, table := range enums {
		members := newObject()
		for _, member := range table.members {
			members.set(member.name, &jnode{kind: jkInteger, text: strconv.FormatInt(member.value, 10)})
		}
		vocabulary.set(table.name, members)
	}
	return vocabulary
}

type classicPageEnumTable struct {
	name    string
	members []classicPageEnumMemberValue
}

type classicPageEnumMemberValue struct {
	name  string
	value int64
}

func (c *Client) classicPageEnumTables(ctx context.Context) ([]classicPageEnumTable, []string) {
	baseURI := strings.TrimRight(c.config.BaseURL, "/")
	if strings.TrimSpace(baseURI) == "" {
		return nil, []string{"The environment URI is not configured; enumVocabulary omitted."}
	}
	candidates := []string{baseURI + "/0/Login/NuiLogin.aspx", baseURI + "/Login/NuiLogin.aspx", baseURI + "/Login/Login.html"}
	if c.config.IsNetCore {
		candidates = []string{baseURI + "/Login/Login.html", baseURI + "/Login/NuiLogin.aspx", baseURI + "/0/Login/NuiLogin.aspx"}
	}
	attempts := []string{}
	sysEnumsURL := ""
	for _, loginPageURL := range candidates {
		page, failure := c.classicPageGet(ctx, loginPageURL)
		if failure != "" {
			attempts = append(attempts, fmt.Sprintf("'%s' -> %s", loginPageURL, failure))
			continue
		}
		match := classicPageContentHash.FindStringSubmatch(page)
		if match == nil {
			attempts = append(attempts, fmt.Sprintf("'%s' -> served no '/core/<hash>/' content-hash marker", loginPageURL))
			continue
		}
		root := match[1]
		if root == "" && strings.Contains(loginPageURL, "/0/Login/") {
			root = "/0"
		}
		sysEnumsURL = baseURI + root + "/core/" + match[2] + "/Terrasoft/core/enums/sysenums.js"
		break
	}
	if sysEnumsURL == "" {
		return nil, []string{"Could not read the login page's '/core/<hash>/' content-hash marker from any known location (" +
			strings.Join(attempts, "; ") + "); enumVocabulary omitted."}
	}
	content, failure := c.classicPageGet(ctx, sysEnumsURL)
	if failure != "" {
		return nil, []string{fmt.Sprintf("Could not fetch sysenums.js from '%s': %s; enumVocabulary omitted.", sysEnumsURL, failure)}
	}
	return classicPageParseEnums(content)
}

// classicPageGet is an anonymous GET of a static page, as clio's named HttpClient makes it.
func (c *Client) classicPageGet(ctx context.Context, url string) (string, string) {
	requestCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, url, nil)
	if err != nil {
		return "", err.Error()
	}
	client := &http.Client{Transport: c.requestClient().Transport, Timeout: 45 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return "", err.Error()
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", response.Status
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if err != nil {
		return "", err.Error()
	}
	return string(payload), ""
}

// classicPageParseEnums is clio's ClassicEnumVocabularySourceParser.
func classicPageParseEnums(content string) ([]classicPageEnumTable, []string) {
	warnings := []string{}
	if strings.TrimSpace(content) == "" {
		return nil, []string{"sysenums.js content is empty; enumVocabulary omitted."}
	}
	scannable := classicPageBlankCommentsAndStrings([]rune(content))
	tables := []classicPageEnumTable{}
	for _, name := range classicPageEnumNames {
		block := classicPageEnumBlock(scannable, name)
		if block == nil {
			warnings = append(warnings, fmt.Sprintf("Could not find a complete 'Terrasoft.%s = { ... }' block in sysenums.js; '%s' omitted from enumVocabulary.", name, name))
			continue
		}
		members := classicPageEnumMembers(string(block))
		if len(members) == 0 {
			warnings = append(warnings, fmt.Sprintf("'Terrasoft.%s' block carried no numeric members; '%s' omitted from enumVocabulary.", name, name))
			continue
		}
		tables = append(tables, classicPageEnumTable{name: name, members: members})
	}
	return tables, warnings
}

func classicPageIdentifierRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '$'
}

func classicPageEnumBlock(content []rune, enumName string) []rune {
	marker := []rune("Terrasoft." + enumName)
	for searchFrom := 0; ; {
		index := classicPageRuneIndex(content, marker, searchFrom)
		if index < 0 {
			return nil
		}
		after := index + len(marker)
		if index > 0 && classicPageIdentifierRune(content[index-1]) {
			searchFrom = index + 1
			continue
		}
		if after < len(content) && classicPageIdentifierRune(content[after]) {
			searchFrom = after
			continue
		}
		cursor := after
		for cursor < len(content) && unicode.IsSpace(content[cursor]) {
			cursor++
		}
		if cursor >= len(content) || content[cursor] != '=' {
			searchFrom = after
			continue
		}
		for cursor++; cursor < len(content) && unicode.IsSpace(content[cursor]); cursor++ {
		}
		if cursor >= len(content) || content[cursor] != '{' {
			searchFrom = after
			continue
		}
		depth := 0
		for end := cursor; end < len(content); end++ {
			switch content[end] {
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					return content[cursor : end+1]
				}
			}
		}
		return nil
	}
}

func classicPageRuneIndex(content, marker []rune, from int) int {
	for index := from; index+len(marker) <= len(content); index++ {
		matched := true
		for offset, r := range marker {
			if content[index+offset] != r {
				matched = false
				break
			}
		}
		if matched {
			return index
		}
	}
	return -1
}

// classicPageEnumMembers reads name: integer members; a later duplicate overwrites the value in place.
func classicPageEnumMembers(block string) []classicPageEnumMemberValue {
	members := []classicPageEnumMemberValue{}
	position := map[string]int{}
	for _, match := range classicPageEnumMember.FindAllStringSubmatchIndex(block, -1) {
		start, end := match[0], match[1]
		if start > 0 {
			previous := []rune(block[:start])
			if last := previous[len(previous)-1]; unicode.IsLetter(last) || unicode.IsDigit(last) || last == '_' || last == '$' {
				continue
			}
		}
		if end < len(block) && block[end] != ',' && block[end] != '}' {
			continue
		}
		name := block[match[2]:match[3]]
		if classicPageBlockedMembers[name] {
			continue
		}
		value, err := strconv.ParseInt(block[match[4]:match[5]], 10, 64)
		if err != nil {
			continue
		}
		if index, seen := position[name]; seen {
			members[index].value = value
			continue
		}
		position[name] = len(members)
		members = append(members, classicPageEnumMemberValue{name: name, value: value})
	}
	return members
}

// classicPageBlankCommentsAndStrings replaces comments and string literals with spaces, keeping offsets.
func classicPageBlankCommentsAndStrings(text []rune) []rune {
	blanked := make([]rune, 0, len(text))
	for index := 0; index < len(text); {
		end := -1
		switch ch := text[index]; {
		case ch == '/' && index+1 < len(text) && text[index+1] == '*':
			end = len(text)
			for scan := index + 2; scan+1 < len(text); scan++ {
				if text[scan] == '*' && text[scan+1] == '/' {
					end = scan + 2
					break
				}
			}
		case ch == '/' && index+1 < len(text) && text[index+1] == '/':
			end = len(text)
			for scan := index + 2; scan < len(text); scan++ {
				if text[scan] == '\n' {
					end = scan
					break
				}
			}
		case ch == '\'' || ch == '"':
			scan := index + 1
			for scan < len(text) && text[scan] != ch {
				if text[scan] == '\\' && scan+1 < len(text) {
					scan += 2
				} else {
					scan++
				}
			}
			end = min(scan+1, len(text))
		}
		if end < 0 {
			blanked = append(blanked, text[index])
			index++
			continue
		}
		for ; index < end; index++ {
			blanked = append(blanked, ' ')
		}
	}
	return blanked
}
