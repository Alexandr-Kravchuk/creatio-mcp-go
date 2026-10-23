package mobileguide

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestFailAnswerShapeAndOrder(t *testing.T) {
	name := "UsrPage"
	encoded, err := json.Marshal(Fail(&name, nil, "boom"))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(encoded); got != `{"success":false,"sourceSchemaName":"UsrPage","error":"boom"}` {
		t.Fatalf("got %s", got)
	}
}

func TestDetectSourceTypeAndRejection(t *testing.T) {
	for input, want := range map[string]string{"web": "freedom-web", "WEB": "freedom-web", "Mobile": "mobile", "": "unknown", "  ": "unknown", " Classic ": "classic"} {
		if got := DetectSourceType(input); got != want {
			t.Errorf("DetectSourceType(%q) = %q, want %q", input, got, want)
		}
	}
	name := "UsrPage"
	if RejectUnsupportedSourceType(&name, "freedom-web") != nil {
		t.Fatal("freedom-web is supported")
	}
	if got := *RejectUnsupportedSourceType(&name, "mobile").Error; got != "Source page 'UsrPage' is already a mobile page. Nothing to convert." {
		t.Fatal(got)
	}
	if got := *RejectUnsupportedSourceType(&name, "unknown").Error; !strings.HasPrefix(got, "Source page 'UsrPage' has source type 'unknown', which is not yet supported") {
		t.Fatal(got)
	}
}

// A null member is left out (WhenWritingNull), an empty list is written as [] and keys keep insertion order.
func TestGuideOmitsNullKeepsEmptyLists(t *testing.T) {
	var names OrderedMap[*string]
	b, a := "b", "a"
	names.Set("z", &b)
	names.Set("y", &a)
	encoded, err := json.Marshal(&Guide{SourceStructure: []*SourceComponentInfo{}, NameMap: &names})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(encoded); got != `{"sourceStructure":[],"nameMap":{"z":"b","y":"a"},"propertyPruneApplied":false}` {
		t.Fatalf("got %s", got)
	}
}

func TestWorseResolvedFromAndRequestWarning(t *testing.T) {
	if WorseResolvedFrom("environment", "latest-fallback") != "latest-fallback" || WorseResolvedFrom("environment-superset", "environment") != "environment-superset" {
		t.Fatal("rank")
	}
	if RequestRegistryWarning("8.3.3", "") != nil || RequestRegistryWarning("latest", "LATEST") != nil {
		t.Fatal("no warning when the served version matches or is unknown")
	}
	if got := *RequestRegistryWarning("8.3.3", "latest"); !strings.HasPrefix(got, "Request support was read from the 'latest' mobile request registry, not from '8.3.3'.") {
		t.Fatal(got)
	}
}
