package mobileguide

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"testing"
)

// The oracle: every WebToMobileAnalysisService.Analyze call clio 8.1.0.142's own MobilePageConverter test suite
// makes (800 tests), recorded with its inputs and clio's answer as clio's MCP server writes it. The recorder is
// a thin wrapper around clio's Analyze (scripts/mobileguide-oracle/, see docs/status/t27-mobile-guide-section.md);
// the Go engine must give the same answer byte for byte.

type oracleRecord struct {
	Name   string                     `json:"name"`
	Inputs map[string]json.RawMessage `json:"inputs"`
	Output *string                    `json:"output"`
	Error  *string                    `json:"error"`
	file   string
}

func loadOracleRecords(t *testing.T) []oracleRecord {
	t.Helper()
	file, err := os.Open("testdata/clio-analyze-records.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	reader := tar.NewReader(gz)
	var records []oracleRecord
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasSuffix(header.Name, ".json") {
			continue
		}
		data, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		var record oracleRecord
		if err := json.Unmarshal(data, &record); err != nil {
			t.Fatalf("%s: %v", header.Name, err)
		}
		record.file = strings.TrimPrefix(header.Name, "./")
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].file < records[j].file })
	return records
}

func isJSONNull(raw json.RawMessage) bool {
	return len(raw) == 0 || strings.TrimSpace(string(raw)) == "null"
}

func decodeSTJ(t *testing.T, raw json.RawMessage) *jn {
	if isJSONNull(raw) {
		return nil
	}
	node, err := parseSTJ(raw)
	if err != nil {
		t.Fatal(err)
	}
	return node
}

func decodeNewtonsoft(t *testing.T, raw json.RawMessage) *jn {
	if isJSONNull(raw) {
		return nil
	}
	node, err := parseNewtonsoft(raw)
	if err != nil {
		t.Fatal(err)
	}
	return node
}

type recordedCollection struct {
	CI      json.RawMessage     `json:"ci"`
	Items   []*string           `json:"items"`
	Entries [][]json.RawMessage `json:"entries"`
}

func decodeCollection(t *testing.T, raw json.RawMessage) *recordedCollection {
	if isJSONNull(raw) {
		return nil
	}
	var collection recordedCollection
	if err := json.Unmarshal(raw, &collection); err != nil {
		t.Fatal(err)
	}
	return &collection
}

func (c *recordedCollection) ci() bool { return string(c.CI) == "true" }

func decodeSet(t *testing.T, raw json.RawMessage) *strSet {
	collection := decodeCollection(t, raw)
	if collection == nil {
		return nil
	}
	set := newSet(collection.ci())
	for _, item := range collection.Items {
		set.add(deref(item))
	}
	return set
}

func decodeDict[V any](t *testing.T, raw json.RawMessage, value func(json.RawMessage) V) *dict[V] {
	collection := decodeCollection(t, raw)
	if collection == nil {
		return nil
	}
	result := newDict[V](collection.ci())
	for _, pair := range collection.Entries {
		var key string
		if err := json.Unmarshal(pair[0], &key); err != nil {
			t.Fatal(err)
		}
		result.set(key, value(pair[1]))
	}
	return result
}

func decodeString(raw json.RawMessage) *string {
	var value *string
	_ = json.Unmarshal(raw, &value)
	return value
}

func decodeEntry(t *testing.T, raw json.RawMessage) *RegistryEntry {
	if isJSONNull(raw) {
		return nil
	}
	var shape struct {
		ComponentType    *string         `json:"componentType"`
		Container        *bool           `json:"container"`
		Description      *string         `json:"description"`
		Example          json.RawMessage `json:"example"`
		DesignerDefaults json.RawMessage `json:"designerDefaults"`
		Properties       json.RawMessage `json:"properties"`
		Inputs           json.RawMessage `json:"inputs"`
		Outputs          json.RawMessage `json:"outputs"`
	}
	if err := json.Unmarshal(raw, &shape); err != nil {
		t.Fatal(err)
	}
	entry := &RegistryEntry{ComponentType: deref(shape.ComponentType), Container: shape.Container, Description: shape.Description}
	if !isJSONNull(shape.Example) {
		entry.Example = shape.Example
	}
	if !isJSONNull(shape.DesignerDefaults) {
		entry.DesignerDefaults = shape.DesignerDefaults
	}
	entry.Properties = decodeDict(t, shape.Properties, func(value json.RawMessage) *PropertyDefinition {
		if isJSONNull(value) {
			return nil
		}
		var definition struct {
			Type    *string         `json:"type"`
			Default json.RawMessage `json:"default"`
		}
		_ = json.Unmarshal(value, &definition)
		result := &PropertyDefinition{Type: definition.Type}
		if !isJSONNull(definition.Default) {
			result.Default = decodeSTJ(t, definition.Default)
		}
		return result
	})
	entry.Inputs = decodeDict(t, shape.Inputs, func(value json.RawMessage) *jn { return decodeSTJ(t, value) })
	entry.Outputs = decodeDict(t, shape.Outputs, func(value json.RawMessage) *jn { return decodeSTJ(t, value) })
	return entry
}

func decodeOracleInput(t *testing.T, inputs map[string]json.RawMessage) *AnalyzeInput {
	in := &AnalyzeInput{}
	if raw := inputs["bundle"]; !isJSONNull(raw) {
		var bundle struct {
			ViewConfig       json.RawMessage `json:"viewConfig"`
			ViewModelConfig  json.RawMessage `json:"viewModelConfig"`
			ModelConfig      json.RawMessage `json:"modelConfig"`
			ResourcesStrings json.RawMessage `json:"resourcesStrings"`
			Handlers         *string         `json:"handlers"`
			Converters       *string         `json:"converters"`
			Validators       *string         `json:"validators"`
		}
		if err := json.Unmarshal(raw, &bundle); err != nil {
			t.Fatal(err)
		}
		in.Bundle = &Bundle{ViewConfig: decodeSTJ(t, bundle.ViewConfig), ViewModelConfig: decodeSTJ(t, bundle.ViewModelConfig),
			ModelConfig: decodeSTJ(t, bundle.ModelConfig), ResourcesStrings: decodeSTJ(t, bundle.ResourcesStrings),
			Handlers: bundle.Handlers, Converters: bundle.Converters, Validators: bundle.Validators}
	}
	in.MobileTypes = decodeSet(t, inputs["mobileTypes"])
	in.WebTypes = decodeSet(t, inputs["webTypes"])
	in.WebByType = decodeDict(t, inputs["webByType"], func(raw json.RawMessage) *RegistryEntry { return decodeEntry(t, raw) })
	in.MobileByType = decodeDict(t, inputs["mobileByType"], func(raw json.RawMessage) *RegistryEntry { return decodeEntry(t, raw) })
	if raw := inputs["rules"]; !isJSONNull(raw) {
		if err := json.Unmarshal(raw, &in.Rules); err != nil {
			t.Fatal(err)
		}
	}
	if raw := inputs["templateRule"]; !isJSONNull(raw) {
		if err := json.Unmarshal(raw, &in.TemplateRule); err != nil {
			t.Fatal(err)
		}
	}
	in.MobileRequestTypes = decodeSet(t, inputs["mobileRequestTypes"])
	in.SourcePage = decodeString(inputs["sourcePage"])
	in.SourceTemplate = decodeString(inputs["sourceTemplate"])
	in.SuggestedTarget = decodeString(inputs["suggestedTarget"])
	in.ContainerNameMap = decodeDict(t, inputs["containerNameMap"], func(raw json.RawMessage) string { return deref(decodeString(raw)) })
	if raw := inputs["sectionRegistration"]; !isJSONNull(raw) {
		if err := json.Unmarshal(raw, &in.SectionRegistration); err != nil {
			t.Fatal(err)
		}
	}
	if raw := inputs["pageBusinessRulesProbe"]; !isJSONNull(raw) {
		var probe struct {
			ProbeOk bool    `json:"probeOk"`
			Note    *string `json:"note"`
			Rules   []*struct {
				Caption        *string         `json:"caption"`
				ConditionIssue int             `json:"conditionIssue"`
				Condition      json.RawMessage `json:"condition"`
				Actions        []*struct {
					ActionType   *string   `json:"actionType"`
					ElementItems []*string `json:"elementItems"`
				} `json:"actions"`
			} `json:"rules"`
		}
		if err := json.Unmarshal(raw, &probe); err != nil {
			t.Fatal(err)
		}
		result := &PageBusinessRuleProbeResult{ProbeOk: probe.ProbeOk, Note: probe.Note}
		for _, rule := range probe.Rules {
			converted := &SourcePageBusinessRule{Caption: rule.Caption, ConditionIssue: rule.ConditionIssue,
				Condition: decodeSTJ(t, rule.Condition)}
			for _, action := range rule.Actions {
				converted.Actions = append(converted.Actions, &SourcePageRuleAction{ActionType: action.ActionType,
					ElementItems: action.ElementItems})
			}
			result.Rules = append(result.Rules, converted)
		}
		in.PageBusinessRulesProbe = result
	}
	in.TemplateComponentNames = decodeSet(t, inputs["templateComponentNames"])
	in.ComponentNameMap = decodeDict(t, inputs["componentNameMap"], func(raw json.RawMessage) *ComponentMappingRule {
		var rule *ComponentMappingRule
		if err := json.Unmarshal(raw, &rule); err != nil {
			t.Fatal(err)
		}
		return rule
	})
	if raw := inputs["positionalPlacements"]; !isJSONNull(raw) {
		var placements []*struct {
			WebAnchor    *string `json:"webAnchor"`
			MobileAnchor *string `json:"mobileAnchor"`
		}
		if err := json.Unmarshal(raw, &placements); err != nil {
			t.Fatal(err)
		}
		in.PositionalPlacements = []*PositionalPlacement{}
		for _, placement := range placements {
			if placement == nil {
				in.PositionalPlacements = append(in.PositionalPlacements, nil)
				continue
			}
			in.PositionalPlacements = append(in.PositionalPlacements, &PositionalPlacement{WebAnchor: placement.WebAnchor,
				MobileAnchor: placement.MobileAnchor})
		}
	}
	in.MobileContainerParents = decodeDict(t, inputs["mobileContainerParents"], func(raw json.RawMessage) string { return deref(decodeString(raw)) })
	in.MobileTemplateViewModelConfig = decodeSTJ(t, inputs["mobileTemplateViewModelConfig"])
	in.MobileTemplateModelConfig = decodeSTJ(t, inputs["mobileTemplateModelConfig"])
	in.MobileTemplateTypesByName = decodeDict(t, inputs["mobileTemplateTypesByName"], decodeString)
	in.MobileTemplateLayoutConfigs = decodeDict(t, inputs["mobileTemplateLayoutConfigs"], func(raw json.RawMessage) *jn { return decodeSTJ(t, raw) })
	in.MobileTemplateNodesByName = decodeDict(t, inputs["mobileTemplateNodesByName"], func(raw json.RawMessage) *jn { return decodeNewtonsoft(t, raw) })
	in.WebTemplateBaselineNodes = decodeDict(t, inputs["webTemplateBaselineNodes"], func(raw json.RawMessage) *jn { return decodeNewtonsoft(t, raw) })
	in.WebTemplateResources = decodeNewtonsoft(t, inputs["webTemplateResources"])
	if raw := inputs["actionTargetsProbe"]; !isJSONNull(raw) {
		var probe struct {
			ProbeOk      bool            `json:"probeOk"`
			Note         *string         `json:"note"`
			TargetsByKey json.RawMessage `json:"targetsByKey"`
		}
		if err := json.Unmarshal(raw, &probe); err != nil {
			t.Fatal(err)
		}
		in.ActionTargetsProbe = &ActionTargetsProbeResult{ProbeOk: probe.ProbeOk, Note: probe.Note,
			TargetsByKey: decodeDict(t, probe.TargetsByKey, func(raw json.RawMessage) *ActionTargetResolution {
				if isJSONNull(raw) {
					return nil
				}
				var resolution struct {
					Kind                        *string `json:"kind"`
					Target                      *string `json:"target"`
					State                       int     `json:"state"`
					ResolvedCandidateSchemaName *string `json:"resolvedCandidateSchemaName"`
				}
				_ = json.Unmarshal(raw, &resolution)
				return &ActionTargetResolution{Kind: resolution.Kind, Target: resolution.Target, State: resolution.State,
					ResolvedCandidateSchemaName: resolution.ResolvedCandidateSchemaName}
			})}
	}
	if raw := inputs["existingMobilePages"]; !isJSONNull(raw) {
		in.ExistingMobilePages = []*ExistingMobilePageInfo{}
		if err := json.Unmarshal(raw, &in.ExistingMobilePages); err != nil {
			t.Fatal(err)
		}
	}
	if raw := inputs["mobileRegistryGeneration"]; !isJSONNull(raw) {
		var generation struct {
			BaseInputs json.RawMessage `json:"baseInputs"`
		}
		_ = json.Unmarshal(raw, &generation)
		in.MobileRegistryGeneration = &RegistryGeneration{BaseInputs: decodeDict(t, generation.BaseInputs,
			func(raw json.RawMessage) *jn { return decodeSTJ(t, raw) })}
	}
	return in
}

func firstDifference(a, b string) string {
	index := 0
	for index < len(a) && index < len(b) && a[index] == b[index] {
		index++
	}
	from := max(0, index-120)
	return fmt.Sprintf("at %d\n  go:   %s\n  clio: %s", index, a[from:min(len(a), index+200)], b[from:min(len(b), index+200)])
}

func TestAnalyzeMatchesClioRecords(t *testing.T) {
	records := loadOracleRecords(t)
	if len(records) == 0 {
		t.Fatal("no oracle records")
	}
	only := os.Getenv("ORACLE_ONLY")
	matched, failed := 0, 0
	var failures []string
	for _, record := range records {
		if only != "" && !strings.Contains(record.file, only) {
			continue
		}
		got, problem := runOracleRecord(t, record)
		want := ""
		if record.Output != nil {
			want = *record.Output
		} else if record.Error != nil {
			want = "error: " + *record.Error
		}
		if problem == "" && got == want {
			matched++
			continue
		}
		failed++
		if problem != "" {
			failures = append(failures, record.file+": "+problem)
		} else {
			failures = append(failures, record.file+": "+firstDifference(got, want))
		}
	}
	t.Logf("oracle: %d matched, %d differ", matched, failed)
	limit := 15
	if os.Getenv("ORACLE_ALL") != "" {
		limit = len(failures)
	}
	for index, failure := range failures {
		if index >= limit {
			break
		}
		t.Error(failure)
	}
}

func runOracleRecord(t *testing.T, record oracleRecord) (result string, problem string) {
	defer func() {
		if recovered := recover(); recovered != nil {
			problem = fmt.Sprintf("panic: %v", recovered)
		}
	}()
	in := decodeOracleInput(t, record.Inputs)
	guide, err := Analyze(in)
	if err != nil {
		return "error: " + err.Error(), ""
	}
	text, err := guideText(guide)
	if err != nil {
		return "", err.Error()
	}
	return text, ""
}
