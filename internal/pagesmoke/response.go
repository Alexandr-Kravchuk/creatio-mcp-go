package pagesmoke

import "github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"

// Response is PageSmokeTool's PageSmokeResponse, in the shape clio's MCP serializer writes it: a null member is
// left out, an empty list is written.
type Response struct {
	Success         *bool              `json:"success,omitzero"`
	Message         *string            `json:"message,omitzero"`
	Error           *string            `json:"error,omitzero"`
	Code            *string            `json:"code,omitzero"`
	BrowserDownload *BrowserDownload   `json:"browserDownload,omitzero"`
	BrowserConsent  *BrowserConsent    `json:"browserConsent,omitzero"`
	URL             *string            `json:"url,omitzero"`
	Mode            *string            `json:"mode,omitzero"`
	Ready           bool               `json:"ready"`
	LoadMs          *int64             `json:"loadMs,omitzero"`
	Title           *string            `json:"title,omitzero"`
	Buttons         []responseButton   `json:"buttons,omitzero"`
	ButtonsOmitted  *int               `json:"buttonsOmitted,omitzero"`
	Fields          []responseField    `json:"fields,omitzero"`
	FieldsOmitted   *int               `json:"fieldsOmitted,omitzero"`
	ConsoleErrors   []PageConsoleError `json:"consoleErrors,omitzero"`
	Click           *responseClick     `json:"click,omitzero"`
	Warnings        []string           `json:"warnings,omitzero"`
	CorrelationID   string             `json:"correlation-id"`
}

type responseButton struct {
	Name     *string `json:"name,omitzero"`
	Caption  *string `json:"caption,omitzero"`
	Disabled bool    `json:"disabled,omitzero"`
}

type responseField struct {
	Name     *string `json:"name,omitzero"`
	Type     *string `json:"type,omitzero"`
	Caption  *string `json:"caption,omitzero"`
	Required bool    `json:"required,omitzero"`
	Readonly bool    `json:"readonly,omitzero"`
	Value    *string `json:"value,omitzero"`
	Error    *string `json:"error,omitzero"`
}

type responseClick struct {
	Outcome            string                  `json:"outcome"`
	Name               *string                 `json:"name,omitzero"`
	Caption            *string                 `json:"caption,omitzero"`
	Candidates         []responseButton        `json:"candidates,omitzero"`
	URL                *string                 `json:"url,omitzero"`
	Navigations        []string                `json:"navigations,omitzero"`
	NavigationsOmitted *int                    `json:"navigationsOmitted,omitzero"`
	ConsoleErrors      []PageConsoleError      `json:"consoleErrors,omitzero"`
	Dialogs            []string                `json:"dialogs,omitzero"`
	Toasts             []string                `json:"toasts,omitzero"`
	InvalidFields      []responseField         `json:"invalidFields,omitzero"`
	Created            []creatio.CreatedRecord `json:"created,omitzero"`
	Settled            *bool                   `json:"settled,omitzero"`
}

// FailureResponse is PageSmokeResponse.Failure.
func FailureResponse(message string) Response {
	failed := false
	return Response{Success: &failed, Error: &message}
}

// NoBrowserResponse is PageSmokeResponse.NoBrowser: a failed download is a failure; every other code is an answer
// the agent acts on, so it carries no success flag (clio-run would otherwise redact the paths the user must see).
func NoBrowserResponse(resolution Resolution) Response {
	code := resolution.Code
	if code == CodeDownloadFailed {
		failed := false
		message := resolution.Message
		return Response{Success: &failed, Code: &code, Error: &message}
	}
	message := resolution.Message
	return Response{Code: &code, Message: &message, BrowserConsent: resolution.Consent, BrowserDownload: resolution.Download}
}

// ResponseFrom is PageSmokeResponse.From.
func ResponseFrom(result Result) Response {
	success := result.Success
	response := Response{Success: &success, Error: result.Error, URL: result.URL, Mode: result.Mode, Ready: result.Ready,
		LoadMs: result.LoadMs, Title: result.Title, Buttons: responseButtons(result.Buttons), Fields: responseFields(result.Fields),
		ConsoleErrors: result.ConsoleErrors}
	if result.ButtonsOmitted > 0 {
		omitted := result.ButtonsOmitted
		response.ButtonsOmitted = &omitted
	}
	if result.FieldsOmitted > 0 {
		omitted := result.FieldsOmitted
		response.FieldsOmitted = &omitted
	}
	if len(result.Warnings) > 0 {
		response.Warnings = result.Warnings
	}
	if click := result.Click; click != nil {
		response.Click = &responseClick{Outcome: click.Outcome, Name: click.Name, Caption: click.Caption,
			Candidates: responseButtons(click.Candidates), URL: click.URL, Navigations: click.Navigations,
			NavigationsOmitted: click.NavigationsOmitted, ConsoleErrors: click.ConsoleErrors, Dialogs: click.Dialogs,
			Toasts: click.Toasts, InvalidFields: responseFields(click.InvalidFields), Created: click.Created, Settled: click.Settled}
	}
	return response
}

func responseButtons(buttons []PageButton) []responseButton {
	if buttons == nil {
		return nil
	}
	view := make([]responseButton, len(buttons))
	for index, button := range buttons {
		view[index] = responseButton(button)
	}
	return view
}

func responseFields(fields []PageField) []responseField {
	if fields == nil {
		return nil
	}
	view := make([]responseField, len(fields))
	for index, field := range fields {
		view[index] = responseField(field)
	}
	return view
}
