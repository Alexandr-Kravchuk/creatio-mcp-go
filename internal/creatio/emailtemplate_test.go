package creatio

import (
	"context"
	"strings"
	"testing"
)

const emailTestID = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"

func TestGetEmailTemplateReadsVariantsWithClioChecksums(t *testing.T) {
	client, requests := groupIServer(t, func(request groupIRequest) (int, string) {
		switch request.Path {
		case "/0/odata/BulkEmail":
			return 200, `{"value":[]}`
		case "/0/odata/EmailTemplate":
			return 200, `{"value":[{"Id":"` + emailTestID + `","Name":"Welcome","Subject":"Subj","Body":"Body","TemplateConfig":null,"ConfigType":0,"IsHtmlBody":true}]}`
		case "/0/odata/BfEmailTemplate":
			return 200, `{"value":[{"Id":"b-1","EmailId":"` + emailTestID + `","Language":"en-US","TemplateLanguageId":null,"PageJson":"{\"page\":\"ü😀\"}",` +
				`"PageHtml":"<p>Hi</p>","AmpHtml":null,"TemplateVersion":3,"IsDefault":true}]}`
		case "/0/odata/EmailTemplateLang":
			return 200, `{"value":[]}`
		}
		t.Errorf("unexpected request %#v", request)
		return 404, ""
	})
	result := client.GetEmailTemplate(context.Background(), emailTestID, " ", "6EBC31FA-EE6C-48E9-81BF-8003AC03B019")
	if !result.Success || *result.HostType != "message-template" || *result.Name != "Welcome" || len(result.Variants) != 3 {
		t.Fatalf("result = %#v", result)
	}
	// Expected digests were computed with clio's own Hash routine in .NET.
	checks := []struct{ format, checksum string }{
		{"beefree", "befbd710c973de13fe8c53941c64c261783ead8da305c3639e0e8847d810ba37"},
		{"legacy", "bb5bde8f1d99e0978c79a2c7abb57d9b21caeb456c81c486540a20ef50f82834"},
		{"legacy", "4dbdd7260dbc5dba1333592af00da77ebd0902d89d572da3b05a80ab197e35bf"},
	}
	for index, check := range checks {
		if variant := result.Variants[index]; variant.Format != check.format || variant.Checksum != check.checksum {
			t.Errorf("variant %d = %#v, want %s %s", index, variant, check.format, check.checksum)
		}
	}
	if absent := result.Variants[2]; absent.Exists || *absent.LanguageID != "6ebc31fa-ee6c-48e9-81bf-8003ac03b019" {
		t.Errorf("absent translation = %#v", absent)
	}
	first := (*requests)[0]
	if first.Path != "/0/odata/BulkEmail" || first.Query != "$filter=Id%20eq%20"+emailTestID+"&$select=Id,Name,TemplateSubject,TemplateBody,TemplateConfig&$top=1" {
		t.Errorf("BulkEmail request = %s?%s", first.Path, first.Query)
	}
}

func TestGetEmailTemplateReportsAMissingEntityAndAMissingHost(t *testing.T) {
	missingEntity := true
	client, _ := groupIServer(t, func(request groupIRequest) (int, string) {
		if missingEntity {
			return 404, "<html>not found</html>"
		}
		return 200, `{"value":[]}`
	})
	result := client.GetEmailTemplate(context.Background(), emailTestID, "", "")
	if result.Success || result.Error == nil || *result.Error != "'<' is an invalid start of a value. LineNumber: 0 | BytePositionInLine: 0." || result.Variants == nil {
		t.Fatalf("result = %#v", result)
	}
	missingEntity = false
	result = client.GetEmailTemplate(context.Background(), emailTestID, "", "")
	if result.Error == nil || !strings.HasPrefix(*result.Error, "No BulkEmail or EmailTemplate host record exists with Id "+emailTestID) {
		t.Fatalf("missing host = %#v", result)
	}
}

func TestEmailTemplateAbsentBeefreeChecksum(t *testing.T) {
	empty := ""
	if got := emailTemplateAbsentVariant(emailTemplateBeefree, &empty, nil).Checksum; got != "1c65e7e0b8df94267df15004052c64439adf822029ec77f8172fb082fe740168" {
		t.Fatalf("checksum = %s", got)
	}
}
