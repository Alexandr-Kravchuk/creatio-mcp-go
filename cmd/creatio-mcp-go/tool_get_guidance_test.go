package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/knowledge"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// clio 8.1.0.142's get-guidance answers a missing name, and name=list, with a compact guide catalog.

func TestGetGuidanceMissingNameWithoutKnowledgeExplainsTheEmptyCatalog(t *testing.T) {
	session := knowledgeTestSession(t)
	for _, args := range []map[string]any{{}, {"name": "list"}} {
		result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "get-guidance", Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		var response map[string]any
		_ = json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &response)
		want := "No guide catalog is available: no compatible verified knowledge bundle is active, or every guide it publishes is disabled by a feature toggle."
		if len(args) == 0 {
			want = "Missing required parameter 'name'. " + want
		}
		if response["success"] != false || response["errorCode"] != "guidance-unavailable" || response["error"] != want ||
			response["availableGuides"] != nil || response["catalog"] != nil {
			t.Fatalf("args %v: %v", args, response)
		}
	}
}

func TestKnowledgeInstructionsPointAtTheGuideCatalog(t *testing.T) {
	if !strings.Contains(knowledgeServerInstructions, "\r\nname=list returns the guide catalog.\r\n") ||
		strings.Contains(knowledgeServerInstructions, "availableGuides") {
		t.Fatal("instructions must name name=list as clio 8.1.0.142 does")
	}
}

func TestGuidanceCatalogHelpersFollowClio(t *testing.T) {
	if got := guidancePurpose(knowledge.Descriptor{Name: "esq", Description: "Builds  ESQ\nqueries, e.g. filters. Second sentence."}); got != "Builds ESQ queries, e.g. filters." {
		t.Fatalf("purpose = %q", got)
	}
	if got := guidancePurpose(knowledge.Descriptor{Name: "x", Title: "x"}); got != "" {
		t.Fatalf("a title equal to the name is no purpose: %q", got)
	}
	if got := guidancePurpose(knowledge.Descriptor{Name: "x", Title: "Version 1.2 notes"}); got != "Version 1.2 notes" {
		t.Fatalf("a period inside a word does not end a sentence: %q", got)
	}
	if got := guidanceCutPurpose("Guides Creatio navigation workplace management: placing a section", 60); got != "Guides Creatio navigation workplace management: placing a…" {
		t.Fatalf("cut = %q", got)
	}
	if got := guidanceCutPurpose("Manage users, organizational and functional roles", 33); got != "Manage users, organizational and…" {
		t.Fatalf("cut = %q", got)
	}
	if got := guidanceCutPurpose("short", 40); got != "short" {
		t.Fatalf("cut = %q", got)
	}
	// System.Text.Json escapes HTML-sensitive ASCII and every non-ASCII unit as \uXXXX.
	if got := clioJSONStringBytes("a\n<…\\"); got != 2+1+2+6+6+2 {
		t.Fatalf("json size = %d", got)
	}
	for _, tc := range []struct {
		needle, candidate string
		score             int
		ok                bool
	}{
		{"core", "core-rules", 0, true}, {"rules", "core-rules", 1, true}, {"core-rulez", "core-rules", 2, true},
		{"zzzz-nothing", "core-rules", 0, false}, {"", "core-rules", 0, false},
	} {
		if score, ok := guidanceCloseness(tc.needle, tc.candidate); ok != tc.ok || (ok && score != tc.score) {
			t.Errorf("%q vs %q = %d %v", tc.needle, tc.candidate, score, ok)
		}
	}
	for keyword, want := range map[string]bool{"LIST": true, "index": true, "catalog": true, "help": true, "lists": false} {
		if guidanceCatalogKeyword(keyword) != want {
			t.Errorf("keyword %q", keyword)
		}
	}
}
