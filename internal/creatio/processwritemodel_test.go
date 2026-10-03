package creatio

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProcessModelParseLocalizationAndWriteExplicitFile(t *testing.T) {
	textType := ""
	for id, clr := range processClrTypes {
		if clr == "System.String" {
			textType = id
			break
		}
	}
	metadata := map[string]any{"metaData": map[string]any{"schema": map[string]any{"parameters": []any{
		map[string]any{"name": "Input", "dataValueType": textType, "direction": 0},
		map[string]any{"name": "Internal", "direction": 2},
		map[string]any{"name": "Rows", "dataValueType": processWriteCompositeListType, "direction": 1, "itemProperties": []any{map[string]any{"name": "Title"}}},
	}}}}
	encoded, _ := json.Marshal(metadata)
	payload, _ := json.Marshal(map[string]any{"schema": map[string]any{"metaData": string(encoded), "description": map[string]string{"en-US": "Model description"}, "resources": map[string]any{"Parameters.Input.Caption": map[string]string{"en-US": "Localized input"}, "Parameters.Rows.Title.Caption": map[string]string{"en-US": "Item title"}}}})
	description, parameters, err := processWriteParseSchema(payload, "en-US")
	if err != nil {
		t.Fatal(err)
	}
	source := processWriteModelFile("UsrParity", description, parameters, "My.Models", "en-US")
	for _, want := range []string{"namespace My.Models", "BusinessProcess(\"UsrParity\")", "Localized input", "Item title", "public List<Rows> Rows", "public System.String Input"} {
		if !strings.Contains(source, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(source, "Internal{") {
		t.Fatal("internal parameter emitted")
	}
	file := filepath.Join(t.TempDir(), "nested", "Explicit.cs")
	if err := processWriteModelSave("UsrParity", source, file); err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile(file)
	if err != nil || string(stored) != source {
		t.Fatalf("stored=%q err=%v", stored, err)
	}
}
