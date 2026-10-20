package creatio

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// Page-write behaviour of clio 8.1.0.142 seen on the live write run (T23): GH-1839 and ENG-103023.

func TestPageWriteSeedFromModelConfigShapes(t *testing.T) {
	parse := func(text string) *jnode {
		node, err := parseJNode([]byte(text))
		if err != nil {
			t.Fatal(err)
		}
		return node
	}
	cases := []struct {
		name, modelConfig, wantOps, wantSource, wantWarning string
		primary                                             bool
	}{
		{"declared primary with config", `{"primaryDataSourceName":"Main","dataSources":{"Main":{"type":"crt.EntityDataSource","config":{}}}}`,
			`[{"operation":"merge","path":["dataSources","Main","config"],"values":{"entitySchemaName":"UsrE"}}]`, "Main", "", true},
		{"declared primary without config", `{"primaryDataSourceName":"Main","dataSources":{"Main":{"type":"crt.EntityDataSource"}}}`,
			`[{"operation":"merge","path":["dataSources","Main"],"values":{"config":{"entitySchemaName":"UsrE"}}}]`, "Main", "", true},
		{"list template PDS, not primary", `{"dataSources":{"PDS":{"type":"crt.EntityDataSource","scope":"viewElement","config":{}}}}`,
			`[{"operation":"merge","path":["dataSources","PDS","config"],"values":{"entitySchemaName":"UsrE"}}]`, "PDS", "", false},
		{"dataSources without PDS", `{"dataSources":{}}`,
			`[{"operation":"merge","path":[],"values":{"primaryDataSourceName":"PDS"}},` +
				`{"operation":"merge","path":["dataSources"],"values":{"PDS":{"type":"crt.EntityDataSource","scope":"page","config":{"entitySchemaName":"UsrE"}}}}]`,
			"PDS", "", true},
		{"undeclared primary", `{"primaryDataSourceName":"Gone"}`,
			`[{"operation":"merge","path":[],"values":{"primaryDataSourceName":"PDS","dataSources":{"PDS":{"type":"crt.EntityDataSource","scope":"page","config":{"entitySchemaName":"UsrE"}}}}}]`,
			"PDS", "The template names primary data source 'Gone' but does not declare it; 'PDS' was added and made primary instead.", true},
	}
	for _, testCase := range cases {
		seed := pageWriteSeedFromModelConfig(parse(testCase.modelConfig), "UsrE")
		if got := string(seed.modelConfigDiff.newtonsoftJSON()); got != testCase.wantOps {
			t.Errorf("%s: ops %s", testCase.name, got)
		}
		if seed.dataSourceName != testCase.wantSource || seed.isPrimary != testCase.primary || seed.warning != testCase.wantWarning {
			t.Errorf("%s: seed %#v", testCase.name, seed)
		}
	}
	unknown := pageWriteSeedFromModelConfig(nil, "UsrE")
	if len(unknown.modelConfigDiff.items) != 0 || unknown.dataSourceName != "" ||
		unknown.warning != "The template's model config is unknown; no data source was bound to 'UsrE' - the entity is recorded in the page dependencies only. Bind it with update-page." {
		t.Errorf("unknown %#v", unknown)
	}
}

func TestPageWriteApplySeedNotes(t *testing.T) {
	var mobile PageCreateResponse
	pageWriteApplySeed(&mobile, "UsrE", nil)
	if mobile.DataSourceName != "" || !strings.HasPrefix(mobile.Note, "Mobile page: the entity is recorded") {
		t.Errorf("mobile %#v", mobile)
	}
	var list PageCreateResponse
	pageWriteApplySeed(&list, "UsrE", &pageEntityDataSourceSeed{modelConfigDiff: newArray(), dataSourceName: "PDS"})
	if list.DataSourceName != "PDS" || list.PrimaryDataSourceName != "" || list.Note != "" {
		t.Errorf("list %#v", list)
	}
}

func TestPageWriteAppendReportsConfigDiffCounts(t *testing.T) {
	current := pageWriteTestPage(`[]`)
	incoming := "/**SCHEMA_VIEW_MODEL_CONFIG_DIFF*/[{\"operation\":\"merge\",\"path\":[\"attributes\"],\"values\":{}}]/**SCHEMA_VIEW_MODEL_CONFIG_DIFF*/" +
		"/**SCHEMA_MODEL_CONFIG_DIFF*/[{\"operation\":\"merge\",\"path\":[],\"values\":{}},{\"operation\":\"merge\",\"path\":[],\"values\":{}}]/**SCHEMA_MODEL_CONFIG_DIFF*/"
	_, projection, message, _ := pageWriteMergeBodies(current, incoming)
	if message != "" || projection.IncomingOperationCount != 0 || projection.IncomingViewModelConfigDiffOperationCount != 1 ||
		projection.IncomingModelConfigDiffOperationCount != 2 {
		t.Fatalf("projection %#v %q", projection, message)
	}
	encoded, _ := json.Marshal(projection)
	if !strings.Contains(string(encoded), `"collapsedIncomingOperationCount":0,"incomingViewModelConfigDiffOperationCount":1,"incomingModelConfigDiffOperationCount":2,"viewConfigDiffApplied":true`) {
		t.Errorf("json %s", encoded)
	}
}

func TestPageWriteAppendRefusesEmptyFragmentAndMissingMarker(t *testing.T) {
	_, _, message, _ := pageWriteMergeBodies(pageWriteTestPage(`[]`), "/**SCHEMA_VIEW_CONFIG_DIFF*/[]/**SCHEMA_VIEW_CONFIG_DIFF*/\n/**SCHEMA_HANDLERS*/[ ]/**SCHEMA_HANDLERS*/")
	if message != pageWriteNothingToAppendMessage {
		t.Errorf("empty fragment: %q", message)
	}
	_, _, message, _ = pageWriteMergeBodies(`{"viewConfigDiff":[]}`, `{"viewConfigDiff":[]}`)
	if message != pageWriteNothingToAppendMessage {
		t.Errorf("empty mobile fragment: %q", message)
	}
	noModelMarker := strings.Replace(pageWriteTestPage(`[]`), "modelConfigDiff: /**SCHEMA_MODEL_CONFIG_DIFF*/[]/**SCHEMA_MODEL_CONFIG_DIFF*/,", "", 1)
	_, _, message, _ = pageWriteMergeBodies(noModelMarker, `/**SCHEMA_MODEL_CONFIG_DIFF*/[{"operation":"merge","path":[],"values":{}}]/**SCHEMA_MODEL_CONFIG_DIFF*/`)
	if !strings.HasPrefix(message, "The page's stored body has no /**SCHEMA_MODEL_CONFIG_DIFF*/ marker pair, so the SCHEMA_MODEL_CONFIG_DIFF content") {
		t.Errorf("missing marker: %q", message)
	}
}

func TestPageWriteAdoptSchemaHeader(t *testing.T) {
	scaffold := pageWriteEmptyReplacingBody("UsrP", false)
	incoming := `define("UsrP", /**SCHEMA_DEPS*/["@creatio-devkit/common"]/**SCHEMA_DEPS*/, function/**SCHEMA_ARGS*/(sdk)/**SCHEMA_ARGS*/ {})`
	adopted := pageWriteAdoptSchemaHeader(scaffold, incoming)
	if !strings.Contains(adopted, `/**SCHEMA_DEPS*/["@creatio-devkit/common"]/**SCHEMA_DEPS*/`) || !strings.Contains(adopted, `/**SCHEMA_ARGS*/(sdk)/**SCHEMA_ARGS*/`) {
		t.Errorf("adopted %q", adopted)
	}
	if got := pageWriteAdoptSchemaHeader(scaffold, `/**SCHEMA_HANDLERS*/[]/**SCHEMA_HANDLERS*/`); got != scaffold {
		t.Errorf("section-only fragment changed the scaffold")
	}
}

func TestUpdatePageAppendOntoAnEmptyBodyMergesIntoTheEmptyPage(t *testing.T) {
	stand := &pageWriteStand{t: t, checksums: []string{"c9"}, ownBody: ""}
	server := groupCServer(t, stand.handle)
	defer server.Close()
	client := newFormsTestClient(t, server.URL)
	fragment := "/**SCHEMA_MODEL_CONFIG_DIFF*/[{\"operation\":\"merge\",\"path\":[],\"values\":{\"primaryDataSourceName\":\"PDS\"}}]/**SCHEMA_MODEL_CONFIG_DIFF*/"
	request := &PageUpdateRequest{SchemaName: "UsrP", Mode: "append", Body: fragment, Validate: true}
	response := client.UpdatePage(context.Background(), request)
	if !response.Success || response.AppendProjection == nil || response.AppendProjection.IncomingModelConfigDiffOperationCount != 1 {
		t.Fatalf("response %#v", response)
	}
	body := stand.saved["body"].(string)
	if !strings.HasPrefix(body, `define("UsrP", /**SCHEMA_DEPS*/[]/**SCHEMA_DEPS*/`) || !strings.Contains(body, `"primaryDataSourceName": "PDS"`) ||
		!strings.Contains(body, "handlers: /**SCHEMA_HANDLERS*/[]/**SCHEMA_HANDLERS*/") {
		t.Fatalf("saved body %q", body)
	}
	AttachPageWriteDigest(request, &response)
	if response.Digest == nil || response.Digest.Basis != "fragment" || len(response.Digest.Components) != 0 ||
		response.Digest.OtherSections == nil || response.Digest.OtherSections.counts["modelConfigDiff"] != 1 {
		t.Fatalf("digest %#v", response.Digest)
	}
	dry := &PageUpdateRequest{SchemaName: "UsrP", Body: fragment, DryRun: true}
	dryResponse := PageUpdateResponse{Success: true}
	AttachPageWriteDigest(dry, &dryResponse)
	if dryResponse.Digest != nil {
		t.Fatal("a dry run carries no digest")
	}
}

func TestSyncPagesReturnsTheDigestOfTheSavedBody(t *testing.T) {
	stand := &pageWriteStand{t: t, checksums: []string{""}, ownBody: pageWriteTestPage("[]")}
	server := groupCServer(t, stand.handle)
	defer server.Close()
	request := PageSyncRequest{Validate: true, Pages: []PageSyncPageInput{{SchemaName: "UsrP", Body: pageWriteTestPage(pageWriteLabel)}}}
	response := newFormsTestClient(t, server.URL).SyncPages(context.Background(), request, PageSyncPrepass(request))
	if !response.Success || response.Pages[0].Digest == nil {
		t.Fatalf("response %#v", response)
	}
	encoded, _ := json.Marshal(response.Pages[0].Digest)
	if string(encoded) != `{"validation":"passed","basis":"body","components":["insert Label1 (crt.Label) in MainContainer"]}` {
		t.Errorf("digest %s", encoded)
	}
}

func TestBuildPageWriteDigest(t *testing.T) {
	body := pageWriteTestPage(`[{"operation":"insert","name":"ParityLabel","values":{"type":"crt.Label"},"parentName":"MainContainer"},{"operation":"remove","name":"Old"}]`)
	body = strings.Replace(body, "modelConfigDiff: /**SCHEMA_MODEL_CONFIG_DIFF*/[]", `modelConfigDiff: /**SCHEMA_MODEL_CONFIG_DIFF*/[{"operation":"merge","path":[],"values":{}}]`, 1)
	digest := BuildPageWriteDigest(body, false, true, nil, "abc")
	encoded, _ := json.Marshal(digest)
	want := `{"validation":"passed","basis":"body","components":["insert ParityLabel (crt.Label) in MainContainer","remove Old"],` +
		`"otherSections":{"modelConfigDiff":1},"checksum":"abc"}`
	if string(encoded) != want {
		t.Errorf("digest %s", encoded)
	}
	fragment := BuildPageWriteDigest(pageWriteTestPage(`[]`), true, false, &PageAppendProjection{ViewConfigDiffApplied: true}, " ")
	encoded, _ = json.Marshal(fragment)
	if string(encoded) != `{"validation":"skipped","basis":"fragment","components":[]}` {
		t.Errorf("fragment digest %s", encoded)
	}
	if BuildPageWriteDigest(body, true, true, &PageAppendProjection{ViewConfigDiffApplied: false, IncomingOperationCount: 1}, "") != nil {
		t.Error("a fragment whose components were not written back must not get a digest")
	}
	var ops strings.Builder
	ops.WriteString("[")
	for index := 0; index < 27; index++ {
		if index > 0 {
			ops.WriteString(",")
		}
		ops.WriteString(`{"operation":"insert","name":"L` + string(rune('a'+index%26)) + `"}`)
	}
	ops.WriteString("]")
	capped := BuildPageWriteDigest(pageWriteTestPage(ops.String()), false, true, nil, "")
	if len(capped.Components) != 25 || capped.ComponentsOmitted == nil || *capped.ComponentsOmitted != 2 {
		t.Errorf("capped %#v", capped)
	}
}
