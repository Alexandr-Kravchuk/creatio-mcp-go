package main

import (
	"strings"
	"testing"
)

func TestFindAppArgumentsRecoverAliasesAndRefuseUnknownKeys(t *testing.T) {
	input, refusal := findAppArguments(map[string]any{"Name": " customer ", "app-code": "CrtApp"})
	if refusal != "" || input.SearchPattern != "customer" || input.Code != "CrtApp" {
		t.Fatalf("aliases were not recovered: %#v, %q", input, refusal)
	}
	input, _ = findAppArguments(map[string]any{"search-pattern": "bound", "query": "alias"})
	if input.SearchPattern != "bound" {
		t.Fatalf("a bound argument must win over its alias: %#v", input)
	}
	if _, refusal := findAppArguments(map[string]any{"bogus": 1}); refusal != "Unknown args: 'bogus'. Valid: code, search-pattern. Use search-pattern for a substring filter." {
		t.Fatalf("unknown key refusal = %q", refusal)
	}
	if _, refusal := findAppArguments(map[string]any{"environment-name": "dev"}); refusal != environmentNameRefusal {
		t.Fatalf("environment-name refusal = %q", refusal)
	}
	if _, refusal := findAppArguments(map[string]any{"code": 5}); !strings.Contains(refusal, "invalid-parameter-type") {
		t.Fatalf("type refusal = %q", refusal)
	}
}

func TestGroupAToolsAreRegistered(t *testing.T) {
	for _, name := range []string{"get-app-info", "find-app", "find-entity-schema", "get-entity-schema-column-properties"} {
		contract, ok := toolContract(name)
		if !ok || contract["inputSchema"] == nil {
			t.Fatalf("%s is not registered with an input schema", name)
		}
	}
}
