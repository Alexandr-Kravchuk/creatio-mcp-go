package creatio

// The answers of clio's entity-schema write tools: the command envelope with its optional Data Forge
// enrichment and compile note (CommandExecutionResult), and the enrichment itself
// (DataForgeEnrichmentBuilder over the dataforge-context aggregation).

import (
	"context"
	"sort"
	"strconv"
	"strings"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/redact"
)

// schemaWriteEntCompileNote is CommandExecutionResult.CompileNotRequiredNote.
const schemaWriteEntCompileNote = "compile-creatio not required"

// SchemaWriteEntResult is clio's CommandExecutionResult as these tools return it.
type SchemaWriteEntResult struct {
	ExitCode  int                      `json:"exit-code"`
	Messages  []LogMessage             `json:"execution-log-messages"`
	DataForge *SchemaWriteEntDataForge `json:"dataforge,omitempty"`
	Note      string                   `json:"note,omitempty"`
}

// SchemaWriteEntFailure is the envelope of a failure raised before the command ran: exit 1, one Error.
func SchemaWriteEntFailure(message string, dataForge *SchemaWriteEntDataForge) SchemaWriteEntResult {
	return SchemaWriteEntResult{ExitCode: 1, Messages: []LogMessage{{MessageType: "Error", Value: message}}, DataForge: dataForge}
}

// SchemaWriteEntDataForge is clio's ApplicationDataForgeResult.
type SchemaWriteEntDataForge struct {
	Used           bool                        `json:"used"`
	Health         *DataForgeHealth            `json:"health,omitempty"`
	Status         *DataForgeMaintenanceStatus `json:"status,omitempty"`
	Coverage       DataForgeCoverage           `json:"coverage"`
	Warnings       []string                    `json:"warnings"`
	ContextSummary schemaWriteEntDataForgeSum  `json:"context-summary"`
}

type schemaWriteEntDataForgeSum struct {
	SimilarTables  []DataForgeSimilarTable     `json:"similar-tables"`
	SimilarLookups []DataForgeSimilarLookup    `json:"similar-lookups"`
	RelationPairs  []string                    `json:"relation-pairs"`
	ColumnHints    []schemaWriteEntDataForgeCH `json:"column-hints"`
}

type schemaWriteEntDataForgeCH struct {
	TableName           string `json:"table-name"`
	ColumnCount         int    `json:"column-count"`
	RequiredColumnCount int    `json:"required-column-count"`
	LookupColumnCount   int    `json:"lookup-column-count"`
}

// SchemaWriteEntDataForgeFailure is the degraded enrichment clio attaches when Data Forge cannot be asked at
// all: a warning that names the failure, empty coverage and an empty summary.
func SchemaWriteEntDataForgeFailure(message string) *SchemaWriteEntDataForge {
	return &SchemaWriteEntDataForge{Used: true, Warnings: []string{"dataforge:" + redact.Text(message)},
		ContextSummary: schemaWriteEntDataForgeSum{SimilarTables: []DataForgeSimilarTable{},
			SimilarLookups: []DataForgeSimilarLookup{}, RelationPairs: []string{}, ColumnHints: []schemaWriteEntDataForgeCH{}}}
}

// schemaWriteEntDistinctTerms is the candidate-term list clio builds: blank terms dropped, trimmed,
// distinct ignoring case, first spelling kept.
func schemaWriteEntDistinctTerms(terms ...string) []string {
	seen := map[string]bool{}
	result := []string{}
	for _, term := range terms {
		trimmed := strings.TrimSpace(term)
		if trimmed == "" || seen[strings.ToLower(trimmed)] {
			continue
		}
		seen[strings.ToLower(trimmed)] = true
		result = append(result, trimmed)
	}
	return result
}

// SchemaWriteEntEnrich is DataForgeEnrichmentBuilder.Build: the dataforge-context aggregation for the terms,
// projected to the compact application shape; any failure becomes a "dataforge:" warning.
func (c *Client) SchemaWriteEntEnrich(ctx context.Context, terms, lookupHints []string) *SchemaWriteEntDataForge {
	summary := ""
	if len(terms) > 0 {
		summary = terms[0]
	}
	pointers := func(values []string) []*string {
		result := make([]*string, 0, len(values))
		for index := range values {
			value := values[index]
			result = append(result, &value)
		}
		return result
	}
	context := c.DataForgeContext(ctx, DataForgeContextRequest{RequirementSummary: summary,
		CandidateTerms: pointers(terms), LookupHints: pointers(lookupHints)})
	if !context.Success {
		message := "Data Forge context failed."
		if context.Error != nil {
			message = context.Error.Message
		}
		return SchemaWriteEntDataForgeFailure(message)
	}
	result := &SchemaWriteEntDataForge{Used: true, Health: context.Health, Status: context.Status,
		Coverage: context.Coverage, Warnings: context.Warnings}
	if result.Warnings == nil {
		result.Warnings = []string{}
	}
	result.ContextSummary = schemaWriteEntDataForgeSum{SimilarTables: context.SimilarTables, SimilarLookups: context.SimilarLookups,
		RelationPairs: []string{}, ColumnHints: []schemaWriteEntDataForgeCH{}}
	if result.ContextSummary.SimilarTables == nil {
		result.ContextSummary.SimilarTables = []DataForgeSimilarTable{}
	}
	if result.ContextSummary.SimilarLookups == nil {
		result.ContextSummary.SimilarLookups = []DataForgeSimilarLookup{}
	}
	if context.Relations != nil {
		pairs := append([]string{}, context.Relations.keys...)
		sort.SliceStable(pairs, func(i, j int) bool { return compareOrdinalIgnoreCase(pairs[i], pairs[j]) < 0 })
		result.ContextSummary.RelationPairs = pairs
	}
	if context.Columns != nil {
		tables := append([]string{}, context.Columns.keys...)
		sort.SliceStable(tables, func(i, j int) bool { return compareOrdinalIgnoreCase(tables[i], tables[j]) < 0 })
		for _, table := range tables {
			columns, _ := context.Columns.values[table].([]DataForgeColumn)
			hint := schemaWriteEntDataForgeCH{TableName: table, ColumnCount: len(columns)}
			for _, column := range columns {
				if column.Required {
					hint.RequiredColumnCount++
				}
				if column.ReferenceSchemaName != nil && strings.TrimSpace(*column.ReferenceSchemaName) != "" {
					hint.LookupColumnCount++
				}
			}
			result.ContextSummary.ColumnHints = append(result.ContextSummary.ColumnHints, hint)
		}
	}
	return result
}

// schemaWriteEntRedact is SensitiveErrorTextRedactor.Redact, which the tools apply to a failure they
// catch themselves.
func schemaWriteEntRedact(text string) string { return redact.Text(text) }

func schemaWriteEntSortColumns(columns []schemaWriteEntRuntimeColumn, less func(i, j int) bool) {
	sort.SliceStable(columns, less)
}

// SchemaWriteEntCreatePrecheck returns the failure create-entity-schema or create-lookup raises while
// building its options, before the environment is used, or "".
func SchemaWriteEntCreatePrecheck(args SchemaWriteEntCreateArgs, lookup bool) string {
	var err error
	if lookup {
		if message := SchemaWriteEntLookupShadowError(args.Columns); message != "" {
			return message
		}
		var options schemaWriteEntCreateOptions
		if options, err = schemaWriteEntBuildCreateOptions(args, schemaWriteEntBaseLookup, false, false, nil); err == nil {
			_, err = schemaWriteEntDefaultTitle(options.titleLocalizations, "Lookup '"+args.SchemaName+"'")
		}
	} else {
		_, err = schemaWriteEntBuildCreateOptions(args, args.ParentSchemaName, args.ExtendParent, args.IsVirtual, args.IsDBView)
	}
	if err != nil {
		return schemaWriteEntRedact(err.Error())
	}
	return ""
}

// UpdateEntitySchemaPrecheck returns the failure update-entity-schema raises while serializing its
// operations, or "".
func UpdateEntitySchemaPrecheck(args UpdateEntitySchemaArgs) string {
	for index, operation := range args.Operations {
		context := "Schema '" + args.SchemaName + "' operation #" + strconv.Itoa(index+1)
		if _, err := schemaWriteEntOperationPayload(operation, context); err != nil {
			return schemaWriteEntRedact(err.Error())
		}
	}
	return ""
}

// ModifyEntitySchemaColumnPrecheck returns the failure modify-entity-schema-column raises while building
// its options, before the environment is used, or "".
func ModifyEntitySchemaColumnPrecheck(args ModifyEntitySchemaColumnArgs) string {
	operation := args.Operation
	columnName, err := schemaWriteEntColumnIdentity(operation.resolveColumnName(), "modify-entity-schema-column", "args")
	if err != nil {
		return schemaWriteEntRedact(err.Error())
	}
	context := "Column '" + columnName + "' action '" + operation.action() + "'"
	titles, err := schemaWriteEntMutationTitles(operation.action(), operation.TitleLocalizations, schemaWriteEntDeref(operation.Title),
		schemaWriteEntDeref(operation.Caption), columnName, context)
	if err == nil {
		_, err = schemaWriteEntNormalizeTitles(titles, "", schemaWriteEntTitleField, "")
	}
	if err == nil {
		_, err = schemaWriteEntMutationDescriptions(operation.action(), operation.DescriptionLocalizations, schemaWriteEntDeref(operation.Description), context)
	}
	if err != nil {
		return schemaWriteEntRedact(err.Error())
	}
	return ""
}
