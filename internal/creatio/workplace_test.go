package creatio

import (
	"encoding/base64"
	"testing"
)

func TestWorkplaceBindingNames(t *testing.T) {
	cases := map[[2]string]string{
		{"Orders Hub", "UsrOrders"}: "OrdersHub",
		{"Продажі", "UsrOrders"}:    "UsrOrders",
		{"  ", "Usr_Orders-1"}:      "UsrOrders1",
		{"Ünïcode 9", "x"}:          "ncode9",
	}
	for input, want := range cases {
		if got := wpBindingSuffix(input[0], input[1]); got != want {
			t.Errorf("wpBindingSuffix(%q, %q) = %q, want %q", input[0], input[1], got, want)
		}
	}
	if got := wpWorkplaceBindingName("MyApps"); got != "SysWorkplace_MyAppsWorkplace" {
		t.Errorf("My applications binding name not avoided: %s", got)
	}
	if got := wpWorkplaceBindingName("Sales"); got != "SysWorkplace_Sales" {
		t.Errorf("binding name = %s", got)
	}
}

func TestWorkplaceBindingDataShipsRow(t *testing.T) {
	row := "55555555-0000-0000-0000-000000000003"
	data := `{"PackageData":[{"Row":[{"SchemaColumnUId":"AE0E45CA-C495-4FE7-A39D-3AB7278E1617","Value":"55555555-0000-0000-0000-000000000003"}]}]}`
	cases := []struct {
		name string
		data string
		want *bool
	}{
		{"json", data, boolPointer(true)},
		{"base64", base64.StdEncoding.EncodeToString([]byte("\uFEFF" + data)), boolPointer(true)},
		{"other row", `{"PackageData":[{"Row":[{"SchemaColumnUId":"ae0e45ca-c495-4fe7-a39d-3ab7278e1617","Value":"x"}]}]}`, boolPointer(false)},
		{"no package data", `{"Other":1}`, nil},
		{"not json", "@@@", nil},
		{"empty", " ", nil},
	}
	for _, c := range cases {
		got := wpBindingDataShipsRow(c.data, row)
		if (got == nil) != (c.want == nil) || (got != nil && *got != *c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func boolPointer(value bool) *bool { return &value }

func TestWorkplaceValidateMatchesClioTexts(t *testing.T) {
	blank := " "
	role := "Sales"
	cases := []struct {
		request WorkplacePlacementRequest
		want    string
	}{
		{WorkplacePlacementRequest{SectionCode: "X", WorkplaceName: "W", ClientType: "web", Mode: "maybe"}, "mode must be 'existing', 'new' or 'none'."},
		{WorkplacePlacementRequest{SectionCode: "X", Mode: " NONE ", Roles: []*string{&role}}, "mode=none writes nothing, so roles cannot be granted; omit roles."},
		{WorkplacePlacementRequest{SectionCode: "X", WorkplaceName: "W", ClientType: "desktop"}, "client-type must be 'web' or 'mobile'."},
		{WorkplacePlacementRequest{SectionCode: "X", WorkplaceName: "W", ClientType: "web", Roles: []*string{&role, &blank}}, "roles must not contain blank entries."},
		{WorkplacePlacementRequest{SectionCode: "X", WorkplaceName: "W", ClientType: "web", Roles: []*string{nil}}, "roles must not contain blank entries."},
		{WorkplacePlacementRequest{SectionCode: "X", WorkplaceName: " my applications ", ClientType: "Web", RemoveFromMyApplications: true},
			"The target is 'My applications' itself; pass remove-from-my-applications=false."},
	}
	for _, c := range cases {
		err := wpValidate(c.request)
		if err == nil || err.Error() != c.want {
			t.Errorf("%+v: got %v, want %q", c.request, err, c.want)
		}
	}
	if err := wpValidate(WorkplacePlacementRequest{SectionCode: "X", WorkplaceName: "My applications", ClientType: "MOBILE"}); err != nil {
		t.Errorf("valid request refused: %v", err)
	}
}
