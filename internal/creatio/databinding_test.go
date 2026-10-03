package creatio

import (
	"context"
	"testing"
)

const dataBindingTestPackage = "88888888-8888-8888-8888-888888888888"

func dataBindingTestServer(t *testing.T, bindings, bound string) (*Client, *[]groupIRequest) {
	return groupIServer(t, func(request groupIRequest) (int, string) {
		switch {
		case request.Root == "SysPackage":
			// InstallType 1 is a locked package; reading its bindings must still work.
			return 200, `{"success":true,"rows":[{"Name":"CrtBase","UId":"` + dataBindingTestPackage + `","InstallType":1}]}`
		case request.Root == "SysPackageSchemaData":
			return 200, bindings
		case request.Path == "/0/ServiceModel/SchemaDataDesignerService.svc/GetBoundSchemaData":
			return 200, bound
		}
		t.Errorf("unexpected request %#v", request)
		return 404, ""
	})
}

func TestReadDataBindingListsColumnsAndRows(t *testing.T) {
	client, requests := dataBindingTestServer(t, `{"success":true,"rows":[{"UId":"99999999-9999-9999-9999-99999999999A","EntitySchemaName":"SysSettingsValue"}]}`,
		`{"items":"[{\"Name\":\"Ünit\",\"Id\":\"1\",\"Owner\":{\"value\":\"o-1\",\"displayValue\":\"Supervisor\"},\"Flag\":true,\"Empty\":null},{\"Id\":\"2\",\"Raw\":{\"x\":\"<\"}}]"}`)
	result := client.ReadDataBinding(context.Background(), "crtbase", "MyBinding")
	want := []string{
		"binding: MyBinding",
		"schema:  SysSettingsValue",
		"uId:     99999999-9999-9999-9999-99999999999a",
		"rows:    2",
		"columns (6): Empty, Flag, Id, Name, Owner, Raw",
		"row[0]: Empty=, Flag=true, Id=1, Name=Ünit, Owner=Supervisor (o-1)",
		"row[1]: Id=2, Raw={\"x\":\"\\u003C\"}",
	}
	if result.ExitCode != 0 || len(result.Messages) != len(want) {
		t.Fatalf("result = %#v", result)
	}
	for index, line := range want {
		if result.Messages[index].Value != line || result.Messages[index].MessageType != "Info" {
			t.Errorf("line %d = %q, want %q", index, result.Messages[index].Value, line)
		}
	}
	for _, request := range *requests {
		if request.Root == "SysPackageSchemaData" && (request.Filters["Name"] != "MyBinding" || request.Filters["SysPackage.UId"] != dataBindingTestPackage) {
			t.Errorf("binding query filters = %#v", request.Filters)
		}
		if request.Path == "/0/ServiceModel/SchemaDataDesignerService.svc/GetBoundSchemaData" && request.Body != `{"uId":"99999999-9999-9999-9999-99999999999a"}` {
			t.Errorf("GetBoundSchemaData body = %s", request.Body)
		}
	}
}

func TestReadDataBindingRefusesAnUnknownBinding(t *testing.T) {
	client, _ := dataBindingTestServer(t, `{"success":true,"rows":[]}`, `{}`)
	result := client.ReadDataBinding(context.Background(), "CrtBase", "Nope")
	if result.ExitCode != 1 || result.Messages[0].MessageType != "Error" || result.Messages[0].Value != "Binding 'Nope' was not found in the remote environment." {
		t.Fatalf("result = %#v", result)
	}
	result = client.ReadDataBinding(context.Background(), "Missing", "Nope")
	if result.Messages[0].Value != "Package 'Missing' was not found in the environment. Check the name against list-packages." {
		t.Fatalf("unknown package = %#v", result)
	}
}
