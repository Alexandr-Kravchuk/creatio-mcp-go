package creatio

import (
	"testing"
)

func TestThemeAdvisorVerdictsAndFullPreview(t *testing.T) {
	triage := AdviseThemePalette(ThemeAdvisorOptions{Operation: "triage", Colors: []string{"white", "black", "invalid"}})
	if triage["acceptedCount"] != 2 || triage["passingCount"] != 1 || triage["highestContrastHex"] != "#000000" {
		t.Fatalf("triage=%v", triage)
	}
	strong := AdviseThemePalette(ThemeAdvisorOptions{Operation: "validate-color", Role: "primary", Color: "white"})
	if strong["verdict"] != "strong" {
		t.Fatalf("primary=%v", strong)
	}
	same := AdviseThemePalette(ThemeAdvisorOptions{Operation: "accent-validate-manual", Primary: "black", Color: "black"})
	if same["verdict"] != "strong" || same["warning"].(map[string]any)["code"] != "ACCENT_TOO_SIMILAR_TO_PRIMARY" {
		t.Fatalf("accent=%v", same)
	}
	adapted := AdviseThemePalette(ThemeAdvisorOptions{Operation: "adapt-primary", Primary: "white"})
	if adapted["adaptationState"] != "adapted" || adapted["adaptedContrastOnWhite"].(float64) < 3 {
		t.Fatalf("adapted=%v", adapted)
	}
	preview := AdviseThemePalette(ThemeAdvisorOptions{Operation: "preview", Primary: "#0055aa", Secondary: "#222222", Accent: "#990000", FullStops: true})
	if preview["success"] != true || len(preview["palettes"].(map[string]any)["primary"].(map[string]string)) != 12 {
		t.Fatalf("preview=%v", preview)
	}
}
