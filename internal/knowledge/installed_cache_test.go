package knowledge

import (
	"os"
	"testing"
)

// TestInstalledCuratedCache verifies the curated library clio installed on this machine with this
// package's verifier: the pinned key, the P1363 signature, every digest. It reads only, and is skipped
// where clio has installed nothing (CI).
func TestInstalledCuratedCache(t *testing.T) {
	if _, err := os.Stat(SettingsPath()); err != nil {
		t.Skip("no clio settings on this machine")
	}
	runtime := NewRuntime(Capabilities{ClioVersion: Version{8, 1, 0}, McpToolContractVersion: Version{1, 1, 0}, Tools: allToolsForTest()})
	settings, err := runtime.LoadSettings()
	if err != nil {
		t.Fatalf("settings: %v", err)
	}
	if _, _, ok := settings.Knowledge.FindSource(CuratedAlias); !ok {
		t.Skip("the curated source is not configured")
	}
	if SourceKey(CuratedAlias) != "8735b75a8c38cfab509b4fe9" {
		t.Fatalf("source key = %s", SourceKey(CuratedAlias))
	}
	lookup := runtime.FindByName("core-rules")
	if lookup.Status != LookupActive {
		t.Skipf("curated knowledge is not active here: %s", runtime.LastDiagnostic())
	}
	if lookup.Provenance.SourceAlias != CuratedAlias || lookup.Article.URI != "docs://knowledge/com.creatio.clio/core-rules" {
		t.Fatalf("unexpected provenance %+v", lookup.Provenance)
	}
	if names := runtime.GuideNames(); len(names) < 10 {
		t.Fatalf("only %d guide names", len(names))
	}
}

// allToolsForTest answers every tool requirement, so the test exercises trust and content checks only.
func allToolsForTest() map[string]bool {
	return map[string]bool{
		"configure-knowledge-feedback-policy": true, "get-guidance": true, "get-knowledge-feedback-policy": true,
		"inspect-access": true, "inspect-license": true, "inspect-role": true, "inspect-user": true,
		"list-knowledge-examples": true, "manage-access": true, "manage-license": true, "manage-role": true, "manage-user": true,
	}
}
