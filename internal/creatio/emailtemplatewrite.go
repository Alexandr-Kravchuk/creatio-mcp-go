package creatio

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type EmailTemplateUpdateOptions struct {
	EmailID          string  `json:"email-id"`
	Format           string  `json:"format"`
	ExpectedChecksum string  `json:"expected-checksum"`
	Confirm          bool    `json:"confirm"`
	Language         string  `json:"language"`
	LanguageID       string  `json:"language-id"`
	PageJSON         *string `json:"page-json"`
	PageHTML         *string `json:"page-html"`
	AmpHTML          *string `json:"amp-html"`
	TemplateVersion  *int    `json:"template-version"`
	Subject          *string `json:"subject"`
	Body             *string `json:"body"`
	TemplateConfig   *string `json:"template-config"`
	ConfigType       *int    `json:"config-type"`
	IsHTMLBody       *bool   `json:"is-html-body"`
}
type EmailTemplateUpdateResult struct {
	Success  bool    `json:"success"`
	Error    *string `json:"error"`
	EmailID  *string `json:"email-id"`
	Format   *string `json:"format"`
	Created  bool    `json:"created"`
	Checksum *string `json:"checksum"`
}

func EmailTemplateUpdateFailure(message string) EmailTemplateUpdateResult {
	return EmailTemplateUpdateResult{Error: &message}
}
func (c *Client) UpdateEmailTemplate(ctx context.Context, o EmailTemplateUpdateOptions) EmailTemplateUpdateResult {
	id, ok := DataWriteParseGUID(o.EmailID)
	if !ok {
		return EmailTemplateUpdateFailure("email-id must be a GUID.")
	}
	if !o.Confirm {
		return EmailTemplateUpdateFailure("confirm=true is required; no email content was changed.")
	}
	format := strings.ToLower(strings.TrimSpace(o.Format))
	if format != "beefree" && format != "legacy" {
		return EmailTemplateUpdateFailure("format must be 'beefree' or 'legacy'.")
	}
	if strings.TrimSpace(o.ExpectedChecksum) == "" {
		return EmailTemplateUpdateFailure("expected-checksum is required. Call get-email-template immediately before updating.")
	}
	if o.LanguageID != "" {
		if _, ok := DataWriteParseGUID(o.LanguageID); !ok {
			return EmailTemplateUpdateFailure("language-id must be a GUID.")
		}
	}
	current := c.GetEmailTemplate(ctx, id, o.Language, o.LanguageID)
	if !current.Success {
		return EmailTemplateUpdateFailure(emailTemplateText(current.Error))
	}
	find := func(current EmailTemplateResponse) *EmailTemplateVariant {
		language := o.Language
		if strings.TrimSpace(language) == "" {
			language = emailTemplateDefaultLanguage(current.Variants)
		}
		for i, v := range current.Variants {
			if v.Format != format {
				continue
			}
			if format == "beefree" && strings.EqualFold(emailTemplateText(v.Language), language) {
				return &current.Variants[i]
			}
			if format == "legacy" && strings.EqualFold(emailTemplateGUID(v.LanguageID), emailTemplateGUID(&o.LanguageID)) {
				return &current.Variants[i]
			}
		}
		return nil
	}
	variant := find(current)
	checksum := ""
	if variant != nil {
		checksum = variant.Checksum
	} else {
		language, languageID := &o.Language, &o.LanguageID
		absent := emailTemplateAbsentVariant(format, language, languageID)
		checksum = absent.Checksum
	}
	if !strings.EqualFold(checksum, strings.TrimSpace(o.ExpectedChecksum)) {
		return EmailTemplateUpdateFailure(fmt.Sprintf("Email content changed after it was read. Expected checksum '%s', current checksum '%s'. Read again and reapply the edit.", o.ExpectedChecksum, checksum))
	}
	payload := map[string]any{}
	entity, record := "", ""
	created := false
	if format == "beefree" {
		if o.PageJSON == nil || strings.TrimSpace(*o.PageJSON) == "" || o.PageHTML == nil || strings.TrimSpace(*o.PageHTML) == "" {
			return EmailTemplateUpdateFailure("page-json and page-html are required for format=beefree.")
		}
		language := o.Language
		if variant != nil {
			language = emailTemplateText(variant.Language)
		}
		amp := ""
		version := 0
		if variant != nil {
			amp = emailTemplateText(variant.AmpHTML)
			if variant.TemplateVer != nil {
				version = *variant.TemplateVer
			}
		}
		if o.AmpHTML != nil {
			amp = *o.AmpHTML
		}
		if o.TemplateVersion != nil {
			version = *o.TemplateVersion
		}
		payload = map[string]any{"EmailId": id, "Language": language, "PageJson": *o.PageJSON, "PageHtml": *o.PageHTML, "AmpHtml": amp, "TemplateVersion": version}
		created = variant == nil || !variant.Exists
		if created {
			payload["IsDefault"] = language == ""
		} else {
			record = emailTemplateText(variant.RecordID)
		}
		entity = "BfEmailTemplate"
	} else {
		translated := strings.TrimSpace(o.LanguageID) != ""
		host := emailTemplateText(current.HostType)
		if o.Subject == nil && o.Body == nil && o.TemplateConfig == nil && o.ConfigType == nil && o.IsHTMLBody == nil {
			return EmailTemplateUpdateFailure("At least one of subject, body, template-config, config-type, or is-html-body is required for format=legacy.")
		}
		if translated && o.ConfigType != nil {
			return EmailTemplateUpdateFailure("config-type is supported only for the primary EmailTemplate variant, not EmailTemplateLang translations.")
		}
		if translated && host != emailTemplateMessageTemplate {
			return EmailTemplateUpdateFailure("language-id is supported only for EmailTemplate message-template hosts.")
		}
		if host != emailTemplateMessageTemplate && (o.ConfigType != nil || o.IsHTMLBody != nil) {
			return EmailTemplateUpdateFailure("config-type and is-html-body are supported only for EmailTemplate message-template hosts.")
		}
		subject, body := "Subject", "Body"
		entity = "EmailTemplate"
		if host == emailTemplateBulkEmail {
			subject = "TemplateSubject"
			body = "TemplateBody"
			entity = "BulkEmail"
		}
		if o.Subject != nil {
			payload[subject] = *o.Subject
		}
		if o.Body != nil {
			payload[body] = *o.Body
		}
		if o.TemplateConfig != nil {
			payload["TemplateConfig"] = *o.TemplateConfig
		}
		if o.ConfigType != nil {
			payload["ConfigType"] = *o.ConfigType
		}
		if o.IsHTMLBody != nil {
			payload["IsHtmlBody"] = *o.IsHTMLBody
		}
		record = id
		if translated {
			entity = "EmailTemplateLang"
			created = variant == nil || !variant.Exists
			if created {
				record = ""
				payload["EmailTemplateId"] = id
				payload["LanguageId"] = o.LanguageID
			} else {
				record = emailTemplateText(variant.RecordID)
			}
		}
	}
	path := "odata/" + entity
	method := http.MethodPost
	if record != "" {
		method = http.MethodPatch
		path += "(" + record + ")"
	}
	body, _ := json.Marshal(payload)
	response, err := c.dataWriteSendOnce(ctx, method, path, body, 30*time.Second)
	if err != nil {
		return EmailTemplateUpdateFailure(err.Error())
	}
	if strings.TrimSpace(response) != "" {
		if !json.Valid([]byte(response)) {
			return EmailTemplateUpdateFailure("Creatio OData response was not valid JSON.")
		}
		if message, failed := dataWriteDetectError([]byte(response), dataWriteODataContext); failed {
			return EmailTemplateUpdateFailure(message)
		}
	}
	result := EmailTemplateUpdateResult{Success: true, EmailID: &id, Format: &format, Created: created}
	persisted := c.GetEmailTemplate(ctx, id, o.Language, o.LanguageID)
	after := find(persisted)
	if !persisted.Success || after == nil || !after.Exists {
		message := "Email content was written, but its checksum could not be confirmed by reading the row back. Call get-email-template again before the next guarded update."
		result.Success = false
		result.Error = &message
		return result
	}
	result.Checksum = &after.Checksum
	return result
}
