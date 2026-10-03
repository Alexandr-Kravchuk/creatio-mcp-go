package creatio

// The remote side of clio's entity-schema write tools: RemoteEntitySchemaDesignerClient (the designer,
// SchemaDesignerRequest and WorkspaceExplorerService calls and their failure texts), EntitySchemaPublisher
// with its ODataBuildGate, the culture catalog guard, the caption culture resolver and
// EntitySchemaDefaultValueSourceResolver. One schemaWriteEntRun lives for one tool call, like clio's
// per-command services, and collects the command log.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode"
)

const (
	schemaWriteEntDesignerRoute   = "ServiceModel/EntitySchemaDesignerService.svc/"
	schemaWriteEntDesignItemRoute = "ServiceModel/EntitySchemaDesignerService.svc/GetSchemaDesignItem"
	schemaWriteEntRequestRoute    = "DataService/json/SyncReply/SchemaDesignerRequest"
	schemaWriteEntRuntimeRoute    = "DataService/json/SyncReply/RuntimeEntitySchemaRequest"
	schemaWriteEntSelectRoute     = "DataService/json/SyncReply/SelectQuery"
	schemaWriteEntInsertRoute     = "DataService/json/SyncReply/InsertQuery"
	schemaWriteEntUpdateRoute     = "DataService/json/SyncReply/UpdateQuery"
	schemaWriteEntODataBuildRoute = "ServiceModel/WorkspaceExplorerService.svc/RunODataBuild"
	schemaWriteEntODataStateRoute = "ServiceModel/WorkspaceExplorerService.svc/IsODataBuildRunning"
	// schemaWriteEntTimeout is clio's default RemoteCommandOptions.TimeOut.
	schemaWriteEntTimeout = 100 * time.Second
	// schemaWriteEntPublishTimeout is RemoteEntitySchemaDesignerClient.PublishConfigurationTimeoutMs.
	schemaWriteEntPublishTimeout = 60 * time.Minute
	// schemaWriteEntGatePolls and schemaWriteEntGateInterval are ODataBuildGate's 30 x 3 s budget.
	schemaWriteEntGatePolls    = 30
	schemaWriteEntGateInterval = 3 * time.Second
)

// schemaWriteEntLog is the command log clio's logger collects for one call.
type schemaWriteEntLog struct {
	messages []LogMessage
}

func (l *schemaWriteEntLog) add(kind, text string) {
	l.messages = append(l.messages, LogMessage{MessageType: kind, Value: text})
}
func (l *schemaWriteEntLog) info(text string)    { l.add("Info", text) }
func (l *schemaWriteEntLog) warning(text string) { l.add("Warning", text) }
func (l *schemaWriteEntLog) err(text string)     { l.add("Error", text) }

// snapshot returns the collected messages and clears the log (FlushAndSnapshotMessages(clearMessages: true)).
func (l *schemaWriteEntLog) snapshot() []LogMessage {
	messages := l.messages
	l.messages = nil
	if messages == nil {
		return []LogMessage{}
	}
	return messages
}

// schemaWriteEntRun is one tool call against one environment.
type schemaWriteEntRun struct {
	ctx    context.Context
	client *Client
	log    *schemaWriteEntLog
	// sleep is the ODataBuildGate's wait; tests replace it.
	sleep         func(time.Duration)
	gateSupported *bool
	systemValues  map[string][]schemaWriteEntSystemValue
	settings      map[string][]schemaWriteEntSettingRow
	records       map[string]int
	packages      []pkgWriteInstalledPackage
}

// schemaWriteEntSleep is the gate's wait between polls; a test replaces it.
var schemaWriteEntSleep = func(ctx context.Context, interval time.Duration) {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

func (c *Client) schemaWriteEntNewRun(ctx context.Context, log *schemaWriteEntLog) *schemaWriteEntRun {
	if log == nil {
		log = &schemaWriteEntLog{}
	}
	run := &schemaWriteEntRun{ctx: ctx, client: c, log: log, systemValues: map[string][]schemaWriteEntSystemValue{},
		settings: map[string][]schemaWriteEntSettingRow{}, records: map[string]int{}}
	run.sleep = func(interval time.Duration) { schemaWriteEntSleep(ctx, interval) }
	return run
}

// schemaWriteEntOutcome classifies an answer the way clio's designer client reads it.
type schemaWriteEntOutcome int

const (
	schemaWriteEntAnswered schemaWriteEntOutcome = iota
	schemaWriteEntMarkup
)

// post sends a body and decodes the answer into target, raising what clio's PostToUrl raises: the sign-in
// answer, an empty body, a markup or unparseable body, or a success:false envelope.
func (r *schemaWriteEntRun) post(route, method string, body []byte, timeout time.Duration, target schemaWriteEntResponse) error {
	outcome, err := r.send(route, method, body, timeout, target, false)
	if err == nil && outcome == schemaWriteEntMarkup {
		err = errors.New(schemaWriteEntMarkupMessage(method, r.client.serviceURL(route)))
	}
	return err
}

// tryPost is clio's TryPostToUrl: a markup answer means "this server cannot answer that" and returns false
// instead of failing.
func (r *schemaWriteEntRun) tryPost(route, method string, body []byte, timeout time.Duration, target schemaWriteEntResponse) (bool, error) {
	outcome, err := r.send(route, method, body, timeout, target, true)
	if err != nil {
		return false, err
	}
	return outcome == schemaWriteEntAnswered, nil
}

func (r *schemaWriteEntRun) send(route, method string, body []byte, timeout time.Duration, target schemaWriteEntResponse, markupIsUnknown bool) (schemaWriteEntOutcome, error) {
	if timeout <= 0 {
		timeout = schemaWriteEntTimeout
	}
	response, err := r.client.serviceRequest(r.ctx, serviceCall{Route: route, Body: body, Timeout: timeout,
		Limit: schemaWriteResponseBytes, Label: method})
	if err != nil {
		return schemaWriteEntAnswered, err
	}
	url := r.client.serviceURL(route)
	text := string(response.payload)
	trimmed := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(text), "\uFEFF"))
	if strings.HasPrefix(trimmed, "<") && looksLikeLoginHTML(response.payload, "") {
		return schemaWriteEntAnswered, errors.New(schemaWriteEntSessionExpired(method, url))
	}
	if markupIsUnknown && strings.HasPrefix(trimmed, "<") {
		return schemaWriteEntMarkup, nil
	}
	if trimmed == "" {
		return schemaWriteEntAnswered, errors.New(schemaWriteEmptyBodyMessage(method, url))
	}
	if err := json.Unmarshal(response.payload, target); err != nil {
		if strings.HasPrefix(trimmed, "<") {
			return schemaWriteEntAnswered, errors.New(schemaWriteEntMarkupMessage(method, url))
		}
		return schemaWriteEntAnswered, fmt.Errorf("%s returned an unparseable response. URL: %s Parser error: %s. Response preview: %s",
			method, url, err.Error(), schemaWritePreview(text))
	}
	base := target.base()
	if !base.Success {
		if base.ErrorInfo != nil && base.ErrorInfo.Message != nil && strings.TrimSpace(*base.ErrorInfo.Message) != "" {
			return schemaWriteEntAnswered, errors.New(*base.ErrorInfo.Message)
		}
		if response.status < http.StatusOK || response.status >= http.StatusMultipleChoices {
			return schemaWriteEntAnswered, fmt.Errorf("%s %s returned HTTP %d", method, route, response.status)
		}
		return schemaWriteEntAnswered, fmt.Errorf("%s failed.", method)
	}
	return schemaWriteEntAnswered, nil
}

func schemaWriteEntSessionExpired(method, url string) string {
	return method + " was answered with the Creatio sign-in response instead of JSON (URL: " + url + "). " +
		"The session expired and the automatic re-authentication did not restore it. Verify the " +
		"environment credentials (for example 'clio reg-web-app --check-login') and retry. " +
		"This response says nothing about the requested schema or package, so do not read it as a " +
		"missing dependency, a missing schema, or a server defect. " +
		"The response body is omitted because a sign-in page can carry session tokens."
}

// schemaWriteEntMarkupMessage is RemoteEntitySchemaDesignerClient.BuildMarkupResponseMessage.
func schemaWriteEntMarkupMessage(method, url string) string {
	return method + " answered with an HTML/XML page instead of JSON (URL: " + url + "). " +
		"The Creatio server did not produce a service response for this request. " +
		"The response body is omitted from this message because an error or sign-in page can carry session " +
		"tokens. clio has not established WHY the server answered this way and states no cause - check the " +
		"Creatio server log for this endpoint."
}

func (r *schemaWriteEntRun) designer(method string, request any, target schemaWriteEntResponse) error {
	return r.post(schemaWriteEntDesignerRoute+method, method, schemaWriteEntNewtonsoft(request), 0, target)
}

func (r *schemaWriteEntRun) createNewSchema(packageUID schemaWriteEntGUID, extendParent bool) (*schemaWriteEntDesignerResponse, error) {
	response := &schemaWriteEntDesignerResponse{}
	err := r.designer("CreateNewSchema", struct {
		PackageUID   schemaWriteEntGUID `json:"packageUId"`
		ExtendParent bool               `json:"extendParent"`
	}{packageUID, extendParent}, response)
	return response, err
}

func (r *schemaWriteEntRun) availableSchemas(method string, packageUID schemaWriteEntGUID) (*schemaWriteEntAvailableResponse, error) {
	response := &schemaWriteEntAvailableResponse{}
	err := r.designer(method, schemaWriteEntAvailableRequest{PackageUID: packageUID}, response)
	return response, err
}

func (r *schemaWriteEntRun) assignParent(schema *schemaWriteEntSchema, parentUID schemaWriteEntGUID) (*schemaWriteEntDesignerResponse, error) {
	response := &schemaWriteEntDesignerResponse{}
	err := r.designer("AssignParentSchema", struct {
		DesignSchema     *schemaWriteEntSchema `json:"designSchema"`
		ParentSchemaUID  schemaWriteEntGUID    `json:"parentSchemaUId"`
		UseFullHierarchy bool                  `json:"useFullHierarchy"`
	}{schema, parentUID, false}, response)
	return response, err
}

func (r *schemaWriteEntRun) checkUniqueName(schemaName string, exclude schemaWriteEntGUID) (bool, error) {
	response := &schemaWriteEntBoolResponse{}
	err := r.designer("CheckUniqueSchemaName", struct {
		ManagerName string             `json:"managerName"`
		SchemaName  string             `json:"schemaName"`
		ExcludeUID  schemaWriteEntGUID `json:"excludeUId"`
	}{schemaWriteEntManagerName, schemaName, exclude}, response)
	return response.Value, err
}

func (r *schemaWriteEntRun) designItem(request schemaWriteEntDesignRequest) (*schemaWriteEntDesignerResponse, error) {
	response := &schemaWriteEntDesignerResponse{}
	err := r.post(schemaWriteEntDesignItemRoute, "GetSchemaDesignItem", schemaWriteEntNewtonsoft(request), 0, response)
	return response, err
}

// tryDesignItem returns nil without failing when the designer answers with markup.
func (r *schemaWriteEntRun) tryDesignItem(request schemaWriteEntDesignRequest) (*schemaWriteEntDesignerResponse, error) {
	response := &schemaWriteEntDesignerResponse{}
	answered, err := r.tryPost(schemaWriteEntDesignItemRoute, "GetSchemaDesignItem", schemaWriteEntNewtonsoft(request), 0, response)
	if err != nil || !answered {
		return nil, err
	}
	return response, nil
}

func (r *schemaWriteEntRun) saveSchema(schema *schemaWriteEntSchema) (*schemaWriteEntSaveResponse, error) {
	response := &schemaWriteEntSaveResponse{}
	err := r.designer("SaveSchema", schema, response)
	return response, err
}

func (r *schemaWriteEntRun) saveDBStructure(schemaUID schemaWriteEntGUID) error {
	request := schemaWriteEntDesignerRequest{SaveSchemaDBStructure: []schemaWriteEntGUID{schemaUID}}
	return r.post(schemaWriteEntRequestRoute, "SaveSchemaDbStructure", schemaWriteEntNewtonsoft(request), 0, &schemaWriteEntPlainResponse{})
}

func (r *schemaWriteEntRun) publishConfiguration() error {
	request := schemaWriteEntDesignerRequest{SaveSchemaDBStructure: []schemaWriteEntGUID{}, BuildWorkspace: true, BuildChangedConfiguration: true}
	return r.post(schemaWriteEntRequestRoute, "PublishConfigurationChanges", schemaWriteEntNewtonsoft(request),
		schemaWriteEntPublishTimeout, &schemaWriteEntPlainResponse{})
}

func (r *schemaWriteEntRun) runODataBuild() error {
	return r.post(schemaWriteEntODataBuildRoute, "RunODataBuild", []byte("{}"), 0, &schemaWriteEntPlainResponse{})
}

// odataBuildRunning is TryGetIsODataBuildRunning: nil when the server cannot answer.
func (r *schemaWriteEntRun) odataBuildRunning() (*bool, error) {
	response := &schemaWriteEntBoolResponse{}
	answered, err := r.tryPost(schemaWriteEntODataStateRoute, "IsODataBuildRunning", []byte("{}"), 0, response)
	if err != nil || !answered {
		return nil, err
	}
	value := response.Value
	return &value, nil
}

func (r *schemaWriteEntRun) runtimeSchemaByUID(uid schemaWriteEntGUID) (*schemaWriteEntRuntimeResponse, error) {
	response := &schemaWriteEntRuntimeResponse{}
	err := r.post(schemaWriteEntRuntimeRoute, "GetRuntimeEntitySchema", schemaWriteEntNewtonsoft(struct {
		UID schemaWriteEntGUID `json:"uId"`
	}{uid}), 0, response)
	return response, err
}

// ---------------------------------------------------------------------------------------------- publisher

// waitForODataBuild is ODataBuildGate.WaitUntilIdle.
func (r *schemaWriteEntRun) waitForODataBuild(operation string) {
	if r.gateSupported != nil && !*r.gateSupported {
		return
	}
	probe := func() (*bool, bool) {
		running, err := r.odataBuildRunning()
		if err != nil {
			r.log.warning("Could not read the OData entities build status: " + err.Error() + " Publishing without waiting; " +
				"if the publish fails on a locked configuration file, retry the command once the build has finished.")
			return nil, true
		}
		return running, false
	}
	running, faulted := probe()
	if faulted {
		return
	}
	if running == nil {
		unsupported := false
		r.gateSupported = &unsupported
		r.log.warning("The OData entities build status could not be read - the environment did not answer " +
			"IsODataBuildRunning with a usable response. Starting '" + operation + "' without waiting for " +
			"a running build. If it fails on a locked configuration file, retry once the build has finished.")
		return
	}
	supported := true
	r.gateSupported = &supported
	if !*running {
		return
	}
	r.log.info("Waiting for the running OData entities build to finish before '" + operation + "'.")
	for attempt := 1; attempt <= schemaWriteEntGatePolls; attempt++ {
		r.sleep(schemaWriteEntGateInterval)
		again, faulted := probe()
		if faulted || again == nil || !*again {
			return
		}
	}
	r.log.warning(fmt.Sprintf("The OData entities build is still running after %ds; starting '%s' anyway. If the operation fails on a locked configuration file, retry the command once the build has finished.",
		int((schemaWriteEntGatePolls*schemaWriteEntGateInterval)/time.Second), operation))
}

// publishSavedChanges is EntitySchemaPublisher.PublishSavedChanges.
func (r *schemaWriteEntRun) publishSavedChanges(schemaName, savedContext string, contractChanged bool) error {
	r.waitForODataBuild(schemaName)
	started := time.Now()
	if err := r.publishConfiguration(); err != nil {
		return fmt.Errorf("Schema '%s' %s, but publishing the configuration failed: %s "+
			"Until the configuration is built (for example via compile-creatio), it stays invisible to lookup "+
			"pickers, sys-setting reference schema lists, and OData.", schemaName, savedContext, err.Error())
	}
	r.log.info(fmt.Sprintf("Schema '%s' published in %.1fs.", schemaName, time.Since(started).Seconds()))
	if !contractChanged {
		return nil
	}
	if err := r.runODataBuild(); err != nil {
		r.log.warning(fmt.Sprintf("Schema '%s' was published, but requesting the OData entities rebuild failed: %s It is usable; it may not be reachable over OData until an OData build runs.",
			schemaName, err.Error()))
		return nil
	}
	r.log.info(fmt.Sprintf("OData entities rebuild requested for '%s'.", schemaName))
	return nil
}

// ---------------------------------------------------------------------------------------------- packages

// resolvePackage is ApplicationPackageListProvider.GetPackages + the case-insensitive name match the
// creator and the column manager do.
func (r *schemaWriteEntRun) resolvePackage(packageName string) (pkgWriteInstalledPackage, error) {
	if r.packages == nil {
		packages, err := r.client.pkgWriteInstalledPackages(r.ctx)
		if err != nil {
			return pkgWriteInstalledPackage{}, err
		}
		r.packages = packages
	}
	for _, item := range r.packages {
		if strings.EqualFold(item.Name, packageName) {
			return item, nil
		}
	}
	return pkgWriteInstalledPackage{}, fmt.Errorf("Package '%s' was not found.", packageName)
}

// ---------------------------------------------------------------------------------------------- cultures

// captionCulture is EntitySchemaCaptionCultureResolver.ResolveEffectiveCulture: a valid override, else the
// connected user's profile culture, else en-US.
func (r *schemaWriteEntRun) captionCulture(override string) (string, error) {
	if strings.TrimSpace(override) != "" {
		culture, ok := schemaWriteEntCanonicalCulture(override)
		if !ok {
			return "", fmt.Errorf("--caption-culture '%s' is not a valid culture name (e.g. en-US, uk-UA).", strings.TrimSpace(override))
		}
		return culture, nil
	}
	return r.client.schemaWriteProfileCulture(r.ctx), nil
}

type schemaWriteEntCulture struct {
	name   string
	active bool
}

// ensureCulturesAvailable is CultureAvailabilityGuard.EnsureAvailable: one SysCulture read, an absent culture
// refused, an inactive one warned about once the whole write is known to proceed.
func (r *schemaWriteEntRun) ensureCulturesAvailable(names []string) error {
	var requested []string
	seen := map[string]bool{}
	for _, name := range names {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" || seen[strings.ToLower(trimmed)] {
			continue
		}
		seen[strings.ToLower(trimmed)] = true
		requested = append(requested, trimmed)
	}
	if len(requested) == 0 {
		return nil
	}
	rows, err := r.client.selectRows(r.ctx, buildSelectQuery("SysCulture", map[string]string{"Name": "Name", "Active": "Active"}, nil, -1))
	if err != nil {
		return err
	}
	var cultures []schemaWriteEntCulture
	for _, row := range rows {
		name := rowString(row, "Name")
		if strings.TrimSpace(name) == "" {
			continue
		}
		var active bool
		_ = json.Unmarshal(row["Active"], &active)
		cultures = append(cultures, schemaWriteEntCulture{name: name, active: active})
	}
	var matches []schemaWriteEntCulture
	for _, name := range requested {
		found := false
		for _, culture := range cultures {
			if strings.EqualFold(culture.name, name) {
				matches = append(matches, culture)
				found = true
				break
			}
		}
		if !found {
			available := make([]string, 0, len(cultures))
			for _, culture := range cultures {
				available = append(available, culture.name)
			}
			return fmt.Errorf("Culture '%s' is not available in this environment. Add it in the Languages section "+
				"(System Designer → Languages) first. Available: %s.", name, strings.Join(available, ", "))
		}
	}
	for _, culture := range matches {
		if !culture.active {
			r.log.warning(fmt.Sprintf("Culture '%s' exists but is inactive; users cannot select it until it is activated in the Languages section. "+
				"After activating it, run a full configuration compile (clio compile-configuration --all); until then the "+
				"UI does not load in that culture.", culture.name))
		}
	}
	return nil
}

// ---------------------------------------------------------------------------------------------- default values

type schemaWriteEntSettingRow struct {
	ID   string
	Code string
	Name string
}

var schemaWriteEntSystemAliases = map[string]string{
	"autoguid": "03fac162-6a98-4f29-8d28-dc2f23ab48da", "sequentialguid": "4e8109a3-dfd7-45ed-badd-6e8663ea7994",
	"currentdatetime": "d7c295d3-3146-4ee1-ac49-3a7bd0edc45d", "currentdate": "91bd3856-9686-4eb2-9e7c-99f1e6ca0f02",
	"currenttime": "0ec58691-f653-4c6b-927e-7cd26f7f5511", "currentuser": "b70afa32-18c9-4e69-949e-89ddac35ca98",
	"currentusercontact": "4f367ca9-549b-4a1a-b64e-a40123f52ac0", "currentuseraccount": "c9a1fa4a-0392-4dc0-887c-b6e3d67470b8",
	"currentuserroles": "843fa5cd-d859-4b4e-ae27-9bfe37268785",
}

// resolveDefault is EntitySchemaDefaultValueSourceResolver.Resolve.
func (r *schemaWriteEntRun) resolveDefault(config *schemaWriteEntDefaultConfig, dataValueType int, context, referenceSchema string) (*schemaWriteEntDefaultConfig, error) {
	source, err := schemaWriteEntDefaultSource(config.source())
	if err != nil {
		return nil, err
	}
	if source < 0 {
		return nil, fmt.Errorf("%s requires default-value-config.source.", context)
	}
	switch source {
	case defaultSourceSystemValue:
		return r.resolveSystemValue(config, dataValueType, context)
	case defaultSourceSettings:
		return r.resolveSettings(config, dataValueType, context)
	case defaultSourceConst:
		return config, r.checkConstRecord(config, referenceSchema, context)
	}
	return config, nil
}

func (r *schemaWriteEntRun) checkConstRecord(config *schemaWriteEntDefaultConfig, referenceSchema, context string) error {
	if strings.TrimSpace(referenceSchema) == "" {
		return nil
	}
	recordID, ok := parseGUID(rawJSONText(config.Value))
	if !ok || recordID == emptyGUID {
		return nil
	}
	key := strings.TrimSpace(referenceSchema) + "|" + recordID
	existence, cached := r.records[key]
	if !cached {
		existence = r.recordExists(strings.TrimSpace(referenceSchema), recordID)
		r.records[key] = existence
	}
	if existence == 2 {
		return fmt.Errorf("%s default value record '%s' was not found in referenced schema '%s'.", context, recordID, referenceSchema)
	}
	return nil
}

// recordExists is CheckRecordExists: 1 exists, 2 not found, 0 unknown (any failure).
func (r *schemaWriteEntRun) recordExists(schemaName, recordID string) int {
	rows, err := r.client.selectRows(r.ctx, buildSelectQuery(schemaName, map[string]string{"Id": "Id"},
		map[string]any{"filter0": comparisonFilter("Id", recordID, 0, 3)}, 1))
	if err != nil {
		return 0
	}
	if len(rows) > 0 {
		return 1
	}
	return 2
}

func (r *schemaWriteEntRun) resolveSystemValue(config *schemaWriteEntDefaultConfig, dataValueType int, context string) (*schemaWriteEntDefaultConfig, error) {
	input := schemaWriteEntTextValue(config.ValueSource)
	if input == nil {
		return nil, fmt.Errorf("%s requires default-value-config.value-source when source is SystemValue.", context)
	}
	typeUID, ok := schemaWriteEntRuntimeTypeUIDs[dataValueType]
	if !ok {
		return nil, fmt.Errorf("Unsupported dataValueType '%d' for default-value-config source SystemValue.", dataValueType)
	}
	values, cached := r.systemValues[typeUID]
	if !cached {
		response := &schemaWriteEntSystemValuesResponse{}
		if err := r.designer("GetSystemValues", struct {
			DataValueTypeUID string `json:"dataValueTypeUId"`
		}{typeUID}, response); err != nil {
			return nil, err
		}
		values = response.Items
		r.systemValues[typeUID] = values
	}
	if len(values) == 0 {
		return nil, fmt.Errorf("%s has no system variables available for dataValueType '%d'.", context, dataValueType)
	}
	describe := func(item schemaWriteEntSystemValue) string {
		return item.Value.String() + " (" + item.DisplayValue + ")"
	}
	var resolved string
	if guid, ok := parseGUID(*input); ok {
		match, err := schemaWriteEntSingle(values, func(item schemaWriteEntSystemValue) bool { return item.Value.String() == guid },
			context, "SystemValue GUID", *input, describe, "Use an exact GUID returned by get-system-values.")
		if err != nil {
			return nil, err
		}
		resolved = match.Value.String()
	} else if alias, known := schemaWriteEntSystemAliases[strings.ToLower(*input)]; known {
		var matches []schemaWriteEntSystemValue
		for _, item := range values {
			if item.Value.String() == alias {
				matches = append(matches, item)
			}
		}
		if len(matches) != 1 {
			return nil, fmt.Errorf("System value alias '%s' is not available for the selected column type.", *input)
		}
		resolved = matches[0].Value.String()
	} else {
		match, err := schemaWriteEntSingle(values, func(item schemaWriteEntSystemValue) bool {
			return schemaWriteEntCaptionMatch(*input, item.DisplayValue)
		},
			context, "SystemValue caption", *input, describe, "Use a GUID or enum alias such as CurrentDateTime.")
		if err != nil {
			return nil, err
		}
		resolved = match.Value.String()
	}
	result := &schemaWriteEntDefaultConfig{Source: config.Source, ValueSource: &resolved, ResolvedValueSource: &resolved}
	return result, nil
}

func schemaWriteEntCaptionMatch(input, caption string) bool {
	if strings.EqualFold(input, caption) {
		return true
	}
	normalize := func(value string) string {
		var builder strings.Builder
		for _, r := range value {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				builder.WriteRune(unicode.ToLower(r))
			}
		}
		return builder.String()
	}
	left, right := normalize(input), normalize(caption)
	return left == right || left == strings.ReplaceAll(right, "and", "")
}

func (r *schemaWriteEntRun) resolveSettings(config *schemaWriteEntDefaultConfig, dataValueType int, context string) (*schemaWriteEntDefaultConfig, error) {
	input := schemaWriteEntTextValue(config.ValueSource)
	if input == nil {
		return nil, fmt.Errorf("%s requires default-value-config.value-source when source is Settings.", context)
	}
	candidates, ok := schemaWriteEntSettingTypes[dataValueType]
	if !ok {
		return nil, fmt.Errorf("Unsupported dataValueType '%d' for default-value-config source Settings.", dataValueType)
	}
	var settings []schemaWriteEntSettingRow
	seen := map[string]bool{}
	for _, valueTypeName := range candidates {
		rows, cached := r.settings[strings.ToLower(valueTypeName)]
		if !cached {
			selected, err := r.client.selectRows(r.ctx, buildSelectQuery("SysSettings",
				map[string]string{"Id": "Id", "Code": "Code", "Name": "Name", "ValueTypeName": "ValueTypeName"},
				map[string]any{"filter0": comparisonFilter("ValueTypeName", valueTypeName, 1, 3)}, -1))
			if err != nil {
				return nil, err
			}
			for _, row := range selected {
				rows = append(rows, schemaWriteEntSettingRow{ID: pkgWriteNormalizeGUID(rowString(row, "Id")), Code: rowString(row, "Code"), Name: rowString(row, "Name")})
			}
			r.settings[strings.ToLower(valueTypeName)] = rows
		}
		for _, row := range rows {
			if !seen[row.ID] {
				seen[row.ID] = true
				settings = append(settings, row)
			}
		}
	}
	if len(settings) == 0 {
		return nil, fmt.Errorf("%s has no system settings available for dataValueType '%d'.", context, dataValueType)
	}
	describe := func(item schemaWriteEntSettingRow) string { return item.Code + " (" + item.Name + ", " + item.ID + ")" }
	var resolved schemaWriteEntSettingRow
	if id, ok := parseGUID(*input); ok {
		match, err := schemaWriteEntSingle(settings, func(item schemaWriteEntSettingRow) bool { return item.ID == id },
			context, "setting id", *input, describe, "Use an exact setting id or setting code.")
		if err != nil {
			return nil, err
		}
		resolved = match
	} else {
		var codeMatches []schemaWriteEntSettingRow
		for _, item := range settings {
			if strings.EqualFold(item.Code, *input) {
				codeMatches = append(codeMatches, item)
			}
		}
		switch {
		case len(codeMatches) == 1:
			resolved = codeMatches[0]
		case len(codeMatches) > 1:
			var described []string
			for _, item := range codeMatches {
				described = append(described, describe(item))
			}
			return nil, fmt.Errorf("%s matched multiple setting code values for '%s': %s. Use an exact setting id.", context, *input, strings.Join(described, ", "))
		default:
			match, err := schemaWriteEntSingle(settings, func(item schemaWriteEntSettingRow) bool { return strings.EqualFold(item.Name, *input) },
				context, "setting name", *input, describe, "Use a unique setting code or exact setting id.")
			if err != nil {
				return nil, err
			}
			resolved = match
		}
	}
	code := resolved.Code
	return &schemaWriteEntDefaultConfig{Source: config.Source, ValueSource: &code, ResolvedValueSource: &code}, nil
}

var schemaWriteEntSettingTypes = map[int][]string{
	0: {"Guid"}, 1: {"Text"}, 4: {"Integer"}, 5: {"Decimal"}, 6: {"Currency"}, 7: {"DateTime"}, 10: {"Lookup"},
	12: {"Boolean"}, 24: {"SecureText"}, 27: {"ShortText", "Text"}, 28: {"MediumText", "Text"},
	29: {"MaxSizeText", "Text"}, 30: {"LongText", "Text"}, 31: {"Decimal"}, 32: {"Decimal"}, 33: {"Decimal"},
	34: {"Decimal"}, 40: {"Decimal"}, 42: {"Text"}, 43: {"Text"}, 44: {"Text"}, 45: {"Text"}, 47: {"Decimal"},
	48: {"Currency"}, 49: {"Currency"}, 50: {"Currency"},
}

// schemaWriteEntSingle is clio's RequireSingleMatch.
func schemaWriteEntSingle[T any](items []T, match func(T) bool, context, kind, value string, describe func(T) string, hint string) (T, error) {
	var matches []T
	for _, item := range items {
		if match(item) {
			matches = append(matches, item)
		}
	}
	var zero T
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return zero, fmt.Errorf("%s could not resolve %s '%s'. %s", context, kind, value, hint)
	}
	var described []string
	for _, item := range matches {
		described = append(described, describe(item))
	}
	return zero, fmt.Errorf("%s matched multiple %s values for '%s': %s. %s", context, kind, value, strings.Join(described, ", "), hint)
}
