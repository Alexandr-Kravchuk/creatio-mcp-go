package creatio

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

const (
	relatedTestPackage = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	relatedTestEntity  = "cccccccc-cccc-cccc-cccc-cccccccccccc"
	relatedTestParent  = "dddddddd-dddd-dddd-dddd-dddddddddddd"
	relatedTestPage    = "eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee"
)

func TestGetRelatedPageAddonDecodesThePageSet(t *testing.T) {
	metaData, _ := json.Marshal(map[string]any{"TypeColumnUId": "f-1", "Pages": []any{
		map[string]any{"PageSchemaUId": relatedTestPage, "IsDefault": true, "Role": "A29A3BA5-4B0D-DE11-9A51-005056C00008", "TypeColumnValue": 7},
		map[string]any{"PageSchemaUId": strings.ToUpper(relatedTestPage), "IsDefault": "false", "Actions": map[string]any{"Add": "True"}, "IsSspDefault": 1},
		"skipped",
	}})
	pageLookups := 0
	client, requests := groupIServer(t, func(request groupIRequest) (int, string) {
		switch {
		case request.Root == "SysPackage":
			return 200, `{"success":true,"rows":[{"UId":"` + strings.ToUpper(relatedTestPackage) + `"}]}`
		case request.Root == "SysSchema":
			pageLookups++
			return 200, `{"success":true,"rows":[{"Name":"UsrItem_FormPage"}]}`
		case request.Path == "/0/ServiceModel/EntitySchemaDesignerService.svc/GetSchemaDesignItem":
			return 200, `{"success":true,"schema":{"uId":"` + relatedTestEntity + `","parentSchema":{"uId":"` + relatedTestParent + `"}}}`
		case request.Path == "/0/ServiceModel/AddonSchemaDesignerService.svc/GetSchema":
			encoded, _ := json.Marshal(string(metaData))
			return 200, `{"success":true,"schema":{"TargetSchemaUId":"` + strings.ToUpper(relatedTestEntity) + `","metaData":` + string(encoded) + `}}`
		}
		t.Errorf("unexpected request %#v", request)
		return 404, ""
	})
	result := client.GetRelatedPageAddon(context.Background(), "UsrItem", "UsrPkg", "Mobile")
	encoded, _ := json.Marshal(result)
	want := `{"success":true,"entitySchemaName":"UsrItem","entitySchemaUId":"` + relatedTestEntity + `","packageName":"UsrPkg","packageUId":"` +
		strings.ToUpper(relatedTestPackage) + `","addonName":"MobileRelatedPage","typeColumnUId":"f-1","pageCount":2,"pages":[` +
		`{"pageSchemaUId":"` + relatedTestPage + `","pageSchemaName":"UsrItem_FormPage","isDefault":true,"isAdd":false,"isSspDefault":false,` +
		`"role":"A29A3BA5-4B0D-DE11-9A51-005056C00008","roleName":"All employees","typeColumnValue":"7"},` +
		`{"pageSchemaUId":"` + strings.ToUpper(relatedTestPage) + `","pageSchemaName":"UsrItem_FormPage","isDefault":false,"isAdd":true,"isSspDefault":false}]}`
	if string(encoded) != want {
		t.Fatalf("result = %s\nwant     %s", encoded, want)
	}
	if pageLookups != 1 {
		t.Errorf("page names were resolved %d times, want once per distinct UId", pageLookups)
	}
	for _, request := range *requests {
		if request.Path == "/0/ServiceModel/AddonSchemaDesignerService.svc/GetSchema" {
			var body map[string]any
			_ = json.Unmarshal([]byte(request.Body), &body)
			if body["addonName"] != "MobileRelatedPage" || body["targetSchemaUId"] != relatedTestEntity || body["targetParentSchemaUId"] != relatedTestParent ||
				body["targetPackageUId"] != relatedTestPackage || body["targetSchemaManagerName"] != "EntitySchemaManager" || body["useFullHierarchy"] != true {
				t.Errorf("GetSchema body = %s", request.Body)
			}
		}
	}
}

func TestGetRelatedPageAddonRefusesUnknownPackageAndObject(t *testing.T) {
	knownPackage := false
	client, _ := groupIServer(t, func(request groupIRequest) (int, string) {
		switch {
		case request.Root == "SysPackage" && knownPackage:
			return 200, `{"success":true,"rows":[{"UId":"` + relatedTestPackage + `"}]}`
		case request.Root == "SysPackage":
			return 200, `{"success":true,"rows":[]}`
		}
		return 200, `{"success":true,"schema":null}`
	})
	result := client.GetRelatedPageAddon(context.Background(), "UsrItem", "Nope", "")
	if result.Success || *result.Error != "Package 'Nope' not found in the target environment." {
		t.Fatalf("unknown package = %#v", result)
	}
	knownPackage = true
	result = client.GetRelatedPageAddon(context.Background(), "UsrNope", "UsrPkg", "web")
	if result.Error == nil || !strings.HasPrefix(*result.Error, "Object (entity schema) 'UsrNope' not found in package 'UsrPkg'.") {
		t.Fatalf("unknown object = %#v", result)
	}
	if bad := client.GetRelatedPageAddon(context.Background(), "UsrItem", "UsrPkg", "tablet"); *bad.Error != "schema-type 'tablet' is not valid; use 'web' or 'mobile'." {
		t.Fatalf("bad schema type = %#v", bad)
	}
}
