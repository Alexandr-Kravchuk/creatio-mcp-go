package creatio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// ProcessModelRequest is clio's GenerateProcessModelCommandOptions after the MCP tool applied its defaults
// (destination ".", namespace "AtfTIDE.ProcessModels", culture "en-US").
type ProcessModelRequest struct {
	Code            string
	DestinationPath string
	Namespace       string
	Culture         string
}

// processWriteModelParameter is the part of clio's ProcessParameter the generated file uses.
type processWriteModelParameter struct {
	Name           string
	DataValueType  string
	Direction      int
	Captions       map[string]string
	Descriptions   map[string]string
	ItemProperties []processWriteModelParameter
}

// processWriteCompositeListType is DataValueTypeMap.CompositeObjectListDataValueTypeUId.
const processWriteCompositeListType = "651ec16f-d140-46db-b9e2-825c985a8ac2"

// processWriteTypeText is DataValueTypeMap.Resolve rendered as Type.ToString(), which is what clio's string
// interpolation writes into the generated file. It differs from FullName only for the generic list.
func processWriteTypeText(dataValueType string) string {
	if dataValueType == processWriteCompositeListType {
		return "System.Collections.Generic.List`1[System.Object]"
	}
	if clrType, ok := processClrTypes[dataValueType]; ok {
		return clrType
	}
	return "System.Object"
}

// GenerateProcessModel is clio's generate-process-model: resolve the process by code or caption through
// VwProcessLib, read its schema through DataService ProcessSchemaRequest, and write the ATF.Repository C#
// model file. Like clio's MCP envelope the success log is empty; a failure is "<code> - <description>".
func (c *Client) GenerateProcessModel(ctx context.Context, request ProcessModelRequest) CommandResult {
	process, _, err := c.resolveProcess(ctx, request.Code)
	if err != nil {
		return CommandFailure(err.Error())
	}
	payload, err := c.postDataServiceJSON(ctx, "ProcessSchemaRequest", processWriteSchemaRequestBody(process.ID), 10*time.Second, maxResponseBytes)
	if err != nil {
		return CommandFailure("GetProcessSchema - Error at step: ExecuteRequest. " + err.Error())
	}
	if strings.TrimSpace(string(payload)) == "" {
		return CommandFailure("Generate - Empty response")
	}
	description, parameters, err := processWriteParseSchema(payload, request.Culture)
	if err != nil {
		return CommandFailure("FromJson - Error deserializing process schema: " + err.Error())
	}
	if parameters == nil {
		// clio's writer enumerates a null parameter list and fails with the runtime's text, which the tool
		// reports as a caller-actionable failure.
		return CommandFailure("Object reference not set to an instance of an object.")
	}
	content := processWriteModelFile(process.Name, description, parameters, request.Namespace, request.Culture)
	if err := processWriteModelSave(process.Name, content, request.DestinationPath); err != nil {
		return CommandFailure(err.Error())
	}
	return CommandResult{ExitCode: 0, Messages: []LogMessage{}}
}

// processWriteSchemaRequestBody is clio's ProcessSchemaRequest.ToString(): indented, packageUId left out.
func processWriteSchemaRequestBody(processID string) []byte {
	return []byte("{\n  \"uId\": \"" + processID + "\",\n  \"convertLocalizableStringToParameter\": true\n}")
}

// processWriteParseSchema reads the schema description and the parameters with their captions and
// descriptions (resources "Parameters.<name>.Caption" / ".Sys_Description", and for an item of a composite
// list "Parameters.<list>.<name>.*"). A nil parameter list means the metadata carried none.
func processWriteParseSchema(payload []byte, culture string) (string, []processWriteModelParameter, error) {
	var response struct {
		Schema *struct {
			MetaData    string                     `json:"metaData"`
			Resources   map[string]json.RawMessage `json:"resources"`
			Description map[string]string          `json:"description"`
		} `json:"schema"`
	}
	if err := json.Unmarshal(payload, &response); err != nil {
		return "", nil, err
	}
	if response.Schema == nil {
		return "", nil, errors.New("The given key was not present in the dictionary.")
	}
	type rawParameter struct {
		Name           string          `json:"name"`
		DataValueType  string          `json:"dataValueType"`
		Direction      json.RawMessage `json:"direction"`
		ItemProperties []rawParameter  `json:"itemProperties"`
	}
	var wrapper struct {
		MetaData *struct {
			Schema *struct {
				Parameters *[]rawParameter `json:"parameters"`
			} `json:"schema"`
		} `json:"metaData"`
	}
	if strings.TrimSpace(response.Schema.MetaData) != "" {
		if err := json.Unmarshal([]byte(response.Schema.MetaData), &wrapper); err != nil {
			return "", nil, err
		}
	}
	description := ""
	if text, ok := response.Schema.Description[culture]; ok && strings.TrimSpace(text) != "" {
		description = text
	}
	if wrapper.MetaData == nil || wrapper.MetaData.Schema == nil || wrapper.MetaData.Schema.Parameters == nil {
		return description, nil, nil
	}
	localized := func(key string) map[string]string {
		var values map[string]string
		if raw, ok := response.Schema.Resources[key]; ok {
			_ = json.Unmarshal(raw, &values)
		}
		return values
	}
	var convert func(raw rawParameter, collection string) processWriteModelParameter
	convert = func(raw rawParameter, collection string) processWriteModelParameter {
		prefix := "Parameters." + raw.Name
		if collection != "" {
			prefix = "Parameters." + collection + "." + raw.Name
		}
		parameter := processWriteModelParameter{
			Name: raw.Name, DataValueType: normalizeGUID(raw.DataValueType),
			Direction: processWriteDirection(raw.Direction),
		}
		parameter.Captions = localized(prefix + ".Caption")
		parameter.Descriptions = localized(prefix + ".Sys_Description")
		for _, item := range raw.ItemProperties {
			// Only the first level of items gets captions (clio's FillCollectionParameterCaption).
			child := processWriteModelParameter{Name: item.Name, DataValueType: normalizeGUID(item.DataValueType),
				Direction: processWriteDirection(item.Direction)}
			if collection == "" {
				child.Captions = localized("Parameters." + raw.Name + "." + item.Name + ".Caption")
				child.Descriptions = localized("Parameters." + raw.Name + "." + item.Name + ".Sys_Description")
			}
			parameter.ItemProperties = append(parameter.ItemProperties, child)
		}
		return parameter
	}
	parameters := []processWriteModelParameter{}
	for _, raw := range *wrapper.MetaData.Schema.Parameters {
		parameters = append(parameters, convert(raw, ""))
	}
	return description, parameters, nil
}

// processWriteDirection reads ProcessParameterDirection, a number or its name; anything else is Input (0).
func processWriteDirection(raw json.RawMessage) int {
	var number int
	if json.Unmarshal(raw, &number) == nil {
		return number
	}
	var name string
	if json.Unmarshal(raw, &name) == nil {
		for value, known := range processDirections {
			if strings.EqualFold(known, strings.TrimSpace(name)) {
				return value
			}
		}
	}
	return 0
}

// processWriteNewLine is .NET's Environment.NewLine, which clio's StringBuilder.AppendLine writes.
func processWriteNewLine() string {
	if runtime.GOOS == "windows" {
		return "\r\n"
	}
	return "\n"
}

// processWriteIndent is clio's IndentWithTab: every line of s prefixed with count tabs, joined with the
// platform newline.
func processWriteIndent(s string, count int) string {
	normalized := strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
	lines := strings.Split(normalized, "\n")
	for index, line := range lines {
		lines[index] = strings.Repeat("\t", count) + line
	}
	return strings.Join(lines, processWriteNewLine())
}

// processWriteModelFile is clio's ProcessModelWriter.CreateFileContent.
func processWriteModelFile(code, description string, parameters []processWriteModelParameter, nameSpace, culture string) string {
	nl := processWriteNewLine()
	var builder strings.Builder
	line := func(text string) { builder.WriteString(text + nl) }
	line("using ATF.Repository;")
	line("using ATF.Repository.Attributes;")
	line("using Newtonsoft.Json;")
	builder.WriteString("namespace " + nameSpace)
	line("{")
	line("")
	if strings.TrimSpace(description) != "" {
		// A C# raw string literal: its own line breaks are the source file's, i.e. "\n".
		line(processWriteIndent("/// <summary>\n/// "+description+" \n/// </summary>", 1))
	}
	line("\t[BusinessProcess(\"" + code + "\")]")
	line("\tpublic class " + code + " : IBusinessProcess {")
	line("")
	main, classes := processWriteModelProperties(parameters, culture)
	line(main)
	line("\t}")
	line("")
	for _, class := range classes {
		line(class)
	}
	line("}")
	return builder.String()
}

func processWriteLocalized(values map[string]string, culture string) string {
	if text, ok := values[culture]; ok && strings.TrimSpace(text) != "" {
		return text
	}
	return ""
}

func processWriteModelProperties(parameters []processWriteModelParameter, culture string) (string, []string) {
	nl := processWriteNewLine()
	var builder strings.Builder
	line := func(text string) { builder.WriteString(text + nl) }
	classes := []string{}
	for _, parameter := range parameters {
		var attribute string
		switch parameter.Direction {
		case 0:
			attribute = processWriteIndent("[BusinessProcessParameter(\""+parameter.Name+"\", BusinessProcessParameterDirection.Input)]", 2)
		case 1:
			attribute = processWriteIndent("[BusinessProcessParameter(\""+parameter.Name+"\", BusinessProcessParameterDirection.Output)]", 2)
		default:
			continue
		}
		var property string
		if parameter.DataValueType == processWriteCompositeListType {
			text, class := processWriteCollectionModel(parameter, culture)
			property = processWriteIndent(text, 2)
			classes = append(classes, class)
		} else {
			property = processWriteIndent("public "+processWriteTypeText(parameter.DataValueType)+" "+parameter.Name+"{ get; set; }", 2)
		}
		if caption := processWriteLocalized(parameter.Captions, culture); caption != "" {
			line(processWriteIndent("/// <summary>", 2))
			builder.WriteString(processWriteIndent("/// ", 2))
			line(caption)
			line(processWriteIndent("/// </summary>", 2))
		}
		if description := processWriteLocalized(parameter.Descriptions, culture); description != "" {
			line(processWriteIndent("/// <remarks>", 2))
			builder.WriteString(processWriteIndent("/// ", 2))
			line(description)
			line(processWriteIndent("/// </remarks>", 2))
		}
		line(attribute)
		line(property)
		line("")
	}
	return builder.String(), classes
}

func processWriteCollectionModel(parameter processWriteModelParameter, culture string) (string, string) {
	nl := processWriteNewLine()
	var builder strings.Builder
	line := func(text string) { builder.WriteString(text + nl) }
	property := "public List<" + parameter.Name + "> " + parameter.Name + " { get; set; }"
	line(processWriteIndent("public class "+parameter.Name+" {", 1))
	line("")
	for _, item := range parameter.ItemProperties {
		if caption := processWriteLocalized(item.Captions, culture); caption != "" {
			line(processWriteIndent("/// <summary>", 2))
			builder.WriteString(processWriteIndent("/// ", 2))
			line(caption)
			line(processWriteIndent("/// </summary>", 2))
		}
		if description := processWriteLocalized(item.Descriptions, culture); description != "" {
			line(processWriteIndent("/// <remarks>", 2))
			builder.WriteString(processWriteIndent("/// ", 2))
			line(description)
			line(processWriteIndent("/// </remarks>", 2))
		}
		if strings.TrimSpace(item.Name) != "" {
			line(processWriteIndent("[JsonProperty(\""+item.Name+"\")]", 2))
			line(processWriteIndent("public "+processWriteTypeText(item.DataValueType)+" "+item.Name+" {get; set;}", 2))
			line("")
		}
	}
	line(processWriteIndent("}", 1))
	return property, builder.String()
}

// processWriteModelSave is clio's ProcessModelWriter.WriteFile: an explicit .cs path is used as it is,
// anything else is a folder that gets "<code>.cs"; the folder is created and an existing file replaced.
func processWriteModelSave(code, content, fileOrFolder string) error {
	path := fileOrFolder
	if !processWriteIsExplicitFile(fileOrFolder) {
		path = filepath.Join(fileOrFolder, code+".cs")
	}
	if directory := processWriteParentDirectory(path); strings.TrimSpace(directory) != "" {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			return fmt.Errorf("could not create the destination folder: %w", err)
		}
	}
	if info, err := os.Stat(path); err == nil && !info.IsDir() {
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("could not replace the existing file: %w", err)
		}
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

func processWriteIsExplicitFile(path string) bool {
	if info, err := os.Stat(path); err == nil {
		return !info.IsDir()
	}
	if strings.HasSuffix(path, string(os.PathSeparator)) || strings.HasSuffix(path, "/") {
		return false
	}
	return processWriteExtension(path) != ""
}

// processWriteExtension is .NET Path.GetExtension: the text from the last '.' after the last separator,
// empty when that '.' is the last character.
func processWriteExtension(path string) string {
	separator := strings.LastIndexAny(path, `/\`)
	if runtime.GOOS != "windows" {
		separator = strings.LastIndex(path, "/")
	}
	dot := strings.LastIndex(path, ".")
	if dot <= separator || dot == len(path)-1 {
		return ""
	}
	return path[dot:]
}

// processWriteParentDirectory is clio's GetParentDirectoryPath.
func processWriteParentDirectory(path string) string {
	last := strings.LastIndexAny(path, `\/`)
	switch {
	case last < 0:
		return ""
	case last == 0:
		return path[:1]
	case last == 2 && len(path) > 2 && path[1] == ':' && (path[2] == '\\' || path[2] == '/'):
		return path[:3]
	default:
		return path[:last]
	}
}
