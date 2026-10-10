package creatio

// create-data-binding, add-data-binding-row and remove-data-binding-row: clio's DataBindingService
// (DataBindingCommand.cs) over a clio workspace on disk. A binding is packages/<package>/Data/<binding>/ with
// descriptor.json, data.json, an empty filter.json and Localization/data.<culture>.json, all written as
// System.Text.Json writes them (indented, null members omitted). create-data-binding takes the schema from a
// built-in offline template (SysSettings, SysModule) or from the environment's runtime schema, and may read a
// lookup's display value from the environment.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	dataWriteLookupTypeUID         = "b295071f-7ea9-4e62-8d1a-919bf3732ff2"
	dataWriteImageReferenceTypeUID = "b039feb0-ee7c-4884-8aa6-d6d45d84316f"
	dataWriteImageContentTypeUID   = "fa6e6e49-b996-475e-a77e-73904e4c5a88"
)

// DataWriteLocalBindingRequest carries the arguments of the three workspace binding tools.
type DataWriteLocalBindingRequest struct {
	PackageName       string
	SchemaName        string
	BindingName       string
	InstallType       int
	ValuesJSON        string
	LocalizationsJSON string
	KeyValue          string
	WorkspacePath     string
	// Remote is true when the call named an environment, which lets a lookup's display value be read from it.
	Remote bool
}

// dataWriteLocalSchema is clio's DataBindingSchema.
type dataWriteLocalSchema struct {
	UID, Name, PrimaryColumnUID, PrimaryDisplayColumn string
	Columns                                           []dataWriteLocalSchemaColumn
}

type dataWriteLocalSchemaColumn struct {
	UID, Name       string
	DataValueType   int
	ReferenceSchema string
	TemplateTypeUID string
}

// dataWriteLocalColumn is DataBindingColumnDefinition.
type dataWriteLocalColumn struct {
	ColumnUID, ColumnName, DataTypeUID, ReferenceSchema string
	IsForceUpdate, IsKey                                bool
}

// dataWriteLocalValue is DataBindingRowValue; Value nil is omitted, as the serializer omits nulls.
type dataWriteLocalValue struct {
	SchemaColumnUID string
	ColumnName      *string
	Value           *jnode
	DisplayValue    *string
}

type dataWriteLocalRow []dataWriteLocalValue

type dataWriteLocalDescriptor struct {
	UID, Name, ModifiedOnUTC string
	InstallType              int
	SchemaUID, SchemaName    string
	Columns                  []dataWriteLocalColumn
}

type dataWriteLocalRuntime struct {
	name    string
	columns []dataWriteLocalColumn
	primary dataWriteLocalColumn
}

func (d dataWriteLocalRuntime) column(name string) (dataWriteLocalColumn, bool) {
	for _, column := range d.columns {
		if strings.EqualFold(column.ColumnName, name) {
			return column, true
		}
	}
	return dataWriteLocalColumn{}, false
}

// dataWriteLocalTemplates is DataBindingTemplateCatalog: the offline schemas create-data-binding needs no
// environment for.
var dataWriteLocalTemplates = map[string]dataWriteLocalSchema{
	"SYSSETTINGS": {UID: "27aeadd6-d508-4572-8061-5b55b667c902", Name: "SysSettings", PrimaryColumnUID: "ae0e45ca-c495-4fe7-a39d-3ab7278e1617",
		Columns: []dataWriteLocalSchemaColumn{
			{"13aad544-ec30-4e76-a373-f0cff3202e24", "Code", 27, "", ""}, {"64fadca1-ab7c-4471-9d25-68599acc729b", "IsSSPAvailable", 12, "", ""},
			{"736c30a7-c0ec-4fa9-b034-2552b319b633", "Name", 28, "", ""}, {"764cd95a-59b3-4060-b17f-2797d5c76aaa", "IsPersonal", 12, "", ""},
			{"9e53fd7c-dde4-4502-a64c-b9e34148108b", "Description", 29, "", ""}, {"ae0e45ca-c495-4fe7-a39d-3ab7278e1617", "Id", 0, "", ""},
			{"b2280617-35f9-4006-8634-c557a2e121c2", "ReferenceSchemaUId", 0, "SysSchema", ""}, {"eb971f1a-dd41-4668-99aa-1f2a6b61a1b9", "IsCacheable", 12, "", ""},
			{"f7960a8a-1fd4-41d2-997a-fd78ea60075f", "ValueTypeName", 28, "", ""},
		}},
	"SYSMODULE": {UID: "2b2ed767-0b4b-4a7b-9de2-d48e14a2c0c5", Name: "SysModule", PrimaryColumnUID: "ae0e45ca-c495-4fe7-a39d-3ab7278e1617",
		Columns: []dataWriteLocalSchemaColumn{
			{"bd3cf32d-f9b5-471b-a0ca-f541296b979d", "Attribute", 27, "", ""}, {"327a0dc4-df63-4f6e-9d33-bc403d284cb6", "CardSchemaUId", 0, "", ""},
			{"cb4bb1d2-d369-406e-8150-502dd7af2199", "CardModuleUId", 0, "", ""}, {"3da3c3b2-02fb-4cca-80c3-7946d4e8f565", "Caption", 27, "", ""},
			{"e0c474a3-e4bc-457e-bb67-c1ec1b399f60", "Code", 28, "", ""}, {"48b260f5-5aad-608c-73a9-2b835ef697f4", "Description", 29, "", ""},
			{"d3afc924-2d21-4c0e-b2f3-9f8c180221f9", "FolderMode", 10, "", ""}, {"eea74681-e019-4885-9a1e-e8261f2665ea", "GlobalSearchAvailable", 12, "", ""},
			{"a0fd39b2-b680-4515-ac3c-72322db4f1b8", "HasActions", 12, "", ""}, {"34dfc288-1b25-4d53-bdf3-16b58a84e276", "HasAnalytics", 12, "", ""},
			{"80769c54-f4f4-43cb-93f8-0824715969a6", "HasRecent", 12, "", ""}, {"9a366fd1-19c8-4ba7-9bdd-039f164c08ec", "HelpContextId", 28, "", ""},
			{"ae0e45ca-c495-4fe7-a39d-3ab7278e1617", "Id", 0, "", ""}, {"48ed5be5-6dcd-44ba-6294-a29c8daef880", "IconBackground", 27, "", ""},
			{"6d827ba7-a622-47cc-8f11-b40b91c7441a", "Image16", 14, "", dataWriteImageContentTypeUID},
			{"ed272316-b65f-41db-a9b4-e53ab939e4d6", "Image20", 14, "", dataWriteImageContentTypeUID},
			{"63f1eb37-455a-4a53-ace2-fa5ef4c3d10f", "Image32", 1, "", dataWriteImageReferenceTypeUID},
			{"dedaabd6-732d-47ac-b229-50a8ee02292c", "IsSystem", 12, "", ""},
			{"380d55b9-487c-429b-9aff-e04101ffc307", "Logo", 1, "", dataWriteImageReferenceTypeUID},
			{"74a0895a-c418-9012-441c-0c888293e434", "MobileSectionSchemaUId", 0, "", ""}, {"7b904e78-84bf-408c-a7a1-1287e66837d3", "ModuleHeader", 27, "", ""},
			{"af5bbb5e-9c78-44b7-8fdd-2bfc4353b4a8", "SectionSchemaUId", 0, "", ""}, {"d57c3c34-e293-4aed-bff6-91dc90408958", "SectionModuleSchemaUId", 0, "", ""},
			{"3f098e0d-6cbd-4e8f-bc3e-00709f2d8d82", "SysModuleEntity", 10, "", ""}, {"e6243d2b-cc8f-4b2d-8646-36bac9fb48e9", "SysModuleVisa", 10, "", ""},
			{"b3fefb7f-2aab-4b16-97aa-6ca3f3bd7ac2", "SysPageSchemaUId", 0, "", ""}, {"1e4741cc-9a6e-446f-9865-5f5910fadd67", "Type", 4, "", ""},
			{"f3a29fb6-f13d-443e-8360-d4f51e8bcec8", "TypeColumnValue", 0, "", ""},
		}},
}

// DataWriteHasBindingTemplate is DataBindingTemplateCatalog.HasTemplate.
func DataWriteHasBindingTemplate(schemaName string) bool {
	_, ok := dataWriteLocalTemplates[strings.ToUpper(schemaName)]
	return strings.TrimSpace(schemaName) != "" && ok
}

// DataWriteValidateWorkspacePath is DataBindingToolPathValidator.ValidateWorkspacePath; its error is raised by
// the tool.
func DataWriteValidateWorkspacePath(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("workspace-path is required.")
	}
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("Workspace path must be absolute: %s. Provide a full path such as C:\\Projects\\MyWorkspace.", path)
	}
	if strings.HasPrefix(path, `\\`) {
		return "", fmt.Errorf("Workspace path must be a local absolute path, not a network share: %s", path)
	}
	full := filepath.Clean(path)
	if info, err := os.Stat(full); err != nil || !info.IsDir() {
		return "", fmt.Errorf("Workspace path not found: %s. Verify the directory exists and the path is correct.", full)
	}
	return full, nil
}

type dataWriteLocalRun struct {
	ctx    context.Context
	client *Client // nil on the offline template path
}

func dataWriteLocalResult(err error) CommandResult {
	if err != nil {
		return CommandFailure(err.Error())
	}
	return NewCommandResult(0, "Info", "Done")
}

// CreateDataBindingLocal is CreateDataBindingCommand over DataBindingService.CreateBinding. client is nil when
// the schema has an offline template.
func CreateDataBindingLocal(ctx context.Context, client *Client, request DataWriteLocalBindingRequest) CommandResult {
	return dataWriteLocalResult((&dataWriteLocalRun{ctx: ctx, client: client}).create(request))
}

// AddDataBindingRowLocal is AddDataBindingRowCommand over DataBindingService.AddOrUpdateRow.
func AddDataBindingRowLocal(ctx context.Context, request DataWriteLocalBindingRequest) CommandResult {
	return dataWriteLocalResult((&dataWriteLocalRun{ctx: ctx}).add(request))
}

// RemoveDataBindingRowLocal is RemoveDataBindingRowCommand over DataBindingService.RemoveRow.
func RemoveDataBindingRowLocal(ctx context.Context, request DataWriteLocalBindingRequest) CommandResult {
	return dataWriteLocalResult((&dataWriteLocalRun{ctx: ctx}).remove(request))
}

func (r *dataWriteLocalRun) create(request DataWriteLocalBindingRequest) error {
	if !DataWriteHasBindingTemplate(request.SchemaName) && r.client == nil {
		return errors.New("create-data-binding requires --environment or --uri.")
	}
	if strings.TrimSpace(request.PackageName) == "" {
		return errors.New("--package is required.")
	}
	if strings.TrimSpace(request.SchemaName) == "" {
		return errors.New("--schema is required.")
	}
	if request.InstallType < 0 || request.InstallType > 3 {
		return errors.New("--install-type must be between 0 and 3.")
	}
	root, err := dataWriteWorkspaceRoot(request.WorkspacePath)
	if err != nil {
		return err
	}
	packagePath, err := dataWritePackagePath(root, request.PackageName)
	if err != nil {
		return err
	}
	schema, err := r.resolveSchema(request.SchemaName)
	if err != nil {
		return err
	}
	bindingName := strings.TrimSpace(request.BindingName)
	if bindingName == "" {
		bindingName = request.SchemaName
	}
	bindingPath := filepath.Join(packagePath, "Data", bindingName)
	existing, err := dataWriteReadDescriptor(bindingPath)
	if err != nil {
		return err
	}
	if existing != nil && existing.SchemaName != schema.Name {
		return fmt.Errorf("Binding '%s' already exists for schema '%s'.", bindingName, existing.SchemaName)
	}
	values, err := dataWriteParseColumnObject(request.ValuesJSON, "values", false)
	if err != nil {
		return err
	}
	localizations, err := dataWriteParseLocalizations(request.LocalizationsJSON)
	if err != nil {
		return err
	}
	template := values == nil
	var selected map[string]bool
	if values != nil {
		selected = map[string]bool{}
		for _, key := range values.keys {
			selected[strings.ToUpper(key)] = true
		}
		if localizations != nil {
			for _, culture := range localizations.keys {
				for _, key := range localizations.values[culture].keys {
					selected[strings.ToUpper(key)] = true
				}
			}
		}
	}
	columns, err := dataWriteDescriptorColumns(schema, selected, template)
	if err != nil {
		return err
	}
	descriptor := dataWriteLocalDescriptor{UID: newGUID(), Name: bindingName, InstallType: request.InstallType, SchemaUID: schema.UID,
		SchemaName: schema.Name, Columns: columns, ModifiedOnUTC: fmt.Sprintf("/Date(%d)/", time.Now().UnixMilli())}
	if existing != nil {
		descriptor.UID = existing.UID
	}
	runtime := dataWriteRuntimeOf(schema.Name, columns)
	var row dataWriteLocalRow
	if template {
		for _, column := range columns {
			value := dataWriteLocalValue{SchemaColumnUID: column.ColumnUID, Value: newString("")}
			if dataWriteRequiresDisplay(column) {
				value.DisplayValue = dataWriteText("")
			}
			row = append(row, value)
		}
	} else if row, err = r.buildRow(runtime, values, true, request.Remote, root); err != nil {
		return err
	}
	files := map[string][]dataWriteLocalRow{}
	var fileOrder []string
	if template {
		files["data.en-US.json"] = []dataWriteLocalRow{dataWriteLocalizationTemplate(runtime, row)}
		fileOrder = append(fileOrder, "data.en-US.json")
	}
	if localizations != nil {
		key, err := dataWritePrimaryKey(row, runtime)
		if err != nil {
			return err
		}
		for _, culture := range localizations.keys {
			localized, err := r.localizationRow(runtime, key, localizations.values[culture], root)
			if err != nil {
				return err
			}
			name := "data." + culture + ".json"
			if _, seen := files[name]; !seen {
				fileOrder = append(fileOrder, name)
			}
			files[name] = []dataWriteLocalRow{localized}
		}
	}
	if err := os.MkdirAll(bindingPath, 0o755); err != nil {
		return err
	}
	if err := dataWriteWriteText(filepath.Join(bindingPath, "descriptor.json"), dataWriteDescriptorJSON(descriptor)); err != nil {
		return err
	}
	if err := dataWriteWriteText(filepath.Join(bindingPath, "data.json"), dataWritePackageDataJSON([]dataWriteLocalRow{row})); err != nil {
		return err
	}
	if err := dataWriteWriteText(filepath.Join(bindingPath, "filter.json"), ""); err != nil {
		return err
	}
	localization := filepath.Join(bindingPath, "Localization")
	if err := os.RemoveAll(localization); err != nil {
		return err
	}
	if err := os.MkdirAll(localization, 0o755); err != nil {
		return err
	}
	for _, name := range fileOrder {
		if err := dataWriteWriteText(filepath.Join(localization, name), dataWritePackageDataJSON(files[name])); err != nil {
			return err
		}
	}
	return nil
}

func (r *dataWriteLocalRun) add(request DataWriteLocalBindingRequest) error {
	if strings.TrimSpace(request.PackageName) == "" {
		return errors.New("--package is required.")
	}
	if strings.TrimSpace(request.BindingName) == "" {
		return errors.New("--binding-name is required.")
	}
	if strings.TrimSpace(request.ValuesJSON) == "" {
		return errors.New("--values is required.")
	}
	root, bindingPath, descriptor, rows, err := dataWriteOpenBinding(request)
	if err != nil {
		return err
	}
	values, err := dataWriteParseColumnObject(request.ValuesJSON, "values", true)
	if err != nil {
		return err
	}
	localizations, err := dataWriteParseLocalizations(request.LocalizationsJSON)
	if err != nil {
		return err
	}
	runtime, err := dataWriteRuntimeFromDescriptor(descriptor)
	if err != nil {
		return err
	}
	row, err := r.buildRow(runtime, values, true, false, root)
	if err != nil {
		return err
	}
	key, err := dataWritePrimaryKey(row, runtime)
	if err != nil {
		return err
	}
	index, err := dataWriteFindRow(rows, runtime, key)
	if err != nil {
		return err
	}
	if index >= 0 {
		rows[index] = row
	} else {
		rows = append(rows, row)
	}
	if err := dataWriteWriteText(filepath.Join(bindingPath, "data.json"), dataWritePackageDataJSON(rows)); err != nil {
		return err
	}
	if localizations == nil {
		return nil
	}
	localization := filepath.Join(bindingPath, "Localization")
	if err := os.MkdirAll(localization, 0o755); err != nil {
		return err
	}
	for _, culture := range localizations.keys {
		path := filepath.Join(localization, "data."+culture+".json")
		var cultureRows []dataWriteLocalRow
		if _, statErr := os.Stat(path); statErr == nil {
			if cultureRows, err = dataWriteReadRows(path); err != nil {
				return err
			}
		}
		localized, err := r.localizationRow(runtime, key, localizations.values[culture], root)
		if err != nil {
			return err
		}
		existing, err := dataWriteFindRow(cultureRows, runtime, key)
		if err != nil {
			return err
		}
		if existing >= 0 {
			cultureRows[existing] = localized
		} else {
			cultureRows = append(cultureRows, localized)
		}
		if err := dataWriteWriteText(path, dataWritePackageDataJSON(cultureRows)); err != nil {
			return err
		}
	}
	return nil
}

func (r *dataWriteLocalRun) remove(request DataWriteLocalBindingRequest) error {
	if strings.TrimSpace(request.PackageName) == "" {
		return errors.New("--package is required.")
	}
	if strings.TrimSpace(request.BindingName) == "" {
		return errors.New("--binding-name is required.")
	}
	if strings.TrimSpace(request.KeyValue) == "" {
		return errors.New("--key-value is required.")
	}
	_, bindingPath, descriptor, rows, err := dataWriteOpenBinding(request)
	if err != nil {
		return err
	}
	runtime, err := dataWriteRuntimeFromDescriptor(descriptor)
	if err != nil {
		return err
	}
	converted, err := dataWriteConvertValue(newString(request.KeyValue), runtime.primary.DataTypeUID, runtime.primary.ColumnName, false, "")
	if err != nil {
		return err
	}
	key, err := dataWriteNormalizeKey(converted, runtime.primary.DataTypeUID)
	if err != nil {
		return err
	}
	index, err := dataWriteFindRow(rows, runtime, key)
	if err != nil {
		return err
	}
	if index < 0 {
		return fmt.Errorf("Row with key '%s' was not found in binding '%s'.", request.KeyValue, request.BindingName)
	}
	rows = append(rows[:index], rows[index+1:]...)
	if err := dataWriteWriteText(filepath.Join(bindingPath, "data.json"), dataWritePackageDataJSON(rows)); err != nil {
		return err
	}
	localization := filepath.Join(bindingPath, "Localization")
	files, _ := filepath.Glob(filepath.Join(localization, "data.*.json"))
	for _, path := range files {
		cultureRows, err := dataWriteReadRows(path)
		if err != nil {
			return err
		}
		existing, err := dataWriteFindRow(cultureRows, runtime, key)
		if err != nil {
			return err
		}
		if existing >= 0 {
			cultureRows = append(cultureRows[:existing], cultureRows[existing+1:]...)
			if err := dataWriteWriteText(path, dataWritePackageDataJSON(cultureRows)); err != nil {
				return err
			}
		}
	}
	return nil
}

// ---------------------------------------------------------------------------------------------- workspace

func dataWriteWorkspaceRoot(path string) (string, error) {
	root, _ := filepath.Abs(path)
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return "", fmt.Errorf("Workspace path not found: %s", root)
	}
	settings := filepath.Join(root, ".clio", "workspaceSettings.json")
	if info, err := os.Stat(settings); err != nil || info.IsDir() {
		return "", fmt.Errorf("Workspace root was not detected at '%s': missing '%s'. Supply --workspace-path with the workspace root containing "+
			".clio/workspaceSettings.json, not the package directory. The package is resolved under packages/<package-name> beneath that root.", root, settings)
	}
	return root, nil
}

func dataWritePackagePath(root, packageName string) (string, error) {
	path := filepath.Join(root, "packages", packageName)
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		return "", fmt.Errorf("Package directory not found: %s", path)
	}
	return path, nil
}

func dataWriteOpenBinding(request DataWriteLocalBindingRequest) (string, string, *dataWriteLocalDescriptor, []dataWriteLocalRow, error) {
	root, err := dataWriteWorkspaceRoot(request.WorkspacePath)
	if err != nil {
		return "", "", nil, nil, err
	}
	packagePath, err := dataWritePackagePath(root, request.PackageName)
	if err != nil {
		return "", "", nil, nil, err
	}
	bindingPath := filepath.Join(packagePath, "Data", request.BindingName)
	if info, err := os.Stat(bindingPath); err != nil || !info.IsDir() {
		return "", "", nil, nil, fmt.Errorf("Binding directory not found: %s", bindingPath)
	}
	descriptor, err := dataWriteReadDescriptor(bindingPath)
	if err != nil {
		return "", "", nil, nil, err
	}
	if descriptor == nil {
		return "", "", nil, nil, fmt.Errorf("Binding descriptor not found in '%s'.", bindingPath)
	}
	dataPath := filepath.Join(bindingPath, "data.json")
	if _, err := os.Stat(dataPath); err != nil {
		return "", "", nil, nil, fmt.Errorf("Binding data file not found in '%s'.", bindingPath)
	}
	rows, err := dataWriteReadRows(dataPath)
	if err != nil {
		return "", "", nil, nil, err
	}
	return root, bindingPath, descriptor, rows, nil
}

func dataWriteWriteText(path, text string) error {
	return os.WriteFile(path, []byte(text), 0o644)
}

func dataWriteReadJSONFile(path string) (*jnode, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	node, err := parseJNode([]byte(strings.TrimPrefix(string(content), "\uFEFF")))
	if err != nil {
		return nil, fmt.Errorf("'%s' is not valid JSON: %s", filepath.Base(path), err.Error())
	}
	return node, nil
}

func dataWriteReadDescriptor(bindingPath string) (*dataWriteLocalDescriptor, error) {
	path := filepath.Join(bindingPath, "descriptor.json")
	if _, err := os.Stat(path); err != nil {
		return nil, nil
	}
	root, err := dataWriteReadJSONFile(path)
	if err != nil {
		return nil, err
	}
	node := root.get("Descriptor")
	descriptor := &dataWriteLocalDescriptor{UID: emptyGUID}
	if node == nil {
		return descriptor, nil
	}
	descriptor.UID = dataWriteGUIDOf(node.get("UId"))
	descriptor.Name = node.get("Name").str()
	descriptor.ModifiedOnUTC = node.get("ModifiedOnUtc").str()
	if installType := node.get("InstallType"); installType != nil {
		descriptor.InstallType, _ = strconv.Atoi(installType.text)
	}
	if schema := node.get("Schema"); schema != nil {
		descriptor.SchemaUID, descriptor.SchemaName = dataWriteGUIDOf(schema.get("UId")), schema.get("Name").str()
	}
	if columns := node.get("Columns"); columns.isArray() {
		for _, item := range columns.items {
			descriptor.Columns = append(descriptor.Columns, dataWriteLocalColumn{ColumnUID: dataWriteGUIDOf(item.get("ColumnUId")),
				ColumnName: item.get("ColumnName").str(), DataTypeUID: dataWriteGUIDOf(item.get("DataTypeValueUId")),
				IsForceUpdate: item.get("IsForceUpdate") != nil && item.get("IsForceUpdate").flag, IsKey: item.get("IsKey") != nil && item.get("IsKey").flag})
		}
	}
	return descriptor, nil
}

func dataWriteGUIDOf(node *jnode) string {
	if node == nil {
		return emptyGUID
	}
	if id, ok := parseGUID(node.text); ok {
		return id
	}
	return emptyGUID
}

func dataWriteReadRows(path string) ([]dataWriteLocalRow, error) {
	root, err := dataWriteReadJSONFile(path)
	if err != nil {
		return nil, err
	}
	rows := []dataWriteLocalRow{}
	data := root.get("PackageData")
	if !data.isArray() {
		return rows, nil
	}
	for _, item := range data.items {
		row := dataWriteLocalRow{}
		if values := item.get("Row"); values.isArray() {
			for _, value := range values.items {
				entry := dataWriteLocalValue{SchemaColumnUID: dataWriteGUIDOf(value.get("SchemaColumnUId"))}
				if name := value.get("ColumnName"); name != nil && name.kind == jkString {
					text := name.text
					entry.ColumnName = &text
				}
				if raw := value.get("Value"); raw != nil && raw.kind != jkNull {
					entry.Value = raw
				}
				if display := value.get("DisplayValue"); display != nil && display.kind == jkString {
					text := display.text
					entry.DisplayValue = &text
				}
				row = append(row, entry)
			}
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// ---------------------------------------------------------------------------------------------- serialization

func dataWriteDescriptorJSON(descriptor dataWriteLocalDescriptor) string {
	columns := []any{}
	for _, column := range descriptor.Columns {
		columns = append(columns, orderedFields{{"ColumnUId", column.ColumnUID}, {"IsForceUpdate", column.IsForceUpdate}, {"IsKey", column.IsKey},
			{"ColumnName", column.ColumnName}, {"DataTypeValueUId", column.DataTypeUID}})
	}
	root := toJNode(orderedFields{{"Descriptor", orderedFields{{"UId", descriptor.UID}, {"Name", descriptor.Name},
		{"ModifiedOnUtc", descriptor.ModifiedOnUTC}, {"InstallType", descriptor.InstallType},
		{"Schema", orderedFields{{"UId", descriptor.SchemaUID}, {"Name", descriptor.SchemaName}}}, {"Columns", columns}}}})
	return string(root.stjIndentedJSON())
}

func dataWritePackageDataJSON(rows []dataWriteLocalRow) string {
	data := []any{}
	for _, row := range rows {
		values := []any{}
		for _, value := range row {
			fields := orderedFields{{"SchemaColumnUId", value.SchemaColumnUID}}
			if value.ColumnName != nil {
				fields = append(fields, field{"ColumnName", *value.ColumnName})
			}
			if value.Value != nil {
				fields = append(fields, field{"Value", value.Value})
			}
			if value.DisplayValue != nil {
				fields = append(fields, field{"DisplayValue", *value.DisplayValue})
			}
			values = append(values, fields)
		}
		data = append(data, orderedFields{{"Row", values}})
	}
	return string(toJNode(orderedFields{{"PackageData", data}}).stjIndentedJSON())
}

// ---------------------------------------------------------------------------------------------- schema

func (r *dataWriteLocalRun) resolveSchema(schemaName string) (dataWriteLocalSchema, error) {
	if template, ok := dataWriteLocalTemplates[strings.ToUpper(schemaName)]; ok && strings.TrimSpace(schemaName) != "" {
		return template, nil
	}
	return r.fetchSchema(schemaName)
}

// fetchSchema is DataBindingSchemaClient.Fetch: the runtime schema with each column's reference schema.
func (r *dataWriteLocalRun) fetchSchema(schemaName string) (dataWriteLocalSchema, error) {
	if r.client == nil {
		return dataWriteLocalSchema{}, errors.New("create-data-binding requires --environment or --uri.")
	}
	if strings.TrimSpace(schemaName) == "" {
		return dataWriteLocalSchema{}, errors.New("Schema name is required.")
	}
	body := toJNode(orderedFields{{"Name", schemaName}}).stjJSON()
	payload, err := r.client.postDataServiceJSON(r.ctx, "RuntimeEntitySchemaRequest", body, dataWriteBindingTimeout, maxResponseBytes)
	if err != nil {
		return dataWriteLocalSchema{}, err
	}
	var response struct {
		Success bool `json:"success"`
		Schema  *struct {
			UID                      string `json:"uId"`
			Name                     string `json:"name"`
			PrimaryColumnUID         string `json:"primaryColumnUId"`
			PrimaryDisplayColumnName string `json:"primaryDisplayColumnName"`
			PrimaryDisplayColumnUID  string `json:"primaryDisplayColumnUId"`
			Columns                  *struct {
				Items map[string]struct {
					UID                 string `json:"uId"`
					Name                string `json:"name"`
					DataValueType       int    `json:"dataValueType"`
					ReferenceSchemaName string `json:"referenceSchemaName"`
				} `json:"items"`
			} `json:"columns"`
		} `json:"schema"`
		ErrorInfo *struct {
			Message *string `json:"message"`
		} `json:"errorInfo"`
	}
	if err := json.Unmarshal(payload, &response); err != nil {
		return dataWriteLocalSchema{}, fmt.Errorf("RuntimeEntitySchemaRequest returned invalid JSON: %w", err)
	}
	if !response.Success || response.Schema == nil {
		if response.ErrorInfo != nil && response.ErrorInfo.Message != nil {
			return dataWriteLocalSchema{}, errors.New(*response.ErrorInfo.Message)
		}
		return dataWriteLocalSchema{}, fmt.Errorf("Runtime schema '%s' was not returned by Creatio.", schemaName)
	}
	guid := func(text string) string {
		if id, ok := parseGUID(text); ok {
			return id
		}
		return emptyGUID
	}
	schema := dataWriteLocalSchema{UID: guid(response.Schema.UID), Name: response.Schema.Name, PrimaryColumnUID: guid(response.Schema.PrimaryColumnUID),
		PrimaryDisplayColumn: response.Schema.PrimaryDisplayColumnName}
	if strings.TrimSpace(schema.Name) == "" {
		schema.Name = strings.TrimSpace(schemaName)
	}
	if response.Schema.Columns != nil {
		for _, column := range response.Schema.Columns.Items {
			schema.Columns = append(schema.Columns, dataWriteLocalSchemaColumn{UID: guid(column.UID), Name: column.Name,
				DataValueType: column.DataValueType, ReferenceSchema: column.ReferenceSchemaName})
			if schema.PrimaryDisplayColumn == "" && guid(column.UID) != emptyGUID && guid(column.UID) == guid(response.Schema.PrimaryDisplayColumnUID) {
				schema.PrimaryDisplayColumn = column.Name
			}
		}
	}
	sort.SliceStable(schema.Columns, func(i, j int) bool {
		return compareOrdinalIgnoreCase(schema.Columns[i].Name, schema.Columns[j].Name) < 0
	})
	if schema.PrimaryColumnUID == emptyGUID {
		return dataWriteLocalSchema{}, fmt.Errorf("Schema '%s' does not expose a primary column.", schemaName)
	}
	return schema, nil
}

func dataWriteDescriptorColumns(schema dataWriteLocalSchema, selected map[string]bool, all bool) ([]dataWriteLocalColumn, error) {
	columns := []dataWriteLocalColumn{}
	for _, column := range schema.Columns {
		if !all && !selected[strings.ToUpper(column.Name)] && !strings.EqualFold(column.UID, schema.PrimaryColumnUID) {
			continue
		}
		typeUID := column.TemplateTypeUID
		if typeUID == "" {
			mapped, ok := schemaWriteEntBindingTypes[column.DataValueType]
			if !ok {
				return nil, fmt.Errorf("Runtime dataValueType '%d' is not supported for data-binding generation.", column.DataValueType)
			}
			typeUID = mapped
		}
		columns = append(columns, dataWriteLocalColumn{ColumnUID: column.UID, IsKey: column.UID == schema.PrimaryColumnUID,
			ColumnName: column.Name, DataTypeUID: typeUID, ReferenceSchema: column.ReferenceSchema})
	}
	sort.SliceStable(columns, func(i, j int) bool { return columns[i].ColumnName < columns[j].ColumnName })
	hasKey := false
	for _, column := range columns {
		hasKey = hasKey || column.IsKey
	}
	if !hasKey {
		return nil, fmt.Errorf("Schema '%s' does not expose a primary column.", schema.Name)
	}
	return columns, nil
}

func dataWriteRuntimeOf(name string, columns []dataWriteLocalColumn) dataWriteLocalRuntime {
	runtime := dataWriteLocalRuntime{name: name, columns: columns}
	for _, column := range columns {
		if column.IsKey {
			runtime.primary = column
		}
	}
	return runtime
}

func dataWriteRuntimeFromDescriptor(descriptor *dataWriteLocalDescriptor) (dataWriteLocalRuntime, error) {
	keys := 0
	for _, column := range descriptor.Columns {
		if column.IsKey {
			keys++
		}
	}
	if keys == 0 {
		return dataWriteLocalRuntime{}, errors.New("Sequence contains no matching element")
	}
	if keys > 1 {
		return dataWriteLocalRuntime{}, errors.New("Sequence contains more than one matching element")
	}
	return dataWriteRuntimeOf(descriptor.Name, descriptor.Columns), nil
}

// ---------------------------------------------------------------------------------------------- rows

// dataWriteOrderedColumns is a JsonObject turned into a dictionary with case-insensitive keys: a repeated
// key (ignoring case) keeps its first spelling and its last value.
type dataWriteOrderedColumns struct {
	keys   []string
	values map[string]*jnode
}

func (c *dataWriteOrderedColumns) get(name string) (string, *jnode, bool) {
	for _, key := range c.keys {
		if strings.EqualFold(key, name) {
			return key, c.values[key], true
		}
	}
	return "", nil, false
}

func dataWriteColumnsOf(object *jnode) *dataWriteOrderedColumns {
	columns := &dataWriteOrderedColumns{values: map[string]*jnode{}}
	for _, key := range object.keys {
		value := object.props[key]
		if value != nil && value.kind == jkNull {
			value = nil
		}
		if existing, _, ok := columns.get(key); ok {
			columns.values[existing] = value
			continue
		}
		columns.keys = append(columns.keys, key)
		columns.values[key] = value
	}
	return columns
}

func dataWriteParseColumnObject(text, argument string, required bool) (*dataWriteOrderedColumns, error) {
	if strings.TrimSpace(text) == "" {
		if required {
			return nil, fmt.Errorf("--%s must contain a JSON object.", argument)
		}
		return nil, nil
	}
	node, err := parseJNode([]byte(text))
	if err != nil {
		return nil, fmt.Errorf("--%s must contain valid JSON. %s", argument, dataWriteSTJNodeParseError(text))
	}
	if !node.isObject() {
		return nil, fmt.Errorf("--%s must be a JSON object keyed by column name.", argument)
	}
	return dataWriteColumnsOf(node), nil
}

type dataWriteLocalizations struct {
	keys   []string
	values map[string]*dataWriteOrderedColumns
}

func dataWriteParseLocalizations(text string) (*dataWriteLocalizations, error) {
	if strings.TrimSpace(text) == "" {
		return nil, nil
	}
	node, err := parseJNode([]byte(text))
	if err != nil {
		return nil, fmt.Errorf("--localizations must contain valid JSON. %s", dataWriteSTJNodeParseError(text))
	}
	if !node.isObject() {
		return nil, errors.New("--localizations must be a JSON object keyed by culture.")
	}
	result := &dataWriteLocalizations{values: map[string]*dataWriteOrderedColumns{}}
	for _, culture := range node.keys {
		values := node.props[culture]
		if !values.isObject() {
			return nil, fmt.Errorf("--localizations entry '%s' must be a JSON object keyed by column name.", culture)
		}
		key := culture
		for _, existing := range result.keys {
			if strings.EqualFold(existing, culture) {
				key = existing
			}
		}
		if _, seen := result.values[key]; !seen {
			result.keys = append(result.keys, key)
		}
		result.values[key] = dataWriteColumnsOf(values)
	}
	return result, nil
}

func dataWriteRequiresDisplay(column dataWriteLocalColumn) bool {
	return column.DataTypeUID == dataWriteLookupTypeUID || column.DataTypeUID == dataWriteImageReferenceTypeUID
}

// buildRow is DataBindingService.BuildRow: every value by column name (ordinal order), the primary key
// generated when it is missing and a GUID, a display value for a lookup or image reference.
func (r *dataWriteLocalRun) buildRow(runtime dataWriteLocalRuntime, values *dataWriteOrderedColumns, autoKey, remote bool, root string) (dataWriteLocalRow, error) {
	resolved := &dataWriteOrderedColumns{keys: append([]string{}, values.keys...), values: map[string]*jnode{}}
	for key, value := range values.values {
		resolved.values[key] = value
	}
	_, keyValue, present := values.get(runtime.primary.ColumnName)
	missing := !present || keyValue == nil || (keyValue.kind == jkString && strings.TrimSpace(keyValue.text) == "")
	if missing && autoKey {
		if dataWriteClrType(runtime.primary.DataTypeUID) != "Guid" {
			return nil, fmt.Errorf("Column '%s' is required because it is the binding primary key.", runtime.primary.ColumnName)
		}
		if existing, _, ok := resolved.get(runtime.primary.ColumnName); ok {
			resolved.values[existing] = newString(newGUID())
		} else {
			resolved.keys = append(resolved.keys, runtime.primary.ColumnName)
			resolved.values[runtime.primary.ColumnName] = newString(newGUID())
		}
	}
	names := append([]string{}, resolved.keys...)
	sort.Strings(names)
	row := dataWriteLocalRow{}
	for _, name := range names {
		column, ok := runtime.column(name)
		if !ok {
			return nil, fmt.Errorf("Column '%s' is not part of binding '%s'.", name, runtime.name)
		}
		value, err := r.mainValue(column, resolved.values[name], !column.IsKey, remote, root)
		if err != nil {
			return nil, err
		}
		row = append(row, value)
	}
	for _, value := range row {
		if value.SchemaColumnUID == runtime.primary.ColumnUID {
			return row, nil
		}
	}
	return nil, fmt.Errorf("Column '%s' is required because it is the binding primary key.", runtime.primary.ColumnName)
}

func (r *dataWriteLocalRun) mainValue(column dataWriteLocalColumn, node *jnode, allowEmpty, remote bool, root string) (dataWriteLocalValue, error) {
	valueNode, hasDisplay, display := node, false, (*string)(nil)
	if node.isObject() {
		if !dataWriteRequiresDisplay(column) {
			return dataWriteLocalValue{}, fmt.Errorf("Column '%s' expects a scalar JSON value and does not support an object payload.", column.ColumnName)
		}
		inner, ok := node.props["value"]
		if !ok {
			return dataWriteLocalValue{}, fmt.Errorf("Column '%s' object payload must contain a 'value' property.", column.ColumnName)
		}
		valueNode = inner
		if valueNode != nil && valueNode.kind == jkNull {
			valueNode = nil
		}
		var displayNode *jnode
		displayNode, hasDisplay = node.props["displayValue"]
		if displayNode != nil && displayNode.kind != jkNull {
			if displayNode.kind != jkString {
				return dataWriteLocalValue{}, fmt.Errorf("Column '%s' displayValue must be a JSON string.", column.ColumnName)
			}
			text := displayNode.text
			display = &text
		}
	}
	converted, err := dataWriteConvertValue(valueNode, column.DataTypeUID, column.ColumnName, allowEmpty, root)
	if err != nil {
		return dataWriteLocalValue{}, err
	}
	if dataWriteRequiresDisplay(column) && converted == nil {
		converted = newString("null")
	}
	value := dataWriteLocalValue{SchemaColumnUID: column.ColumnUID, Value: converted}
	if !dataWriteRequiresDisplay(column) {
		return value, nil
	}
	if hasDisplay {
		text := ""
		if display != nil {
			text = *display
		}
		value.DisplayValue = &text
		return value, nil
	}
	plain := dataWriteValueText(converted)
	if converted == nil || strings.EqualFold(plain, "null") {
		text := ""
		if column.DataTypeUID == dataWriteLookupTypeUID {
			text = "null"
		}
		value.DisplayValue = &text
		return value, nil
	}
	if remote {
		resolved, ok, err := r.displayValue(column, plain)
		if err != nil {
			return dataWriteLocalValue{}, err
		}
		if ok {
			value.DisplayValue = &resolved
			return value, nil
		}
	}
	return dataWriteLocalValue{}, fmt.Errorf("Column '%s' requires displayValue. Pass the value as an object like {\"value\":\"%s\",\"displayValue\":\"...\"}.", column.ColumnName, plain)
}

// displayValue is DataBindingDisplayValueResolver: the reference record's primary display column (or Name,
// or Caption) read by Id.
func (r *dataWriteLocalRun) displayValue(column dataWriteLocalColumn, value string) (string, bool, error) {
	if strings.TrimSpace(column.ReferenceSchema) == "" || r.client == nil || strings.TrimSpace(value) == "" || strings.EqualFold(value, "null") {
		return "", false, nil
	}
	reference, err := r.fetchSchema(column.ReferenceSchema)
	if err != nil {
		return "", false, err
	}
	displayColumn := reference.PrimaryDisplayColumn
	for _, candidate := range []string{"Name", "Caption"} {
		if strings.TrimSpace(displayColumn) != "" {
			break
		}
		for _, schemaColumn := range reference.Columns {
			if strings.EqualFold(schemaColumn.Name, candidate) {
				displayColumn = schemaColumn.Name
				break
			}
		}
	}
	if strings.TrimSpace(displayColumn) == "" {
		return "", false, nil
	}
	query := map[string]any{"rootSchemaName": column.ReferenceSchema, "useLocalization": true,
		"filters": standardFilters(map[string]any{"idFilter": comparisonFilter("Id", value, 0, 3)}),
		"columns": map[string]any{"items": map[string]any{displayColumn: map[string]any{"expression": map[string]any{"expressionType": 0, "columnPath": displayColumn}}}}}
	rows, err := r.client.selectRows(r.ctx, query)
	if err != nil {
		return "", false, err
	}
	if len(rows) == 0 {
		return "", false, nil
	}
	raw, ok := rows[0][displayColumn]
	if !ok {
		return "", false, nil
	}
	node, err := parseJNode(raw)
	if err != nil || node.kind == jkNull {
		return "", false, nil
	}
	if node.kind == jkString {
		return node.text, true, nil
	}
	return strings.Trim(string(raw), `"`), true, nil
}

func (r *dataWriteLocalRun) localizationRow(runtime dataWriteLocalRuntime, key string, values *dataWriteOrderedColumns, root string) (dataWriteLocalRow, error) {
	keyValue, err := dataWriteRestoreKey(key, runtime.primary)
	if err != nil {
		return nil, err
	}
	primaryName := runtime.primary.ColumnName
	row := dataWriteLocalRow{{SchemaColumnUID: runtime.primary.ColumnUID, ColumnName: &primaryName, Value: keyValue}}
	names := append([]string{}, values.keys...)
	sort.Strings(names)
	for _, name := range names {
		column, ok := runtime.column(name)
		if !ok {
			return nil, fmt.Errorf("Localization column '%s' is not part of binding '%s'.", name, runtime.name)
		}
		if dataWriteClrType(column.DataTypeUID) != "String" {
			return nil, fmt.Errorf("Localization column '%s' must be a string-compatible data type.", name)
		}
		converted, err := dataWriteConvertValue(values.values[name], column.DataTypeUID, column.ColumnName, true, root)
		if err != nil {
			return nil, err
		}
		columnName := column.ColumnName
		row = append(row, dataWriteLocalValue{SchemaColumnUID: column.ColumnUID, ColumnName: &columnName, Value: converted})
	}
	return row, nil
}

func dataWriteLocalizationTemplate(runtime dataWriteLocalRuntime, source dataWriteLocalRow) dataWriteLocalRow {
	key, _ := dataWritePrimaryKey(source, runtime)
	keyValue, _ := dataWriteRestoreKey(key, runtime.primary)
	primaryName := runtime.primary.ColumnName
	row := dataWriteLocalRow{{SchemaColumnUID: runtime.primary.ColumnUID, ColumnName: &primaryName, Value: keyValue}}
	columns := []dataWriteLocalColumn{}
	for _, column := range runtime.columns {
		if !column.IsKey && dataWriteClrType(column.DataTypeUID) == "String" {
			columns = append(columns, column)
		}
	}
	sort.SliceStable(columns, func(i, j int) bool { return columns[i].ColumnName < columns[j].ColumnName })
	for _, column := range columns {
		name := column.ColumnName
		row = append(row, dataWriteLocalValue{SchemaColumnUID: column.ColumnUID, ColumnName: &name, Value: newString("")})
	}
	return row
}

func dataWriteRestoreKey(key string, primary dataWriteLocalColumn) (*jnode, error) {
	if strings.TrimSpace(key) == "" {
		return newString(""), nil
	}
	converted, err := dataWriteConvertValue(newString(key), primary.DataTypeUID, primary.ColumnName, false, "")
	if err != nil {
		return nil, err
	}
	if converted == nil {
		return newString(key), nil
	}
	return converted, nil
}

// dataWritePrimaryKey is GetPrimaryKey: the row's primary value normalized.
func dataWritePrimaryKey(row dataWriteLocalRow, runtime dataWriteLocalRuntime) (string, error) {
	var found *dataWriteLocalValue
	for index := range row {
		if row[index].SchemaColumnUID == runtime.primary.ColumnUID {
			if found != nil {
				return "", errors.New("Sequence contains more than one matching element")
			}
			found = &row[index]
		}
	}
	if found == nil {
		return "", fmt.Errorf("Row does not contain the primary key column '%s'.", runtime.primary.ColumnName)
	}
	return dataWriteNormalizeKey(found.Value, runtime.primary.DataTypeUID)
}

func dataWriteFindRow(rows []dataWriteLocalRow, runtime dataWriteLocalRuntime, key string) (int, error) {
	for index, row := range rows {
		rowKey, err := dataWritePrimaryKey(row, runtime)
		if err != nil {
			return -1, err
		}
		if rowKey == key {
			return index, nil
		}
	}
	return -1, nil
}

// dataWriteNormalizeKey is DataBindingValueConverter.NormalizeKeyValue.
func dataWriteNormalizeKey(value *jnode, typeUID string) (string, error) {
	if value == nil {
		return "", nil
	}
	text := dataWriteValueText(value)
	if dataWriteClrType(typeUID) != "Guid" {
		return text, nil
	}
	if strings.TrimSpace(text) == "" {
		return "", nil
	}
	id, ok := parseGUID(text)
	if !ok {
		return "", errors.New("Unrecognized Guid format.")
	}
	return id, nil
}

// dataWriteValueText is Convert.ToString(value, InvariantCulture) of a row value: a string as it is, a
// boolean as True/False, a number as its text.
func dataWriteValueText(value *jnode) string {
	if value == nil {
		return ""
	}
	switch value.kind {
	case jkString:
		return value.text
	case jkBool:
		if value.flag {
			return "True"
		}
		return "False"
	case jkNull:
		return ""
	case jkObject, jkArray:
		return string(value.stjJSON())
	}
	return value.text
}

// dataWriteClrType is DataValueTypeMap.Resolve's type name for a data-value-type id.
func dataWriteClrType(typeUID string) string {
	name, ok := processClrTypes[strings.ToLower(typeUID)]
	if !ok {
		return "Object"
	}
	name = strings.TrimPrefix(strings.TrimPrefix(name, "System.Drawing."), "System.")
	if strings.HasPrefix(name, "Collections.Generic.List") {
		return "List`1"
	}
	return name
}

// dataWriteConvertValue is DataBindingValueConverter.ConvertValue: the JSON value coerced to the column's
// type, or the "is not valid for data type" refusal.
func dataWriteConvertValue(node *jnode, typeUID, columnName string, allowEmpty bool, root string) (*jnode, error) {
	if node == nil || node.kind == jkNull {
		return nil, nil
	}
	target := dataWriteClrType(typeUID)
	scalar := node.kind != jkObject && node.kind != jkArray
	if scalar {
		switch target {
		case "Boolean":
			if node.kind == jkBool {
				return newBool(node.flag), nil
			}
			if node.kind == jkString {
				switch strings.ToLower(strings.TrimSpace(node.text)) {
				case "true":
					return newBool(true), nil
				case "false":
					return newBool(false), nil
				}
			}
		case "Int32":
			if node.kind == jkInteger {
				if value, err := strconv.ParseInt(node.text, 10, 32); err == nil {
					return newInt(int(value)), nil
				}
			}
			if node.kind == jkString {
				if value, err := strconv.ParseInt(strings.Trim(node.text, " \t\n\v\f\r"), 10, 32); err == nil {
					return newInt(int(value)), nil
				}
			}
		case "Single":
			if node.kind == jkInteger || node.kind == jkFloat {
				if value, err := strconv.ParseFloat(node.text, 64); err == nil {
					return &jnode{kind: jkFloat, text: dataWriteSingleText(float32(value))}, nil
				}
			}
			if node.kind == jkString {
				if value, err := strconv.ParseFloat(strings.TrimSpace(node.text), 32); err == nil {
					return &jnode{kind: jkFloat, text: dataWriteSingleText(float32(value))}, nil
				}
			}
		case "Decimal":
			if node.kind == jkInteger || node.kind == jkFloat || node.kind == jkString {
				if text, ok := dataWriteDecimalText(strings.TrimSpace(node.text)); ok {
					return &jnode{kind: jkFloat, text: text}, nil
				}
			}
		case "Guid":
			if node.kind == jkString {
				if strings.EqualFold(node.text, "null") {
					return newString("null"), nil
				}
				if allowEmpty && strings.TrimSpace(node.text) == "" {
					return newString(""), nil
				}
				if id, ok := parseGUID(node.text); ok {
					return newString(id), nil
				}
			}
		case "DateTime":
			if node.kind == jkString {
				if text, ok := dataWriteRoundTripDate(node.text); ok {
					return newString(text), nil
				}
			}
		default:
			if node.kind == jkString {
				if !allowEmpty && strings.TrimSpace(node.text) == "" {
					return nil, fmt.Errorf("Column '%s' cannot be empty.", columnName)
				}
				converted, err := dataWriteImageContent(node.text, typeUID, root)
				if err != nil {
					return nil, err
				}
				return newString(converted), nil
			}
		}
	}
	if target == "String" {
		text := strings.Trim(string(node.stjJSON()), `"`)
		if !allowEmpty && strings.TrimSpace(text) == "" {
			return nil, fmt.Errorf("Column '%s' cannot be empty.", columnName)
		}
		converted, err := dataWriteImageContent(text, typeUID, root)
		if err != nil {
			return nil, err
		}
		return newString(converted), nil
	}
	return nil, fmt.Errorf("Column '%s' value '%s' is not valid for data type '%s'.", columnName, dataWriteNodeText(node), target)
}

// dataWriteImageContent is TryConvertImageContentFile: for an image-content column, a path to a file inside the
// workspace becomes the file's base64 content; anything else stays as given.
func dataWriteImageContent(text, typeUID, root string) (string, error) {
	if typeUID != dataWriteImageContentTypeUID || strings.TrimSpace(text) == "" || strings.EqualFold(text, "null") {
		return text, nil
	}
	path := text
	if !filepath.IsAbs(path) && strings.TrimSpace(root) != "" {
		path = filepath.Join(root, path)
	}
	path, _ = filepath.Abs(path)
	if info, err := os.Stat(path); err != nil || info.IsDir() {
		return text, nil
	}
	if strings.TrimSpace(root) != "" {
		workspace, _ := filepath.Abs(root)
		workspace = strings.TrimRight(workspace, string(filepath.Separator))
		if strings.EqualFold(path, workspace) {
			return "", fmt.Errorf("Image file path must point to a file inside the workspace: %s", path)
		}
		if !strings.HasPrefix(strings.ToLower(path), strings.ToLower(workspace+string(filepath.Separator))) {
			return "", fmt.Errorf("Image file path must stay inside the workspace: %s", path)
		}
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(content), nil
}

// dataWriteSingleText is a float as System.Text.Json writes it: the shortest round-trip digits, scientific
// from 1E+15 and below 1E-04.
func dataWriteSingleText(value float32) string {
	text := strconv.FormatFloat(float64(value), 'g', -1, 32)
	mantissa, exponent, scientific := strings.Cut(text, "e")
	if !scientific {
		return text
	}
	power, _ := strconv.Atoi(exponent)
	if power >= -5 && power < 15 {
		return strconv.FormatFloat(float64(value), 'f', -1, 32)
	}
	sign := "+"
	if power < 0 {
		sign, power = "-", -power
	}
	return fmt.Sprintf("%sE%s%02d", mantissa, sign, power)
}

// dataWriteDecimalText is decimal.TryParse with NumberStyles.Float and its invariant text: the digits as
// given (trailing zeros of the fraction kept), leading zeros and a '+' dropped, an exponent applied.
func dataWriteDecimalText(text string) (string, bool) {
	if text == "" {
		return "", false
	}
	if strings.ContainsAny(text, "eE") {
		value, ok := new(big.Rat).SetString(text)
		if !ok {
			return "", false
		}
		return strings.TrimRight(strings.TrimRight(value.FloatString(28), "0"), "."), true
	}
	sign := ""
	if text[0] == '-' || text[0] == '+' {
		if text[0] == '-' {
			sign = "-"
		}
		text = text[1:]
	}
	whole, fraction, hasFraction := strings.Cut(text, ".")
	if whole == "" && fraction == "" {
		return "", false
	}
	for _, part := range []string{whole, fraction} {
		for _, r := range part {
			if r < '0' || r > '9' {
				return "", false
			}
		}
	}
	whole = strings.TrimLeft(whole, "0")
	if whole == "" {
		whole = "0"
	}
	result := whole
	if hasFraction && fraction != "" {
		result += "." + fraction
	}
	if sign == "-" && strings.Trim(result, "0.") == "" {
		sign = ""
	}
	return sign + result, true
}
