package creatio

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEmailTemplateUpdateGuardsChecksumAndReadsPersistedReceipt(t *testing.T) {
	subject := "Before"
	writes := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ServiceModel/AuthService.svc/Login":
			io.WriteString(w, `{"Code":0}`)
		case "/0/odata/BulkEmail", "/0/odata/BfEmailTemplate", "/0/odata/EmailTemplateLang":
			io.WriteString(w, `{"value":[]}`)
		case "/0/odata/EmailTemplate":
			fmt.Fprintf(w, `{"value":[{"Id":"%s","Name":"Welcome","Subject":%q,"Body":"body","TemplateConfig":"","ConfigType":0,"IsHtmlBody":true}]}`, emailTestID, subject)
		case "/0/odata/EmailTemplate(" + emailTestID + ")":
			if r.Method != http.MethodPatch {
				t.Errorf("method=%s", r.Method)
			}
			body, _ := io.ReadAll(r.Body)
			var payload map[string]any
			if err := json.Unmarshal(body, &payload); err != nil {
				t.Error(err)
			}
			if len(payload) != 1 {
				t.Errorf("unexpected extra fields=%v", payload)
			}
			subject = payload["Subject"].(string)
			writes++
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected route=%s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := newFormsTestClient(t, server.URL)
	before := client.GetEmailTemplate(context.Background(), emailTestID, "", "")
	var checksum string
	for _, v := range before.Variants {
		if v.Format == "legacy" {
			checksum = v.Checksum
		}
	}
	updated := "After"
	options := EmailTemplateUpdateOptions{EmailID: emailTestID, Format: "legacy", ExpectedChecksum: checksum, Confirm: true, Subject: &updated}
	options.Confirm = false
	if result := client.UpdateEmailTemplate(context.Background(), options); result.Success || writes != 0 {
		t.Fatalf("unconfirmed=%#v writes=%d", result, writes)
	}
	options.Confirm = true
	options.ExpectedChecksum = "stale"
	if result := client.UpdateEmailTemplate(context.Background(), options); result.Success || writes != 0 || !strings.Contains(*result.Error, "Email content changed") {
		t.Fatalf("stale=%#v writes=%d", result, writes)
	}
	options.ExpectedChecksum = checksum
	result := client.UpdateEmailTemplate(context.Background(), options)
	if !result.Success || writes != 1 || result.Checksum == nil || *result.Checksum == checksum {
		t.Fatalf("write=%#v writes=%d", result, writes)
	}
	after := client.GetEmailTemplate(context.Background(), emailTestID, "", "")
	for _, v := range after.Variants {
		if v.Format == "legacy" && v.Checksum != *result.Checksum {
			t.Fatalf("receipt differs from stored checksum")
		}
	}
}
