package creatio

// The payload mirrors the observable request shape needed for SysInstalledApp, not C# source.
func selectQuery() map[string]any {
	column := func(path string) map[string]any {
		return map[string]any{"expression": map[string]any{"expressionType": 0, "columnPath": path}, "orderDirection": 0, "orderPosition": -1, "isVisible": true}
	}
	return map[string]any{
		"rootSchemaName": "SysInstalledApp", "operationType": 0, "allColumns": false, "isDistinct": false,
		"ignoreDisplayValues": false, "rowCount": -1, "rowsOffset": -1, "isPageable": false,
		"columns": map[string]any{"items": map[string]any{"Id": column("Id"), "Code": column("Code"), "Name": column("Name"), "Version": column("Version"), "Description": column("Description")}},
		"filters": map[string]any{"filterType": 6, "isEnabled": true, "trimDateTimeParameterToDate": false, "logicalOperation": 0, "items": map[string]any{}},
		"__type": "Terrasoft.Nui.ServiceModel.DataContract.SelectQuery", "queryKind": 0,
	}
}

type selectResponse struct { Success bool `json:"success"`; ErrorInfo errorInfo `json:"errorInfo"`; Rows []appRow `json:"rows"` }
type errorInfo struct { Message string `json:"message"` }
type appRow struct { ID string `json:"Id"`; Name string `json:"Name"`; Code string `json:"Code"`; Version string `json:"Version"`; Description string `json:"Description"` }
