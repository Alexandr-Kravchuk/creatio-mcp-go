package creatio

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// themeWriteFake is a small Creatio for the branding writes: sys-settings with All-Users values, packages,
// package data bindings, features, images and the background gallery. It records every request as
// "<route> <detail>" so a test can check order and payloads.
type themeWriteFake struct {
	t        *testing.T
	mu       sync.Mutex
	server   *httptest.Server
	settings map[string]*themeWriteFakeSetting
	// currentPackage is the CurrentPackageId value (a package UId).
	currentPackage string
	packages       []map[string]any
	bindings       map[string]string // binding name -> entity schema
	saved          []map[string]any  // SaveSchema bodies
	deleted        []string
	features       map[string]string // feature code -> id
	featureStates  map[string]any    // feature id -> FeatureState value of the All-Users row
	images         map[string]bool
	galleryRows    map[string]string // image id -> SysImageInTag row id
	galleryTagID   string
	requests       []string
	// refuse lists setting codes PostSysSettingsValues refuses.
	refuse map[string]bool
}

type themeWriteFakeSetting struct {
	id, valueType, value string
	allUsers             bool
}

func newThemeWriteFake(t *testing.T) *themeWriteFake {
	fake := &themeWriteFake{t: t, settings: map[string]*themeWriteFakeSetting{}, bindings: map[string]string{},
		features: map[string]string{}, featureStates: map[string]any{}, images: map[string]bool{},
		galleryRows: map[string]string{}, galleryTagID: "273c2402-7cae-456b-a9c4-067d2024f1a7", refuse: map[string]bool{},
		currentPackage: "11111111-1111-1111-1111-111111111111"}
	fake.packages = []map[string]any{
		{"Name": "UsrBrand", "UId": "11111111-1111-1111-1111-111111111111", "InstallType": 0},
		{"Name": "CrtBase", "UId": "22222222-2222-2222-2222-222222222222", "InstallType": 1},
	}
	for index, code := range []string{"LogoImage", "MenuLogoImage", "ConfigurationPageLogoImage", "CrtAppToolbarLogo", "FaviconImage"} {
		fake.settings[code] = &themeWriteFakeSetting{id: fmt.Sprintf("aaaaaaaa-0000-0000-0000-%012d", index), valueType: "Binary"}
	}
	fake.settings["HideSplashScreenLogoImage"] = &themeWriteFakeSetting{id: "bbbbbbbb-0000-0000-0000-000000000001", valueType: "Boolean", value: "false", allUsers: true}
	fake.settings["UseFaviconFromSysSettings"] = &themeWriteFakeSetting{id: "bbbbbbbb-0000-0000-0000-000000000002", valueType: "Boolean", value: "false", allUsers: true}
	fake.settings["FileSecurityMode"] = &themeWriteFakeSetting{id: "cccccccc-0000-0000-0000-000000000001", valueType: "Lookup",
		value: "9801c625-fafb-4ed3-9383-c3c942a5c1e3", allUsers: true}
	fake.settings["CrtBackgroundConfig"] = &themeWriteFakeSetting{id: "dddddddd-0000-0000-0000-000000000001", valueType: "Text", allUsers: true}
	fake.features["UsePanelIconBackground"] = "eeeeeeee-0000-0000-0000-000000000001"
	fake.featureStates["eeeeeeee-0000-0000-0000-000000000001"] = 1
	fake.server = httptest.NewServer(http.HandlerFunc(fake.serve))
	t.Cleanup(fake.server.Close)
	return fake
}

func (f *themeWriteFake) client() *Client {
	client, _ := NewClient(Config{BaseURL: f.server.URL, Login: "example-user", Password: "replace-me"})
	return client
}

func (f *themeWriteFake) log(entry string) { f.requests = append(f.requests, entry) }

func (f *themeWriteFake) count(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, entry := range f.requests {
		if strings.HasPrefix(entry, prefix) {
			n++
		}
	}
	return n
}

// themeWriteFakeFilters reads "column -> value" from a SelectQuery's filter group.
func themeWriteFakeFilters(query map[string]any) map[string]string {
	result := map[string]string{}
	filters, _ := query["filters"].(map[string]any)
	items, _ := filters["items"].(map[string]any)
	for _, item := range items {
		filter, _ := item.(map[string]any)
		left, _ := filter["leftExpression"].(map[string]any)
		right, _ := filter["rightExpression"].(map[string]any)
		parameter, _ := right["parameter"].(map[string]any)
		column, _ := left["columnPath"].(string)
		result[column] = fmt.Sprint(parameter["value"])
	}
	return result
}

func (f *themeWriteFake) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	var decoded map[string]any
	_ = json.Unmarshal(body, &decoded)
	path := strings.TrimPrefix(r.URL.Path, "/0/")
	write := func(value any) { _ = json.NewEncoder(w).Encode(value) }
	switch {
	case r.URL.Path == "/ServiceModel/AuthService.svc/Login":
		_, _ = w.Write([]byte(`{"Code":0}`))
	case path == "DataService/json/SyncReply/SelectQuery":
		root, _ := decoded["rootSchemaName"].(string)
		filters := themeWriteFakeFilters(decoded)
		f.log("SelectQuery " + root)
		write(map[string]any{"success": true, "rows": f.selectRows(root, filters)})
	case path == "DataService/json/SyncReply/QuerySysSettings":
		f.log("QuerySysSettings")
		write(map[string]any{"success": true, "values": map[string]any{"CurrentPackageId": f.currentPackage}})
	case path == "DataService/json/SyncReply/PostSysSettingsValues":
		values, _ := decoded["sysSettingsValues"].(map[string]any)
		saveResult := map[string]bool{}
		for code, value := range values {
			f.log(fmt.Sprintf("PostSysSettingsValues %s=%v", code, value))
			setting := f.settings[code]
			if setting == nil || f.refuse[code] {
				saveResult[code] = false
				continue
			}
			setting.value, setting.allUsers = fmt.Sprint(value), true
			saveResult[code] = true
		}
		write(map[string]any{"success": true, "saveResult": saveResult})
	case path == "DataService/json/SyncReply/RuntimeEntitySchemaRequest":
		name, _ := decoded["Name"].(string)
		f.log("RuntimeEntitySchemaRequest " + name)
		columns := map[string]any{}
		for index, column := range []string{"Id", "Name", "Code", "SysSettings", "SysAdminUnit", "IsDef", "TextValue", "IntegerValue",
			"FloatValue", "BooleanValue", "DateTimeValue", "GuidValue", "BinaryValue", "Feature", "FeatureState", "Data", "MimeType",
			"Entity", "Tag"} {
			columns[fmt.Sprintf("c%d", index)] = map[string]any{"uId": fmt.Sprintf("00000000-0000-0000-0000-%012d", index+1), "name": column, "dataValueType": 1}
		}
		write(map[string]any{"success": true, "schema": map[string]any{"uId": "99999999-0000-0000-0000-000000000001", "name": name,
			"primaryColumnUId": "00000000-0000-0000-0000-000000000001", "columns": map[string]any{"items": columns}}})
	case path == "ServiceModel/SchemaDataDesignerService.svc/SaveSchema":
		name, _ := decoded["name"].(string)
		schema, _ := decoded["entitySchemaName"].(string)
		f.log("SaveSchema " + name)
		f.bindings[name] = schema
		f.saved = append(f.saved, decoded)
		write(map[string]any{"success": true})
	case path == "DataService/json/SyncReply/DeletePackageSchemaDataRequest":
		name, _ := decoded["packageSchemaDataName"].(string)
		f.log("DeletePackageSchemaDataRequest " + name)
		delete(f.bindings, name)
		f.deleted = append(f.deleted, name)
		write(map[string]any{"success": true})
	case path == "DataService/json/SyncReply/BatchQuery":
		items, _ := decoded["items"].([]any)
		query, _ := items[0].(map[string]any)
		kind, _ := query["__type"].(string)
		f.log("BatchQuery " + kind[strings.LastIndex(kind, ".")+1:])
		f.featureStates["eeeeeeee-0000-0000-0000-000000000001"] = 0
		write(map[string]any{"success": true, "queryResults": []any{map[string]any{"success": true}}})
	case strings.HasPrefix(path, "rest/FeatureService/ClearFeaturesCacheForAllUsers/"):
		code, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(path, "rest/FeatureService/ClearFeaturesCacheForAllUsers/"))
		f.log("ClearFeaturesCacheForAllUsers " + string(code))
		write(map[string]any{"success": true})
	case path == "ImageAPIService/upload":
		id := r.URL.Query().Get("fileId")
		f.log("upload " + id)
		f.images[id] = true
		_, _ = w.Write([]byte(`{"success":true}`))
	case strings.HasPrefix(path, "img/entity/hash/SysImage/Data/"):
		_, _ = w.Write([]byte("png bytes"))
	case path == "odata/SysImage" && r.Method == http.MethodGet:
		f.log("odata SysImage " + r.URL.Query().Get("$filter"))
		id := strings.TrimPrefix(r.URL.Query().Get("$filter"), "Id eq ")
		rows := []any{}
		if f.images[id] {
			rows = append(rows, map[string]any{"Id": id})
		}
		write(map[string]any{"value": rows})
	case path == "odata/SysImageInTag" && r.Method == http.MethodGet:
		filter := r.URL.Query().Get("$filter")
		f.log("odata SysImageInTag " + filter)
		rows := []any{}
		for image, row := range f.galleryRows {
			if strings.Contains(filter, "Entity/Id eq "+image+" and Tag/Id eq "+f.galleryTagID) {
				rows = append(rows, map[string]any{"Id": row})
			}
		}
		write(map[string]any{"value": rows})
	case path == "odata/SysImageInTag" && r.Method == http.MethodPost:
		f.log("odata POST SysImageInTag " + string(body))
		image, _ := decoded["EntityId"].(string)
		f.galleryRows[image] = "ffffffff-0000-0000-0000-000000000001"
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"Id":"ffffffff-0000-0000-0000-000000000001"}`))
	default:
		f.t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *themeWriteFake) selectRows(root string, filters map[string]string) []map[string]any {
	rows := []map[string]any{}
	switch root {
	case "SysSettings":
		if setting := f.settings[filters["Code"]]; setting != nil {
			rows = append(rows, map[string]any{"Id": setting.id, "ValueTypeName": setting.valueType})
		}
	case "SysSettingsValue":
		for _, setting := range f.settings {
			if setting.id != filters["SysSettings"] || !setting.allUsers {
				continue
			}
			row := map[string]any{"Id": "7" + setting.id[1:], "TextValue": setting.value}
			if setting.valueType == "Boolean" {
				row["BooleanValue"] = setting.value == "true"
			}
			if setting.valueType == "Lookup" {
				row["GuidValue"] = setting.value
			}
			rows = append(rows, row)
		}
	case "SysPackage":
		for _, item := range f.packages {
			if uid, ok := filters["UId"]; !ok || uid == item["UId"] {
				rows = append(rows, item)
			}
		}
	case "SysPackageSchemaData":
		if schema, ok := f.bindings[filters["Name"]]; ok {
			rows = append(rows, map[string]any{"UId": "88888888-0000-0000-0000-000000000001", "EntitySchemaName": schema})
		}
	case "AppFeature", "Feature":
		if id, ok := f.features[filters["Code"]]; ok {
			rows = append(rows, map[string]any{"Id": id, "Code": filters["Code"]})
		}
	case "AdminUnitFeatureState":
		if state, ok := f.featureStates[filters["Feature"]]; ok {
			rows = append(rows, map[string]any{"Id": "abababab-0000-0000-0000-000000000001", "FeatureState": state})
		}
	case "AppFeatureState":
		rows = append(rows, map[string]any{"Id": filters["Id"]})
	case "SysImage", "SysImageInTag":
		rows = append(rows, map[string]any{"Id": filters["Id"]})
	default:
		f.t.Errorf("unexpected SelectQuery on %s", root)
	}
	return rows
}

func TestSetLogoAppliesBindsAndTurnsCompanionsOn(t *testing.T) {
	fake := newThemeWriteFake(t)
	logo := themeWriteTestImage(t, "logo.png", []byte("png"))
	favicon := themeWriteTestImage(t, "icon.ico", []byte("ico"))

	result := fake.client().SetLogo(t.Context(), SetLogoRequest{Logo: logo, Favicon: favicon})

	if !result.Success || result.Error != "" || result.Package != "UsrBrand" || len(result.Warnings) != 0 {
		t.Fatalf("result = %#v", result)
	}
	if strings.Join(result.Applied, ",") != "login-logo,menu-logo,configuration-logo,dark-logo,favicon" {
		t.Errorf("applied = %v", result.Applied)
	}
	wantBound := "LogoImage,MenuLogoImage,ConfigurationPageLogoImage,CrtAppToolbarLogo,FaviconImage,HideSplashScreenLogoImage,UseFaviconFromSysSettings"
	if strings.Join(result.Bound, ",") != wantBound {
		t.Errorf("bound = %v", result.Bound)
	}
	encoded := base64.StdEncoding.EncodeToString([]byte("png"))
	for _, want := range []string{"PostSysSettingsValues LogoImage=" + encoded, "PostSysSettingsValues HideSplashScreenLogoImage=true",
		"PostSysSettingsValues UseFaviconFromSysSettings=true", "SaveSchema SysSettingsValue_FaviconImage"} {
		if !themeWriteContainsFold(fake.requests, want) {
			t.Errorf("missing request %q in %v", want, fake.requests)
		}
	}
	// The value binding keys on the setting's natural key and force-updates the value columns.
	for _, saved := range fake.saved {
		if saved["name"] != "SysSettingsValue_LogoImage" {
			continue
		}
		if saved["entitySchemaName"] != "SysSettingsValue" || fmt.Sprint(saved["boundRecordIds"]) != "[7aaaaaaa-0000-0000-0000-000000000000]" {
			t.Errorf("saved = %v", saved)
		}
		for _, column := range saved["columns"].([]any) {
			item := column.(map[string]any)
			key := item["name"] == "SysSettings" || item["name"] == "SysAdminUnit"
			if item["isKey"] != key || (item["isForceUpdate"] == true) != (item["name"] == "BinaryValue" || item["name"] == "TextValue" ||
				item["name"] == "IsDef" || item["name"] == "IntegerValue" || item["name"] == "FloatValue" || item["name"] == "BooleanValue" ||
				item["name"] == "DateTimeValue" || item["name"] == "GuidValue") {
				t.Errorf("column %v", item)
			}
		}
	}
	// The favicon was never taken from logo: FaviconImage holds the icon bytes.
	if fake.settings["FaviconImage"].value != base64.StdEncoding.EncodeToString([]byte("ico")) {
		t.Errorf("favicon value = %q", fake.settings["FaviconImage"].value)
	}
}

func TestSetLogoPartialFailureKeepsWhatApplied(t *testing.T) {
	fake := newThemeWriteFake(t)
	fake.refuse["MenuLogoImage"] = true
	logo := themeWriteTestImage(t, "logo.png", []byte("png"))
	dark := themeWriteTestImage(t, "dark.tiff", []byte("tiff"))

	result := fake.client().SetLogo(t.Context(), SetLogoRequest{LoginLogo: logo, MenuLogo: logo, DarkLogo: dark})

	want := "Applying 2 image(s) failed: dark-logo (CrtAppToolbarLogo) from '" + dark + "': '.tiff' is not a supported image format " +
		"(dark-logo accepts: png, jpg, jpeg, gif, bmp, webp, svg); menu-logo (MenuLogoImage) from '" + logo + "': " + themeWriteRefusedUpdate + "."
	if result.Success || result.Error != want {
		t.Fatalf("error = %q\nwant  %q", result.Error, want)
	}
	if strings.Join(result.Applied, ",") != "login-logo" || strings.Join(result.Bound, ",") != "LogoImage,HideSplashScreenLogoImage" || result.Package != "UsrBrand" {
		t.Errorf("result = %#v", result)
	}
	if fake.count("PostSysSettingsValues CrtAppToolbarLogo") != 0 {
		t.Errorf("a refused format was written")
	}
}

func TestSetLogoRefusalsBeforeWriting(t *testing.T) {
	fake := newThemeWriteFake(t)
	text := themeWriteTestImage(t, "notes", []byte("x"))
	cases := []struct {
		request SetLogoRequest
		want    string
	}{
		{SetLogoRequest{}, SetLogoNoImageError},
		{SetLogoRequest{Logo: "/no/such.png", Favicon: "/no/icon.ico"}, "Files not found: '/no/such.png' (passed for login-logo); " +
			"'/no/such.png' (passed for menu-logo); '/no/such.png' (passed for configuration-logo); '/no/such.png' (passed for dark-logo); " +
			"'/no/icon.ico' (passed for favicon)."},
		{SetLogoRequest{Favicon: text}, "Applying 1 image(s) failed: favicon (FaviconImage) from '" + text + "': the file has no extension, " +
			"so it cannot be verified as an image (favicon accepts: png, jpg, jpeg, gif, bmp, webp, svg, ico)."},
	}
	for _, c := range cases {
		result := fake.client().SetLogo(t.Context(), c.request)
		if result.Success || result.Error != c.want || result.Applied != nil || result.Package != "" {
			t.Errorf("%#v\nwant %q", result, c.want)
		}
	}
	if len(fake.requests) != 0 {
		t.Errorf("requests = %v", fake.requests)
	}
}

func TestSetLogoReportsBindingProblems(t *testing.T) {
	fake := newThemeWriteFake(t)
	fake.bindings["SysSettingsValue_LogoImage"] = "Contact"
	delete(fake.settings, "HideSplashScreenLogoImage")
	logo := themeWriteTestImage(t, "logo.svg", []byte("<svg/>"))

	result := fake.client().SetLogo(t.Context(), SetLogoRequest{LoginLogo: logo, Package: "crtbase"})
	if result.Success || !strings.Contains(result.Error, "Package 'CrtBase' is locked") || result.Package != "" {
		t.Fatalf("locked package: %#v", result)
	}

	result = fake.client().SetLogo(t.Context(), SetLogoRequest{LoginLogo: logo})
	// The splash setting does not exist: its write is refused and reads as off, a warning (not required).
	if result.Success || result.Package != "UsrBrand" || !strings.Contains(result.Error,
		"The applied branding could not be bound into package 'UsrBrand': Package data binding 'SysSettingsValue_LogoImage' already exists for schema 'Contact'") {
		t.Fatalf("collision: %#v", result)
	}
	if len(result.Warnings) != 1 || !strings.HasPrefix(result.Warnings[0], "The logos were applied, but setting HideSplashScreenLogoImage failed") {
		t.Errorf("warnings = %v", result.Warnings)
	}
}

func TestSetBackgroundUploadsRegistersConfiguresAndBinds(t *testing.T) {
	fake := newThemeWriteFake(t)
	image := themeWriteTestImage(t, "background.png", []byte("png bytes"))

	result := fake.client().SetBackground(t.Context(), SetBackgroundRequest{File: image})

	if !result.Success || result.Error != "" || result.Package != "UsrBrand" {
		t.Fatalf("result = %#v", result)
	}
	if strings.Join(result.Bound, ",") != "image,gallery-membership,background-config,panel-icon-off-state" || len(result.Warnings) != 0 {
		t.Errorf("bound = %v warnings = %v", result.Bound, result.Warnings)
	}
	want := []string{
		"upload " + result.ImageID,
		"odata SysImageInTag Entity/Id eq " + result.ImageID + " and Tag/Id eq 273c2402-7cae-456b-a9c4-067d2024f1a7",
		`odata POST SysImageInTag {"EntityId":"` + result.ImageID + `","TagId":"273c2402-7cae-456b-a9c4-067d2024f1a7"}`,
		`PostSysSettingsValues CrtBackgroundConfig={"imageId":"` + result.ImageID + `","mode":"Image"}`,
		"BatchQuery UpdateQuery",
		"ClearFeaturesCacheForAllUsers UsePanelIconBackground",
		"SaveSchema SysImage_ShellBackground",
		"SaveSchema SysImageInTag_ShellBackground",
		"SaveSchema SysSettingsValue_CrtBackgroundConfig",
		"SaveSchema Feature_UsePanelIconBackground",
		"SaveSchema AdminUnitFeatureState_UsePanelIconBackground",
	}
	position := 0
	for _, entry := range fake.requests {
		if position < len(want) && entry == want[position] {
			position++
		}
	}
	if position != len(want) {
		t.Fatalf("request %q missing or out of order in %v", want[position], fake.requests)
	}
}

func TestSetBackgroundByIdAndItsRefusals(t *testing.T) {
	fake := newThemeWriteFake(t)
	fake.images["12345678-1234-1234-1234-1234567890ab"] = true
	fake.featureStates["eeeeeeee-0000-0000-0000-000000000001"] = 0

	result := fake.client().SetBackground(t.Context(), SetBackgroundRequest{ImageID: "{12345678-1234-1234-1234-1234567890AB}", KeepIconBackground: true})

	if !result.Success || result.ImageID != "12345678-1234-1234-1234-1234567890ab" {
		t.Fatalf("result = %#v", result)
	}
	if len(result.Warnings) != 1 || !strings.HasPrefix(result.Warnings[0], "The UsePanelIconBackground feature was left as is (keep-icon-background)") {
		t.Errorf("warnings = %v", result.Warnings)
	}
	if fake.count("BatchQuery") != 0 || fake.count("ClearFeaturesCacheForAllUsers") != 0 {
		t.Errorf("the feature was touched although kept")
	}

	cases := map[string]string{
		"not-a-guid":                           "image-id 'not-a-guid' is not a valid id. Pass the id printed by upload-image.",
		"00000000-0000-0000-0000-000000000000": "image-id '00000000-0000-0000-0000-000000000000' is not a valid id. Pass the id printed by upload-image.",
		"aaaaaaaa-1234-1234-1234-1234567890ab": "No uploaded image with id 'aaaaaaaa-1234-1234-1234-1234567890ab' was found in the environment. " +
			"Upload the file first with upload-image and pass the id it prints.",
	}
	for id, want := range cases {
		if result := fake.client().SetBackground(t.Context(), SetBackgroundRequest{ImageID: id}); result.Success || result.Error != want {
			t.Errorf("%s: %#v", id, result)
		}
	}
	if result := fake.client().SetBackground(t.Context(), SetBackgroundRequest{ImageID: "x", File: "y"}); result.Error != SetBackgroundBothSourcesError {
		t.Errorf("both: %#v", result)
	}
}

func TestSetBackgroundBindingRefusalsBecomeWarnings(t *testing.T) {
	fake := newThemeWriteFake(t)
	fake.images["12345678-1234-1234-1234-1234567890ab"] = true
	// The feature is not defined: the turn-off leaves it alone, and its off-state binding is refused (and a
	// previously shipped folder of it removed) while the rest binds.
	delete(fake.features, "UsePanelIconBackground")
	fake.bindings["Feature_UsePanelIconBackground"] = "Feature"
	result := fake.client().SetBackground(t.Context(), SetBackgroundRequest{ImageID: "12345678-1234-1234-1234-1234567890ab", Package: "UsrBrand"})
	if !result.Success || strings.Join(result.Bound, ",") != "image,gallery-membership,background-config" {
		t.Fatalf("result = %#v", result)
	}
	wantWarnings := "UsePanelIconBackground: the feature is not defined on this environment|" +
		"Feature_UsePanelIconBackground: previously shipped binding removed, it no longer has a source row"
	if strings.Join(result.Warnings, "|") != wantWarnings || fake.count("BatchQuery") != 0 {
		t.Errorf("warnings = %v", result.Warnings)
	}

	fake = newThemeWriteFake(t)
	fake.images["12345678-1234-1234-1234-1234567890ab"] = true
	fake.refuse["CrtBackgroundConfig"] = true
	result = fake.client().SetBackground(t.Context(), SetBackgroundRequest{ImageID: "12345678-1234-1234-1234-1234567890ab"})
	if result.Success || result.Error != "The image is in the background gallery, but writing the CrtBackgroundConfig setting failed." {
		t.Fatalf("refused config: %#v", result)
	}

	fake = newThemeWriteFake(t)
	fake.images["12345678-1234-1234-1234-1234567890ab"] = true
	fake.bindings["SysImageInTag_ShellBackground"] = "Contact"
	result = fake.client().SetBackground(t.Context(), SetBackgroundRequest{ImageID: "12345678-1234-1234-1234-1234567890ab"})
	want := "The background was applied (image 12345678-1234-1234-1234-1234567890ab), but binding it into package 'UsrBrand' failed: " +
		"Package data binding 'SysImageInTag_ShellBackground' already exists for schema 'Contact', but this delivery needs it for " +
		"'SysImageInTag'. Rename or remove the existing binding first. Already bound and left in place: image."
	if result.Success || result.Error != want || result.Package != "UsrBrand" || strings.Join(result.Bound, ",") != "image" {
		t.Fatalf("collision: %#v", result)
	}
}

func TestThemeWriteReadOffState(t *testing.T) {
	cases := map[string]string{`0`: "true", `1`: "false", `false`: "true", `true`: "false", `"0"`: "true", `"True"`: "false",
		`" 1 "`: "false", `"x"`: "nil", `null`: "nil", `1.5`: "nil"}
	for raw, want := range cases {
		got := "nil"
		if off := themeWriteReadOffState(json.RawMessage(raw)); off != nil {
			got = fmt.Sprint(*off)
		}
		if got != want {
			t.Errorf("%s = %s, want %s", raw, got, want)
		}
	}
}
