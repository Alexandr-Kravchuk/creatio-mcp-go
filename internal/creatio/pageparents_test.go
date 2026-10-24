package creatio

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
)

func pageParentsBody(t *testing.T, diff any) string {
	t.Helper()
	text, err := json.Marshal(diff)
	if err != nil {
		t.Fatal(err)
	}
	return `define("X", /**SCHEMA_DEPS*/[]/**SCHEMA_DEPS*/, function/**SCHEMA_ARGS*/()/**SCHEMA_ARGS*/ { return {
		viewConfigDiff: /**SCHEMA_VIEW_CONFIG_DIFF*/` + string(text) + `/**SCHEMA_VIEW_CONFIG_DIFF*/,
		viewModelConfigDiff: /**SCHEMA_VIEW_MODEL_CONFIG_DIFF*/[]/**SCHEMA_VIEW_MODEL_CONFIG_DIFF*/,
		modelConfigDiff: /**SCHEMA_MODEL_CONFIG_DIFF*/[]/**SCHEMA_MODEL_CONFIG_DIFF*/,
		handlers: /**SCHEMA_HANDLERS*/[]/**SCHEMA_HANDLERS*/,
		converters: /**SCHEMA_CONVERTERS*/{}/**SCHEMA_CONVERTERS*/,
		validators: /**SCHEMA_VALIDATORS*/{}/**SCHEMA_VALIDATORS*/ }; });`
}

func pageParentInsert(name, parent string, values map[string]any) map[string]any {
	if values == nil {
		values = map[string]any{}
	}
	return map[string]any{"operation": "insert", "name": name, "parentName": parent, "propertyName": "items", "values": values}
}

// The texts and the warn/fail switch were compared with clio 8.1.0.142's validate-page on the bodies of
// scripts/parity-cases/validate-page-parents.json.
func TestValidatePageParentsWarnsWithoutKnownContainersAndFailsWithThem(t *testing.T) {
	body := pageParentsBody(t, []any{pageParentInsert("UsrButton", "UsrMissing", nil)})
	warned := mustValidatePage(t, PageValidateRequest{Body: body})
	want := "Element 'UsrButton' has unresolved parentName 'UsrMissing'."
	if !warned.Valid || len(warned.Validation.Errors) != 0 ||
		!slices.Equal(warned.Validation.Warnings, []string{want + pageParentUnverifiedHint, PageValidateGapWarning}) {
		t.Fatalf("warned = %#v", warned)
	}
	failed := mustValidatePage(t, PageValidateRequest{Body: body, KnownContainers: []string{}})
	if failed.Valid || failed.Validation.ContentOK || !slices.Equal(failed.Validation.Errors, []string{want}) ||
		!slices.Equal(failed.Validation.Warnings, []string{PageValidateGapWarning}) {
		t.Fatalf("failed = %#v", failed)
	}
	known := mustValidatePage(t, PageValidateRequest{Body: body, KnownContainers: []string{"UsrMissing"}})
	if !known.Valid || len(known.Validation.Errors) != 0 {
		t.Fatalf("known = %#v", known)
	}
}

func TestValidatePageParentsKnowsTheDiffsOwnElements(t *testing.T) {
	diff := []any{
		map[string]any{"operation": "remove", "name": "Gone"},
		map[string]any{"operation": "remove", "name": "Kept", "properties": []string{"caption"}},
		pageParentInsert("UsrTabs", "Kept", map[string]any{"name": "UsrIgnored", "items": []any{
			map[string]any{"name": "UsrTab", "nested": map[string]any{"name": "UsrDeep"}}}}),
		pageParentInsert("UsrA", "UsrDeep", nil),
		pageParentInsert("UsrB", "UsrTab", nil),
		pageParentInsert("UsrC", "UsrIgnored", nil),
		pageParentInsert("UsrD", "Gone", nil),
		map[string]any{"operation": "move", "name": "Gone", "parentName": "Nowhere"},
		map[string]any{"operation": "move", "name": "Stranger", "parentName": "Nowhere"},
		map[string]any{"operation": "move", "name": "Header", "parentName": "Nowhere"},
		map[string]any{"operation": "merge", "name": "UsrA", "parentName": "Nowhere"},
		map[string]any{"operation": "insert", "name": "UsrE", "parentName": ""},
	}
	// clio 8.1.0.142 answers these three errors for this diff (with propertyName on every operation, which its
	// placement-slot check requires).
	result, err := ValidatePageParents(pageParentsBody(t, diff), []string{"Gone", "Kept", "Header"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"Element 'UsrC' has unresolved parentName 'UsrIgnored'. Closest known element: 'UsrDeep'.",
		"Element 'UsrD' has unresolved parentName 'Gone'. Closest known element: 'Kept'.",
		"Element 'Header' has unresolved parentName 'Nowhere'. Closest known element: 'Kept'.",
	}
	if !slices.Equal(result.Errors, want) || len(result.Warnings) != 0 {
		t.Fatalf("errors = %q", result.Errors)
	}
}

func TestValidatePageParentsClosestNameAndCasts(t *testing.T) {
	result, _ := ValidatePageParents(pageParentsBody(t, []any{pageParentInsert("UsrButton", "AB", nil)}), []string{"AD", "AC"})
	if !slices.Equal(result.Errors, []string{"Element 'UsrButton' has unresolved parentName 'AB'. Closest known element: 'AC'."}) {
		t.Fatalf("tie = %q", result.Errors)
	}
	result, _ = ValidatePageParents(pageParentsBody(t, []any{map[string]any{"operation": "insert", "parentName": "Missing"}}), []string{"Header"})
	if !slices.Equal(result.Errors, []string{"Element '' has unresolved parentName 'Missing'. Closest known element: 'Header'."}) {
		t.Fatalf("no name = %q", result.Errors)
	}
	result, _ = ValidatePageParents(pageParentsBody(t, []any{map[string]any{"operation": "insert", "name": "UsrButton", "parentName": 7}}), []string{"7"})
	if len(result.Errors) != 0 {
		t.Fatalf("a number parent is read as its text: %q", result.Errors)
	}
	for diff, want := range map[string]string{
		`[{"operation": {"a": 1}, "name": "UsrButton", "parentName": "Missing"}]`: "Cannot cast Newtonsoft.Json.Linq.JObject to Newtonsoft.Json.Linq.JToken.",
		`[{"operation": "insert", "name": "UsrButton", "parentName": ["x"]}]`:     "Cannot cast Newtonsoft.Json.Linq.JArray to Newtonsoft.Json.Linq.JToken.",
	} {
		body := strings.Replace(pageParentsBody(t, []any{}), "/**SCHEMA_VIEW_CONFIG_DIFF*/[]", "/**SCHEMA_VIEW_CONFIG_DIFF*/"+diff, 1)
		if _, err := ValidatePage(PageValidateRequest{Body: body, KnownContainers: []string{}}); err == nil || err.Error() != want {
			t.Errorf("%s: err = %v", diff, err)
		}
	}
}

// Past 512 known names clio's hint looks only at the first 512 in HashSet order, where a removed name's slot
// is reused by the next name added.
func TestValidatePageParentsHintFollowsHashSetOrder(t *testing.T) {
	set := newPageParentSet()
	for _, name := range []string{"a", "b", "c", "d"} {
		set.add(name)
	}
	set.remove("b")
	set.remove("d")
	set.add("e")
	set.add("f")
	set.add("a")
	if got := set.values(); !slices.Equal(got, []string{"a", "f", "c", "e"}) {
		t.Fatalf("order = %q", got)
	}
	known := make([]string, 0, 513)
	for index := range 512 {
		known = append(known, fmt.Sprintf("Far%03d", index))
	}
	known = append(known, "Missin")
	message := pageParentDiagnostic(nil, "Missing", known)
	if strings.Contains(message, "'Missin'") || !strings.HasSuffix(message, "Closest known element: 'Far000'.") {
		t.Fatalf("message = %s", message)
	}
	if got := pageParentDistance(pageParentUnits("kitten"), pageParentUnits("sitting")); got != 3 {
		t.Fatalf("distance = %d", got)
	}
}
