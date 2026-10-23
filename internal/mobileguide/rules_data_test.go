package mobileguide

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

// The embedded rules must be exactly what scripts/generate-mobile-conversion-rules.py copied from clio.
func TestBundledRulesMatchGeneratedSum(t *testing.T) {
	sum := sha256.Sum256(bundledRulesJSON)
	if got, want := hex.EncodeToString(sum[:]), bundledDataSHA256["WebToMobilePageConversionRules.json"]; got != want {
		t.Fatalf("embedded rules sha256 %s, generator recorded %s: rerun the generator", got, want)
	}
}

// When the clio 8.1.0.142 checkout is on this machine, the embedded bytes must equal clio's file.
func TestBundledRulesEqualClioSource(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	source := filepath.Join(home, ".cache", "creatio-mcp-go", "clio-8.1.0.142", "clio", "Command", "McpServer", "Data", "WebToMobilePageConversionRules.json")
	if dir := os.Getenv("CLIO_SOURCE"); dir != "" {
		source = filepath.Join(dir, "clio", "Command", "McpServer", "Data", "WebToMobilePageConversionRules.json")
	}
	data, err := os.ReadFile(source)
	if err != nil {
		t.Skipf("clio source not present: %v", err)
	}
	if string(data) != string(bundledRulesJSON) {
		t.Fatalf("embedded rules differ from %s: rerun scripts/generate-mobile-conversion-rules.py", source)
	}
}
