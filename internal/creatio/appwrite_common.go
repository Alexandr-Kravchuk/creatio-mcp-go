package creatio

// Shared pieces of clio's application write tools (create-app, create-app-section, update-app-section,
// delete-app-section): the Freedom UI icon palette (ApplicationSectionColorPalette, SectionIconPalette), the
// navigation cache reset (NavigationCacheResetter), the OData build gate (ODataBuildGate), the caption-culture
// override (CaptionCultureResolver), the random SysAppIcons pick, the ApplicationSection record and the
// DataService expression shapes of clio's SelectQueryHelper.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/redact"
)

const (
	appWriteGUIDDataValueType = 0
	appWriteTextDataValueType = 1
	appWriteIntDataValueType  = 4
	// appWriteWebClientTypeID is the web-only Creatio client type: pinning it skips mobile page generation.
	appWriteWebClientTypeID = "195785B4-F55A-4E72-ACE3-6480B54C8FA5"
	appWriteSectionSchema   = "ApplicationSection"
	// appWriteNavigationRoute is ServiceUrlBuilder.KnownRoute.GetConfigurationData.
	appWriteNavigationRoute = "rest/ConfigurationDataService/GetData"
	appWriteNavigationWarn  = "navigation cache reset failed: "
)

// appWritePalette is ApplicationSectionColorPalette.Colors with SectionIconPalette's English names.
var appWritePalette = []struct{ hex, name string }{
	{"#A6DE00", "Lime"}, {"#20A959", "Green"}, {"#22AC14", "Emerald"}, {"#FFAC07", "Amber"},
	{"#FF8800", "Orange"}, {"#F9307F", "Pink"}, {"#FF602E", "Coral"}, {"#FF4013", "Red"},
	{"#B87CCF", "Lavender"}, {"#7848EE", "Violet"}, {"#247EE5", "Sky"}, {"#0058EF", "Blue"},
	{"#009DE3", "Cyan"}, {"#4F43C2", "Indigo"}, {"#08857E", "Teal"}, {"#00BFA5", "Mint"},
}

// appWritePaletteAliases is SectionIconPalette.Aliases (matched case-insensitively).
var appWritePaletteAliases = map[string]string{
	"lime": "#A6DE00", "лайм": "#A6DE00",
	"green": "#20A959", "зелений": "#20A959", "зеленый": "#20A959",
	"emerald": "#22AC14", "смарагд": "#22AC14", "изумруд": "#22AC14", "emerald green": "#22AC14",
	"amber": "#FFAC07", "бурштин": "#FFAC07", "янтарь": "#FFAC07", "yellow": "#FFAC07", "жовтий": "#FFAC07", "жёлтый": "#FFAC07", "желтый": "#FFAC07",
	"orange": "#FF8800", "помаранчевий": "#FF8800", "оранжевый": "#FF8800",
	"pink": "#F9307F", "рожевий": "#F9307F", "розовый": "#F9307F", "magenta": "#F9307F",
	"coral": "#FF602E", "корал": "#FF602E", "коралл": "#FF602E",
	"red": "#FF4013", "червоний": "#FF4013", "красный": "#FF4013",
	"lavender": "#B87CCF", "лаванда": "#B87CCF", "бузковий": "#B87CCF", "сиреневый": "#B87CCF",
	"violet": "#7848EE", "purple": "#7848EE", "фіолетовий": "#7848EE", "фиолетовый": "#7848EE",
	"sky": "#247EE5", "sky blue": "#247EE5", "небесний": "#247EE5", "небесный": "#247EE5", "голубий": "#247EE5", "голубой": "#247EE5",
	"blue": "#0058EF", "синій": "#0058EF", "синий": "#0058EF",
	"cyan": "#009DE3", "циан": "#009DE3", "бірюзовий": "#009DE3", "бирюзовый": "#009DE3",
	"indigo": "#4F43C2", "індиго": "#4F43C2", "индиго": "#4F43C2",
	"teal": "#08857E", "темно-бірюзовий": "#08857E", "тёмно-бирюзовый": "#08857E",
	"mint": "#00BFA5", "м'ятний": "#00BFA5", "мятный": "#00BFA5",
}

var appWriteHexColorPattern = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)

func appWritePaletteJoined() string {
	colors := make([]string, len(appWritePalette))
	for i, entry := range appWritePalette {
		colors[i] = entry.hex
	}
	return strings.Join(colors, ", ")
}

// appWriteValidatePalette is ApplicationSectionColorPalette.ValidateOrThrow.
func appWriteValidatePalette(value string) error {
	if strings.TrimSpace(value) == "" {
		return errors.New("icon-background cannot be empty.")
	}
	trimmed := strings.TrimSpace(value)
	if !appWriteHexColorPattern.MatchString(trimmed) {
		return fmt.Errorf("icon-background '%s' must use #RRGGBB format (six hex digits).", value)
	}
	for _, entry := range appWritePalette {
		if entry.hex == strings.ToUpper(trimmed) {
			return nil
		}
	}
	return fmt.Errorf("icon-background '%s' is not a Freedom UI palette color. Use one of: %s.", value, appWritePaletteJoined())
}

// appWritePickColor is ApplicationSectionColorPalette.PickRandom.
func appWritePickColor() string {
	return appWritePalette[rand.IntN(len(appWritePalette))].hex
}

// AppWriteResolveIconColor is SectionIconPalette.ResolveAsync with elicitation disabled (clio passes no
// server): a palette hex (any case) or a known color name in EN/UA/RU, otherwise clio's refusal.
func AppWriteResolveIconColor(requested string) (string, error) {
	trimmed := strings.TrimSpace(requested)
	if trimmed != "" {
		for _, entry := range appWritePalette {
			if strings.EqualFold(entry.hex, trimmed) {
				return entry.hex, nil
			}
		}
		if hex, ok := appWritePaletteAliases[strings.ToLower(trimmed)]; ok {
			return hex, nil
		}
	}
	names := make([]string, len(appWritePalette))
	for i, entry := range appWritePalette {
		names[i] = fmt.Sprintf("%s (%s)", entry.name, entry.hex)
	}
	lead := "icon-background is required."
	if trimmed != "" {
		lead = fmt.Sprintf("icon-background '%s' is not a recognised color.", requested)
	}
	return "", fmt.Errorf("%s Allowed values: %s. Pass the hex or a color name (EN/UA/RU).", lead, strings.Join(names, ", "))
}

// appWriteBrowserSessionNote is NavigationCacheResetter.BuildBrowserSessionNote.
func (c *Client) appWriteBrowserSessionNote() string {
	return fmt.Sprintf("Open Creatio browser tabs refresh their menu over the websocket only if they were connected when the "+
		"change was made. If a tab still does not show the change after a reload, its server session keeps the "+
		"old menu: run fetch('%s', {method:'POST', headers:{'Content-Type':'application/json', "+
		"BPMCSRF:document.cookie.match(/BPMCSRF=([^;]+)/)[1]}, body:'true'}) in the developer console of that "+
		"tab, then reload the tab. Do not clear Redis for this: it logs out every user.", c.serviceURL(appWriteNavigationRoute))
}

// appWriteResetNavigation is NavigationCacheResetter.TryReset: GetData(forceGet = true) in this client's
// session, 30 s, one attempt. It never fails the caller: a failure comes back as a warning.
func (c *Client) appWriteResetNavigation(ctx context.Context) string {
	response, err := c.serviceRequest(ctx, serviceCall{Route: appWriteNavigationRoute, Body: []byte("true"), Timeout: 30 * time.Second})
	if err != nil {
		return appWriteNavigationWarn + redact.Text(err.Error())
	}
	failure := appWriteNavigationFailure(response.payload)
	if failure == "" {
		return ""
	}
	return appWriteNavigationWarn + redact.Text(failure)
}

// appWriteNavigationFailure is NavigationCacheResetter.ReadFailure: success is read from the body.
func appWriteNavigationFailure(payload []byte) string {
	if strings.TrimSpace(string(payload)) == "" {
		return "empty response"
	}
	var root any
	if err := json.Unmarshal(payload, &root); err != nil {
		return "the response is not JSON"
	}
	object, ok := root.(map[string]any)
	if !ok {
		return "the service did not report success"
	}
	if success, ok := object["success"].(bool); ok && success {
		return ""
	}
	if info, ok := object["errorInfo"].(map[string]any); ok {
		if message, ok := info["message"].(string); ok {
			return message
		}
	}
	return "the service did not report success"
}

// appWriteODataGate is ODataBuildGate.WaitUntilIdle: probe IsODataBuildRunning (10 s, one attempt); when a
// build runs, poll every 3 s up to 30 times. A fault or an unusable answer stops waiting.
func (c *Client) appWriteODataGate(ctx context.Context, interval time.Duration) {
	probe := func() (bool, bool) {
		payload, err := c.callService(ctx, serviceCall{Route: "ServiceModel/WorkspaceExplorerService.svc/IsODataBuildRunning", Body: []byte("{}"), Timeout: 10 * time.Second})
		if err != nil {
			return false, false
		}
		var answer struct {
			Value *bool `json:"value"`
		}
		if json.Unmarshal(payload, &answer) != nil || answer.Value == nil {
			return false, false
		}
		return *answer.Value, true
	}
	running, ok := probe()
	if !ok || !running {
		return
	}
	for attempt := 1; attempt <= 30; attempt++ {
		if !appWriteSleep(ctx, interval) {
			return
		}
		if running, ok = probe(); !ok || !running {
			return
		}
	}
}

// appWriteSleep waits, returning false when the context ends first.
func appWriteSleep(ctx context.Context, delay time.Duration) bool {
	if delay <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// appWriteCaptionCulture is CaptionCultureResolver.Resolve: a valid override wins, otherwise the profile
// culture (en-US when it cannot be read).
func (c *Client) appWriteCaptionCulture(ctx context.Context, override string) (string, error) {
	if strings.TrimSpace(override) != "" {
		trimmed := strings.TrimSpace(override)
		culture, ok := canonicalCultureName(trimmed)
		if !ok {
			return "", fmt.Errorf("--caption-culture '%s' is not a valid culture name (e.g. en-US, uk-UA).", trimmed)
		}
		return culture, nil
	}
	return c.schemaWriteProfileCulture(ctx), nil
}

// appWriteParam is SelectQueryHelper.BuildParameterExpression.
func appWriteParam(dataValueType int, value any) map[string]any {
	return map[string]any{"expressionType": 2, "parameter": map[string]any{"dataValueType": dataValueType, "value": value}}
}

// appWriteIDFilter is SelectQueryHelper.BuildIdFilter.
func appWriteIDFilter(id string, dataValueType int) map[string]any {
	return map[string]any{"filterType": 6, "isEnabled": true, "trimDateTimeParameterToDate": false, "logicalOperation": 0,
		"items": map[string]any{"primaryFilter": map[string]any{"filterType": 1, "comparisonType": 3, "isEnabled": true,
			"trimDateTimeParameterToDate": false, "leftExpression": map[string]any{"expressionType": 0, "columnPath": "Id"},
			"rightExpression": appWriteParam(dataValueType, id)}}}
}

// appWriteDataServiceAnswer is the success/errorInfo head of a DataService answer.
type appWriteDataServiceAnswer struct {
	Success   bool `json:"success"`
	ErrorInfo *struct {
		Message *string `json:"message"`
	} `json:"errorInfo"`
}

func (a appWriteDataServiceAnswer) message(fallback string) string {
	if a.ErrorInfo != nil && a.ErrorInfo.Message != nil {
		return *a.ErrorInfo.Message
	}
	return fallback
}

// appWritePost posts a JSON body to a DataService/ServiceModel route and returns the raw body.
func (c *Client) appWritePost(ctx context.Context, route string, body any, timeout time.Duration) ([]byte, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	return c.callService(ctx, serviceCall{Route: route, Body: encoded, Timeout: timeout})
}

// appWriteRandomIconID is ResolveRandomIconId: every SysAppIcons Id, one picked at random. create-app's
// query localizes (useLocalization true), create-app-section's does not.
func (c *Client) appWriteRandomIconID(ctx context.Context, useLocalization bool) (string, error) {
	emptyFilter := map[string]any{"filterType": 6, "isEnabled": true, "items": map[string]any{}, "logicalOperation": 0, "trimDateTimeParameterToDate": false}
	query := map[string]any{
		"rootSchemaName": "SysAppIcons", "operationType": 0, "allColumns": false, "isDistinct": false,
		"ignoreDisplayValues": false, "rowCount": -1, "rowsOffset": -1, "isPageable": false, "conditionalValues": nil,
		"isHierarchical": false, "hierarchicalMaxDepth": 0, "hierarchicalColumnFiltersValue": emptyFilter,
		"hierarchicalColumnName": nil, "hierarchicalColumnValue": nil, "hierarchicalFullDataLoad": false,
		"useLocalization": useLocalization, "useRecordDeactivation": false,
		"columns": map[string]any{"items": map[string]any{"Id": map[string]any{
			"expression": map[string]any{"expressionType": 0, "columnPath": "Id"}, "orderDirection": 0, "orderPosition": -1, "isVisible": true}}},
		"filters":   map[string]any{"filterType": 6, "isEnabled": true, "trimDateTimeParameterToDate": false, "logicalOperation": 0, "items": map[string]any{}},
		"__type":    "Terrasoft.Nui.ServiceModel.DataContract.SelectQuery",
		"queryKind": 0, "serverESQCacheParameters": map[string]any{"cacheLevel": 0, "cacheGroup": "", "cacheItemName": ""},
		"queryOptimize": false, "useMetrics": false, "querySource": 0,
	}
	payload, err := c.appWritePost(ctx, "DataService/json/SyncReply/SelectQuery", query, 0)
	if err != nil {
		return "", err
	}
	var answer struct {
		appWriteDataServiceAnswer
		Rows []struct {
			ID string `json:"Id"`
		} `json:"rows"`
	}
	if err := json.Unmarshal(payload, &answer); err != nil {
		return "", fmt.Errorf("SysAppIcons query returned an unreadable response: %w", err)
	}
	if !answer.Success {
		return "", errors.New(answer.message("Failed to query SysAppIcons."))
	}
	if len(answer.Rows) == 0 {
		return "", errors.New("No icons found in SysAppIcons.")
	}
	return answer.Rows[rand.IntN(len(answer.Rows))].ID, nil
}

// appWriteSectionRecord is clio's ApplicationSectionRecord.
type appWriteSectionRecord struct {
	ID                string  `json:"Id"`
	ApplicationID     string  `json:"ApplicationId"`
	Caption           *string `json:"Caption"`
	Code              string  `json:"Code"`
	Description       *string `json:"Description"`
	EntitySchemaName  *string `json:"EntitySchemaName"`
	PackageID         *string `json:"PackageId"`
	SectionSchemaUID  *string `json:"SectionSchemaUId"`
	LogoID            *string `json:"LogoId"`
	IconBackground    *string `json:"IconBackground"`
	ClientTypeID      *string `json:"ClientTypeId"`
	CardSchemaUID     *string `json:"CardSchemaUId"`
	SysModuleEntityID *string `json:"SysModuleEntityId"`
}

// section is clio's MapSection into ApplicationSectionResult (AppSection: null members omitted, as clio writes
// them).
func (r appWriteSectionRecord) section() AppSection {
	caption := ""
	if r.Caption != nil {
		caption = *r.Caption
	}
	return AppSection{ID: r.ID, Code: r.Code, Caption: caption, Description: r.Description,
		EntitySchemaName: r.EntitySchemaName, PackageID: r.PackageID, SectionSchemaUID: r.SectionSchemaUID,
		IconID: r.LogoID, IconBackground: r.IconBackground, ClientTypeID: r.ClientTypeID}
}

// appWriteSectionColumns are the ApplicationSection columns the section tools select, in clio's order.
var appWriteSectionColumns = []string{"Id", "ApplicationId", "Caption", "Code", "Description", "EntitySchemaName",
	"PackageId", "SectionSchemaUId", "LogoId", "IconBackground", "ClientTypeId"}

func appWriteSectionQuery(applicationID string, extra ...string) map[string]any {
	columns := map[string]string{}
	for _, column := range append(append([]string{}, appWriteSectionColumns...), extra...) {
		columns[column] = column
	}
	return buildSelectQuery(appWriteSectionSchema, columns,
		map[string]any{"filter0": comparisonFilter("ApplicationId", applicationID, appWriteGUIDDataValueType, 3)}, 10000)
}

// appWriteSelectSections runs the ApplicationSection query. A rejected query fails with its message, or with
// fallback when the server gave none; an empty fallback is SelectQueryHelper's "SelectQuery failed: <detail>".
func (c *Client) appWriteSelectSections(ctx context.Context, query map[string]any, timeout time.Duration, fallback string) ([]appWriteSectionRecord, error) {
	payload, err := c.appWritePost(ctx, "DataService/json/SyncReply/SelectQuery", query, timeout)
	if err != nil {
		return nil, err
	}
	var answer struct {
		appWriteDataServiceAnswer
		Rows []appWriteSectionRecord `json:"rows"`
	}
	if err := json.Unmarshal(payload, &answer); err != nil {
		return nil, fmt.Errorf("ApplicationSection select query returned an unreadable response: %w", err)
	}
	if !answer.Success {
		if fallback == "" {
			return nil, fmt.Errorf("SelectQuery failed: %s", answer.message(string(payload)))
		}
		return nil, errors.New(answer.message(fallback))
	}
	return answer.Rows, nil
}

// appWriteIsTimeout reports a transport failure that is a timeout rather than an unreachable server.
func appWriteIsTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// appWriteStatusIsTimeout is clio's ClassifyHttpStatus: the statuses that mean "not answered in time".
func appWriteStatusIsTimeout(status int) bool {
	switch status {
	case http.StatusRequestTimeout, http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

// appWriteLoadInfo is ApplicationInfoService.GetApplicationInfo returning clio's exceptions as errors.
func (c *Client) appWriteLoadInfo(ctx context.Context, id, code string) (AppInfoResponse, error) {
	return c.appInfo(ctx, strings.TrimSpace(id), strings.TrimSpace(code))
}

// appWriteRetryable is the InvalidOperationException clio's polling loops retry: anything but a transport,
// authentication or cancellation failure.
func appWriteRetryable(ctx context.Context, err error) bool {
	return ctx.Err() == nil && !isTransportError(err) && !isAuthenticationError(err)
}
