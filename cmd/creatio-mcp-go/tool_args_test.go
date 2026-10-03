package main

import "testing"

func TestUnknownArgumentErrorMirrorsClioWording(t *testing.T) {
	cases := []struct {
		args map[string]any
		want string
	}{
		{map[string]any{}, ""},
		{map[string]any{"environment-name": "x"}, ""},
		{map[string]any{"foo": 1}, "Unknown args: 'foo'. Valid: environment-name."},
		{map[string]any{"environmentName": "x", "b": 1, "a": 1},
			"Rename: 'environmentName' -> 'environment-name'. Unknown args: 'a', 'b'. Valid: environment-name."},
		{map[string]any{"Environment": "x"}, "Rename: 'Environment' -> 'environment-name'."},
	}
	for _, c := range cases {
		if got := unknownArgumentError(c.args, "environment-name"); got != c.want {
			t.Errorf("unknownArgumentError(%v) = %q, want %q", c.args, got, c.want)
		}
	}
}

func TestUnknownArgumentErrorListsTheAcceptedArgumentsInClioOrder(t *testing.T) {
	got := unknownArgumentError(map[string]any{"bogus": 1}, "environment-name", "process-name", "culture")
	if want := "Unknown args: 'bogus'. Valid: environment-name, process-name, culture."; got != want {
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
}

func TestEnvironmentSelectorOfTheWrongTypeIsABindingError(t *testing.T) {
	_, refusal, err := staticEnvironments(nil).resolve("get-page", map[string]any{"environment-name": 5.0}, scopeDirect)
	want := "invalid-parameter-type: argument 'environment-name' for MCP tool 'get-page' must be a string. Received an incompatible JSON value."
	if refusal != nil || err == nil || err.Error() != want {
		t.Fatalf("refusal = %v, err = %v", refusal, err)
	}
	_, _, err = staticEnvironments(nil).resolve("get-page", map[string]any{"uri": 5.0}, scopeDirect)
	if err == nil {
		t.Fatal("a non-string uri was accepted")
	}
}
