package creatio

// Package dependency and hotfix writes, ported from clio's PackageDependencyManager (add-package-dependency,
// remove-package-dependency) and PackageEditableMutator (unlock-for-hotfix, finish-hotfix). Both read the
// installed packages through the SysPackage SelectQuery clio's ApplicationPackageListProvider sends, then
// call PackageService.svc.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	pkgWritePackagePropertiesRoute     = "ServiceModel/PackageService.svc/GetPackageProperties"
	pkgWriteSavePackagePropertiesRoute = "ServiceModel/PackageService.svc/SavePackageProperties"
	pkgWriteStartHotfixRoute           = "ServiceModel/PackageService.svc/StartPackageHotfix"
	pkgWriteFinishHotfixRoute          = "ServiceModel/PackageService.svc/FinishPackageHotfix"
	// pkgWritePackageServiceTimeout bounds one PackageService call; clio waits without a bound
	// (Timeout.Infinite), which an MCP call cannot afford.
	pkgWritePackageServiceTimeout = 10 * time.Minute
	pkgWriteEmptyGUID             = "00000000-0000-0000-0000-000000000000"
)

// pkgWriteInstalledPackage is the part of clio's PackageInfo the writes use.
type pkgWriteInstalledPackage struct {
	Name    string
	UID     string
	Version string
}

// pkgWriteArgumentNull is .NET's ArgumentNullException text for a null or blank argument.
func pkgWriteArgumentNull(name string) error {
	return &pkgWriteException{typeName: "ArgumentNullException", message: fmt.Sprintf("Value cannot be null. (Parameter '%s')", name)}
}

// pkgWriteException is a failure that clio raises as an exception: the type name goes into the bracketed
// prefix of the command envelope's -1 text.
type pkgWriteException struct {
	typeName string
	message  string
}

func (e *pkgWriteException) Error() string { return e.message }

// PkgWriteExceptionText is clio's CommandExecutionResult.FormatExceptionChain for one exception.
func PkgWriteExceptionText(err error) string {
	var typed *pkgWriteException
	if errors.As(err, &typed) {
		return "[" + typed.typeName + "] " + typed.message
	}
	return "[Exception] " + err.Error()
}

// pkgWriteInstalledPackages is ApplicationPackageListProvider.GetPackages("{}"): every SysPackage row.
func (c *Client) pkgWriteInstalledPackages(ctx context.Context) ([]pkgWriteInstalledPackage, error) {
	rows, err := c.selectRows(ctx, buildSelectQuery("SysPackage", map[string]string{
		"Name": "Name", "UId": "UId", "Maintainer": "Maintainer", "Version": "Version",
	}, nil, 10000))
	if err != nil {
		return nil, err
	}
	packages := make([]pkgWriteInstalledPackage, 0, len(rows))
	for _, row := range rows {
		uid := pkgWriteNormalizeGUID(rowString(row, "UId"))
		packages = append(packages, pkgWriteInstalledPackage{Name: rowString(row, "Name"), UID: uid, Version: rowString(row, "Version")})
	}
	return packages, nil
}

// pkgWriteNormalizeGUID is Guid.TryParse then ToString(): lower-case "D" form, the empty GUID when invalid.
func pkgWriteNormalizeGUID(text string) string {
	trimmed := strings.Trim(strings.TrimSpace(text), "{}()")
	compact := strings.ReplaceAll(trimmed, "-", "")
	if len(compact) != 32 {
		return pkgWriteEmptyGUID
	}
	for _, r := range compact {
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			return pkgWriteEmptyGUID
		}
	}
	lower := strings.ToLower(compact)
	return lower[0:8] + "-" + lower[8:12] + "-" + lower[12:16] + "-" + lower[16:20] + "-" + lower[20:32]
}

func pkgWriteFindPackage(packages []pkgWriteInstalledPackage, name string, ignoreCase bool) (pkgWriteInstalledPackage, bool) {
	for _, item := range packages {
		if item.Name == name || (ignoreCase && strings.EqualFold(item.Name, name)) {
			return item, true
		}
	}
	return pkgWriteInstalledPackage{}, false
}

// pkgWriteBaseResponse is clio's BaseResponse.
type pkgWriteBaseResponse struct {
	Success   bool `json:"success"`
	ErrorInfo *struct {
		ErrorCode string `json:"errorCode"`
		Message   string `json:"message"`
	} `json:"errorInfo"`
}

func (r pkgWriteBaseResponse) errorMessage() (string, bool) {
	if r.ErrorInfo == nil {
		return "", false
	}
	return r.ErrorInfo.Message, true
}

// pkgWritePostPackageService posts a fixed PackageService route and returns the JSON body.
func (c *Client) pkgWritePostPackageService(ctx context.Context, route string, body []byte) ([]byte, error) {
	return c.callService(ctx, serviceCall{Route: route, Body: body, Timeout: pkgWritePackageServiceTimeout, Label: "PackageService"})
}

// pkgWriteDependencySpec is clio's PackageDependencySpec.
type pkgWriteDependencySpec struct {
	Name    string
	Version string
}

// PkgWriteParseDependency is AddPackageDependencyCommand.ParseDependency: "name" or "name:version", split at
// the first colon, both halves trimmed.
func PkgWriteParseDependency(value string) pkgWriteDependencySpec {
	trimmed := strings.TrimSpace(value)
	index := strings.Index(trimmed, ":")
	if index < 0 {
		return pkgWriteDependencySpec{Name: trimmed}
	}
	return pkgWriteDependencySpec{Name: strings.TrimSpace(trimmed[:index]), Version: strings.TrimSpace(trimmed[index+1:])}
}

// pkgWritePackageDTO is clio's WorkspacePackageDto read from GetPackageProperties: the four fields clio models
// and every other field in document order, so the package round-trips into SavePackageProperties without
// losing anything (the core overwrites what the posted DTO leaves out).
type pkgWritePackageDTO struct {
	UID          string
	Name         *string
	Version      *string
	Dependencies []*pkgWritePackageDTO
	HasDeps      bool
	extraKeys    []string
	extra        map[string]any
}

// pkgWriteDecodeDTO reads an object the way Newtonsoft binds WorkspacePackageDto: the modelled properties
// match case-insensitively, the rest go to the extension data.
func pkgWriteDecodeDTO(value any) (*pkgWritePackageDTO, error) {
	object, ok := value.(*orderedObject)
	if !ok {
		return nil, fmt.Errorf("package properties are not a JSON object")
	}
	dto := &pkgWritePackageDTO{UID: pkgWriteEmptyGUID, extra: map[string]any{}}
	for _, key := range object.keys {
		item := object.values[key]
		switch strings.ToLower(key) {
		case "uid":
			text, _ := item.(string)
			dto.UID = pkgWriteNormalizeGUID(text)
		case "name":
			if text, ok := item.(string); ok {
				dto.Name = &text
			}
		case "version":
			if text, ok := item.(string); ok {
				dto.Version = &text
			}
		case "dependsonpackages":
			items, ok := item.([]any)
			if !ok {
				continue
			}
			dto.HasDeps = true
			dto.Dependencies = make([]*pkgWritePackageDTO, 0, len(items))
			for _, element := range items {
				dependency, err := pkgWriteDecodeDTO(element)
				if err != nil {
					return nil, err
				}
				dto.Dependencies = append(dto.Dependencies, dependency)
			}
		default:
			dto.extraKeys = append(dto.extraKeys, key)
			dto.extra[key] = item
		}
	}
	return dto, nil
}

// encode writes the DTO as Newtonsoft does: uId, name (null included), version and dependsOnPackages when
// set, then the extension data in its original order.
func (dto *pkgWritePackageDTO) encode() *orderedObject {
	object := &orderedObject{values: map[string]any{}}
	add := func(key string, value any) {
		object.keys = append(object.keys, key)
		object.values[key] = value
	}
	add("uId", dto.UID)
	if dto.Name != nil {
		add("name", *dto.Name)
	} else {
		add("name", nil)
	}
	if dto.Version != nil {
		add("version", *dto.Version)
	}
	if dto.HasDeps {
		items := make([]any, 0, len(dto.Dependencies))
		for _, dependency := range dto.Dependencies {
			items = append(items, dependency.encode())
		}
		add("dependsOnPackages", items)
	}
	for _, key := range dto.extraKeys {
		if _, taken := object.values[key]; !taken {
			add(key, dto.extra[key])
		}
	}
	return object
}

func pkgWriteDependencyNames(dependencies []*pkgWritePackageDTO) []string {
	names := make([]string, 0, len(dependencies))
	for _, dependency := range dependencies {
		if dependency.Name != nil {
			names = append(names, *dependency.Name)
		} else {
			names = append(names, dependency.UID)
		}
	}
	return names
}

// pkgWriteLoadTarget is PackageDependencyManager.LoadTargetPackage.
func (c *Client) pkgWriteLoadTarget(ctx context.Context, packageName string) ([]pkgWriteInstalledPackage, *pkgWritePackageDTO, error) {
	installed, err := c.pkgWriteInstalledPackages(ctx)
	if err != nil {
		return nil, nil, err
	}
	target, ok := pkgWriteFindPackage(installed, packageName, true)
	if !ok {
		return nil, nil, fmt.Errorf("Package with name \"%s\" not found in the environment.", packageName)
	}
	body, _ := json.Marshal(target.UID)
	payload, err := c.pkgWritePostPackageService(ctx, pkgWritePackagePropertiesRoute, body)
	if err != nil {
		return nil, nil, err
	}
	couldNotRead := fmt.Sprintf("Could not read properties of package \"%s\".", packageName)
	var head pkgWriteBaseResponse
	if err := json.Unmarshal(payload, &head); err != nil {
		return nil, nil, fmt.Errorf("PackageService GetPackageProperties returned invalid JSON: %v", err)
	}
	if !head.Success {
		if message, ok := head.errorMessage(); ok {
			return nil, nil, fmt.Errorf("%s", message)
		}
		return nil, nil, fmt.Errorf("%s", couldNotRead)
	}
	parsed, err := parseOrderedJSON(payload)
	if err != nil {
		return nil, nil, fmt.Errorf("PackageService GetPackageProperties returned invalid JSON: %v", err)
	}
	var packageValue any
	if object, ok := parsed.(*orderedObject); ok {
		for _, key := range object.keys {
			if strings.EqualFold(key, "package") {
				packageValue = object.values[key]
			}
		}
	}
	if packageValue == nil {
		return nil, nil, fmt.Errorf("%s", couldNotRead)
	}
	dto, err := pkgWriteDecodeDTO(packageValue)
	if err != nil {
		return nil, nil, err
	}
	return installed, dto, nil
}

// pkgWriteSaveResponse is clio's SavePackagePropertiesResponse.
type pkgWriteSaveResponse struct {
	pkgWriteBaseResponse
	CompilationRequired bool `json:"compilationRequired"`
	ValidationErrors    []struct {
		PackageName string `json:"packageName"`
		ItemName    string `json:"itemName"`
		Message     string `json:"message"`
	} `json:"validationErrors"`
}

// pkgWriteSave is PackageDependencyManager.SavePackageProperties; log receives clio's warning and info lines.
func (c *Client) pkgWriteSave(ctx context.Context, dto *pkgWritePackageDTO, log *[]LogMessage) error {
	body, err := json.Marshal(dto.encode())
	if err != nil {
		return err
	}
	payload, err := c.pkgWritePostPackageService(ctx, pkgWriteSavePackagePropertiesRoute, body)
	if err != nil {
		return err
	}
	var response pkgWriteSaveResponse
	if err := json.Unmarshal(payload, &response); err != nil {
		return fmt.Errorf("PackageService SavePackageProperties returned invalid JSON: %v", err)
	}
	validation := make([]string, 0, len(response.ValidationErrors))
	for _, item := range response.ValidationErrors {
		validation = append(validation, fmt.Sprintf("%s/%s: %s", item.PackageName, item.ItemName, item.Message))
	}
	if len(validation) > 0 {
		*log = append(*log, LogMessage{MessageType: "Warning", Value: "Validation warnings from server: " + strings.Join(validation, "; ")})
	}
	if !response.Success {
		message, ok := response.errorMessage()
		if !ok {
			message = "Failed to save package dependencies."
		}
		if len(validation) > 0 {
			message += " Validation errors: " + strings.Join(validation, "; ")
		}
		return fmt.Errorf("%s", message)
	}
	if response.CompilationRequired {
		*log = append(*log, LogMessage{MessageType: "Info", Value: "The dependency change requires a configuration compilation. Run compile-configuration to apply."})
	}
	return nil
}

// AddPackageDependencies is AddPackageDependencyCommand.Execute: raw entries are "name" or "name:version".
func (c *Client) AddPackageDependencies(ctx context.Context, packageName string, raw []string) CommandResult {
	specs := make([]pkgWriteDependencySpec, 0, len(raw))
	for _, value := range raw {
		if strings.TrimSpace(value) != "" {
			specs = append(specs, PkgWriteParseDependency(value))
		}
	}
	if len(specs) == 0 {
		return CommandFailure("At least one dependency must be specified via --dependencies.")
	}
	log := []LogMessage{{MessageType: "Info", Value: fmt.Sprintf("Adding %d dependency(ies) to package \"%s\"...", len(specs), packageName)}}
	names, err := c.pkgWriteAddDependencies(ctx, packageName, specs, &log)
	if err != nil {
		return CommandResult{ExitCode: 1, Messages: append(log, LogMessage{MessageType: "Error", Value: err.Error()})}
	}
	log = append(log,
		LogMessage{MessageType: "Info", Value: fmt.Sprintf("Package \"%s\" now depends on: %s", packageName, strings.Join(names, ", "))},
		LogMessage{MessageType: "Info", Value: "Done"})
	return CommandResult{ExitCode: 0, Messages: log}
}

func (c *Client) pkgWriteAddDependencies(ctx context.Context, packageName string, specs []pkgWriteDependencySpec, log *[]LogMessage) ([]string, error) {
	if strings.TrimSpace(packageName) == "" {
		return nil, pkgWriteArgumentNull("packageName")
	}
	requested := make([]pkgWriteDependencySpec, 0, len(specs))
	for _, spec := range specs {
		if strings.TrimSpace(spec.Name) != "" {
			requested = append(requested, spec)
		}
	}
	if len(requested) == 0 {
		return nil, fmt.Errorf("At least one dependency must be specified. (Parameter 'dependencies')")
	}
	installed, dto, err := c.pkgWriteLoadTarget(ctx, packageName)
	if err != nil {
		return nil, err
	}
	dto.HasDeps = true
	if dto.Dependencies == nil {
		dto.Dependencies = []*pkgWritePackageDTO{}
	}
	for _, spec := range requested {
		found, ok := pkgWriteFindPackage(installed, spec.Name, true)
		if !ok {
			return nil, fmt.Errorf("Dependency package with name \"%s\" not found in the environment.", spec.Name)
		}
		present := false
		for _, existing := range dto.Dependencies {
			if existing.UID == found.UID {
				present = true
				break
			}
		}
		if present {
			continue
		}
		name, version := found.Name, spec.Version
		if strings.TrimSpace(version) == "" {
			version = found.Version
		}
		dto.Dependencies = append(dto.Dependencies, &pkgWritePackageDTO{UID: found.UID, Name: &name, Version: &version, extra: map[string]any{}})
	}
	if err := c.pkgWriteSave(ctx, dto, log); err != nil {
		return nil, err
	}
	return pkgWriteDependencyNames(dto.Dependencies), nil
}

// RemovePackageDependencies is RemovePackageDependencyCommand.Execute: entries are names, a ":version" suffix
// is accepted and ignored; nothing is saved when no dependency matched.
func (c *Client) RemovePackageDependencies(ctx context.Context, packageName string, raw []string) CommandResult {
	names := make([]string, 0, len(raw))
	for _, value := range raw {
		if strings.TrimSpace(value) == "" {
			continue
		}
		if name := PkgWriteParseDependency(value).Name; strings.TrimSpace(name) != "" {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return CommandFailure("At least one dependency must be specified via --dependencies.")
	}
	log := []LogMessage{{MessageType: "Info", Value: fmt.Sprintf("Removing %d dependency(ies) from package \"%s\"...", len(names), packageName)}}
	remaining, err := c.pkgWriteRemoveDependencies(ctx, packageName, names, &log)
	if err != nil {
		return CommandResult{ExitCode: 1, Messages: append(log, LogMessage{MessageType: "Error", Value: err.Error()})}
	}
	summary := fmt.Sprintf("Package \"%s\" now depends on: %s", packageName, strings.Join(remaining, ", "))
	if len(remaining) == 0 {
		summary = fmt.Sprintf("Package \"%s\" now has no dependencies", packageName)
	}
	log = append(log, LogMessage{MessageType: "Info", Value: summary}, LogMessage{MessageType: "Info", Value: "Done"})
	return CommandResult{ExitCode: 0, Messages: log}
}

func (c *Client) pkgWriteRemoveDependencies(ctx context.Context, packageName string, names []string, log *[]LogMessage) ([]string, error) {
	if strings.TrimSpace(packageName) == "" {
		return nil, pkgWriteArgumentNull("packageName")
	}
	remove := map[string]bool{}
	for _, name := range names {
		if strings.TrimSpace(name) != "" {
			remove[strings.ToUpper(strings.TrimSpace(name))] = true
		}
	}
	if len(remove) == 0 {
		return nil, fmt.Errorf("At least one dependency must be specified. (Parameter 'dependencyNames')")
	}
	_, dto, err := c.pkgWriteLoadTarget(ctx, packageName)
	if err != nil {
		return nil, err
	}
	dto.HasDeps = true
	kept := make([]*pkgWritePackageDTO, 0, len(dto.Dependencies))
	removed := 0
	for _, existing := range dto.Dependencies {
		if existing.Name != nil && remove[strings.ToUpper(*existing.Name)] {
			removed++
			continue
		}
		kept = append(kept, existing)
	}
	dto.Dependencies = kept
	if removed > 0 {
		if err := c.pkgWriteSave(ctx, dto, log); err != nil {
			return nil, err
		}
	}
	return pkgWriteDependencyNames(dto.Dependencies), nil
}

// SetPackageHotfix is PackageHotFixCommand.Execute. clio lets the command's exception escape to the tool
// envelope, so a failure is exit code -1 with the exception type in brackets.
func (c *Client) SetPackageHotfix(ctx context.Context, packageName string, enable bool) CommandResult {
	state := "Disable"
	route := pkgWriteFinishHotfixRoute
	if enable {
		state, route = "Enable", pkgWriteStartHotfixRoute
	}
	log := []LogMessage{{MessageType: "Info", Value: fmt.Sprintf("%s hotfix state for package: \"%s\"", state, packageName)}}
	fail := func(err error) CommandResult {
		return CommandResult{ExitCode: -1, Messages: append(log, LogMessage{MessageType: "Error", Value: PkgWriteExceptionText(err)})}
	}
	if strings.TrimSpace(packageName) == "" {
		return fail(pkgWriteArgumentNull("packageName"))
	}
	installed, err := c.pkgWriteInstalledPackages(ctx)
	if err != nil {
		return fail(err)
	}
	// BasePackageOperation.GetPackageUId matches the name exactly (ordinal, case-sensitive).
	target, ok := pkgWriteFindPackage(installed, packageName, false)
	if !ok {
		return fail(fmt.Errorf("Package with name %s not found", packageName))
	}
	payload, err := c.pkgWritePostPackageService(ctx, route, []byte(`{"uId": "`+target.UID+`"}`))
	if err != nil {
		return fail(err)
	}
	var response pkgWriteBaseResponse
	if err := json.Unmarshal(payload, &response); err != nil {
		return fail(fmt.Errorf("PackageService returned invalid JSON: %v", err))
	}
	if !response.Success {
		message, ok := response.errorMessage()
		if !ok {
			// clio dereferences a missing errorInfo and fails with the runtime's null-reference text.
			return fail(&pkgWriteException{typeName: "NullReferenceException", message: "Object reference not set to an instance of an object."})
		}
		return fail(fmt.Errorf("%s", message))
	}
	return CommandResult{ExitCode: 0, Messages: append(log, LogMessage{MessageType: "Info", Value: "Done"})}
}
