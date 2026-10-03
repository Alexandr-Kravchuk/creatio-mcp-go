package redact

import (
	"encoding/json"
	"os"
	"sync"
	"testing"
)

// testdata/clio-golden.json holds {in, out} pairs produced by clio's own SensitiveErrorTextRedactor.Redact
// (clio 8.1.0.134, called by reflection): every string literal of clio's SensitiveErrorTextRedactorTests
// plus extra probes, each as written and as System.Text.Json serializes it. Pairs that would trip
// scripts/check-public-safety.sh were left out.
func TestTextMatchesClioGolden(t *testing.T) {
	raw, err := os.ReadFile("testdata/clio-golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var pairs []struct{ In, Out string }
	if err := json.Unmarshal(raw, &pairs); err != nil {
		t.Fatal(err)
	}
	if len(pairs) < 1000 {
		t.Fatalf("golden file has %d pairs, expected the full set", len(pairs))
	}
	for _, pair := range pairs {
		if got := Text(pair.In); got != pair.Out {
			t.Errorf("Text(%q)\n got %q\nwant %q", pair.In, got, pair.Out)
		}
	}
}

func TestTextKeyRules(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"Environment 'Foo' not found.", "Environment 'Foo' not found."},
		{"POST https://admin:pw@crm.example.com/0/x returned 401.", "POST [redacted-uri] returned 401."},
		{`{"password":"<password>"}`, `{"password":"[redacted]"}`},
		{`{\u0022password\u0022:\u0022pw\u0022}`, `{\u0022password\u0022:\u0022[redacted]\u0022}`},
		{"open /Users/someone/x.json: no such file", "open [redacted-path]: no such file"},
		{`read C:\Users\x\a.json failed`, "read [redacted-path] failed"},
		{"dial tcp 10.1.2.3:443: refused", "dial tcp [redacted-uri]: refused"},
		{"user john.doe@acme.com and clio@8.1.0.57", "user [redacted] and clio@8.1.0.57"},
		{"Authorization: Bearer abc", "Authorization=[redacted]"},
		{"SchemaUId=1 uid=2", "SchemaUId=1 uid=[redacted]"},
	}
	for _, c := range cases {
		if got := Text(c.in); got != c.want {
			t.Errorf("Text(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestTextIsIdempotentOnPlaceholders(t *testing.T) {
	once := Text(`call {"command":"x","args":{"password":"<password>"}} at https://h.example.com/x`)
	if twice := Text(once); twice != once {
		t.Fatalf("second pass changed %q to %q", once, twice)
	}
}

func TestTextIsSafeForConcurrentUse(t *testing.T) {
	var group sync.WaitGroup
	for i := 0; i < 16; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for j := 0; j < 50; j++ {
				if got := Text("see https://h.example.com/x"); got != "see [redacted-uri]" {
					t.Errorf("got %q", got)
				}
			}
		}()
	}
	group.Wait()
}

func TestAll(t *testing.T) {
	got := All([]string{"ok", "see https://h.example.com"})
	if len(got) != 2 || got[0] != "ok" || got[1] != "see [redacted-uri]" {
		t.Fatalf("All = %q", got)
	}
}
