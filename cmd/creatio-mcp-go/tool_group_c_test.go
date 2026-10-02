package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// invokeRegistered calls a self-registered tool with no Creatio client; only argument refusals may run.
func invokeRegistered(t *testing.T, name string, args map[string]any) (map[string]any, error) {
	t.Helper()
	tool, ok := registeredTools[name]
	if !ok {
		t.Fatalf("tool %q is not registered", name)
	}
	result, err := tool.invoke(context.Background(), nil, args)
	if err != nil {
		return nil, err
	}
	var decoded map[string]any
	encoded, _ := json.Marshal(result.StructuredContent)
	_ = json.Unmarshal(encoded, &decoded)
	return decoded, nil
}

func TestGetTargetPackageAnswersLegacyAliasLikeClio(t *testing.T) {
	result, err := invokeRegistered(t, "get-target-package", map[string]any{"package-name": "Custom", "zz": 1})
	if err != nil {
		t.Fatal(err)
	}
	if result["success"] != false || result["resolutionFailed"] != false ||
		result["error"] != "Rename: 'package-name' -> 'package'. Unknown args: 'zz'. Valid: package." {
		t.Fatalf("result = %#v", result)
	}
}

func TestGroupCToolsRefuseEnvironmentSelectors(t *testing.T) {
	for _, name := range []string{"get-schema-name-prefix", "get-target-package", "list-entity-client-schemas", "get-page"} {
		result, err := invokeRegistered(t, name, map[string]any{"environment-name": "other"})
		if err != nil || result["success"] != false || result["error"] != environmentNameRefusal {
			t.Fatalf("%s result = %#v, err = %v", name, result, err)
		}
	}
	result, err := invokeRegistered(t, "get-page", map[string]any{"schema-name": "Page", "uri": "http://other"})
	if err != nil || result["error"] != directConnectionRefusal {
		t.Fatalf("get-page uri result = %#v, err = %v", result, err)
	}
}

func TestGetPageRejectsNonBooleanIncludeOperations(t *testing.T) {
	_, err := invokeRegistered(t, "get-page", map[string]any{"schema-name": "Page", "include-operations": "no"})
	if err == nil || !strings.HasPrefix(err.Error(), "invalid-parameter-type") {
		t.Fatalf("err = %v", err)
	}
}
