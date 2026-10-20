package creatio

import (
	"encoding/json"
	"testing"
)

// clio 8.1.0.142's SchemaDesignerHelper.ExtractCaption picks the caption in the user's profile culture.
func TestSchemaCaptionPrefersTheProfileCulture(t *testing.T) {
	both := json.RawMessage(`[{"cultureName":"ru-RU","value":"Раздел контакты"},{"cultureName":"en-US","value":"Contacts section"},{"cultureName":"uk-UA","value":" "}]`)
	asked := 0
	culture := func(name string) func() string {
		return func() string { asked++; return name }
	}
	for _, tc := range []struct {
		caption json.RawMessage
		culture string
		want    string
		asks    int
	}{
		{both, "en-us", "Contacts section", 1},
		{both, "ru-RU", "Раздел контакты", 1},
		{both, "uk-UA", "Contacts section", 1}, // blank value is no caption: en-US next
		{json.RawMessage(`[{"cultureName":"ru-RU","value":"А"},{"cultureName":"de-DE","value":"B"}]`), "fr-FR", "А", 1},
		{json.RawMessage(`[{"cultureName":"ru-RU","value":"Only"}]`), "en-US", "Only", 0}, // one value: no request
		{json.RawMessage(`[]`), "en-US", "", 0},
		{json.RawMessage(`"Plain"`), "en-US", "Plain", 0},
	} {
		asked = 0
		if got := schemaCaption(tc.caption, culture(tc.culture)); got != tc.want || asked != tc.asks {
			t.Errorf("%s in %s = %q (asked %d), want %q (asked %d)", tc.caption, tc.culture, got, asked, tc.want, tc.asks)
		}
	}
}
