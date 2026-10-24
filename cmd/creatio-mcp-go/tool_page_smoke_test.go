package main

import "testing"

func TestPageSmokeGUIDFormats(t *testing.T) {
	for input, want := range map[string]string{
		"6B9D1A3E-0000-4000-8000-00000000000A":    "6b9d1a3e-0000-4000-8000-00000000000a",
		" {6B9D1A3E-0000-4000-8000-00000000000A}": "6b9d1a3e-0000-4000-8000-00000000000a",
		"6B9D1A3E000040008000000000000000":        "6b9d1a3e-0000-4000-8000-000000000000",
	} {
		if got, ok := parseDotnetGUID(input); !ok || got != want {
			t.Errorf("%q: got %q, %v", input, got, ok)
		}
	}
	if _, ok := parseDotnetGUID("abc"); ok {
		t.Error("abc parsed")
	}
}

func TestPageSmokeBindMatchesMembersIgnoringCase(t *testing.T) {
	bound, extra := pageSmokeBind(map[string]any{"Page": "X", "Environment-Name": "e", "zzz": 1})
	if bound["page"] != "X" || bound["environment-name"] != "e" || len(extra) != 1 {
		t.Fatalf("bound %v extra %v", bound, extra)
	}
}
