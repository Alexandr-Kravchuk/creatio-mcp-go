package creatio

import "strings"

// pageWriteDigestMaxComponents is PageWriteDigest.MaxComponents.
const pageWriteDigestMaxComponents = 25

// PageWriteDigest is clio 8.1.0.142's PageWriteDigest (ENG-103023): what a successful page write added or
// changed, whether content validation ran, and the post-save checksum, so the caller does not re-read the page.
type PageWriteDigest struct {
	Validation        string         `json:"validation"`
	Basis             string         `json:"basis"`
	Components        []string       `json:"components"`
	OtherSections     *orderedCounts `json:"otherSections,omitempty"`
	Checksum          string         `json:"checksum,omitempty"`
	ComponentsOmitted *int           `json:"componentsOmitted,omitempty"`
}

// BuildPageWriteDigest is PageWriteDigest.TryBuild: nil when the body cannot be parsed (a digest never fails
// the write), and nil for an append whose merged viewConfigDiff was never written back while the fragment had
// component operations, so a digest never lists components that did not reach the page.
func BuildPageWriteDigest(body string, appendMode, validationRan bool, projection *PageAppendProjection, checksum string) *PageWriteDigest {
	if appendMode && projection != nil && !projection.ViewConfigDiffApplied && projection.IncomingOperationCount > 0 {
		return nil
	}
	parsed, err := parsePageBody(body)
	if err != nil {
		return nil
	}
	other := &orderedCounts{counts: map[string]int{}}
	add := func(section string, count int) {
		if count > 0 {
			other.keys = append(other.keys, section)
			other.counts[section] = count
		}
	}
	add("viewModelConfigDiff", countItems(parsed.viewModelConfigDiff))
	add("modelConfigDiff", countItems(parsed.modelConfigDiff))
	handlers, _ := handlerInfo(strings.TrimSpace(parsed.handlers))
	add("handlers", handlers)
	lines := []string{}
	for _, operation := range pageViewConfigOps(parsed) {
		lines = append(lines, pageWriteDigestLine(operation))
	}
	digest := &PageWriteDigest{Validation: "skipped", Basis: "body"}
	if validationRan {
		digest.Validation = "passed"
	}
	if appendMode {
		digest.Basis = "fragment"
	}
	if strings.TrimSpace(checksum) != "" {
		digest.Checksum = checksum
	}
	digest.Components = lines
	if len(lines) > pageWriteDigestMaxComponents {
		digest.Components = lines[:pageWriteDigestMaxComponents]
		omitted := len(lines) - pageWriteDigestMaxComponents
		digest.ComponentsOmitted = &omitted
	}
	if len(other.keys) > 0 {
		digest.OtherSections = other
	}
	return digest
}

// AttachPageWriteDigest is PageUpdateTool's ENG-103023 step: a successful real save carries the digest of the
// body as sent (the fragment for an append), with the post-save checksum.
func AttachPageWriteDigest(request *PageUpdateRequest, response *PageUpdateResponse) {
	if !response.Success || request.DryRun {
		return
	}
	response.Digest = BuildPageWriteDigest(request.Body, strings.EqualFold(request.Mode, "append"), request.Validate,
		response.AppendProjection, response.NewChecksum)
}

// pageWriteDigestLine is PageWriteDigest.Describe: "operation name (type) in parent".
func pageWriteDigestLine(operation PageOperation) string {
	orUnknown := func(value *string) string {
		if value == nil {
			return "?"
		}
		return *value
	}
	line := orUnknown(operation.Operation) + " " + orUnknown(operation.Name)
	if operation.Type != nil && strings.TrimSpace(*operation.Type) != "" {
		line += " (" + *operation.Type + ")"
	}
	if operation.ParentName != nil && strings.TrimSpace(*operation.ParentName) != "" {
		line += " in " + *operation.ParentName
	}
	return line
}
