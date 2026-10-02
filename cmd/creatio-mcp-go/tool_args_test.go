package main

import "testing"

func TestUnknownArgumentErrorMirrorsClioWording(t *testing.T) {
	cases := []struct {
		args map[string]any
		want string
	}{
		{map[string]any{}, ""},
		{map[string]any{"foo": 1}, "Unknown args: 'foo'. This tool takes no arguments; the environment comes from the CREATIO_* variables."},
		{map[string]any{"environmentName": "x", "b": 1, "a": 1}, environmentNameRefusal},
		{map[string]any{"environment-name": "x"}, environmentNameRefusal},
	}
	for _, c := range cases {
		if got := unknownArgumentError(c.args, nil); got != c.want {
			t.Errorf("unknownArgumentError(%v) = %q, want %q", c.args, got, c.want)
		}
	}
}

func TestUnknownArgumentErrorListsTheAcceptedArguments(t *testing.T) {
	got := unknownArgumentError(map[string]any{"bogus": 1}, map[string]bool{"process-name": true, "culture": true})
	if want := "Unknown args: 'bogus'. Valid: culture, process-name."; got != want {
		t.Fatalf("unknownArgumentError = %q, want %q", got, want)
	}
}

func TestOptionalStringArgRefusesNonStrings(t *testing.T) {
	if value, err := optionalStringArg(map[string]any{"entity-name": nil}, "list-printables", "entity-name"); err != nil || value != "" {
		t.Fatalf("null = %q, %v", value, err)
	}
	_, err := optionalStringArg(map[string]any{"entity-name": 5.0}, "list-printables", "entity-name")
	want := "invalid-parameter-type: argument 'entity-name' for MCP tool 'list-printables' must be a string. Received an incompatible JSON value."
	if err == nil || err.Error() != want {
		t.Fatalf("number = %v", err)
	}
	if refusesConnectionArgs(map[string]any{"uri": "x"}) != directConnectionRefusal {
		t.Fatal("uri was not refused")
	}
}
