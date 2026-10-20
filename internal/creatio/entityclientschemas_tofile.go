package creatio

// list-entity-client-schemas-to-file: clio 8.1.0.142's ListEntityClientSchemasToFileTool over the inline
// lookup (ListEntityClientSchemas).

import (
	"bytes"
	"encoding/json"
)

// EntityClientSchemasToFileResult is clio's ListEntityClientSchemasToFileResponse.
type EntityClientSchemasToFileResult struct {
	Success    bool            `json:"success"`
	Entity     string          `json:"entity,omitempty"`
	EntityUID  string          `json:"entityUId,omitempty"`
	OutputFile string          `json:"output-file,omitempty"`
	Sections   *PageKindCounts `json:"sections,omitempty"`
	EditPages  *PageKindCounts `json:"editPages,omitempty"`
	Warnings   []string        `json:"warnings,omitempty"`
	Note       string          `json:"note,omitempty"`
	Error      string          `json:"error,omitempty"`
}

// PageKindCounts is clio's PageKindCounts: how many pages of one list are classic, freedom or unknown.
type PageKindCounts struct {
	Total   int `json:"total"`
	Classic int `json:"classic"`
	Freedom int `json:"freedom"`
	Unknown int `json:"unknown"`
}

// EntityClientSchemasToFileFailure is ListEntityClientSchemasToFileResponse.Failure; nothing was written.
func EntityClientSchemasToFileFailure(message string) EntityClientSchemasToFileResult {
	return EntityClientSchemasToFileResult{Error: message}
}

func pageKindCounts(kinds []string) *PageKindCounts {
	counts := &PageKindCounts{Total: len(kinds)}
	for _, kind := range kinds {
		switch kind {
		case "classic":
			counts.Classic++
		case "freedom":
			counts.Freedom++
		}
	}
	counts.Unknown = counts.Total - counts.Classic - counts.Freedom
	return counts
}

// EntityClientSchemasToFile is ListEntityClientSchemasToFileTool.ResolveToFile after the path was confined:
// the inline lookup's answer goes to path as clio serializes it (MCP serializer options: compact, nulls
// omitted, McpResultJsonEncoder escaping) and the caller gets the classic/freedom counts instead.
func EntityClientSchemasToFile(response EntityClientSchemasResult, path string) EntityClientSchemasToFileResult {
	if !response.Success {
		message := response.Error
		if message == "" {
			message = "list-entity-client-schemas returned no response"
		}
		return EntityClientSchemasToFileFailure(message)
	}
	sectionKinds := make([]string, 0, len(response.Sections))
	for _, section := range response.Sections {
		sectionKinds = append(sectionKinds, section.Kind)
	}
	editKinds := make([]string, 0, len(response.EditPages))
	for _, page := range response.EditPages {
		editKinds = append(editKinds, page.Kind)
	}
	summary := EntityClientSchemasToFileResult{
		Success: true, Entity: response.Entity, EntityUID: response.EntityUID, OutputFile: path,
		Sections: pageKindCounts(sectionKinds), EditPages: pageKindCounts(editKinds),
		Warnings: response.Warnings, Note: response.Note,
	}
	content, err := entityClientSchemasFileContent(response)
	if err != nil {
		return EntityClientSchemasToFileFailure(err.Error())
	}
	if refusal := ToFileWriteNew(path, content); refusal != "" {
		return EntityClientSchemasToFileFailure(refusal)
	}
	return summary
}

// entityClientSchemasFileContent writes ListEntityClientSchemasResponse as clio's MCP serializer does: every
// member in declaration order, a null one omitted. On success clio's sections and editPages are lists, so an
// empty one is written as [].
func entityClientSchemasFileContent(response EntityClientSchemasResult) ([]byte, error) {
	sections := response.Sections
	if sections == nil {
		sections = []MigrationSection{}
	}
	editPages := response.EditPages
	if editPages == nil {
		editPages = []MigrationEditPage{}
	}
	var note, failure *string
	if response.Note != "" {
		note = &response.Note
	}
	if response.Error != "" {
		failure = &response.Error
	}
	var warnings []string
	if len(response.Warnings) > 0 {
		warnings = response.Warnings
	}
	encoded, err := json.Marshal(struct {
		Success   bool                `json:"success"`
		Entity    string              `json:"entity"`
		EntityUID string              `json:"entityUId"`
		Sections  []MigrationSection  `json:"sections"`
		EditPages []MigrationEditPage `json:"editPages"`
		Warnings  []string            `json:"warnings,omitempty"`
		Note      *string             `json:"note,omitempty"`
		Error     *string             `json:"error,omitempty"`
	}{response.Success, response.Entity, response.EntityUID, sections, editPages, warnings, note, failure})
	if err != nil {
		return nil, err
	}
	node, err := parseJNode(encoded)
	if err != nil {
		return nil, err
	}
	var buffer bytes.Buffer
	writeMcpResultJSON(&buffer, node)
	return buffer.Bytes(), nil
}
