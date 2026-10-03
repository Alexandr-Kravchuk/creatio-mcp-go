package creatio

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
)

// EmailTemplateResponse is clio's get-email-template answer.
type EmailTemplateResponse struct {
	Success  bool                   `json:"success"`
	Error    *string                `json:"error,omitempty"`
	EmailID  *string                `json:"email-id,omitempty"`
	HostType *string                `json:"host-type,omitempty"`
	Name     *string                `json:"name,omitempty"`
	Variants []EmailTemplateVariant `json:"variants"`
}

// EmailTemplateVariant is one language and storage-format variant of an email, with its update checksum.
type EmailTemplateVariant struct {
	Format         string  `json:"format"`
	Exists         bool    `json:"exists"`
	RecordID       *string `json:"record-id,omitempty"`
	Language       *string `json:"language,omitempty"`
	LanguageID     *string `json:"language-id,omitempty"`
	Checksum       string  `json:"checksum"`
	PageJSON       *string `json:"page-json,omitempty"`
	PageHTML       *string `json:"page-html,omitempty"`
	AmpHTML        *string `json:"amp-html,omitempty"`
	TemplateVer    *int    `json:"template-version,omitempty"`
	Subject        *string `json:"subject,omitempty"`
	Body           *string `json:"body,omitempty"`
	TemplateConfig *string `json:"template-config,omitempty"`
	ConfigType     *int    `json:"config-type,omitempty"`
	IsHTMLBody     *bool   `json:"is-html-body,omitempty"`
	IsDefault      *bool   `json:"is-default,omitempty"`
}

const (
	emailTemplateBeefree         = "beefree"
	emailTemplateLegacy          = "legacy"
	emailTemplateBulkEmail       = "bulk-email"
	emailTemplateMessageTemplate = "message-template"
)

// EmailTemplateFailure is clio's failed read: success false, the error, and an empty variant list.
func EmailTemplateFailure(message string) EmailTemplateResponse {
	return EmailTemplateResponse{Error: &message, Variants: []EmailTemplateVariant{}}
}

// GetEmailTemplate reads every Beefree (BfEmailTemplate) and legacy (Body/Subject/TemplateConfig) content
// variant of a BulkEmail or EmailTemplate host over OData, each with clio's SHA-256 checksum. language
// selects which Beefree variant must be represented (empty: the one the email sends by default) and
// languageID which EmailTemplateLang translation; a missing one is returned as exists=false.
// emailID and languageID must already be validated GUIDs (languageID may be empty).
func (c *Client) GetEmailTemplate(ctx context.Context, emailID, language, languageID string) EmailTemplateResponse {
	response, err := c.emailTemplateLoad(ctx, emailID, language, languageID)
	if err != nil {
		return EmailTemplateFailure(err.Error())
	}
	return response
}

func (c *Client) emailTemplateLoad(ctx context.Context, emailID, language, languageID string) (EmailTemplateResponse, error) {
	var requestedLanguage *string
	if strings.TrimSpace(language) != "" {
		requestedLanguage = &language
	}
	bulkEmail, err := c.emailTemplateRead(ctx, "BulkEmail", "Id eq "+emailID, "Id,Name,TemplateSubject,TemplateBody,TemplateConfig", 1)
	if err != nil {
		return EmailTemplateResponse{}, err
	}
	messageTemplate, err := c.emailTemplateRead(ctx, "EmailTemplate", "Id eq "+emailID, "Id,Name,Subject,Body,TemplateConfig,ConfigType,IsHtmlBody", 1)
	if err != nil {
		return EmailTemplateResponse{}, err
	}
	var host map[string]json.RawMessage
	hostType := ""
	switch {
	case len(bulkEmail) > 0 && bulkEmail[0] != nil:
		host, hostType = bulkEmail[0], emailTemplateBulkEmail
	case len(messageTemplate) > 0 && messageTemplate[0] != nil:
		host, hostType = messageTemplate[0], emailTemplateMessageTemplate
	default:
		return EmailTemplateFailure(fmt.Sprintf("No BulkEmail or EmailTemplate host record exists with Id %s.", emailID)), nil
	}
	beefreeRows, err := c.emailTemplateRead(ctx, "BfEmailTemplate", "EmailId eq "+emailID,
		"Id,EmailId,Language,TemplateLanguageId,PageJson,PageHtml,AmpHtml,TemplateVersion,IsDefault", 100)
	if err != nil {
		return EmailTemplateResponse{}, err
	}
	variants := []EmailTemplateVariant{}
	for _, row := range beefreeRows {
		variants = append(variants, emailTemplateBeefreeVariant(row))
	}
	beefreeLanguage := emailTemplateDefaultLanguage(variants)
	if requestedLanguage != nil {
		beefreeLanguage = *requestedLanguage
	}
	represented := false
	for _, variant := range variants {
		if variant.Format == emailTemplateBeefree && strings.EqualFold(emailTemplateText(variant.Language), beefreeLanguage) {
			represented = true
			break
		}
	}
	if !represented {
		variants = append(variants, emailTemplateAbsentVariant(emailTemplateBeefree, &beefreeLanguage, nil))
	}
	if hostType == emailTemplateBulkEmail {
		if strings.TrimSpace(languageID) != "" {
			return EmailTemplateFailure("language-id is supported only for EmailTemplate message-template hosts."), nil
		}
		variants = append(variants, emailTemplateLegacyVariant(host, nil))
	} else {
		variants = append(variants, emailTemplateLegacyVariant(host, nil))
		translations, err := c.emailTemplateRead(ctx, "EmailTemplateLang", "EmailTemplateId eq "+emailID,
			"Id,EmailTemplateId,LanguageId,Subject,Body,TemplateConfig,IsHtmlBody", 100)
		if err != nil {
			return EmailTemplateResponse{}, err
		}
		for _, row := range translations {
			variants = append(variants, emailTemplateLegacyVariant(row, emailTemplateString(row, "LanguageId")))
		}
		if wanted := emailTemplateGUID(&languageID); wanted != "" {
			found := false
			for _, variant := range variants {
				if variant.Format == emailTemplateLegacy && emailTemplateGUID(variant.LanguageID) == wanted {
					found = true
					break
				}
			}
			if !found {
				variants = append(variants, emailTemplateAbsentVariant(emailTemplateLegacy, nil, &wanted))
			}
		}
	}
	return EmailTemplateResponse{Success: true, EmailID: &emailID, HostType: &hostType,
		Name: emailTemplateString(host, "Name"), Variants: variants}, nil
}

// emailTemplateDefaultLanguage is the Beefree language sent when none is named: the IsDefault row, else the
// only stored row, else the empty language.
func emailTemplateDefaultLanguage(variants []EmailTemplateVariant) string {
	stored := []EmailTemplateVariant{}
	for _, variant := range variants {
		if variant.Format == emailTemplateBeefree && variant.Exists {
			stored = append(stored, variant)
		}
	}
	for _, variant := range stored {
		if variant.IsDefault != nil && *variant.IsDefault {
			return emailTemplateText(variant.Language)
		}
	}
	if len(stored) == 1 {
		return emailTemplateText(stored[0].Language)
	}
	return ""
}

func emailTemplateBeefreeVariant(row map[string]json.RawMessage) EmailTemplateVariant {
	language := ""
	if value := emailTemplateString(row, "Language"); value != nil {
		language = *value
	}
	languageID := emailTemplateString(row, "TemplateLanguageId")
	pageJSON, pageHTML, ampHTML := emailTemplateString(row, "PageJson"), emailTemplateString(row, "PageHtml"), emailTemplateString(row, "AmpHtml")
	version := emailTemplateInt(row, "TemplateVersion")
	return EmailTemplateVariant{
		Format: emailTemplateBeefree, Exists: true, RecordID: emailTemplateString(row, "Id"), Language: &language, LanguageID: languageID,
		Checksum: emailTemplateHash(stringPointer(emailTemplateBeefree), &language, languageID, pageJSON, pageHTML, ampHTML, emailTemplateIntText(version)),
		PageJSON: pageJSON, PageHTML: pageHTML, AmpHTML: ampHTML, TemplateVer: version, IsDefault: emailTemplateBool(row, "IsDefault"),
	}
}

func emailTemplateLegacyVariant(row map[string]json.RawMessage, languageID *string) EmailTemplateVariant {
	subject := emailTemplateString(row, "Subject")
	if subject == nil {
		subject = emailTemplateString(row, "TemplateSubject")
	}
	body := emailTemplateString(row, "Body")
	if body == nil {
		body = emailTemplateString(row, "TemplateBody")
	}
	config := emailTemplateString(row, "TemplateConfig")
	configType := emailTemplateInt(row, "ConfigType")
	isHTML := emailTemplateBool(row, "IsHtmlBody")
	// bool?.ToString() in .NET is "True" or "False".
	var isHTMLText *string
	if isHTML != nil {
		isHTMLText = stringPointer("False")
		if *isHTML {
			isHTMLText = stringPointer("True")
		}
	}
	normalizedID := emailTemplateGUID(languageID)
	return EmailTemplateVariant{
		Format: emailTemplateLegacy, Exists: true, RecordID: emailTemplateString(row, "Id"), LanguageID: languageID,
		Checksum: emailTemplateHash(stringPointer(emailTemplateLegacy), nil, &normalizedID, subject, body, config, emailTemplateIntText(configType), isHTMLText),
		Subject:  subject, Body: body, TemplateConfig: config, ConfigType: configType, IsHTMLBody: isHTML,
	}
}

func emailTemplateAbsentVariant(format string, language, languageID *string) EmailTemplateVariant {
	slot := languageID
	if format == emailTemplateLegacy {
		normalized := emailTemplateGUID(languageID)
		slot = &normalized
	}
	return EmailTemplateVariant{Format: format, Language: language, LanguageID: languageID,
		Checksum: emailTemplateHash(&format, language, slot, stringPointer("<absent>"))}
}

// emailTemplateHash is clio's checksum: SHA-256 over "<UTF-16 length>:<value>|" per value, null as "<null>".
func emailTemplateHash(values ...*string) string {
	var builder strings.Builder
	for _, value := range values {
		text := "<null>"
		if value != nil {
			text = *value
		}
		builder.WriteString(strconv.Itoa(len(utf16.Encode([]rune(text)))))
		builder.WriteByte(':')
		builder.WriteString(text)
		builder.WriteByte('|')
	}
	sum := sha256.Sum256([]byte(builder.String()))
	return hex.EncodeToString(sum[:])
}

// emailTemplateGUID is clio's NormalizeGuid: the lower-case "D" form, or "" for anything that is not a GUID.
func emailTemplateGUID(value *string) string {
	if value == nil {
		return ""
	}
	if uid, ok := parseGUID(*value); ok {
		return uid
	}
	return ""
}

func emailTemplateText(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func emailTemplateString(row map[string]json.RawMessage, name string) *string {
	var text string
	if raw, ok := row[name]; ok && json.Unmarshal(raw, &text) == nil && strings.HasPrefix(strings.TrimSpace(string(raw)), `"`) {
		return &text
	}
	return nil
}

func emailTemplateInt(row map[string]json.RawMessage, name string) *int {
	var number int32
	if raw, ok := row[name]; ok && json.Unmarshal(raw, &number) == nil && string(raw) != "null" {
		value := int(number)
		return &value
	}
	return nil
}

func emailTemplateIntText(value *int) *string {
	if value == nil {
		return nil
	}
	return stringPointer(strconv.Itoa(*value))
}

func emailTemplateBool(row map[string]json.RawMessage, name string) *bool {
	var flag bool
	if raw, ok := row[name]; ok && string(raw) != "null" && json.Unmarshal(raw, &flag) == nil {
		return &flag
	}
	return nil
}

// emailTemplateRead is clio's OData read: odata/<entity>?$filter=...&$select=...&$top=..., with an error
// envelope or a body without a value array refused.
func (c *Client) emailTemplateRead(ctx context.Context, entity, filter, selectColumns string, top int) ([]map[string]json.RawMessage, error) {
	requestCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	response, _, err := c.doAuthenticated(requestCtx, c.requestClient(), func() (*http.Request, error) {
		target := c.serviceURL("odata/"+entity) + "?$filter=" + strings.ReplaceAll(url.QueryEscape(filter), "+", "%20") +
			"&$select=" + selectColumns + "&$top=" + strconv.Itoa(top)
		request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, target, nil)
		if err != nil {
			return nil, fmt.Errorf("build OData request: %w", err)
		}
		request.Header.Set("Accept", "application/json")
		return request, nil
	})
	if err != nil {
		if isTransportError(err) {
			return nil, fmt.Errorf("OData transport failure: %w", err)
		}
		return nil, err
	}
	payload, err := readResponse(response)
	if err != nil {
		return nil, fmt.Errorf("OData response: %w", err)
	}
	if strings.TrimSpace(string(payload)) == "" {
		return nil, fmt.Errorf("Creatio OData %s response was empty.", entity)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(payload, &root); err != nil {
		if trimmed := strings.TrimSpace(string(payload)); strings.HasPrefix(trimmed, "<") {
			// The System.Text.Json text clio reports for an HTML page (an entity the environment does not have).
			return nil, fmt.Errorf("'<' is an invalid start of a value. LineNumber: 0 | BytePositionInLine: 0.")
		}
		return nil, fmt.Errorf("Creatio OData %s response is not JSON: %v", entity, err)
	}
	if raw, ok := root["error"]; ok && string(raw) != "null" {
		var detail struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(raw, &detail) == nil && strings.TrimSpace(detail.Message) != "" {
			return nil, fmt.Errorf("%s", detail.Message)
		}
		return nil, fmt.Errorf("Creatio OData %s returned an error envelope.", entity)
	}
	var rows []map[string]json.RawMessage
	if raw, ok := root["value"]; !ok || json.Unmarshal(raw, &rows) != nil || rows == nil {
		return nil, fmt.Errorf("Creatio OData %s response did not contain a value array.", entity)
	}
	return rows, nil
}
