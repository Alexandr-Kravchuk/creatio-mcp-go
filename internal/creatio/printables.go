package creatio

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
)

// msWordPrintableTypeID is the SysModuleReport.Type of MS Word printables, the only kind the Freedom UI print
// button offers.
const msWordPrintableTypeID = "8bc259ef-4276-4906-b7a6-23dc59be7fe2"

// PrintableItem names its fields after the crt.PrintablesRequest parameters they fill.
type PrintableItem struct {
	TemplateID             string  `json:"templateId"`
	PrintableCaption       string  `json:"printableCaption"`
	ConvertInPDF           bool    `json:"convertInPDF"`
	ShowInCard             bool    `json:"showInCard"`
	ShowInSection          bool    `json:"showInSection"`
	EntitySchemaName       *string `json:"entitySchemaName,omitempty"`
	ModuleEntitySchemaName *string `json:"moduleEntitySchemaName,omitempty"`
}

type PrintableListResult struct {
	Success    bool            `json:"success"`
	Error      string          `json:"error,omitempty"`
	Count      int             `json:"count"`
	Printables []PrintableItem `json:"printables"`
}

// ListPrintables reads every MS Word printable from SysModuleReport and matches entityName in memory against the
// printable's own entity or its section module's entity, as the Freedom UI printables service does. The match is
// case-insensitive; the result is ordered by caption.
func (c *Client) ListPrintables(ctx context.Context, entityName string) PrintableListResult {
	rows, err := c.selectRows(ctx, buildSelectQuery("SysModuleReport", map[string]string{
		"Id": "Id", "Caption": "Caption", "ConvertInPDF": "ConvertInPDF", "ShowInCard": "ShowInCard",
		"ShowInSection": "ShowInSection", "SysEntitySchemaName": "SysEntitySchema.Name",
		"SysModuleEntityName": "SysModule.SysModuleEntity.[SysSchema:UId:SysEntitySchemaUId].Name",
	}, map[string]any{"filter0": comparisonFilter("Type.Id", msWordPrintableTypeID, 0, 3)}, 10000))
	if err != nil {
		return PrintableListResult{Error: err.Error(), Printables: []PrintableItem{}}
	}
	wanted := strings.TrimSpace(entityName)
	printables := make([]PrintableItem, 0, len(rows))
	for _, row := range rows {
		entity, module := rowString(row, "SysEntitySchemaName"), rowString(row, "SysModuleEntityName")
		if wanted != "" && !strings.EqualFold(entity, wanted) && !strings.EqualFold(module, wanted) {
			continue
		}
		printables = append(printables, PrintableItem{
			TemplateID: rowString(row, "Id"), PrintableCaption: rowString(row, "Caption"),
			ConvertInPDF: printableRowBool(row, "ConvertInPDF"), ShowInCard: printableRowBool(row, "ShowInCard"),
			ShowInSection:    printableRowBool(row, "ShowInSection"),
			EntitySchemaName: printableNonBlank(entity), ModuleEntitySchemaName: printableNonBlank(module),
		})
	}
	sort.SliceStable(printables, func(i, j int) bool {
		return compareOrdinalIgnoreCase(printables[i].PrintableCaption, printables[j].PrintableCaption) < 0
	})
	return PrintableListResult{Success: true, Count: len(printables), Printables: printables}
}

func printableRowBool(row map[string]json.RawMessage, key string) bool {
	var value bool
	_ = json.Unmarshal(row[key], &value)
	return value
}

func printableNonBlank(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return &value
}
