package creatio

// update-app-section: clio master's ApplicationSectionUpdateService with its localization support
// (ApplicationSectionLocalizationClient, SectionLocalizationPlanner, CreatioCultureCatalog): the section
// UpdateQuery, the snapshot and restore of the section's other cultures (the platform deletes them on an
// ApplicationSection update), a caption in another culture through UpdateLocalizationQuery, the package data
// binding refresh, verification, and the navigation cache reset.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/redact"
)

// AppSectionUpdateRequest is clio's ApplicationSectionUpdateRequest; nil members were not sent.
type AppSectionUpdateRequest struct {
	ApplicationCode string
	SectionCode     string
	Caption         *string
	Description     *string
	IconID          *string
	IconBackground  *string
	CaptionCulture  string
}

// AppSectionUpdateResponse is clio's ApplicationSectionUpdateContextResponse.
type AppSectionUpdateResponse struct {
	Success             bool        `json:"success"`
	PackageUID          string      `json:"package-u-id,omitempty"`
	PackageName         string      `json:"package-name,omitempty"`
	ApplicationID       string      `json:"application-id,omitempty"`
	ApplicationName     string      `json:"application-name,omitempty"`
	ApplicationCode     string      `json:"application-code,omitempty"`
	ApplicationVersion  *string     `json:"application-version,omitempty"`
	PreviousSection     *AppSection `json:"previous-section,omitempty"`
	Section             *AppSection `json:"section,omitempty"`
	Error               string      `json:"error,omitempty"`
	CaptionCulture      string      `json:"caption-culture,omitempty"`
	CaptionCultureValue *string     `json:"caption-culture-value,omitempty"`
	PreservedCultures   []string    `json:"preserved-cultures,omitzero"`
	Warnings            []string    `json:"warnings,omitempty"`
	NextStep            string      `json:"next-step,omitempty"`
}

// AppSectionUpdateFailure is clio's CreateSectionUpdateContextErrorResponse.
func AppSectionUpdateFailure(message string) AppSectionUpdateResponse {
	return AppSectionUpdateResponse{Error: message}
}

// ValidateAppSectionUpdate is ApplicationSectionUpdateTool.ValidateSectionUpdateArgs.
func ValidateAppSectionUpdate(request AppSectionUpdateRequest, localizationMaps bool) error {
	if strings.TrimSpace(request.ApplicationCode) == "" {
		return errors.New("application-code is required.")
	}
	if strings.TrimSpace(request.SectionCode) == "" {
		return errors.New("section-code is required.")
	}
	if localizationMaps {
		return errors.New("update-app-section is scalar-only. Do not send title-localizations, description-localizations, caption-localizations, or name-localizations.")
	}
	if request.Caption == nil && request.Description == nil && request.IconID == nil && request.IconBackground == nil {
		return errors.New("Provide at least one mutable field: caption, description, icon-id, or icon-background.")
	}
	return nil
}

const (
	appWriteCaptionColumn      = "Caption"
	appWriteDescriptionColumn  = "Description"
	appWriteModuleHeaderColumn = "ModuleHeader"
	appWriteDefaultCulture     = "en-US"
)

// appWriteLocalizationRow is SectionLocalizationRow.
type appWriteLocalizationRow struct {
	CultureName  string  `json:"CultureName"`
	Caption      *string `json:"Caption"`
	Description  *string `json:"Description"`
	ModuleHeader *string `json:"ModuleHeader"`
}

func (r appWriteLocalizationRow) cell(column string) *string {
	switch column {
	case appWriteCaptionColumn:
		return r.Caption
	case appWriteDescriptionColumn:
		return r.Description
	default:
		return r.ModuleHeader
	}
}

func appWriteReadCell(rows []appWriteLocalizationRow, column, culture string) *string {
	for _, row := range rows {
		if strings.EqualFold(row.CultureName, culture) {
			return row.cell(column)
		}
	}
	return nil
}

// appWriteOrderedStrings is an insertion-ordered string map (clio's Dictionary keeps insertion order).
type appWriteOrderedStrings struct {
	keys   []string
	values map[string]string
}

func (m *appWriteOrderedStrings) set(key, value string) {
	if m.values == nil {
		m.values = map[string]string{}
	}
	if _, ok := m.values[key]; !ok {
		m.keys = append(m.keys, key)
	}
	m.values[key] = value
}

func (m *appWriteOrderedStrings) MarshalJSON() ([]byte, error) {
	var buffer bytes.Buffer
	buffer.WriteByte('{')
	for i, key := range m.keys {
		if i > 0 {
			buffer.WriteByte(',')
		}
		name, _ := json.Marshal(key)
		value, _ := json.Marshal(m.values[key])
		buffer.Write(name)
		buffer.WriteByte(':')
		buffer.Write(value)
	}
	buffer.WriteByte('}')
	return buffer.Bytes(), nil
}

// appWriteExpectedCell is ExpectedLocalizationCell.
type appWriteExpectedCell struct {
	column, culture, value string
	verify, target         bool
}

// appWriteLocalizationPlan is SectionLocalizationPlan.
type appWriteLocalizationPlan struct {
	columnKeys     []string
	columns        map[string]*appWriteOrderedStrings
	expected       []appWriteExpectedCell
	preserved      []string
	hasWrites      bool
	snapshot       []appWriteLocalizationRow
	restoresValues bool
}

// appWriteBuildPlan is SectionLocalizationPlanner.BuildPlan.
func appWriteBuildPlan(snapshot []appWriteLocalizationRow, sectionUpdateRan bool, profile, target string,
	localizedCaption *string, captionThroughSection bool, currentProfileCaption string, descriptionThroughSection bool) appWriteLocalizationPlan {
	plan := appWriteLocalizationPlan{columns: map[string]*appWriteOrderedStrings{}, snapshot: snapshot}
	column := func(name string) *appWriteOrderedStrings {
		if existing, ok := plan.columns[name]; ok {
			return existing
		}
		created := &appWriteOrderedStrings{}
		plan.columns[name] = created
		plan.columnKeys = append(plan.columnKeys, name)
		return created
	}
	add := func(name, culture string, value *string, skip bool) {
		if value == nil || *value == "" {
			return
		}
		plan.expected = append(plan.expected, appWriteExpectedCell{column: name, culture: culture, value: *value, verify: !skip})
		if !skip && sectionUpdateRan {
			column(name).set(culture, *value)
		}
	}
	for _, row := range snapshot {
		isProfile := strings.EqualFold(row.CultureName, profile)
		isTarget := localizedCaption != nil && strings.EqualFold(row.CultureName, target)
		add(appWriteCaptionColumn, row.CultureName, row.Caption, isTarget || isProfile && captionThroughSection)
		add(appWriteDescriptionColumn, row.CultureName, row.Description, isProfile && descriptionThroughSection)
		add(appWriteModuleHeaderColumn, row.CultureName, row.ModuleHeader, false)
	}
	plan.restoresValues = sectionUpdateRan && len(plan.columnKeys) > 0
	if localizedCaption != nil {
		column(appWriteCaptionColumn).set(target, *localizedCaption)
		plan.expected = append(plan.expected, appWriteExpectedCell{column: appWriteCaptionColumn, culture: target,
			value: *localizedCaption, verify: !strings.EqualFold(target, appWriteDefaultCulture), target: true})
	}
	if captions, ok := plan.columns[appWriteCaptionColumn]; ok {
		hasProfile := false
		for _, culture := range captions.keys {
			if strings.EqualFold(culture, profile) {
				hasProfile = true
			}
		}
		if !hasProfile {
			captions.set(profile, currentProfileCaption)
		}
	}
	plan.hasWrites = localizedCaption != nil || sectionUpdateRan && len(plan.columnKeys) > 0
	if !plan.hasWrites {
		plan.columnKeys, plan.columns = nil, map[string]*appWriteOrderedStrings{}
	}
	seen := map[string]bool{}
	plan.preserved = []string{}
	for _, cell := range plan.expected {
		if cell.verify && !cell.target && !seen[strings.ToLower(cell.culture)] {
			seen[strings.ToLower(cell.culture)] = true
			plan.preserved = append(plan.preserved, cell.culture)
		}
	}
	sort.SliceStable(plan.preserved, func(i, j int) bool { return compareOrdinalIgnoreCase(plan.preserved[i], plan.preserved[j]) < 0 })
	return plan
}

// UpdateAppSection is ApplicationSectionUpdateService.UpdateSection after the tool's checks and color
// resolution.
func (c *Client) UpdateAppSection(ctx context.Context, request AppSectionUpdateRequest) AppSectionUpdateResponse {
	result, err := c.appWriteUpdateSection(ctx, request)
	if err != nil {
		return AppSectionUpdateFailure(redact.Text(err.Error()))
	}
	return result
}

func (c *Client) appWriteUpdateSection(ctx context.Context, request AppSectionUpdateRequest) (AppSectionUpdateResponse, error) {
	if err := appWriteValidateUpdate(request); err != nil {
		return AppSectionUpdateResponse{}, err
	}
	warnings := []string{}
	profile, _ := c.appWriteCaptionCulture(ctx, "")
	if request.Description != nil {
		if err := schemaWriteCaptionMatchesCulture(profile, *request.Description, "description"); err != nil {
			return AppSectionUpdateResponse{}, err
		}
	}
	target := profile
	if strings.TrimSpace(request.CaptionCulture) != "" {
		name, warning, err := c.appWriteFindCulture(ctx, strings.TrimSpace(request.CaptionCulture))
		if err != nil {
			return AppSectionUpdateResponse{}, err
		}
		if warning != "" {
			warnings = append(warnings, warning)
		}
		target = name
	}
	throughSection := request.Caption != nil && strings.EqualFold(target, profile)
	throughLocalization := request.Caption != nil && !throughSection
	if request.Caption != nil {
		if err := schemaWriteCaptionMatchesCulture(target, *request.Caption, "caption"); err != nil {
			return AppSectionUpdateResponse{}, err
		}
	}
	info, err := c.appWriteLoadInfo(ctx, "", request.ApplicationCode)
	if err != nil {
		return AppSectionUpdateResponse{}, err
	}
	if info.ApplicationID == "" {
		return AppSectionUpdateResponse{}, errors.New("Application id was not returned by get-app-info.")
	}
	previous, err := c.appWriteSectionByCode(ctx, info.ApplicationID, request.SectionCode)
	if err != nil {
		return AppSectionUpdateResponse{}, err
	}
	snapshot, err := c.appWriteReadLocalizations(ctx, previous.ID)
	if err != nil {
		return AppSectionUpdateResponse{}, err
	}
	var caption *string
	if throughSection {
		caption = appWriteTrimmed(request.Caption)
	}
	description, iconID, iconBackground := appWriteTrimmed(request.Description), appWriteTrimmed(request.IconID), appWriteTrimmed(request.IconBackground)
	updateNeeded := throughSection || description != nil || iconID != nil || iconBackground != nil
	if updateNeeded {
		if err := c.appWriteUpdateSectionRow(ctx, previous, caption, description, iconID, iconBackground); err != nil {
			return AppSectionUpdateResponse{}, err
		}
	}
	snapshotProfileCaption := appWriteReadCell(snapshot, appWriteCaptionColumn, profile)
	currentProfileCaption := ""
	switch {
	case throughSection:
		currentProfileCaption = *caption
	case snapshotProfileCaption != nil:
		currentProfileCaption = *snapshotProfileCaption
	case previous.Caption != nil:
		currentProfileCaption = *previous.Caption
	}
	var localized *string
	if throughLocalization {
		localized = appWriteTrimmed(request.Caption)
	}
	plan := appWriteBuildPlan(snapshot, updateNeeded, profile, target, localized, throughSection, currentProfileCaption, description != nil)
	if plan.hasWrites && !throughSection && snapshotProfileCaption == nil && !strings.EqualFold(profile, appWriteDefaultCulture) {
		if captions, ok := plan.columns[appWriteCaptionColumn]; ok {
			for _, culture := range captions.keys {
				if strings.EqualFold(culture, profile) {
					warnings = append(warnings, fmt.Sprintf("The section title had no '%[1]s' translation, and Creatio requires one when titles in other cultures are "+
						"written, so '%[1]s' now holds the fallback title '%[2]s'. Translate it with update-app-section --caption "+
						"--caption-culture %[1]s if that text is wrong for %[1]s.", profile, currentProfileCaption))
					break
				}
			}
		}
	}
	if err := c.appWriteApplyPlan(ctx, previous.ID, info.PackageUID, previous.Code, plan, &warnings); err != nil {
		return AppSectionUpdateResponse{}, err
	}
	updated, err := c.appWriteSectionByCode(ctx, info.ApplicationID, request.SectionCode)
	if err != nil {
		return AppSectionUpdateResponse{}, err
	}
	stored := snapshot
	if plan.hasWrites || updateNeeded {
		if stored, err = c.appWriteReadLocalizations(ctx, previous.ID); err != nil {
			return AppSectionUpdateResponse{}, err
		}
	}
	var mismatches []string
	for _, cell := range plan.expected {
		if !cell.verify {
			continue
		}
		if value := appWriteReadCell(stored, cell.column, cell.culture); value == nil || *value != cell.value {
			mismatches = append(mismatches, fmt.Sprintf("%s [%s]", cell.column, cell.culture))
		}
	}
	if len(mismatches) > 0 {
		return AppSectionUpdateResponse{}, fmt.Errorf("The section was saved, but these localized values are not stored as expected: %s. "+
			"Check the culture in the Languages section and the section in Creatio, then retry.", strings.Join(mismatches, ", "))
	}
	if warning := c.appWriteResetNavigation(ctx); warning != "" {
		warnings = append(warnings, warning)
	}
	var cultureValue *string
	if request.Caption != nil {
		if throughSection {
			cultureValue = updated.Caption
		} else {
			cultureValue = appWriteReadCell(stored, appWriteCaptionColumn, target)
			if cultureValue == nil && !strings.EqualFold(target, profile) && strings.EqualFold(target, appWriteDefaultCulture) {
				warnings = append(warnings, fmt.Sprintf("The caption in '%s' is stored in the section record itself and could not be read back "+
					"under the current profile culture; verify it in Creatio.", target))
				cultureValue = appWriteTrimmed(request.Caption)
			}
		}
	}
	previousSection, updatedSection := previous.section(), updated.section()
	response := AppSectionUpdateResponse{Success: true, PackageUID: info.PackageUID, PackageName: info.PackageName,
		ApplicationID: info.ApplicationID, ApplicationName: info.ApplicationName,
		ApplicationCode: firstNonEmpty(info.ApplicationCode, request.ApplicationCode), ApplicationVersion: info.ApplicationVersion,
		PreviousSection: &previousSection, Section: &updatedSection, CaptionCultureValue: cultureValue,
		PreservedCultures: plan.preserved, NextStep: c.appWriteBrowserSessionNote()}
	if request.Caption != nil {
		response.CaptionCulture = target
	}
	if len(warnings) > 0 {
		response.Warnings = warnings
	}
	return response, nil
}

// appWriteValidateUpdate is ApplicationSectionUpdateService.ValidateRequest.
func appWriteValidateUpdate(request AppSectionUpdateRequest) error {
	if strings.TrimSpace(request.ApplicationCode) == "" {
		return errors.New("application-code is required.")
	}
	if strings.TrimSpace(request.SectionCode) == "" {
		return errors.New("section-code is required.")
	}
	if request.Caption == nil && request.Description == nil && request.IconID == nil && request.IconBackground == nil {
		return errors.New("At least one mutable field is required: caption, description, icon-id, or icon-background.")
	}
	if request.Caption != nil && strings.TrimSpace(*request.Caption) == "" {
		return errors.New("caption cannot be empty.")
	}
	if request.Caption == nil && strings.TrimSpace(request.CaptionCulture) != "" {
		return errors.New("caption-culture requires caption.")
	}
	if request.Description != nil && strings.TrimSpace(*request.Description) == "" {
		return errors.New("description cannot be empty.")
	}
	if request.IconID != nil {
		if _, ok := parseGUID(*request.IconID); !ok {
			return errors.New("icon-id must be a valid GUID.")
		}
	}
	if request.IconBackground != nil {
		return appWriteValidatePalette(*request.IconBackground)
	}
	return nil
}

// appWriteFindCulture is CreatioCultureCatalog.Find with CultureMessages: the stored culture name, a warning
// when it is inactive, a failure when it is absent.
func (c *Client) appWriteFindCulture(ctx context.Context, requested string) (string, string, error) {
	rows, err := c.selectRows(ctx, buildSelectQuery("SysCulture", map[string]string{"Name": "Name", "Active": "Active"}, nil, 10000))
	if err != nil {
		return "", "", err
	}
	var names []string
	for _, row := range rows {
		name := rowString(row, "Name")
		if strings.TrimSpace(name) == "" {
			continue
		}
		names = append(names, name)
		if strings.EqualFold(name, requested) {
			var active bool
			_ = json.Unmarshal(row["Active"], &active)
			if !active {
				return name, fmt.Sprintf("Culture '%s' exists but is inactive; users cannot select it until it is activated in the Languages section. "+
					"After activating it, run a full configuration compile (clio compile-configuration --all); until then the "+
					"UI does not load in that culture.", name), nil
			}
			return name, "", nil
		}
	}
	return "", "", fmt.Errorf("Culture '%s' is not available in this environment. Add it in the Languages section "+
		"(System Designer → Languages) first. Available: %s.", requested, strings.Join(names, ", "))
}

// appWriteSectionByCode is the update/delete GetSectionRecord: the section of the application with the code.
func (c *Client) appWriteSectionByCode(ctx context.Context, applicationID, code string, extra ...string) (appWriteSectionRecord, error) {
	rows, err := c.appWriteSelectSections(ctx, appWriteSectionQuery(applicationID, extra...), 0, "ApplicationSection select query failed.")
	if err != nil {
		return appWriteSectionRecord{}, err
	}
	for _, row := range rows {
		if strings.EqualFold(row.Code, code) {
			return row, nil
		}
	}
	return appWriteSectionRecord{}, fmt.Errorf("Section '%s' was not found in application '%s'.", code, applicationID)
}

// appWriteUpdateSectionRow is ExecuteSectionUpdate.
func (c *Client) appWriteUpdateSectionRow(ctx context.Context, previous appWriteSectionRecord, caption, description, iconID, iconBackground *string) error {
	logo, pkg, background := "", "", ""
	if previous.LogoID != nil {
		logo = *previous.LogoID
	}
	if iconID != nil {
		logo = *iconID
	}
	if previous.PackageID != nil {
		pkg = *previous.PackageID
	}
	if previous.IconBackground != nil {
		background = *previous.IconBackground
	}
	if iconBackground != nil {
		background = *iconBackground
	}
	items := map[string]any{
		"Id":             appWriteParam(appWriteGUIDDataValueType, previous.ID),
		"ApplicationId":  appWriteParam(appWriteGUIDDataValueType, previous.ApplicationID),
		"LogoId":         appWriteParam(appWriteGUIDDataValueType, logo),
		"PackageId":      appWriteParam(appWriteGUIDDataValueType, pkg),
		"IconBackground": appWriteParam(appWriteTextDataValueType, background),
	}
	if caption != nil {
		items["Caption"] = appWriteParam(appWriteTextDataValueType, *caption)
	}
	if description != nil {
		items["Description"] = appWriteParam(appWriteTextDataValueType, *description)
	}
	payload, err := c.appWritePost(ctx, "DataService/json/SyncReply/UpdateQuery", map[string]any{
		"__type": "Terrasoft.Nui.ServiceModel.DataContract.UpdateQuery", "operationType": 2,
		"rootSchemaName": appWriteSectionSchema, "isForceUpdate": false, "columnValues": map[string]any{"items": items},
		"filters": appWriteIDFilter(previous.ID, appWriteTextDataValueType)}, 0)
	if err != nil {
		return err
	}
	var answer *appWriteDataServiceAnswer
	if err := json.Unmarshal(payload, &answer); err != nil {
		return fmt.Errorf("UpdateQuery returned an unreadable response: %w", err)
	}
	if answer == nil {
		return errors.New("UpdateQuery returned an empty response.")
	}
	if !answer.Success {
		return errors.New(answer.message("UpdateQuery failed."))
	}
	return nil
}

// appWriteDescribeServerText is ApplicationSectionLocalizationClient.DescribeServerText: redacted, then cut.
func appWriteDescribeServerText(text string) string {
	return dataWriteTruncate(redact.Text(text))
}

// appWriteReadLocalizations is ApplicationSectionLocalizationClient.ReadLocalizations.
func (c *Client) appWriteReadLocalizations(ctx context.Context, sectionID string) ([]appWriteLocalizationRow, error) {
	query := buildSelectQuery("SysModule", map[string]string{"CultureName": "SysCulture.Name", "Caption": "Caption",
		"Description": "Description", "ModuleHeader": "ModuleHeader"},
		map[string]any{"filter0": comparisonFilter("Record", sectionID, appWriteGUIDDataValueType, 3)}, 10000)
	payload, err := c.appWritePost(ctx, "DataService/json/SyncReply/SelectLocalizationQuery", query, 0)
	if err != nil {
		return nil, err
	}
	var answer struct {
		appWriteDataServiceAnswer
		Rows []appWriteLocalizationRow `json:"rows"`
	}
	if err := json.Unmarshal(payload, &answer); err != nil {
		return nil, fmt.Errorf("SelectLocalizationQuery returned an unparseable response: %w", err)
	}
	if !answer.Success {
		return nil, fmt.Errorf("SelectLocalizationQuery failed: %s", appWriteDescribeServerText(answer.message(string(payload))))
	}
	rows := []appWriteLocalizationRow{}
	for _, row := range answer.Rows {
		if strings.TrimSpace(row.CultureName) != "" {
			rows = append(rows, row)
		}
	}
	return rows, nil
}

// appWriteApplyPlan is SectionLocalizationPlanner.Apply.
func (c *Client) appWriteApplyPlan(ctx context.Context, sectionID, packageUID, sectionCode string, plan appWriteLocalizationPlan, warnings *[]string) error {
	if !plan.hasWrites {
		return nil
	}
	if err := c.appWriteWriteLocalizations(ctx, sectionID, plan); err != nil {
		if plan.restoresValues && ctx.Err() == nil {
			var parts []string
			quote := func(value *string) string {
				if value == nil {
					return "(none)"
				}
				return "'" + *value + "'"
			}
			for _, row := range plan.snapshot {
				parts = append(parts, fmt.Sprintf("%s: Caption=%s, Description=%s, ModuleHeader=%s", row.CultureName, quote(row.Caption), quote(row.Description), quote(row.ModuleHeader)))
			}
			return fmt.Errorf("The section was updated, but writing back its values in other cultures failed: %s The platform deleted them during the update. "+
				"Re-send each value with update-app-section caption + caption-culture, or enter it in Creatio. Values before the update: %s.",
				err.Error(), strings.Join(parts, "; "))
		}
		return err
	}
	found, err := c.appWriteRefreshBinding(ctx, packageUID, sectionCode)
	switch {
	case err != nil && ctx.Err() == nil:
		*warnings = append(*warnings, fmt.Sprintf("The translated section title is stored, but re-saving package data binding 'SysModule_%s' failed: %s",
			sectionCode, appWriteDescribeServerText(err.Error())))
	case err == nil && !found:
		*warnings = append(*warnings, fmt.Sprintf("Package data binding 'SysModule_%s' was not found in the application package; the "+
			"translated section title is stored in the environment but is not part of the package data.", sectionCode))
	}
	return nil
}

// appWriteWriteLocalizations is ApplicationSectionLocalizationClient.WriteLocalizations.
func (c *Client) appWriteWriteLocalizations(ctx context.Context, sectionID string, plan appWriteLocalizationPlan) error {
	items := map[string]any{}
	for _, name := range plan.columnKeys {
		values := plan.columns[name]
		if len(values.keys) == 0 {
			continue
		}
		encoded, _ := values.MarshalJSON()
		items[name] = appWriteParam(19, string(encoded))
	}
	if len(items) == 0 {
		return nil
	}
	payload, err := c.appWritePost(ctx, "DataService/json/SyncReply/UpdateLocalizationQuery", map[string]any{
		"rootSchemaName": "SysModule", "operationType": 2, "isForceUpdate": false,
		"columnValues": map[string]any{"items": items}, "filters": appWriteIDFilter(sectionID, appWriteGUIDDataValueType)}, 0)
	if err != nil {
		return err
	}
	return appWriteThrowIfUnsuccessful(payload, "UpdateLocalizationQuery")
}

// appWriteThrowIfUnsuccessful is DataServiceResponse.ThrowIfUnsuccessful.
func appWriteThrowIfUnsuccessful(payload []byte, operation string) error {
	if strings.TrimSpace(string(payload)) == "" {
		return nil
	}
	var root any
	if err := json.Unmarshal(payload, &root); err != nil {
		return fmt.Errorf("%s failed: the environment answered with a body that is not a service response "+
			"(an authentication redirect or an error page), so the request never reached the service.", operation)
	}
	object, ok := root.(map[string]any)
	if !ok {
		return fmt.Errorf("%s failed: the environment answered with a bare JSON value rather than a service "+
			"response envelope, so the request never reached the service.", operation)
	}
	success, present := object["success"]
	if !present {
		return nil
	}
	flag, isBool := success.(bool)
	if !isBool {
		return fmt.Errorf("%s failed: the environment answered with a 'success' flag that is not a "+
			"true/false value, so whether the write reached the service cannot be determined.", operation)
	}
	if flag {
		return nil
	}
	message := "Unknown error"
	if info, ok := object["errorInfo"].(map[string]any); ok {
		if text, ok := info["message"].(string); ok {
			message = text
		}
	} else if status, ok := object["responseStatus"].(map[string]any); ok {
		if text, ok := status["Message"].(string); ok {
			message = text
		}
	}
	guidance := ""
	if strings.Contains(strings.ToLower(message), "does not have permissions for the") {
		guidance = " This is an object-permission refusal, not a bad request: DB-first bindings apply rows through " +
			"the DataService, which enforces object permissions, so a protected system object is refused " +
			"regardless of the authenticated user's administrative rights. Bindings for ordinary schemas are " +
			"unaffected. For record-level access rights use the set-record-rights tool (it goes through the " +
			"native RightsService instead). Object-operation rights (SysEntitySchemaOperationRight) have no " +
			"administration-capable path in clio yet — deploy them through Creatio's own Object permissions " +
			"administration or a package installation script."
	}
	return fmt.Errorf("%s failed: %s%s", operation, message, guidance)
}

// appWriteRefreshBinding is RefreshSectionPackageBinding: re-save the SysModule_<code> data binding of the
// application package unchanged, so its snapshot re-reads every culture. false when there is no binding.
func (c *Client) appWriteRefreshBinding(ctx context.Context, packageUID, sectionCode string) (bool, error) {
	bindingName := "SysModule_" + sectionCode
	query := buildSelectQuery("SysPackageSchemaData", map[string]string{"UId": "UId"}, map[string]any{
		"filter0": comparisonFilter("Name", bindingName, appWriteTextDataValueType, 3),
		"filter1": comparisonFilter("SysPackage.UId", packageUID, appWriteGUIDDataValueType, 3),
	}, 10000)
	payload, err := c.appWritePost(ctx, "DataService/json/SyncReply/SelectQuery", query, 0)
	if err != nil {
		return false, err
	}
	var answer struct {
		appWriteDataServiceAnswer
		Rows []struct {
			UID string `json:"UId"`
		} `json:"rows"`
	}
	if err := json.Unmarshal(payload, &answer); err != nil {
		return false, fmt.Errorf("SelectQuery returned an unparseable response: %w", err)
	}
	if !answer.Success {
		return false, fmt.Errorf("SelectQuery failed: %s", appWriteDescribeServerText(answer.message(string(payload))))
	}
	bindingUID := ""
	for _, row := range answer.Rows {
		if strings.TrimSpace(row.UID) != "" {
			bindingUID = row.UID
			break
		}
	}
	if bindingUID == "" {
		return false, nil
	}
	schemaPayload, err := c.appWritePost(ctx, "ServiceModel/SchemaDataDesignerService.svc/GetSchema", map[string]string{"schemaUId": bindingUID}, 0)
	if err != nil {
		return false, err
	}
	if err := appWriteThrowIfUnsuccessful(schemaPayload, "SchemaDataDesignerService.GetSchema"); err != nil {
		return false, err
	}
	root, err := parseJNode(schemaPayload)
	if err != nil || !root.isObject() || !root.get("schema").isObject() {
		return false, fmt.Errorf("SchemaDataDesignerService.GetSchema returned no schema for binding '%s'.", bindingName)
	}
	schema := root.get("schema")
	boundPayload, err := c.appWritePost(ctx, "ServiceModel/SchemaDataDesignerService.svc/GetBoundSchemaData", map[string]string{"uId": bindingUID}, 0)
	if err != nil {
		return false, err
	}
	if err := appWriteThrowIfUnsuccessful(boundPayload, "SchemaDataDesignerService.GetBoundSchemaData"); err != nil {
		return false, err
	}
	var bound struct {
		Items *string `json:"items"`
	}
	_ = json.Unmarshal(boundPayload, &bound)
	if bound.Items == nil || strings.TrimSpace(*bound.Items) == "" {
		return false, fmt.Errorf("SchemaDataDesignerService.GetBoundSchemaData returned no rows for binding '%s'.", bindingName)
	}
	var records []map[string]any
	_ = json.Unmarshal([]byte(*bound.Items), &records)
	ids := newArray()
	for _, record := range records {
		if id, ok := record["Id"].(string); ok && strings.TrimSpace(id) != "" {
			ids.items = append(ids.items, newString(id))
		}
	}
	if len(ids.items) == 0 {
		return false, fmt.Errorf("SchemaDataDesignerService.GetBoundSchemaData returned no record ids for binding '%s'.", bindingName)
	}
	schema.set("boundRecordIds", ids)
	savePayload, err := c.callService(ctx, serviceCall{Route: "ServiceModel/SchemaDataDesignerService.svc/SaveSchema", Body: schema.stjJSON()})
	if err != nil {
		return false, err
	}
	return true, appWriteThrowIfUnsuccessful(savePayload, "SchemaDataDesignerService.SaveSchema")
}
