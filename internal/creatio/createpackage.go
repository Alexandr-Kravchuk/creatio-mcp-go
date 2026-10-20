package creatio

// create-package, ported from clio 8.1.0.142's CreatePackageCommand and RemotePackageCreator: the name gets the
// environment's SchemaNamePrefix, every refusal (taken name, missing dependency, unknown application) is
// decided before anything is written, the create request is sent once (never replayed after a re-login), the
// dependencies are saved by a second request, and the package is read back.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	createPackageRoute      = "ServiceModel/PackageService.svc/CreatePackage"
	createPackageInAppRoute = "ServiceModel/ApplicationPackagesService.svc/CreatePackageInApp"
	createPackageTimeout    = 100 * time.Second
)

// CreatePackageRequest is clio's CreatePackageOptions as the MCP tool fills it.
type CreatePackageRequest struct {
	PackageName     string
	Description     *string
	Dependencies    []*string
	ApplicationCode *string
}

// CreatePackageResponse is clio's CreatePackageResponse.
type CreatePackageResponse struct {
	Success         bool       `json:"success"`
	PackageCreated  *bool      `json:"package-created"`
	Error           *string    `json:"error,omitempty"`
	PackageUID      *string    `json:"package-uid,omitempty"`
	PackageName     *string    `json:"package-name,omitempty"`
	Maintainer      *string    `json:"maintainer,omitempty"`
	Description     *string    `json:"description,omitempty"`
	Dependencies    *[]*string `json:"dependencies,omitempty"`
	InstallType     *int       `json:"install-type,omitempty"`
	Editable        *bool      `json:"editable,omitempty"`
	ApplicationCode *string    `json:"application-code,omitempty"`
}

// CreatePackageFailure is CreatePackageTool.Failure: nothing was created.
func CreatePackageFailure(message string) CreatePackageResponse {
	if strings.TrimSpace(message) == "" {
		message = "unknown"
	}
	created := false
	return CreatePackageResponse{PackageCreated: &created, Error: &message}
}

// createPackageRefusal is RemotePackageCreator's InvalidOperationException: refused, nothing created.
type createPackageRefusal struct{ message string }

func (e *createPackageRefusal) Error() string { return e.message }

// createPackageUnknown is PackageCreationOutcomeUnknownException.
type createPackageUnknown struct{ message string }

func (e *createPackageUnknown) Error() string { return e.message }

// createPackageResult is RemotePackageCreateResult.
type createPackageResult struct {
	uid              string
	name             string
	maintainer       *string
	description      *string
	dependencies     []*string
	installType      *int
	applicationCode  *string
	incompleteReason *string
}

// CreatePackage is CreatePackageCommand.CreatePackage (the MCP tool redacts the error afterwards).
func (c *Client) CreatePackage(ctx context.Context, input CreatePackageRequest) CreatePackageResponse {
	result, err := c.createPackage(ctx, input)
	if err != nil {
		message := c.createPackageErrorText(err)
		var unknown *createPackageUnknown
		if errors.As(err, &unknown) {
			return CreatePackageResponse{Error: &message}
		}
		created := false
		return CreatePackageResponse{PackageCreated: &created, Error: &message}
	}
	created := true
	response := CreatePackageResponse{
		Success: result.incompleteReason == nil, PackageCreated: &created, Error: result.incompleteReason,
		PackageUID: &result.uid, PackageName: &result.name, Maintainer: result.maintainer,
		Description: result.description, Dependencies: &result.dependencies, InstallType: result.installType,
	}
	if result.dependencies == nil {
		response.Dependencies = &[]*string{}
	}
	if result.installType != nil {
		editable := *result.installType == 0
		response.Editable = &editable
	}
	if result.applicationCode != nil && strings.TrimSpace(*result.applicationCode) != "" {
		response.ApplicationCode = result.applicationCode
	}
	return response
}

// createPackageErrorText is the exception message clio reports: transport failures in the words of clio's client.
func (c *Client) createPackageErrorText(err error) string {
	var refusal *createPackageRefusal
	var unknown *createPackageUnknown
	if errors.As(err, &refusal) || errors.As(err, &unknown) {
		return err.Error()
	}
	return c.rightsTransportFault(err, 0).Error()
}

func (c *Client) createPackage(ctx context.Context, input CreatePackageRequest) (createPackageResult, error) {
	if strings.TrimSpace(input.PackageName) == "" {
		return createPackageResult{}, &createPackageRefusal{message: "Value cannot be null or whitespace. (Parameter 'PackageName')"}
	}
	prefixValue, err := c.readSysSettingValue(ctx, schemaNamePrefixSettingCode)
	if err != nil {
		return createPackageResult{}, err
	}
	prefix := strings.TrimSpace(strings.Trim(strings.TrimSpace(prefixValue), `"`))
	packageName := dotnetTrim(input.PackageName)
	if prefix != "" && !strings.HasPrefix(strings.ToLower(packageName), strings.ToLower(prefix)) {
		packageName = prefix + packageName
	}
	installed, err := c.pkgWriteInstalledPackages(ctx)
	if err != nil {
		return createPackageResult{}, err
	}
	if existing, ok := pkgWriteFindPackage(installed, packageName, true); ok {
		return createPackageResult{}, &createPackageRefusal{message: fmt.Sprintf(
			"Package \"%s\" already exists in the environment. Nothing was created.", existing.Name)}
	}
	dependencies, err := createPackageResolveDependencies(installed, input.Dependencies)
	if err != nil {
		return createPackageResult{}, err
	}
	var applicationID string
	if input.ApplicationCode != nil && strings.TrimSpace(*input.ApplicationCode) != "" {
		if applicationID, err = c.createPackageFindApplication(ctx, strings.TrimSpace(*input.ApplicationCode)); err != nil {
			return createPackageResult{}, err
		}
	}
	description := ""
	if input.Description != nil {
		description = *input.Description
	}
	packageUID := newGUID()
	if err := c.createPackageSend(ctx, newGUID(), packageUID, packageName, description, applicationID); err != nil {
		var refusal *createPackageRefusal
		if errors.As(err, &refusal) {
			return createPackageResult{}, err
		}
		text := c.clioRequestFailureText(err)
		exists, checkErr := c.createPackageExists(ctx, packageUID)
		switch {
		case checkErr == nil && !exists:
			return createPackageResult{}, &createPackageRefusal{message: fmt.Sprintf("Creating package \"%s\" failed: %s "+
				"No package with the identifier clio sent was found afterwards. Nothing was created.", packageName, text)}
		case checkErr != nil:
			return createPackageResult{}, &createPackageUnknown{message: fmt.Sprintf("Creating package \"%s\" failed: %s The environment could not be "+
				"asked afterwards whether package UId %s exists. Check list-packages for \"%s\" before creating it again.",
				packageName, text, packageUID, packageName)}
		}
	}
	var incomplete *string
	if len(dependencies) > 0 {
		var log []LogMessage
		if _, err := c.pkgWriteAddDependencies(ctx, packageName, dependencies, &log); err != nil {
			text := fmt.Sprintf("Package \"%s\" was created, but its dependencies were not applied: %s Add them with add-package-dependency.",
				packageName, c.createPackageErrorText(err))
			incomplete = &text
		}
	}
	stored, err := c.createPackageReadBack(ctx, packageUID)
	if err != nil {
		if incomplete == nil {
			text := fmt.Sprintf("Package \"%s\" was created, but reading it back failed: %s", packageName, c.createPackageErrorText(err))
			incomplete = &text
		}
		return createPackageResult{uid: packageUID, name: packageName, description: input.Description, dependencies: []*string{},
			applicationCode: input.ApplicationCode, incompleteReason: incomplete}, nil
	}
	name := packageName
	if stored.Name != nil {
		name = *stored.Name
	}
	names := make([]*string, 0, len(stored.Dependencies))
	for _, dependency := range stored.Dependencies {
		names = append(names, dependency.Name)
	}
	result := createPackageResult{uid: packageUID, name: name, maintainer: createPackageExtraString(stored, "maintainer"),
		description: createPackageExtraString(stored, "description"), dependencies: names,
		applicationCode: input.ApplicationCode, incompleteReason: incomplete}
	if text := createPackageExtraString(stored, "installType"); text != nil {
		if value, err := strconv.ParseInt(strings.TrimSpace(*text), 10, 32); err == nil {
			number := int(value)
			result.installType = &number
		}
	}
	return result, nil
}

// createPackageResolveDependencies is RemotePackageCreator.ResolveDependencies: blank names dropped, the rest
// trimmed and de-duplicated ignoring case; any name not installed refuses the whole call.
func createPackageResolveDependencies(installed []pkgWriteInstalledPackage, requested []*string) ([]pkgWriteDependencySpec, error) {
	var names []string
	seen := map[string]bool{}
	for _, item := range requested {
		if item == nil || strings.TrimSpace(*item) == "" {
			continue
		}
		name := dotnetTrim(*item)
		if seen[strings.ToLower(name)] {
			continue
		}
		seen[strings.ToLower(name)] = true
		names = append(names, name)
	}
	var missing []string
	for _, name := range names {
		if _, ok := pkgWriteFindPackage(installed, name, true); !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return nil, &createPackageRefusal{message: fmt.Sprintf("Dependency package(s) not found in the environment: %s. Nothing was created.",
			strings.Join(missing, ", "))}
	}
	specs := make([]pkgWriteDependencySpec, 0, len(names))
	for _, name := range names {
		specs = append(specs, pkgWriteDependencySpec{Name: name})
	}
	return specs, nil
}

// createPackageFindApplication is ApplicationInfoService.FindApplicationId: the SysInstalledApp row by code.
func (c *Client) createPackageFindApplication(ctx context.Context, code string) (string, error) {
	rows, err := c.selectRows(ctx, buildSelectQuery("SysInstalledApp", map[string]string{
		"Id": "Id", "Code": "Code", "Name": "Name", "Version": "Version",
	}, map[string]any{"filter0": comparisonFilter("Code", code, 1, 3)}, appInfoRowCount))
	if err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return "", &createPackageRefusal{message: fmt.Sprintf("Application '%s' not found.", code)}
	}
	id := normalizeGUID(rowString(rows[0], "Id"))
	if id == "" {
		return "", &createPackageRefusal{message: "Unrecognized Guid format."}
	}
	return id, nil
}

// createPackageSend is SendCreateRequest: one non-replayable POST; success:false is a refusal.
func (c *Client) createPackageSend(ctx context.Context, id, uid, name, description, applicationID string) error {
	dto := &orderedObject{values: map[string]any{}}
	for _, field := range []struct {
		key   string
		value any
	}{{"id", id}, {"uId", uid}, {"name", name}, {"description", description}, {"type", 0}, {"installBehavior", 0},
		{"isChanged", true}, {"position", 0}} {
		dto.keys = append(dto.keys, field.key)
		dto.values[field.key] = field.value
	}
	route, body := createPackageRoute, any(dto)
	if applicationID != "" {
		route = createPackageInAppRoute
		body = &orderedObject{keys: []string{"package", "appId"}, values: map[string]any{"package": dto, "appId": applicationID}}
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return err
	}
	text, err := c.processWriteSendOnce(ctx, route, encoded, createPackageTimeout)
	if err != nil {
		return err
	}
	var response pkgWriteBaseResponse
	if strings.TrimSpace(text) != "" && strings.TrimSpace(text) != "null" {
		if err := json.Unmarshal([]byte(text), &response); err != nil {
			return fmt.Errorf("Unexpected character encountered while parsing value: %s. Path '', line 0, position 0.", firstRune(text))
		}
	}
	if !response.Success {
		message := "no error details returned"
		if detail, ok := response.errorMessage(); ok && detail != "" {
			message = detail
		}
		return &createPackageRefusal{message: fmt.Sprintf("The environment refused to create package \"%s\": %s. Nothing was created.", name, message)}
	}
	return nil
}

// clioRequestFailureText is the message of the exception ExecuteNonReplayablePostRequest throws when the
// create request gets no answer: a connection closed or reset before the response is the AggregateException
// over HttpRequestException clio reports (measured against clio 8.1.0.142 on a dropped connection); other
// transport failures use the client's usual texts.
func (c *Client) clioRequestFailureText(err error) string {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET) {
		return "One or more errors occurred. (An error occurred while sending the request.)"
	}
	return c.rightsTransportFault(err, 0).Error()
}

func firstRune(text string) string {
	for _, r := range strings.TrimSpace(text) {
		return string(r)
	}
	return ""
}

// createPackageReadBack is ReadBack: GetPackageProperties by UId; success:false or no package is a refusal
// (InvalidOperationException), anything else a failure of the request.
func (c *Client) createPackageReadBack(ctx context.Context, uid string) (*pkgWritePackageDTO, error) {
	body, _ := json.Marshal(uid)
	payload, err := c.pkgWritePostPackageService(ctx, pkgWritePackagePropertiesRoute, body)
	if err != nil {
		return nil, err
	}
	var head pkgWriteBaseResponse
	if err := json.Unmarshal(payload, &head); err != nil {
		return nil, fmt.Errorf("PackageService GetPackageProperties returned invalid JSON: %v", err)
	}
	parsed, err := parseOrderedJSON(payload)
	if err != nil {
		return nil, fmt.Errorf("PackageService GetPackageProperties returned invalid JSON: %v", err)
	}
	var packageValue any
	if object, ok := parsed.(*orderedObject); ok {
		for _, key := range object.keys {
			if strings.EqualFold(key, "package") {
				packageValue = object.values[key]
			}
		}
	}
	if !head.Success || packageValue == nil {
		message, ok := head.errorMessage()
		if !ok || message == "" {
			message = "GetPackageProperties returned no package."
		}
		return nil, &createPackageRefusal{message: message}
	}
	return pkgWriteDecodeDTO(packageValue)
}

// createPackageExists is Exists: found, answered "not found", or (error) could not be asked.
func (c *Client) createPackageExists(ctx context.Context, uid string) (bool, error) {
	_, err := c.createPackageReadBack(ctx, uid)
	if err == nil {
		return true, nil
	}
	var refusal *createPackageRefusal
	if errors.As(err, &refusal) {
		return false, nil
	}
	return false, err
}

// createPackageExtraString is ReadString over WorkspacePackageDto.AdditionalData: JToken.ToString() of the
// value under the exact key, nil when the key is absent.
func createPackageExtraString(dto *pkgWritePackageDTO, key string) *string {
	value, ok := dto.extra[key]
	if !ok {
		return nil
	}
	var text string
	switch typed := value.(type) {
	case nil:
		text = ""
	case string:
		text = typed
	case bool:
		text = "False"
		if typed {
			text = "True"
		}
	case json.Number:
		text = typed.String()
	case float64:
		text = strconv.FormatFloat(typed, 'f', -1, 64)
	default:
		encoded, _ := json.Marshal(typed)
		text = string(encoded)
	}
	return &text
}
