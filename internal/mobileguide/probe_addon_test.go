package mobileguide

import "testing"

func TestRelatedPageMetadataClassification(t *testing.T) {
	cases := map[string]int{
		"":         TargetUnknown,
		"not json": TargetUnknown,
		`[]`:       TargetUnknown,
		`{}`:       TargetMissing,
		`{"Pages":[{"IsDefault":false,"PageSchemaUId":"a"}]}`:                      TargetMissing,
		`{"Pages":[{"IsDefault":true,"TypeColumnValue":"x","PageSchemaUId":"a"}]}`: TargetMissing,
		`{"Pages":[{"IsDefault":"True","PageSchemaUId":"b"}]}`:                     TargetResolved,
	}
	for metaData, want := range cases {
		if got := classifyRelatedPageMetadata(metaData); got != want {
			t.Errorf("%q: got %d, want %d", metaData, got, want)
		}
	}
	if uid := extractDefaultPageSchemaUID(`{"Pages":[{"IsDefault":true,"TypeColumnValue":" ","PageSchemaUId":"c"}]}`); deref(uid) != "c" {
		t.Errorf("uid = %v", uid)
	}
}

func TestCollectSourceEntityNames(t *testing.T) {
	model := mustSTJ([]byte(`{"primaryDataSourceName":"Main","dataSources":{"PDS":{"config":{"entitySchemaName":"Other"}},"Main":{"config":{"entitySchemaName":" Lead "}}}}`))
	if names := collectSourceEntityNames(model); names.len() != 1 || !names.contains("lead") {
		t.Errorf("names = %v", names.keys())
	}
	fallback := mustSTJ([]byte(`{"dataSources":{"PDS":{"config":{"entitySchemaName":"Account"}}}}`))
	if names := collectSourceEntityNames(fallback); !names.contains("Account") {
		t.Errorf("PDS fallback = %v", names.keys())
	}
}
