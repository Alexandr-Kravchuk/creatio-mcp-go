package creatio

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ProcessSignatureResponse is clio's get-process-signature envelope.
type ProcessSignatureResponse struct {
	Success                 bool                        `json:"success"`
	ProcessResolutionFailed bool                        `json:"processResolutionFailed"`
	ProcessCode             string                      `json:"processCode,omitempty"`
	ProcessCaption          string                      `json:"processCaption,omitempty"`
	ProcessID               string                      `json:"processId,omitempty"`
	Parameters              []ProcessSignatureParameter `json:"parameters"`
	Error                   string                      `json:"error,omitempty"`
}

type ProcessSignatureParameter struct {
	Name               string  `json:"name"`
	Caption            *string `json:"caption,omitempty"`
	ClrType            string  `json:"clrType"`
	DataValueTypeID    *string `json:"dataValueTypeId,omitempty"`
	Direction          string  `json:"direction"`
	IsLookup           bool    `json:"isLookup"`
	ReferenceSchemaUID *string `json:"referenceSchemaUId,omitempty"`
}

// processClrTypes is clio's DataValueTypeMap.Resolve rendered as .NET Type.FullName on net10.0.
var processClrTypes = map[string]string{
	"90b65bf8-0ffc-4141-8779-2420877af907": "System.Boolean",
	"325a73b8-0f47-44a0-8412-7606f78003ac": "System.String",
	"ddb3a1ee-07e8-4d62-b7a9-d0e618b00fbd": "System.String",
	"5ca35f10-a101-4c67-a96a-383da6afacfc": "System.String",
	"c0f04627-4620-4bc0-84e5-9419dc8516b1": "System.String",
	"8b3f29bb-ea14-4ce5-a5c5-293a929b6ba2": "System.String",
	"26cba63c-daf1-4f36-b2ea-73c0d675d90c": "System.String",
	"26cba64c-daf1-4f36-b2ea-73c0d695d90c": "System.String",
	"66cba64c-daf1-4f36-b8ea-73c0d695d90c": "System.String",
	"dafb71f9-ee9f-4e0b-a4d7-37aa15987155": "System.Drawing.Color",
	"b039feb0-ee7c-4884-8aa6-d6d45d84316f": "System.String",
	"fa6e6e49-b996-475e-a77e-73904e4c5a88": "System.String",
	"79bccffa-8c8b-4863-b376-a69d2244182b": "System.String",
	"3509b9dd-2c90-4540-b82e-8f6ae85d8248": "System.String",
	"ecbcce18-2a17-4ead-829a-9d02fa9578a4": "System.String",
	"6b6b74e2-820d-490e-a017-2b73d4ccf2b0": "System.Int32",
	"57ee4c31-5ec4-45fa-b95d-3a2868aa89a8": "System.Single",
	"07ba84ce-0bf7-44b4-9f2c-7b15032eb98c": "System.Single",
	"5cc8060d-6d10-4773-89fc-8c12d6f659a6": "System.Single",
	"3f62414e-6c25-4182-bcef-a73c9e396f31": "System.Single",
	"ff22e049-4d16-46ee-a529-92d8808932dc": "System.Single",
	"a4aaf398-3531-4a0d-9d75-a587f5b5b59e": "System.Single",
	"969093e2-2b4e-463b-883a-3d3b8c61f0cd": "System.Decimal",
	"b295071f-7ea9-4e62-8d1a-919bf3732ff2": "System.Guid",
	"23018567-a13c-4320-8687-fd6f9e3699bd": "System.Guid",
	"603d4960-a1a2-45e9-b232-206a54421b01": "System.DateOnly",
	"d21e9ef4-c064-4012-b286-fa1a8171da44": "System.DateTime",
	"04cc757b-8f06-482c-8a1a-0c0e171d2410": "System.TimeOnly",
	"651ec16f-d140-46db-b9e2-825c985a8ac2": "System.Collections.Generic.List`1[[System.Object, System.Private.CoreLib, Version=10.0.0.0, Culture=neutral, PublicKeyToken=7cec85d7bea7798e]]",
}

var processDirections = map[int]string{0: "Input", 1: "Output", 2: "Bidirectional", 3: "Internal"}

type processLibRow struct {
	ID              string
	Name            string
	Caption         *string
	VersionParentID string
	IsActiveVersion *bool
}

// GetProcessSignature resolves a process by code or caption through VwProcessLib, then reads its
// parameters through DataService ProcessSchemaRequest, as clio's ProcessModelGenerator does.
func (c *Client) GetProcessSignature(ctx context.Context, processName, culture string) ProcessSignatureResponse {
	if strings.TrimSpace(processName) == "" {
		return processSignatureError(false, "process-name is required")
	}
	if strings.TrimSpace(culture) == "" {
		culture = businessRuleDefaultCulture
	}
	process, resolutionFailed, err := c.resolveProcess(ctx, processName)
	if err != nil {
		return processSignatureError(resolutionFailed, err.Error())
	}
	body, _ := json.Marshal(map[string]any{"uId": process.ID, "convertLocalizableStringToParameter": true})
	payload, err := c.postDataServiceJSON(ctx, "ProcessSchemaRequest", body, 30*time.Second, maxResponseBytes)
	if err != nil {
		return processSignatureError(false, "GetProcessSchema - Error at step: ExecuteRequest. "+err.Error())
	}
	if strings.TrimSpace(string(payload)) == "" {
		return processSignatureError(false, "Generate - Empty response")
	}
	parameters, err := processSchemaParameters(payload, culture)
	if err != nil {
		return processSignatureError(false, "FromJson - Error deserializing process schema: "+err.Error())
	}
	caption := ""
	if process.Caption != nil {
		caption = *process.Caption
	}
	return ProcessSignatureResponse{
		Success: true, ProcessCode: process.Name, ProcessCaption: caption,
		ProcessID: process.ID, Parameters: parameters,
	}
}

func processSignatureError(resolutionFailed bool, message string) ProcessSignatureResponse {
	return ProcessSignatureResponse{ProcessResolutionFailed: resolutionFailed, Parameters: []ProcessSignatureParameter{}, Error: message}
}

func (c *Client) queryProcessLib(ctx context.Context, column, value string, rowCount int) ([]processLibRow, error) {
	rows, err := c.selectRows(ctx, buildSelectQuery("VwProcessLib", map[string]string{
		"Id": "Id", "Name": "Name", "Caption": "Caption", "VersionParentUId": "VersionParentUId", "IsActiveVersion": "IsActiveVersion",
	}, map[string]any{column: comparisonFilter(column, value, 1, 3)}, rowCount))
	if err != nil {
		return nil, err
	}
	result := make([]processLibRow, 0, len(rows))
	for _, row := range rows {
		item := processLibRow{
			ID: guidOrEmpty(rowString(row, "Id")), Name: rowString(row, "Name"), Caption: rowStringPointer(row, "Caption"),
			VersionParentID: guidOrEmpty(rowString(row, "VersionParentUId")),
		}
		var active bool
		if raw := row["IsActiveVersion"]; len(raw) > 0 && json.Unmarshal(raw, &active) == nil && string(raw) != "null" {
			item.IsActiveVersion = &active
		}
		result = append(result, item)
	}
	return result, nil
}

// resolveProcess is clio's GetProcessIdFromName plus ProcessLibResolver. The bool reports a NotFound or
// Conflict resolution failure (clio's processResolutionFailed).
func (c *Client) resolveProcess(ctx context.Context, nameOrCaption string) (processLibRow, bool, error) {
	byName, err := c.queryProcessLib(ctx, "Name", nameOrCaption, 1)
	if err != nil {
		return processLibRow{}, false, fmt.Errorf("GetProcessIdFromName - Error at step: QueryCreatio. %v", err)
	}
	var resolved processLibRow
	if len(byName) > 0 {
		resolved = byName[0]
	} else {
		byCaption, err := c.queryProcessLib(ctx, "Caption", nameOrCaption, -1)
		if err != nil {
			return processLibRow{}, false, fmt.Errorf("GetProcessIdFromName - Error at step: QueryCreatio. %v", err)
		}
		picked, err := resolveProcessByCaption(nameOrCaption, byCaption)
		if err != nil {
			return processLibRow{}, true, err
		}
		resolved = picked
	}
	if resolved.ID == emptyGUID || resolved.Caption == nil || strings.TrimSpace(*resolved.Caption) == "" {
		reason := "Empty Caption"
		if resolved.ID == emptyGUID {
			reason = "Empty Id"
		}
		return processLibRow{}, false, fmt.Errorf("GetProcessIdFromName - Error at step: QueryCreatio. Process '%s' has invalid data. %s", nameOrCaption, reason)
	}
	return resolved, false, nil
}

const processCandidatesNamed = 5

func resolveProcessByCaption(nameOrCaption string, matches []processLibRow) (processLibRow, error) {
	const code = "ResolveProcessByNameOrCaption - "
	if len(matches) == 0 {
		return processLibRow{}, fmt.Errorf("%sCould not find process with name or caption:%s", code, nameOrCaption)
	}
	families := []string{}
	familyKeyUnestablished := false
	for _, match := range matches {
		if match.VersionParentID == emptyGUID {
			familyKeyUnestablished = true
		}
		seen := false
		for _, family := range families {
			if family == match.VersionParentID {
				seen = true
				break
			}
		}
		if !seen {
			families = append(families, match.VersionParentID)
		}
	}
	oneFamily := len(families) == 1 && families[0] != emptyGUID
	active := []processLibRow{}
	for _, match := range matches {
		if match.IsActiveVersion != nil && *match.IsActiveVersion {
			active = append(active, match)
		}
	}
	if oneFamily && len(active) == 1 {
		return active[0], nil
	}
	if len(matches) == 1 && (matches[0].IsActiveVersion == nil || *matches[0].IsActiveVersion) {
		return matches[0], nil
	}
	candidates := processCandidates(matches)
	var reason string
	switch {
	case familyKeyUnestablished:
		reason = fmt.Sprintf("Caption '%s' matches %d schemas for which the process library established no version family key, so nothing shows whether they are one process or several: %s.", nameOrCaption, len(matches), candidates)
	case !oneFamily:
		reason = fmt.Sprintf("Multiple processes match caption '%s': %s.", nameOrCaption, candidates)
	case len(active) > 1:
		reason = fmt.Sprintf("Caption '%s' belongs to one process family, but the process library flags %d of its versions as active: %s.", nameOrCaption, len(active), candidates)
	case len(matches) == 1:
		reason = fmt.Sprintf("Caption '%s' matches only %s, which the process library reports is NOT the active version of its family.", nameOrCaption, candidates)
	default:
		reason = fmt.Sprintf("Caption '%s' belongs to one process family, but the process library established no active version for it: %s.", nameOrCaption, candidates)
	}
	return processLibRow{}, fmt.Errorf("%s%s Re-run with the exact process code.", code, reason)
}

func processCandidates(matches []processLibRow) string {
	listed := []string{}
	for index, match := range matches {
		if index >= processCandidatesNamed {
			break
		}
		caption := ""
		if match.Caption != nil {
			caption = *match.Caption
		}
		listed = append(listed, fmt.Sprintf("'%s' (code: %s)", caption, match.Name))
	}
	text := strings.Join(listed, "; ")
	if len(matches) > processCandidatesNamed {
		text += fmt.Sprintf("; and %d more", len(matches)-processCandidatesNamed)
	}
	return text
}

type processSchemaParameter struct {
	Name               string          `json:"name"`
	DataValueType      string          `json:"dataValueType"`
	ReferenceSchemaUID *string         `json:"referenceSchemaUId"`
	Direction          json.RawMessage `json:"direction"`
}

// processSchemaParameters reads the top-level parameters of the process metadata and their captions.
func processSchemaParameters(payload []byte, culture string) ([]ProcessSignatureParameter, error) {
	var response struct {
		Schema *struct {
			MetaData  string                     `json:"metaData"`
			Resources map[string]json.RawMessage `json:"resources"`
		} `json:"schema"`
	}
	if err := json.Unmarshal(payload, &response); err != nil {
		return nil, err
	}
	if response.Schema == nil {
		return nil, fmt.Errorf("the response has no schema")
	}
	var wrapper struct {
		MetaData *struct {
			Schema *struct {
				Parameters []processSchemaParameter `json:"parameters"`
			} `json:"schema"`
		} `json:"metaData"`
	}
	if strings.TrimSpace(response.Schema.MetaData) != "" {
		if err := json.Unmarshal([]byte(response.Schema.MetaData), &wrapper); err != nil {
			return nil, err
		}
	}
	parameters := []ProcessSignatureParameter{}
	if wrapper.MetaData == nil || wrapper.MetaData.Schema == nil {
		return parameters, nil
	}
	for _, parameter := range wrapper.MetaData.Schema.Parameters {
		dataValueType := normalizeGUID(parameter.DataValueType)
		item := ProcessSignatureParameter{Name: parameter.Name, ClrType: "System.Object"}
		if clrType, ok := processClrTypes[dataValueType]; ok {
			item.ClrType = clrType
		}
		if dataValueType != "" && dataValueType != emptyGUID {
			item.DataValueTypeID = &dataValueType
		}
		item.Direction = processDirection(parameter.Direction)
		if parameter.ReferenceSchemaUID != nil {
			if reference := normalizeGUID(*parameter.ReferenceSchemaUID); reference != "" && reference != emptyGUID {
				item.IsLookup = true
				item.ReferenceSchemaUID = &reference
			}
		}
		var captions map[string]string
		if raw := response.Schema.Resources["Parameters."+parameter.Name+".Caption"]; len(raw) > 0 && json.Unmarshal(raw, &captions) == nil {
			if caption, ok := captions[culture]; ok {
				item.Caption = &caption
			}
		}
		parameters = append(parameters, item)
	}
	return parameters, nil
}

// processDirection renders ProcessParameterDirection.ToString(); the wire value is a number or a name.
func processDirection(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return processDirections[0]
	}
	var number int
	if json.Unmarshal(raw, &number) == nil {
		if name, ok := processDirections[number]; ok {
			return name
		}
		return strconv.Itoa(number)
	}
	var name string
	if json.Unmarshal(raw, &name) == nil {
		for _, known := range processDirections {
			if strings.EqualFold(known, strings.TrimSpace(name)) {
				return known
			}
		}
		return name
	}
	return string(raw)
}
