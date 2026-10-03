package creatio

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRuleWriteDeleteIntegration(t *testing.T) {
	for _, saveFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "save-failure"}[saveFailure], func(t *testing.T) {
			var calls []string
			var saved map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				calls = append(calls, r.URL.Path)
				switch {
				case strings.HasSuffix(r.URL.Path, "/Login"):
					io.WriteString(w, `{"Code":0}`)
				case strings.HasSuffix(r.URL.Path, "/SelectQuery"):
					io.WriteString(w, `{"success":true,"rows":[{"Name":"Own","UId":"aaaaaaaa-0000-0000-0000-000000000001"}]}`)
				case strings.HasSuffix(r.URL.Path, "/GetSchemaDesignItem"):
					io.WriteString(w, `{"success":true,"schema":{"uId":"bbbbbbbb-0000-0000-0000-000000000002"}}`)
				case strings.HasSuffix(r.URL.Path, "/GetSchema"):
					var request map[string]any
					json.Unmarshal(body, &request)
					if request["targetSchemaManagerName"] != "EntitySchemaManager" || request["useFullHierarchy"] != true {
						t.Errorf("request %#v", request)
					}
					metadata := `{"rules":[{"uId":"parent","name":"Delete"},{"uId":"child","name":"Helper","parentUId":"PARENT"},{"uId":"keep","name":"Keep"}],"unrelated":true,"integer":9007199254740993,"decimal":10.50}`
					json.NewEncoder(w).Encode(map[string]any{"success": true, "schema": map[string]any{"metaData": metadata, "extension": "preserve", "resources": []any{map[string]any{"key": "AddonConfig.Rules.parent.Caption", "value": []any{}}, map[string]any{"key": "child.Caption", "value": []any{}}, map[string]any{"key": "AddonConfig.Rules.keep.Caption", "value": []any{}}}}})
				case strings.HasSuffix(r.URL.Path, "/SaveSchema"):
					json.Unmarshal(body, &saved)
					if saveFailure {
						io.WriteString(w, `{"success":true,"value":false,"errorInfo":{"message":"save rejected"}}`)
					} else {
						io.WriteString(w, `{"success":true}`)
					}
				case strings.HasSuffix(r.URL.Path, "/ResetScriptCache"), strings.HasSuffix(r.URL.Path, "/BuildConfiguration"):
					// A failed post-save refresh cannot negate the committed save.
					http.Error(w, "refresh failed", http.StatusInternalServerError)
				default:
					t.Errorf("unexpected %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			result := newFormsTestClient(t, server.URL).DeleteBusinessRules(context.Background(), false, BusinessRulesReadRequest{PackageName: "Own", SchemaName: "OwnEntity"}, []string{"delete", "missing", "", "delete"})
			if saveFailure {
				if result.Succeeded != 0 || result.Failed != 4 || result.Results[0].Error != "save rejected" {
					t.Fatalf("%#v", result)
				}
			} else {
				if result.Succeeded != 1 || result.Failed != 3 || result.Results[0].RuleName != "delete" {
					t.Fatalf("%#v", result)
				}
			}
			if !strings.Contains(saved["metaData"].(string), `"integer":9007199254740993`) || !strings.Contains(saved["metaData"].(string), `"decimal":10.50`) {
				t.Fatalf("numeric constants changed %s", saved["metaData"])
			}
			if saved["extension"] != "preserve" {
				t.Fatalf("extension lost %#v", saved)
			}
			var metadata map[string]any
			json.Unmarshal([]byte(saved["metaData"].(string)), &metadata)
			rules := metadata["rules"].([]any)
			if len(rules) != 1 || rules[0].(map[string]any)["name"] != "Keep" || metadata["unrelated"] != true {
				t.Fatalf("metadata %#v", metadata)
			}
			resources := saved["resources"].([]any)
			if len(resources) != 1 || resources[0].(map[string]any)["key"] != "keep.Caption" {
				t.Fatalf("resources %#v", resources)
			}
			refresh := 0
			for _, call := range calls {
				if strings.HasSuffix(call, "/ResetScriptCache") || strings.HasSuffix(call, "/BuildConfiguration") {
					refresh++
				}
			}
			if saveFailure && refresh != 0 || !saveFailure && refresh != 2 {
				t.Fatalf("refresh calls %v", calls)
			}
		})
	}
}

func TestRuleWriteDeleteNoSaveForAmbiguousOrMissing(t *testing.T) {
	saves := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/Login"):
			io.WriteString(w, `{"Code":0}`)
		case strings.HasSuffix(r.URL.Path, "/SelectQuery"):
			io.WriteString(w, `{"success":true,"rows":[{"Name":"Own","UId":"aaaaaaaa-0000-0000-0000-000000000001"}]}`)
		case strings.HasSuffix(r.URL.Path, "/GetSchemaDesignItem"):
			io.WriteString(w, `{"success":true,"schema":{"uId":"bbbbbbbb-0000-0000-0000-000000000002"}}`)
		case strings.HasSuffix(r.URL.Path, "/GetSchema"):
			json.NewEncoder(w).Encode(map[string]any{"success": true, "schema": map[string]any{"metaData": `{"rules":[{"name":"Duplicate"},{"name":"DUPLICATE"}]}`}})
		default:
			saves++
			t.Errorf("unexpected mutation %s", r.URL.Path)
		}
	}))
	defer server.Close()
	result := newFormsTestClient(t, server.URL).DeleteBusinessRules(context.Background(), false, BusinessRulesReadRequest{PackageName: "Own", SchemaName: "OwnEntity"}, []string{"duplicate", "missing"})
	if result.Failed != 2 || saves != 0 || !strings.Contains(result.Results[0].Error, "more than one rule") {
		t.Fatalf("%#v", result)
	}
}

func TestRuleWriteDeletePageIntegration(t *testing.T) {
	var addon map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		switch {
		case strings.HasSuffix(r.URL.Path, "/Login"):
			io.WriteString(w, `{"Code":0}`)
		case strings.HasSuffix(r.URL.Path, "/SelectQuery"):
			if strings.Contains(string(body), `"SysPackage"`) {
				io.WriteString(w, `{"success":true,"rows":[{"Name":"Own","UId":"aaaaaaaa-0000-0000-0000-000000000001"}]}`)
			} else {
				io.WriteString(w, `{"success":true,"rows":[{"UId":"dddddddd-0000-0000-0000-000000000004"}]}`)
			}
		case strings.HasSuffix(r.URL.Path, "/GetParentSchemas"):
			io.WriteString(w, `{"success":true,"values":[{"uId":"dddddddd-0000-0000-0000-000000000004","name":"OwnPage"}]}`)
		case strings.HasSuffix(r.URL.Path, "/GetSchema"):
			json.Unmarshal(body, &addon)
			json.NewEncoder(w).Encode(map[string]any{"success": true, "schema": map[string]any{"metaData": `{"rules":[{"name":"Rule","uId":"r"}]}`}})
		case strings.HasSuffix(r.URL.Path, "/SaveSchema"), strings.HasSuffix(r.URL.Path, "/ResetScriptCache"), strings.HasSuffix(r.URL.Path, "/BuildConfiguration"):
			io.WriteString(w, `{"success":true}`)
		default:
			t.Errorf("unexpected %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	result := newFormsTestClient(t, server.URL).DeleteBusinessRules(context.Background(), true, BusinessRulesReadRequest{PackageName: "Own", SchemaName: "OwnPage"}, []string{"Rule"})
	if result.Succeeded != 1 || addon["targetParentSchemaUId"] != "dddddddd-0000-0000-0000-000000000004" || addon["targetSchemaManagerName"] != "ClientUnitSchemaManager" || normalizeGUID(addon["targetSchemaUId"].(string)) == "" {
		t.Fatalf("%#v; %#v", result, addon)
	}
}
