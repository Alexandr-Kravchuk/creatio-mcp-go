package creatio

// create-user-task and modify-user-task-parameters: clio's CreateUserTaskCommand (verb add-user-task) and
// ModifyUserTaskParametersCommand with UserTaskSchemaSupport. Both work on a package of a local clio
// workspace: the user task designer service creates or reads the schema, clio fills or edits it and saves
// it, then builds the package. An explicit parameter direction is not carried by the designer's DTO, so it is
// written into the workspace metadata and loaded to the database (file design mode), then built again.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	processWriteTaskTimeout       = 100 * time.Second
	processWriteTaskResponseBytes = 128 << 20
	processWriteTaskDefaultColor  = "#839DC3"
	processWriteTaskTextIcon      = "data-type-text-icon.svg"
	processWriteCompositeType     = 39
	processWriteCompositeGUID     = 0
	processWriteGUIDType          = 11
)

// ProcessWriteTaskParameter is the MCP shape of one parameter (UserTaskParameterArgs, and with Parent the
// UserTaskParameterItemArgs). Optional flags are nil when the caller sent none.
type ProcessWriteTaskParameter struct {
	Parent              string
	Code                string
	Title               string
	Type                string
	Lookup              string
	Direction           string
	Required            *bool
	Resulting           *bool
	Serializable        *bool
	CopyValue           *bool
	LazyLoad            *bool
	ContainsPerformerID *bool
	Items               []ProcessWriteTaskParameter
}

// ProcessWriteCreateTaskRequest is create-user-task's options.
type ProcessWriteCreateTaskRequest struct {
	Code           string
	Package        string
	Title          string
	Description    *string
	Culture        string
	WorkspacePath  string
	Parameters     []string
	ParameterItems []string
}

// ProcessWriteModifyTaskRequest is modify-user-task-parameters' options.
type ProcessWriteModifyTaskRequest struct {
	UserTaskName      string
	Culture           string
	WorkspacePath     string
	AddParameters     []string
	AddParameterItems []string
	RemoveParameters  []string
	SetDirections     []string
}

// ---------------------------------------------------------------------------------------- MCP serialization

// ProcessWriteTaskDefinitions is UserTaskToolSupport.SerializeParameterDefinitions: one
// "code=..;title=..;type=.." definition per parameter, nil for none.
func ProcessWriteTaskDefinitions(parameters []ProcessWriteTaskParameter) []string {
	if parameters == nil {
		return nil
	}
	definitions := make([]string, 0, len(parameters))
	for _, parameter := range parameters {
		definitions = append(definitions, processWriteTaskDefinition("", parameter, false))
	}
	return definitions
}

// ProcessWriteTaskNestedItems is SerializeNestedParameterItemDefinitions over the parameters' items.
func ProcessWriteTaskNestedItems(parameters []ProcessWriteTaskParameter) []string {
	if parameters == nil {
		return nil
	}
	items := []string{}
	for _, parameter := range parameters {
		items = append(items, processWriteTaskNested(parameter)...)
	}
	return items
}

// ProcessWriteTaskItems is SerializeParameterItemDefinitions over add-parameter-items: each item with its
// own parent, then its nested items.
func ProcessWriteTaskItems(items []ProcessWriteTaskParameter) []string {
	if items == nil {
		return nil
	}
	definitions := []string{}
	for _, item := range items {
		definitions = append(definitions, processWriteTaskDefinition(item.Parent, item, true))
		for _, child := range item.Items {
			definitions = append(definitions, processWriteTaskDefinition(item.Code, child, true))
			definitions = append(definitions, processWriteTaskNested(child)...)
		}
	}
	return definitions
}

func processWriteTaskNested(parameter ProcessWriteTaskParameter) []string {
	definitions := []string{}
	for _, item := range parameter.Items {
		definitions = append(definitions, processWriteTaskDefinition(parameter.Code, item, true))
		definitions = append(definitions, processWriteTaskNested(item)...)
	}
	return definitions
}

func processWriteTaskDefinition(parent string, parameter ProcessWriteTaskParameter, withParent bool) string {
	segments := []string{}
	if withParent {
		segments = append(segments, "parent="+parent)
	}
	segments = append(segments, "code="+parameter.Code, "title="+parameter.Title, "type="+parameter.Type)
	for _, optional := range []struct{ key, value string }{{"lookup", parameter.Lookup}, {"direction", parameter.Direction}} {
		if strings.TrimSpace(optional.value) != "" {
			segments = append(segments, optional.key+"="+strings.TrimSpace(optional.value))
		}
	}
	for _, flag := range []struct {
		key   string
		value *bool
	}{{"required", parameter.Required}, {"resulting", parameter.Resulting}, {"serializable", parameter.Serializable},
		{"copyValue", parameter.CopyValue}, {"lazyLoad", parameter.LazyLoad}, {"containsPerformerId", parameter.ContainsPerformerID}} {
		if flag.value != nil {
			segments = append(segments, fmt.Sprintf("%s=%t", flag.key, *flag.value))
		}
	}
	return strings.Join(segments, ";")
}

// ----------------------------------------------------------------------------------------------- DTOs

// processWriteGUID is a System.Text.Json Guid: a string in the 36-character form, written lower-case.
type processWriteGUID string

func (g processWriteGUID) MarshalJSON() ([]byte, error) {
	if g == "" {
		return []byte(`"` + processWriteEmptyGUID + `"`), nil
	}
	return json.Marshal(string(g))
}

func (g *processWriteGUID) UnmarshalJSON(data []byte) error {
	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		return fmt.Errorf("The JSON value could not be converted to System.Guid.")
	}
	parsed := ""
	if len(text) == 36 {
		parsed = processWriteParseGUID(text)
	}
	if parsed == "" {
		return fmt.Errorf("The JSON value could not be converted to System.Guid.")
	}
	*g = processWriteGUID(parsed)
	return nil
}

type processWriteLocalizable struct {
	CultureName *string `json:"cultureName"`
	Value       *string `json:"value"`
}

type processWriteTaskParameterDTO struct {
	UID                 processWriteGUID                `json:"uId"`
	Name                *string                         `json:"name"`
	Caption             []processWriteLocalizable       `json:"caption"`
	ItemProperties      []*processWriteTaskParameterDTO `json:"itemProperties"`
	Type                int                             `json:"type"`
	Value               *string                         `json:"value,omitempty"`
	Lookup              *processWriteGUID               `json:"lookup,omitempty"`
	Schema              *string                         `json:"schema,omitempty"`
	Required            *bool                           `json:"required,omitempty"`
	Resulting           bool                            `json:"resulting"`
	ContainsPerformerID *bool                           `json:"containsPerformerId,omitempty"`
	LazyLoad            *bool                           `json:"lazyLoad,omitempty"`
	Serializable        bool                            `json:"serializable"`
	CopyValue           *bool                           `json:"copyValue,omitempty"`
	Direction           *int                            `json:"direction,omitempty"`
	Icon                *string                         `json:"icon"`
}

type processWriteKeyValue struct {
	Key   *string         `json:"key"`
	Value json.RawMessage `json:"value"`
}

type processWriteImage struct {
	IsChanged    bool    `json:"_isChanged"`
	ImageContent *string `json:"imageContent"`
}

type processWritePackageDTO struct {
	Name *string          `json:"name"`
	UID  processWriteGUID `json:"uId"`
	Type int              `json:"type"`
}

// processWriteTaskSchema is ProcessUserTaskDesignSchemaDto: what the designer returns and what SaveSchema
// receives, in its declaration order; fields the DTO does not declare are not kept.
type processWriteTaskSchema struct {
	UID                         processWriteGUID                `json:"uId"`
	ID                          processWriteGUID                `json:"id"`
	Name                        *string                         `json:"name"`
	Body                        *string                         `json:"body"`
	MetaData                    *string                         `json:"metaData"`
	Caption                     []processWriteLocalizable       `json:"caption"`
	Description                 []processWriteLocalizable       `json:"description"`
	Parameters                  []*processWriteTaskParameterDTO `json:"parameters"`
	LocalizableStrings          []json.RawMessage               `json:"localizableStrings"`
	AddonTypes                  []*string                       `json:"addonTypes"`
	OptionalProperties          []processWriteKeyValue          `json:"optionalProperties"`
	Dependencies                json.RawMessage                 `json:"dependencies"`
	IsReadOnly                  bool                            `json:"isReadOnly"`
	UseFullHierarchy            bool                            `json:"useFullHierarchy"`
	ForceSave                   bool                            `json:"forceSave"`
	IsUserLevelSchema           bool                            `json:"userLevelSchema"`
	IsFullHierarchyDesignSchema bool                            `json:"isFullHierarchyDesignSchema"`
	IsPartial                   bool                            `json:"partial"`
	IsUserTask                  bool                            `json:"userTask"`
	EnableCustomEventHandlers   bool                            `json:"customEventHandler"`
	EditPageSchemaUID           processWriteGUID                `json:"editPageSchemaUId"`
	DcmEditPageSchemaUID        processWriteGUID                `json:"dcmEditPageSchemaUId"`
	SmallSvgImage               *processWriteImage              `json:"smallSvgImage"`
	LargeSvgImage               *processWriteImage              `json:"largeSvgImage"`
	TitleSvgImage               *processWriteImage              `json:"titleSvgImage"`
	DcmSmallSvgImage            *processWriteImage              `json:"dcmSmallSvgImage"`
	Color                       *string                         `json:"color"`
	SerializeToDB               bool                            `json:"serializeToDB"`
	Package                     *processWritePackageDTO         `json:"package"`
}

type processWriteBaseResponse struct {
	Success   bool `json:"success"`
	ErrorInfo *struct {
		Message *string `json:"message"`
	} `json:"errorInfo"`
}

type processWriteSchemaResponse struct {
	processWriteBaseResponse
	Schema *processWriteTaskSchema `json:"schema"`
}

type processWriteSaveResponse struct {
	processWriteBaseResponse
	SchemaUID        processWriteGUID `json:"schemaUid"`
	ValidationErrors []struct {
		Source    *string `json:"source"`
		Reference *string `json:"reference"`
		Package   *string `json:"package"`
	} `json:"validationErrors"`
}

// --------------------------------------------------------------------------------------------- workspace

// processWriteWorkspace is the part of clio's WorkspacePathBuilder these commands use.
type processWriteWorkspace struct {
	root string
}

// newProcessWriteWorkspace uses the explicit path (trimmed) or, like clio, the nearest directory holding a
// .clio folder above the current directory, else the current directory.
func newProcessWriteWorkspace(path string) processWriteWorkspace {
	if strings.TrimSpace(path) != "" {
		return processWriteWorkspace{root: strings.TrimSpace(path)}
	}
	current, err := os.Getwd()
	if err != nil {
		return processWriteWorkspace{}
	}
	for directory := current; ; {
		if info, err := os.Stat(filepath.Join(directory, ".clio")); err == nil && info.IsDir() {
			return processWriteWorkspace{root: directory}
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return processWriteWorkspace{root: current}
		}
		directory = parent
	}
}

func (w processWriteWorkspace) settingsPath() string {
	return filepath.Join(w.root, ".clio", "workspaceSettings.json")
}

func (w processWriteWorkspace) isWorkspace() bool { return processWriteFileExists(w.settingsPath()) }

func (w processWriteWorkspace) packagePath(name string) string {
	return filepath.Join(w.root, "packages", name)
}

func (w processWriteWorkspace) metadataPath(pkg, schema string) string {
	return filepath.Join(w.packagePath(pkg), "Schemas", schema, "metadata.json")
}

// processWriteWorkspacePackage is CreateUserTaskCommand.ResolveWorkspacePackage.
type processWriteWorkspacePackage struct {
	name string
	uid  string
	typ  int
}

func (w processWriteWorkspace) resolvePackage(name string) (processWriteWorkspacePackage, error) {
	if !w.isWorkspace() {
		return processWriteWorkspacePackage{}, errors.New("Current directory is not a workspace. Please run this command from a workspace directory.")
	}
	packagePath := w.packagePath(name)
	descriptorPath := filepath.Join(packagePath, "descriptor.json")
	if info, err := os.Stat(packagePath); err != nil || !info.IsDir() || !processWriteFileExists(descriptorPath) {
		return processWriteWorkspacePackage{}, fmt.Errorf("Package '%s' is not part of the current workspace.", name)
	}
	data, err := os.ReadFile(descriptorPath)
	if err != nil {
		return processWriteWorkspacePackage{}, err
	}
	var document struct {
		Descriptor *struct {
			UID  string          `json:"UId"`
			Name *string         `json:"Name"`
			Type json.RawMessage `json:"Type"`
		} `json:"Descriptor"`
	}
	if err := json.Unmarshal(processWriteStripBOM(data), &document); err != nil {
		return processWriteWorkspacePackage{}, fmt.Errorf("Package '%s' has an invalid descriptor at '%s'.", name, descriptorPath)
	}
	if document.Descriptor == nil {
		return processWriteWorkspacePackage{}, fmt.Errorf("Package '%s' has an invalid descriptor at '%s'.", name, descriptorPath)
	}
	resolved := processWriteWorkspacePackage{name: processWriteDeref(document.Descriptor.Name), uid: processWriteParseGUID(document.Descriptor.UID)}
	if resolved.uid == "" {
		resolved.uid = processWriteEmptyGUID
	}
	_ = json.Unmarshal(document.Descriptor.Type, &resolved.typ)
	return resolved, nil
}

func processWriteStripBOM(data []byte) []byte {
	return []byte(strings.TrimPrefix(string(data), "\xef\xbb\xbf"))
}

// -------------------------------------------------------------------------------------- commands

// processWriteTaskCommand carries one RemoteCommand run: its log and the workspace.
type processWriteTaskCommand struct {
	client    *Client
	log       *processWriteLog
	workspace processWriteWorkspace
}

// CreateUserTask is CreateUserTaskCommand (verb add-user-task) as BaseTool runs it.
func (c *Client) CreateUserTask(ctx context.Context, request ProcessWriteCreateTaskRequest) ProcessWriteResult {
	command := &processWriteTaskCommand{client: c, log: &processWriteLog{}, workspace: newProcessWriteWorkspace(request.WorkspacePath)}
	if err := command.create(ctx, request); err != nil {
		command.log.error(err.Error())
		return command.result(1)
	}
	command.log.info("Done add-user-task")
	return command.result(0)
}

// ModifyUserTaskParameters is ModifyUserTaskParametersCommand as BaseTool runs it.
func (c *Client) ModifyUserTaskParameters(ctx context.Context, request ProcessWriteModifyTaskRequest) ProcessWriteResult {
	command := &processWriteTaskCommand{client: c, log: &processWriteLog{}, workspace: newProcessWriteWorkspace(request.WorkspacePath)}
	if err := command.modify(ctx, request); err != nil {
		command.log.error(err.Error())
		return command.result(1)
	}
	command.log.info("Done modify-user-task-parameters")
	return command.result(0)
}

// result is the RemoteCommand envelope: no compile note (these tools build the package themselves).
func (p *processWriteTaskCommand) result(exitCode int) ProcessWriteResult {
	messages := p.log.messages
	if messages == nil {
		messages = []LogMessage{}
	}
	return ProcessWriteResult{ExitCode: exitCode, Messages: messages}
}

func (p *processWriteTaskCommand) create(ctx context.Context, request ProcessWriteCreateTaskRequest) error {
	pkg, err := p.workspace.resolvePackage(request.Package)
	if err != nil {
		return err
	}
	code := strings.TrimSpace(request.Code)
	if code == "" {
		return errors.New("User task code cannot be empty.")
	}
	if !strings.HasPrefix(code, "Usr") {
		return fmt.Errorf("User task code '%s' must start with 'Usr'.", code)
	}
	directions, err := processWriteTaskDirections(request.Parameters)
	if err != nil {
		return err
	}
	createBody, _ := json.Marshal(struct {
		PackageUID processWriteGUID `json:"packageUId"`
		Cultures   []string         `json:"cultures"`
	}{PackageUID: processWriteGUID(pkg.uid)})
	var created processWriteSchemaResponse
	if err := p.post(ctx, "ServiceModel/ProcessUserTaskSchemaDesignerService.svc/CreateNewSchema", createBody, "CreateNewSchema", &created); err != nil {
		return err
	}
	if err := processWriteTaskSucceeded(created.processWriteBaseResponse, "CreateNewSchema"); err != nil {
		return err
	}
	schema := created.Schema
	if schema == nil {
		return errors.New("CreateNewSchema did not return a schema payload.")
	}
	resolve := func(value string) (*processWriteGUID, error) { return p.resolveLookup(ctx, pkg.uid, value) }
	schema.Name = &code
	schema.Caption = processWriteTaskLocalizable(request.Culture, &request.Title, false)
	schema.Description = processWriteTaskLocalizable(request.Culture, request.Description, true)
	parameters, err := processWriteTaskBuildParameters(request.Culture, request.Parameters, resolve)
	if err != nil {
		return err
	}
	schema.Parameters = parameters
	items, err := processWriteTaskBuildItems(request.Culture, request.ParameterItems, resolve)
	if err != nil {
		return err
	}
	if err := processWriteTaskAttachItems(schema.Parameters, items); err != nil {
		return err
	}
	if schema.LocalizableStrings == nil {
		schema.LocalizableStrings = []json.RawMessage{}
	}
	if schema.OptionalProperties == nil {
		schema.OptionalProperties = []processWriteKeyValue{}
	}
	if schema.Body == nil {
		empty := ""
		schema.Body = &empty
	}
	if schema.Color == nil || strings.TrimSpace(*schema.Color) == "" {
		color := processWriteTaskDefaultColor
		schema.Color = &color
	}
	schema.IsPartial, schema.IsUserTask, schema.EnableCustomEventHandlers, schema.SerializeToDB = false, true, false, true
	schema.UseFullHierarchy, schema.IsUserLevelSchema, schema.IsFullHierarchyDesignSchema, schema.ForceSave = false, false, false, false
	if schema.Package == nil {
		schema.Package = &processWritePackageDTO{}
	}
	name := pkg.name
	schema.Package.Name, schema.Package.UID, schema.Package.Type = &name, processWriteGUID(pkg.uid), pkg.typ
	for _, image := range []**processWriteImage{&schema.SmallSvgImage, &schema.LargeSvgImage, &schema.TitleSvgImage, &schema.DcmSmallSvgImage} {
		if *image == nil {
			*image = &processWriteImage{}
		}
	}
	metadata := processWriteTaskMetadata(string(schema.UID), code, pkg.uid, *schema.Color)
	schema.MetaData = &metadata
	saved, err := p.save(ctx, schema)
	if err != nil {
		return err
	}
	p.log.info(fmt.Sprintf("Created user task schema '%s' (%s).", code, saved))
	if err := p.buildPackage(ctx, pkg.name); err != nil {
		return err
	}
	if len(directions.keys) == 0 {
		return nil
	}
	return p.applyDirections(ctx, pkg.name, code, directions, fmt.Sprintf("The user task schema '%s' has already been created and built on the environment, so do not "+
		"re-run add-user-task: enable file system development mode (clio turn-fsm on) and run "+
		"'clio pkg-to-db' followed by 'clio compile-package %s' to finish applying "+
		"the directions.", code, pkg.name))
}

func (p *processWriteTaskCommand) modify(ctx context.Context, request ProcessWriteModifyTaskRequest) error {
	if info, err := os.Stat(p.workspace.root); err != nil || !info.IsDir() || !p.workspace.isWorkspace() {
		return errors.New("Current directory is not a workspace. Please run this command from a workspace directory.")
	}
	name := strings.TrimSpace(request.UserTaskName)
	if name == "" {
		return errors.New("User task name cannot be empty.")
	}
	removals, err := processWriteTaskRemovals(request.RemoveParameters)
	if err != nil {
		return err
	}
	added, err := processWriteTaskDirections(request.AddParameters)
	if err != nil {
		return err
	}
	updates, err := processWriteTaskDirectionUpdates(request.SetDirections)
	if err != nil {
		return err
	}
	if len(removals) == 0 && len(request.AddParameters) == 0 && len(request.AddParameterItems) == 0 && len(updates.keys) == 0 {
		return errors.New("Specify at least one `--add-parameter`, `--add-parameter-item`, `--remove-parameter`, or `--set-direction` operation.")
	}
	packages, err := p.workspacePackages()
	if err != nil {
		return err
	}
	item, err := p.findWorkspaceTask(ctx, name, packages)
	if err != nil {
		return err
	}
	schema, err := p.loadSchema(ctx, item)
	if err != nil {
		return err
	}
	existing, err := p.readDirections(item.packageName, processWriteDeref(schema.Name))
	if err != nil {
		return err
	}
	preserved := processWriteDirections{}
	for _, key := range existing.keys {
		if processWriteContainsFold(removals, key) || !processWriteTaskHasParameter(schema.Parameters, key) {
			continue
		}
		preserved.set(key, existing.values[strings.ToLower(key)])
	}
	resolve := func(value string) (*processWriteGUID, error) { return p.resolveLookup(ctx, item.packageUID, value) }
	toAdd, err := processWriteTaskBuildParameters(request.Culture, request.AddParameters, resolve)
	if err != nil {
		return err
	}
	itemsToAdd, err := processWriteTaskBuildItems(request.Culture, request.AddParameterItems, resolve)
	if err != nil {
		return err
	}
	if err := processWriteTaskApplyChanges(schema, toAdd, removals, updates); err != nil {
		return err
	}
	if err := processWriteTaskAttachItems(schema.Parameters, itemsToAdd); err != nil {
		return err
	}
	saved, err := p.save(ctx, schema)
	if err != nil {
		return err
	}
	schemaName := processWriteDeref(schema.Name)
	p.log.info(fmt.Sprintf("Updated user task schema '%s' (%s).", schemaName, saved))
	if err := p.buildPackage(ctx, item.packageName); err != nil {
		return err
	}
	merged := processWriteDirections{}
	for _, source := range []processWriteDirections{preserved, added, updates} {
		for _, key := range source.keys {
			merged.set(key, source.values[strings.ToLower(key)])
		}
	}
	if len(merged.keys) == 0 {
		return nil
	}
	return p.applyDirections(ctx, item.packageName, schemaName, merged, fmt.Sprintf("The other parameter changes to '%s' were already saved and built on the environment, so only "+
		"the directions are missing: enable file system development mode (clio turn-fsm on) and run "+
		"'clio pkg-to-db' followed by 'clio compile-package %s'.", schemaName, item.packageName))
}

// post sends one designer request and decodes the answer; a JSON null is "<operation> returned an empty
// response.".
func (p *processWriteTaskCommand) post(ctx context.Context, route string, body []byte, operation string, target any) error {
	payload, err := p.client.callService(ctx, serviceCall{Route: route, Body: body, Timeout: processWriteTaskTimeout, Limit: processWriteTaskResponseBytes})
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(payload)) == "null" {
		return fmt.Errorf("%s returned an empty response.", operation)
	}
	if err := json.Unmarshal(processWriteStripBOM(payload), target); err != nil {
		return fmt.Errorf("%s returned a response that is not valid JSON: %v", operation, err)
	}
	return nil
}

func processWriteTaskSucceeded(response processWriteBaseResponse, operation string) error {
	if response.Success {
		return nil
	}
	if response.ErrorInfo != nil && response.ErrorInfo.Message != nil && strings.TrimSpace(*response.ErrorInfo.Message) != "" {
		return errors.New(*response.ErrorInfo.Message)
	}
	return fmt.Errorf("%s failed.", operation)
}

// save is SaveSchema with EnsureSaveSucceeded; it returns the saved schema's UId.
func (p *processWriteTaskCommand) save(ctx context.Context, schema *processWriteTaskSchema) (string, error) {
	body, err := json.Marshal(schema)
	if err != nil {
		return "", err
	}
	var saved processWriteSaveResponse
	if err := p.post(ctx, "ServiceModel/ProcessUserTaskSchemaDesignerService.svc/SaveSchema", body, "SaveSchema", &saved); err != nil {
		return "", err
	}
	if !saved.Success && len(saved.ValidationErrors) > 0 {
		parts := make([]string, len(saved.ValidationErrors))
		for index, failure := range saved.ValidationErrors {
			parts[index] = fmt.Sprintf("%s -> %s (%s)", processWriteDeref(failure.Source), processWriteDeref(failure.Reference), processWriteDeref(failure.Package))
		}
		return "", fmt.Errorf("SaveSchema failed validation: %s", strings.Join(parts, "; "))
	}
	if err := processWriteTaskSucceeded(saved.processWriteBaseResponse, "SaveSchema"); err != nil {
		return "", err
	}
	uid := string(saved.SchemaUID)
	if uid == "" {
		uid = processWriteEmptyGUID
	}
	return uid, nil
}

// buildPackage is BuildPackage: the answer is not read, only a failed request fails.
func (p *processWriteTaskCommand) buildPackage(ctx context.Context, name string) error {
	body, _ := json.Marshal(struct {
		PackageName string `json:"packageName"`
	}{name})
	p.log.info(fmt.Sprintf("Building package '%s'...", name))
	_, err := p.client.callService(ctx, serviceCall{Route: "ServiceModel/WorkspaceExplorerService.svc/BuildPackage", Body: body, Timeout: processWriteTaskTimeout, Limit: processWriteTaskResponseBytes})
	return err
}

// resolveLookup is UserTaskLookupSchemaResolver.Resolve: a schema UId or a unique name among the entity
// schemas available to the package.
func (p *processWriteTaskCommand) resolveLookup(ctx context.Context, packageUID, value string) (*processWriteGUID, error) {
	lookup := strings.TrimSpace(value)
	if lookup == "" {
		return nil, errors.New("Lookup parameters must include a non-empty 'lookup' value with an entity schema name or schema UId.")
	}
	body, _ := json.Marshal(struct {
		PackageUID processWriteGUID `json:"packageUId"`
	}{processWriteGUID(packageUID)})
	var response struct {
		Items []struct {
			UID  processWriteGUID `json:"uId"`
			Name *string          `json:"name"`
		} `json:"items"`
	}
	if err := p.post(ctx, "ServiceModel/SchemaDataDesignerService.svc/GetAvailableEntitySchemas", body, "GetAvailableEntitySchemas", &response); err != nil {
		return nil, err
	}
	if uid := processWriteParseGUID(lookup); uid != "" {
		for _, item := range response.Items {
			if string(item.UID) == uid {
				found := item.UID
				return &found, nil
			}
		}
		return nil, fmt.Errorf("Lookup schema '%s' was not found in Creatio.", lookup)
	}
	var matches []processWriteGUID
	for _, item := range response.Items {
		if item.Name != nil && strings.EqualFold(*item.Name, lookup) {
			matches = append(matches, item.UID)
		}
	}
	switch len(matches) {
	case 1:
		return &matches[0], nil
	case 0:
		return nil, fmt.Errorf("Lookup schema '%s' was not found in Creatio.", lookup)
	}
	return nil, fmt.Errorf("Lookup schema '%s' resolved to multiple Creatio schemas.", lookup)
}

func (p *processWriteTaskCommand) workspacePackages() ([]string, error) {
	data, err := os.ReadFile(p.workspace.settingsPath())
	if err != nil {
		return nil, err
	}
	var settings struct {
		Packages []string `json:"Packages"`
	}
	_ = json.Unmarshal(processWriteStripBOM(data), &settings)
	packages := []string{}
	for _, name := range settings.Packages {
		if !processWriteContainsFold(packages, name) {
			packages = append(packages, name)
		}
	}
	if len(packages) == 0 {
		return nil, errors.New("The current workspace does not contain any packages.")
	}
	return packages, nil
}

type processWriteWorkspaceItem struct {
	uid         string
	name        string
	packageUID  string
	packageName string
}

// findWorkspaceTask is FindWorkspaceUserTaskItem: exactly one workspace item of that name in a workspace
// package.
func (p *processWriteTaskCommand) findWorkspaceTask(ctx context.Context, name string, packages []string) (processWriteWorkspaceItem, error) {
	var response struct {
		Items []struct {
			UID         processWriteGUID `json:"uId"`
			Name        *string          `json:"name"`
			PackageUID  processWriteGUID `json:"packageUId"`
			PackageName *string          `json:"packageName"`
		} `json:"items"`
	}
	if err := p.post(ctx, "ServiceModel/WorkspaceExplorerService.svc/GetWorkspaceItems", []byte{}, "GetWorkspaceItems", &response); err != nil {
		return processWriteWorkspaceItem{}, err
	}
	var matches []processWriteWorkspaceItem
	for _, item := range response.Items {
		if item.PackageName == nil || !processWriteContainsFold(packages, *item.PackageName) || item.Name == nil || !strings.EqualFold(*item.Name, name) {
			continue
		}
		matches = append(matches, processWriteWorkspaceItem{uid: string(item.UID), name: *item.Name, packageUID: string(item.PackageUID), packageName: *item.PackageName})
	}
	switch len(matches) {
	case 0:
		return processWriteWorkspaceItem{}, fmt.Errorf("User task '%s' is not part of the current workspace.", name)
	case 1:
		return matches[0], nil
	}
	names := []string{}
	for _, match := range matches {
		if !processWriteContainsFold(names, match.packageName) {
			names = append(names, match.packageName)
		}
	}
	return processWriteWorkspaceItem{}, fmt.Errorf("User task '%s' exists in multiple workspace packages: %s. Update is ambiguous.", name, strings.Join(names, ", "))
}

func (p *processWriteTaskCommand) loadSchema(ctx context.Context, item processWriteWorkspaceItem) (*processWriteTaskSchema, error) {
	body, _ := json.Marshal(struct {
		SchemaUID        processWriteGUID `json:"schemaUId"`
		UseFullHierarchy bool             `json:"useFullHierarchy"`
		Cultures         []string         `json:"cultures"`
	}{SchemaUID: processWriteGUID(item.uid), Cultures: []string{}})
	var response processWriteSchemaResponse
	if err := p.post(ctx, "ServiceModel/ProcessUserTaskSchemaDesignerService.svc/GetSchema", body, "GetSchema", &response); err != nil {
		return nil, err
	}
	if err := processWriteTaskSucceeded(response.processWriteBaseResponse, "GetSchema"); err != nil {
		return nil, err
	}
	if response.Schema == nil {
		return nil, fmt.Errorf("GetSchema did not return schema payload for '%s'.", item.name)
	}
	return response.Schema, nil
}

// ----------------------------------------------------------------------------------------- directions

// processWriteDirections is a case-insensitive, insertion-ordered name -> direction map.
type processWriteDirections struct {
	keys   []string
	values map[string]int
}

func (d *processWriteDirections) set(key string, value int) {
	if d.values == nil {
		d.values = map[string]int{}
	}
	lower := strings.ToLower(key)
	if _, exists := d.values[lower]; !exists {
		d.keys = append(d.keys, key)
	}
	d.values[lower] = value
}

func (d *processWriteDirections) has(key string) bool {
	_, ok := d.values[strings.ToLower(key)]
	return ok
}

// readDirections is UserTaskMetadataDirectionApplier.ReadDirections: L12 of every FJ1 parameter in the
// workspace metadata, none when the file is absent.
func (p *processWriteTaskCommand) readDirections(pkg, schema string) (processWriteDirections, error) {
	directions := processWriteDirections{}
	path := p.workspace.metadataPath(pkg, schema)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return directions, nil
		}
		return directions, err
	}
	root, err := parseJNode(processWriteStripBOM(data))
	if err != nil {
		return directions, err
	}
	parameters := root.get("MetaData").get("Schema").get("FJ1")
	if root.get("MetaData").get("Schema") == nil {
		return directions, errors.New("The given key was not present in the dictionary.")
	}
	if !parameters.isArray() {
		return directions, nil
	}
	for _, parameter := range parameters.items {
		if direction := parameter.get("L12"); direction != nil {
			var value int
			_ = json.Unmarshal([]byte(direction.text), &value)
			name := processWriteNodeString(parameter.get("A2"))
			if directions.has(name) {
				return directions, errors.New("An item with the same key has already been added.")
			}
			directions.set(name, value)
		}
	}
	return directions, nil
}

// applyDirections writes the directions into the workspace metadata, loads the workspace packages to the
// database and builds the package again.
func (p *processWriteTaskCommand) applyDirections(ctx context.Context, pkg, schema string, directions processWriteDirections, recovery string) error {
	p.log.info(fmt.Sprintf("Applying direction metadata for %d parameter(s) on '%s'...", len(directions.keys), schema))
	path := p.workspace.metadataPath(pkg, schema)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("User task metadata file was not found at '%s'.", path)
		}
		return err
	}
	root, err := parseJNode(processWriteStripBOM(data))
	if err != nil || !root.isObject() {
		return fmt.Errorf("User task metadata file '%s' is empty or invalid.", path)
	}
	parameters := root.get("MetaData").get("Schema").get("FJ1")
	if !parameters.isArray() {
		return fmt.Errorf("User task metadata file '%s' does not contain schema parameter metadata.", path)
	}
	for _, key := range directions.keys {
		var target *jnode
		for _, parameter := range parameters.items {
			if parameter.isObject() && strings.EqualFold(processWriteNodeString(parameter.get("A2")), key) {
				target = parameter
				break
			}
		}
		if target == nil {
			return fmt.Errorf("Parameter '%s' was not found in metadata for user task '%s'.", key, schema)
		}
		target.set("L12", newInt(directions.values[strings.ToLower(key)]))
	}
	if err := os.WriteFile(path, []byte(stjIndented(root)), 0o644); err != nil {
		return err
	}
	if err := p.loadPackagesToDB(ctx, schema, recovery); err != nil {
		return err
	}
	return p.buildPackage(ctx, pkg)
}

// loadPackagesToDB is UserTaskSchemaSupport.LoadWorkspacePackagesToDatabase over FileDesignModePackages.
func (p *processWriteTaskCommand) loadPackagesToDB(ctx context.Context, schema, recovery string) error {
	p.log.info("Loading workspace packages to database to apply direction metadata changes...")
	result, err := p.loadToStorage(ctx)
	if err != nil {
		return err
	}
	if result == "completed" {
		return nil
	}
	reason := "the platform refused to load the workspace packages"
	switch result {
	case "disabled":
		reason = "file system development mode is disabled on the environment"
	case "unknown":
		reason = "the file system development mode state of the environment could not be read"
	}
	return fmt.Errorf("Parameter direction metadata for user task '%s' was written to the workspace but not "+
		"applied on the environment: %s. %s", schema, reason, recovery)
}

type processWriteErrorInfo struct {
	Message   *string `json:"message"`
	ErrorCode *string `json:"errorCode"`
}

func processWriteErrorDetails(info *processWriteErrorInfo) string {
	if info == nil {
		return "unknown error"
	}
	return fmt.Sprintf("%s (error code: %s)", processWriteDeref(info.Message), processWriteDeref(info.ErrorCode))
}

// loadToStorage is FileDesignModeFileDesignModePackages.LoadPackagesToStorage for the database: a failed
// request or an unreadable answer is raised, as clio lets it escape to the command.
func (p *processWriteTaskCommand) loadToStorage(ctx context.Context) (string, error) {
	const storage = "database"
	failure := func(text string) {
		p.log.error("Load packages to " + storage + " on a web application ended with error: " + text)
	}
	payload, err := p.client.callService(ctx, serviceCall{Route: "ServiceModel/WorkspaceExplorerService.svc/GetIsFileDesignMode", Body: []byte{}, Timeout: 10 * time.Minute, Limit: maxResponseBytes})
	if err != nil {
		return "", err
	}
	var mode struct {
		Success   bool                   `json:"success"`
		Value     bool                   `json:"value"`
		ErrorInfo *processWriteErrorInfo `json:"errorInfo"`
	}
	if err := json.Unmarshal(processWriteStripBOM(payload), &mode); err != nil {
		return "", err
	}
	if !mode.Success {
		p.log.error("Get file design mode ended with error: " + processWriteErrorDetails(mode.ErrorInfo))
		failure("file design mode state is unknown")
		return "unknown", nil
	}
	if !mode.Value {
		return "disabled", nil
	}
	p.log.add("None", "Start load packages to "+storage+" on a web application")
	payload, err = p.client.callService(ctx, serviceCall{Route: "ServiceModel/AppInstallerService.svc/LoadPackagesToDB", Body: []byte{}, Timeout: 30 * time.Minute, Limit: maxResponseBytes})
	if err != nil {
		return "", err
	}
	var response struct {
		Success   bool                   `json:"success"`
		ErrorInfo *processWriteErrorInfo `json:"errorInfo"`
		Errors    []*struct {
			WorkspaceItem *struct {
				Name *string `json:"name"`
			} `json:"workspaceItem"`
			ErrorInfo *processWriteErrorInfo `json:"errorInfo"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(processWriteStripBOM(payload), &response); err != nil {
		return "", err
	}
	if len(response.Errors) > 0 {
		if !response.Success && response.ErrorInfo != nil {
			failure(processWriteErrorDetails(response.ErrorInfo))
		}
		for _, item := range response.Errors {
			name := "unknown"
			var info *processWriteErrorInfo
			if item != nil {
				info = item.ErrorInfo
				if item.WorkspaceItem != nil && item.WorkspaceItem.Name != nil {
					name = *item.WorkspaceItem.Name
				}
			}
			failure(fmt.Sprintf("Synchronization rejected item '%s': %s", name, processWriteErrorDetails(info)))
		}
		return "refused", nil
	}
	if response.Success {
		p.log.info("Load packages to " + storage + " on a web application completed")
		return "completed", nil
	}
	failure(processWriteErrorDetails(response.ErrorInfo))
	return "refused", nil
}

// ---------------------------------------------------------------------------------- parameter parsing

var processWriteTaskTypeNames = []string{"Boolean", "Date", "DateTime", "Float", "Guid", "Integer", "Lookup", "Money",
	"Unique identifier", "Serializable list of composite values", "Text", "Unlimited text", "Time"}

type processWriteTaskType struct {
	id   int
	icon string
}

var processWriteTaskTypes = map[string]processWriteTaskType{
	"boolean": {12, "data-type-boolean-icon.svg"}, "bool": {12, "data-type-boolean-icon.svg"},
	"date": {8, "data-type-date-icon.svg"}, "datetime": {7, "data-type-datetime-icon.svg"},
	"float": {5, "data-type-float1-icon.svg"}, "double": {5, "data-type-float1-icon.svg"},
	"guid": {11, "data-type-guid-icon.svg"}, "unique identifier": {11, "data-type-guid-icon.svg"}, "uniqueidentifier": {11, "data-type-guid-icon.svg"},
	"integer": {4, "data-type-integer-icon.svg"}, "int": {4, "data-type-integer-icon.svg"},
	"lookup": {10, "data-type-lookup-icon.svg"},
	"money":  {6, "data-type-currency-icon.svg"}, "decimal": {6, "data-type-currency-icon.svg"},
	"serializable list of composite values": {processWriteCompositeType, "data-type-other-icon.svg"},
	"serializablelistofcompositevalues":     {processWriteCompositeType, "data-type-other-icon.svg"},
	"serializablecompositevaluelist":        {processWriteCompositeType, "data-type-other-icon.svg"},
	"compositevaluelist":                    {processWriteCompositeType, "data-type-other-icon.svg"},
	"text":                                  {1, processWriteTaskTextIcon}, "string": {1, processWriteTaskTextIcon},
	"unlimited text": {29, processWriteTaskTextIcon}, "maxsizetext": {29, processWriteTaskTextIcon},
	"time": {9, "data-type-time-icon.svg"},
}

var processWriteTaskDirectionValues = map[string]int{"0": 0, "in": 0, "1": 1, "out": 1, "2": 2, "variable": 2}

// processWriteTaskValues is ParseParameterDefinitionValues: ';'-separated key=value (or key:value) pairs,
// keys case-insensitive, the last one winning.
type processWriteTaskValues struct {
	values map[string]string
}

func (v processWriteTaskValues) get(key string) (string, bool) {
	value, ok := v.values[strings.ToLower(key)]
	return value, ok
}

func processWriteTaskParse(definition string) (processWriteTaskValues, error) {
	if strings.TrimSpace(definition) == "" {
		return processWriteTaskValues{}, errors.New("Parameter definition cannot be empty.")
	}
	values := processWriteTaskValues{values: map[string]string{}}
	for _, part := range strings.Split(definition, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		separator := strings.Index(part, "=")
		if separator < 0 {
			separator = strings.Index(part, ":")
		}
		if separator <= 0 {
			return values, fmt.Errorf("Parameter definition '%s' must use '<key>=<value>' pairs separated by ';'.", definition)
		}
		key, value := strings.TrimSpace(part[:separator]), strings.TrimSpace(part[separator+1:])
		if key == "" {
			return values, fmt.Errorf("Parameter definition '%s' contains an empty key.", definition)
		}
		values.values[strings.ToLower(key)] = value
	}
	return values, nil
}

func processWriteTaskRequired(values processWriteTaskValues, key, definition string) (string, error) {
	value, ok := values.get(key)
	if !ok || strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("Parameter definition '%s' must include a non-empty '%s' value.", definition, key)
	}
	return value, nil
}

func processWriteTaskBool(values processWriteTaskValues, key, definition string) (*bool, error) {
	value, ok := values.get(key)
	if !ok {
		return nil, nil
	}
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true":
		flag := true
		return &flag, nil
	case "false":
		flag := false
		return &flag, nil
	}
	return nil, fmt.Errorf("Parameter definition '%s' has invalid boolean value '%s' for '%s'.", definition, value, key)
}

func processWriteTaskDirection(value, context string) (int, error) {
	if direction, ok := processWriteTaskDirectionValues[strings.ToLower(strings.TrimSpace(value))]; ok && strings.TrimSpace(value) != "" {
		return direction, nil
	}
	return 0, fmt.Errorf("Parameter definition '%s' has invalid direction '%s'. Supported values: In, Out, Variable or 0, 1, 2.", context, value)
}

// processWriteTaskDirections is ExtractExplicitDirections.
func processWriteTaskDirections(definitions []string) (processWriteDirections, error) {
	directions := processWriteDirections{}
	for _, definition := range definitions {
		values, err := processWriteTaskParse(definition)
		if err != nil {
			return directions, err
		}
		value, ok := values.get("direction")
		if !ok || strings.TrimSpace(value) == "" {
			continue
		}
		code, err := processWriteTaskRequired(values, "code", definition)
		if err != nil {
			return directions, err
		}
		code = strings.TrimSpace(code)
		direction, err := processWriteTaskDirection(value, definition)
		if err != nil {
			return directions, err
		}
		if directions.has(code) {
			return directions, fmt.Errorf("Parameter '%s' is defined more than once with an explicit direction.", code)
		}
		directions.set(code, direction)
	}
	return directions, nil
}

type processWriteTaskResolver func(value string) (*processWriteGUID, error)

// processWriteTaskBuildParameters is BuildParameters: unique names, in order.
func processWriteTaskBuildParameters(culture string, definitions []string, resolve processWriteTaskResolver) ([]*processWriteTaskParameterDTO, error) {
	parameters := []*processWriteTaskParameterDTO{}
	seen := []string{}
	for _, definition := range definitions {
		values, err := processWriteTaskParse(definition)
		if err != nil {
			return nil, err
		}
		parameter, err := processWriteTaskParameter(culture, values, definition, resolve, false)
		if err != nil {
			return nil, err
		}
		if processWriteContainsFold(seen, *parameter.Name) {
			return nil, fmt.Errorf("Parameter '%s' is defined more than once. Parameter names must be unique.", *parameter.Name)
		}
		seen = append(seen, *parameter.Name)
		parameters = append(parameters, parameter)
	}
	return parameters, nil
}

type processWriteTaskItem struct {
	parent    string
	parameter *processWriteTaskParameterDTO
}

// processWriteTaskBuildItems is BuildParameterItems.
func processWriteTaskBuildItems(culture string, definitions []string, resolve processWriteTaskResolver) ([]processWriteTaskItem, error) {
	items := []processWriteTaskItem{}
	for _, definition := range definitions {
		values, err := processWriteTaskParse(definition)
		if err != nil {
			return nil, err
		}
		parent, err := processWriteTaskRequired(values, "parent", definition)
		if err != nil {
			return nil, err
		}
		delete(values.values, "parent")
		parameter, err := processWriteTaskParameter(culture, values, definition, resolve, true)
		if err != nil {
			return nil, err
		}
		items = append(items, processWriteTaskItem{parent: strings.TrimSpace(parent), parameter: parameter})
	}
	return items, nil
}

// processWriteTaskParameter is ParseParameter.
func processWriteTaskParameter(culture string, values processWriteTaskValues, definition string, resolve processWriteTaskResolver, item bool) (*processWriteTaskParameterDTO, error) {
	name, err := processWriteTaskRequired(values, "code", definition)
	if err != nil {
		return nil, err
	}
	title, err := processWriteTaskRequired(values, "title", definition)
	if err != nil {
		return nil, err
	}
	typeName, err := processWriteTaskRequired(values, "type", definition)
	if err != nil {
		return nil, err
	}
	kind, ok := processWriteTaskTypes[strings.ToLower(strings.TrimSpace(typeName))]
	if !ok {
		return nil, fmt.Errorf("Unsupported parameter type '%s'. Supported types: %s.", typeName, strings.Join(processWriteTaskTypeNames, ", "))
	}
	if item && kind.id == processWriteGUIDType {
		kind.id = processWriteCompositeGUID
	}
	isLookup := strings.EqualFold(strings.TrimSpace(typeName), "Lookup")
	composite := kind.id == processWriteCompositeType
	lookupValue, hasLookup := values.get("lookup")
	hasLookup = hasLookup && strings.TrimSpace(lookupValue) != ""
	var lookup *processWriteGUID
	switch {
	case !isLookup && hasLookup:
		return nil, fmt.Errorf("Parameter definition '%s' can only use 'lookup' when type=Lookup.", definition)
	case isLookup && !hasLookup:
		return nil, fmt.Errorf("Lookup parameter definition '%s' must include a non-empty 'lookup' value.", definition)
	case isLookup:
		if lookup, err = resolve(strings.TrimSpace(lookupValue)); err != nil {
			return nil, err
		}
	}
	defaults := !(item || composite)
	required, err := processWriteTaskBool(values, "required", definition)
	if err != nil {
		return nil, err
	}
	resulting, err := processWriteTaskBool(values, "resulting", definition)
	if err != nil {
		return nil, err
	}
	performer, err := processWriteTaskBool(values, "containsPerformerId", definition)
	if err != nil {
		return nil, err
	}
	lazy, err := processWriteTaskBool(values, "lazyLoad", definition)
	if err != nil {
		return nil, err
	}
	serializable, err := processWriteTaskBool(values, "serializable", definition)
	if err != nil {
		return nil, err
	}
	copyValue, err := processWriteTaskBool(values, "copyValue", definition)
	if err != nil {
		return nil, err
	}
	direction := 2
	if value, ok := values.get("direction"); ok && strings.TrimSpace(value) != "" {
		if direction, err = processWriteTaskDirection(value, definition); err != nil {
			return nil, err
		}
	}
	trimmed, cultureName, icon := strings.TrimSpace(name), culture, kind.icon
	parameter := &processWriteTaskParameterDTO{UID: processWriteGUID(processWriteNewGUID()), Name: &trimmed,
		Caption: []processWriteLocalizable{{CultureName: &cultureName, Value: &title}}, ItemProperties: []*processWriteTaskParameterDTO{},
		Type: kind.id, Lookup: lookup, Required: required, Resulting: defaults, ContainsPerformerID: performer, LazyLoad: lazy,
		Serializable: defaults, CopyValue: copyValue, Direction: &direction, Icon: &icon}
	if resulting != nil {
		parameter.Resulting = *resulting
	}
	if serializable != nil {
		parameter.Serializable = *serializable
	}
	return parameter, nil
}

// processWriteTaskFind is FindParametersByName: every parameter of that name, nested items included.
func processWriteTaskFind(parameters []*processWriteTaskParameterDTO, name string) []*processWriteTaskParameterDTO {
	found := []*processWriteTaskParameterDTO{}
	for _, parameter := range parameters {
		if parameter == nil {
			continue
		}
		if strings.EqualFold(processWriteDeref(parameter.Name), name) {
			found = append(found, parameter)
		}
		found = append(found, processWriteTaskFind(parameter.ItemProperties, name)...)
	}
	return found
}

// processWriteTaskAttachItems is AttachParameterItems.
func processWriteTaskAttachItems(parameters []*processWriteTaskParameterDTO, items []processWriteTaskItem) error {
	for _, item := range items {
		parents := processWriteTaskFind(parameters, item.parent)
		switch {
		case len(parents) == 0:
			return fmt.Errorf("Composite list parameter '%s' was not found.", item.parent)
		case len(parents) > 1:
			return fmt.Errorf("Composite list parameter '%s' is ambiguous because it appears more than once.", item.parent)
		case parents[0].Type != processWriteCompositeType:
			return fmt.Errorf("Parameter '%s' is not type=Serializable list of composite values.", item.parent)
		}
		parent := parents[0]
		if parent.ItemProperties == nil {
			parent.ItemProperties = []*processWriteTaskParameterDTO{}
		}
		for _, existing := range parent.ItemProperties {
			if existing != nil && strings.EqualFold(processWriteDeref(existing.Name), processWriteDeref(item.parameter.Name)) {
				return fmt.Errorf("Composite list parameter '%s' already contains item '%s'.", item.parent, processWriteDeref(item.parameter.Name))
			}
		}
		parent.ItemProperties = append(parent.ItemProperties, item.parameter)
	}
	return nil
}

// processWriteTaskLocalizable is BuildLocalizableValues for the primary culture only (MCP sends no
// additional localizations).
func processWriteTaskLocalizable(culture string, value *string, allowEmpty bool) []processWriteLocalizable {
	values := []processWriteLocalizable{}
	if value != nil && (allowEmpty || strings.TrimSpace(*value) != "") {
		cultureName, text := culture, *value
		values = append(values, processWriteLocalizable{CultureName: &cultureName, Value: &text})
	}
	return values
}

// processWriteTaskMetadata is BuildMetaData.
func processWriteTaskMetadata(schemaUID, name, packageUID, color string) string {
	if schemaUID == "" {
		schemaUID = processWriteEmptyGUID
	}
	encodedName, _ := json.Marshal(name)
	encodedColor, _ := json.Marshal(color)
	return `{"metaData":{"schema":{"managerName":"ProcessUserTaskSchemaManager","uId":"` + schemaUID + `","name":` + string(encodedName) +
		`,"methods":[],"localizableStrings":[],"usings":[],"packageUId":"` + packageUID + `","parameters":[],"color":` + string(encodedColor) + `}}}`
}

// processWriteTaskRemovals is NormalizeRemoveParameterNames.
func processWriteTaskRemovals(names []string) ([]string, error) {
	removals := []string{}
	for _, name := range names {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" {
			return nil, errors.New("Remove parameter name cannot be empty.")
		}
		if processWriteContainsFold(removals, trimmed) {
			return nil, fmt.Errorf("Parameter '%s' is listed more than once for removal.", trimmed)
		}
		removals = append(removals, trimmed)
	}
	return removals, nil
}

// processWriteTaskDirectionUpdates is NormalizeDirectionUpdates over "<name>=<direction>" texts.
func processWriteTaskDirectionUpdates(updates []string) (processWriteDirections, error) {
	directions := processWriteDirections{}
	for _, update := range updates {
		if strings.TrimSpace(update) == "" {
			return directions, errors.New("Set direction value cannot be empty.")
		}
		separator := strings.Index(update, "=")
		if separator <= 0 {
			separator = strings.Index(update, ":")
		}
		if separator <= 0 {
			return directions, fmt.Errorf("Set direction value '%s' must be in '<name>=<direction>' format.", update)
		}
		name, value := strings.TrimSpace(update[:separator]), strings.TrimSpace(update[separator+1:])
		if name == "" {
			return directions, fmt.Errorf("Set direction value '%s' must include a parameter name.", update)
		}
		direction, err := processWriteTaskDirection(value, update)
		if err != nil {
			return directions, err
		}
		if directions.has(name) {
			return directions, fmt.Errorf("Parameter '%s' is listed more than once for direction updates.", name)
		}
		directions.set(name, direction)
	}
	return directions, nil
}

// processWriteTaskApplyChanges is ApplyParameterChanges: removals, then additions, then direction updates.
func processWriteTaskApplyChanges(schema *processWriteTaskSchema, toAdd []*processWriteTaskParameterDTO, removals []string, updates processWriteDirections) error {
	if schema.Parameters == nil {
		schema.Parameters = []*processWriteTaskParameterDTO{}
	}
	name := processWriteDeref(schema.Name)
	for _, removal := range removals {
		index := processWriteTaskIndex(schema.Parameters, removal)
		if index < 0 {
			return fmt.Errorf("Parameter '%s' does not exist on user task '%s'.", removal, name)
		}
		schema.Parameters = append(schema.Parameters[:index], schema.Parameters[index+1:]...)
	}
	for _, parameter := range toAdd {
		if processWriteTaskIndex(schema.Parameters, processWriteDeref(parameter.Name)) >= 0 {
			return fmt.Errorf("Parameter '%s' already exists on user task '%s'.", processWriteDeref(parameter.Name), name)
		}
		schema.Parameters = append(schema.Parameters, parameter)
	}
	for _, key := range updates.keys {
		index := processWriteTaskIndex(schema.Parameters, key)
		if index < 0 {
			return fmt.Errorf("Parameter '%s' does not exist on user task '%s'.", key, name)
		}
		direction := updates.values[strings.ToLower(key)]
		schema.Parameters[index].Direction = &direction
	}
	return nil
}

func processWriteTaskIndex(parameters []*processWriteTaskParameterDTO, name string) int {
	for index, parameter := range parameters {
		if parameter != nil && strings.EqualFold(processWriteDeref(parameter.Name), name) {
			return index
		}
	}
	return -1
}

func processWriteTaskHasParameter(parameters []*processWriteTaskParameterDTO, name string) bool {
	return processWriteTaskIndex(parameters, name) >= 0
}

func processWriteContainsFold(values []string, wanted string) bool {
	for _, value := range values {
		if strings.EqualFold(value, wanted) {
			return true
		}
	}
	return false
}
