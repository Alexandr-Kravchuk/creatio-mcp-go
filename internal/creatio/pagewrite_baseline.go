package creatio

// The page conflict baseline: clio's PageBaselineStore and PageBaselineGuard. get-page records the editable
// schema's checksum in .clio-pages/{schema}/meta.json; update-page and sync-pages arm their external-
// modification check from it and refresh it after a save. clio and this server read and write the same file
// under the same .clio-pages/.locks/{schema}.lock, so either server can save a page the other one read.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/redact"
)

const (
	pageWritePinnedMergeAdvice = "If it was copied out of a conflict response rather than from a fresh get-page, this save " +
		"overwrites the change that caused the conflict - re-read the page and merge before saving."
)

// pageWriteBaseline is clio's PageBaselineInfo.
type pageWriteBaseline struct {
	SchemaName           string
	EnvironmentName      string
	EnvironmentURI       string
	EditableSchemaExists bool
	EditableSchemaUID    string
	Checksum             string
	ModifiedOn           string
	CapturedAt           string
}

func pageWriteBaselineFrom(node *jnode) *pageWriteBaseline {
	if !node.isObject() {
		return nil
	}
	text := func(key string) string {
		if value := node.get(key); value != nil && value.kind == jkString {
			return value.text
		}
		return ""
	}
	exists := node.get("editableSchemaExists")
	return &pageWriteBaseline{SchemaName: text("schemaName"), EnvironmentName: text("environmentName"),
		EnvironmentURI: text("environmentUri"), EditableSchemaExists: exists != nil && exists.kind == jkBool && exists.flag,
		EditableSchemaUID: text("editableSchemaUId"), Checksum: text("checksum"), ModifiedOn: text("modifiedOn"),
		CapturedAt: text("capturedAt")}
}

func (b *pageWriteBaseline) node() *jnode {
	fields := orderedFields{{"schemaName", pageWriteNullable(b.SchemaName)}}
	if b.EnvironmentName != "" {
		fields = append(fields, field{"environmentName", b.EnvironmentName})
	}
	if b.EnvironmentURI != "" {
		fields = append(fields, field{"environmentUri", b.EnvironmentURI})
	}
	fields = append(fields, field{"editableSchemaExists", b.EditableSchemaExists})
	for _, entry := range []field{{"editableSchemaUId", b.EditableSchemaUID}, {"checksum", b.Checksum}, {"modifiedOn", b.ModifiedOn}} {
		if entry.value.(string) != "" {
			fields = append(fields, entry)
		}
	}
	fields = append(fields, field{"capturedAt", pageWriteNullable(b.CapturedAt)})
	return toJNode(fields)
}

func pageWriteNullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// pageWriteMetaPath is PageBaselineStore.ResolveMetaFilePath: a body-file inside .clio-pages/{schema}/ names
// its own meta.json; otherwise the workspace anchor (or output-directory) does.
func pageWriteMetaPath(outputDirectory, bodyFile, schemaName string) (string, string, error) {
	warning := ""
	if strings.TrimSpace(bodyFile) != "" {
		if absolute, err := filepath.Abs(bodyFile); err != nil {
			warning = fmt.Sprintf("The body-file path '%s' could not be inspected (%s), so the conflict-detection baseline was located "+
				"from the workspace anchor instead of next to the body file. If the two differ, external-modification detection is not "+
				"armed for this page.", bodyFile, err.Error())
		} else {
			bodyDir := filepath.Dir(absolute)
			if strings.EqualFold(filepath.Base(bodyDir), schemaName) && filepath.Base(filepath.Dir(bodyDir)) == clioPagesDirectoryName {
				return filepath.Join(bodyDir, "meta.json"), "", nil
			}
		}
	}
	anchor, err := pageOutputAnchor(outputDirectory)
	if err != nil {
		return "", warning, err
	}
	return filepath.Join(anchor, clioPagesDirectoryName, schemaName, "meta.json"), warning, nil
}

// pageWriteLockPath is ResolveSchemaLockFilePath: .clio-pages/.locks/{schema}.lock beside the schema directory.
func pageWriteLockPath(metaPath string) string {
	schemaDir := filepath.Dir(metaPath)
	return filepath.Join(filepath.Dir(schemaDir), ".locks", filepath.Base(schemaDir)+".lock")
}

// pageWriteAbsenceDefinitive is AbsenceIsDefinitive: no .clio-pages root at all.
func pageWriteAbsenceDefinitive(metaPath string) bool {
	root := filepath.Dir(filepath.Dir(metaPath))
	info, err := os.Stat(root)
	return err != nil || !info.IsDir()
}

func pageWriteFileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// pageWriteGated runs action under the schema's interprocess lock, as clio's IInterprocessFileGate does.
func pageWriteGated[T any](metaPath string, action func() (T, error)) (T, error) {
	lockPath := pageWriteLockPath(metaPath)
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		var zero T
		return zero, err
	}
	unlock, err := lockPageFile(lockPath, pageLockTimeout)
	if err != nil {
		var zero T
		return zero, err
	}
	defer unlock()
	return action()
}

func pageWriteReadMeta(metaPath string) (*jnode, error) {
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return nil, err
	}
	node, err := parseJNode([]byte(strings.TrimPrefix(string(data), "\ufeff")))
	if err != nil {
		return nil, err
	}
	return node, nil
}

// pageWriteReadBaseline is PageBaselineStore.TryReadBaseline.
func pageWriteReadBaseline(metaPath string) (*pageWriteBaseline, string) {
	if strings.TrimSpace(metaPath) == "" || (!pageWriteFileExists(metaPath) && pageWriteAbsenceDefinitive(metaPath)) {
		return nil, ""
	}
	baseline, err := pageWriteGated(metaPath, func() (*pageWriteBaseline, error) {
		if !pageWriteFileExists(metaPath) {
			return nil, nil
		}
		meta, err := pageWriteReadMeta(metaPath)
		if err != nil {
			return nil, err
		}
		if !meta.isObject() {
			return nil, nil
		}
		return pageWriteBaselineFrom(meta.get("baseline")), nil
	})
	if err != nil {
		return nil, fmt.Sprintf("External-modification detection is DISARMED for this page: the conflict baseline '%s' exists but could not be read (%s). Re-run get-page to recapture it.", metaPath, err.Error())
	}
	return baseline, ""
}

// pageWriteMatchesEnvironment is PageBaselineStore.MatchesEnvironment: the recorded environment name decides
// when both sides have one, otherwise the recorded URI against a caller-supplied URI.
func pageWriteMatchesEnvironment(baseline *pageWriteBaseline, environmentName, uri string) bool {
	if baseline == nil {
		return false
	}
	if strings.TrimSpace(baseline.EnvironmentName) != "" && strings.TrimSpace(environmentName) != "" {
		return strings.EqualFold(baseline.EnvironmentName, environmentName)
	}
	if strings.TrimSpace(baseline.EnvironmentURI) != "" && strings.TrimSpace(uri) != "" {
		normalize := func(text string) string { return strings.TrimRight(strings.TrimSpace(text), "/") }
		return strings.EqualFold(normalize(baseline.EnvironmentURI), normalize(uri))
	}
	return false
}

// pageWriteRewriteMeta rewrites meta.json with a new (or no) baseline, keeping fetchedAt and page.
func pageWriteRewriteMeta(metaPath string, update func(previous *pageWriteBaseline) *pageWriteBaseline, keepIfNoBaseline bool) error {
	_, err := pageWriteGated(metaPath, func() (struct{}, error) {
		if !pageWriteFileExists(metaPath) {
			return struct{}{}, nil
		}
		meta, err := pageWriteReadMeta(metaPath)
		if err != nil {
			return struct{}{}, err
		}
		if meta == nil || meta.kind == jkNull {
			return struct{}{}, nil
		}
		if !meta.isObject() {
			return struct{}{}, errors.New("The JSON value could not be converted to Clio.Command.PageMetaFileModel.")
		}
		previous := pageWriteBaselineFrom(meta.get("baseline"))
		if keepIfNoBaseline && previous == nil {
			return struct{}{}, nil
		}
		next := update(previous)
		fields := orderedFields{{"fetchedAt", pageWriteMetaValue(meta.get("fetchedAt"))}, {"page", pageWriteMetaValue(meta.get("page"))}}
		if next != nil {
			fields = append(fields, field{"baseline", next.node()})
		}
		return struct{}{}, pageWriteMetaAtomically(metaPath, toJNode(fields).stjJSON())
	})
	return err
}

func pageWriteMetaValue(node *jnode) *jnode {
	if node == nil {
		return jsonNullNode()
	}
	return node
}

// pageWriteMetaAtomically is WriteMetaAtomically: a temporary sibling renamed over meta.json.
func pageWriteMetaAtomically(metaPath string, content []byte) error {
	temporary := filepath.Join(filepath.Dir(metaPath), "."+filepath.Base(metaPath)+"."+randomHex(16)+".tmp")
	defer os.Remove(temporary)
	if err := os.WriteFile(temporary, content, 0o644); err != nil {
		return err
	}
	return os.Rename(temporary, metaPath)
}

// pageWriteRefreshBaseline is PageBaselineStore.RefreshExistingBaseline (with MergeEnvironmentIdentity).
func pageWriteRefreshBaseline(metaPath string, refreshed *pageWriteBaseline) string {
	if strings.TrimSpace(metaPath) == "" || (!pageWriteFileExists(metaPath) && pageWriteAbsenceDefinitive(metaPath)) {
		return ""
	}
	err := pageWriteRewriteMeta(metaPath, func(previous *pageWriteBaseline) *pageWriteBaseline {
		return pageWriteMergeIdentity(refreshed, previous)
	}, false)
	if err != nil {
		return fmt.Sprintf("The page was saved, but its conflict baseline '%s' could not be updated to the post-save checksum (%s). "+
			"The next save of this page may report a conflict that is not real; re-run get-page to recapture the baseline.", metaPath, err.Error())
	}
	return ""
}

func pageWriteMergeIdentity(refreshed, previous *pageWriteBaseline) *pageWriteBaseline {
	if refreshed == nil || previous == nil {
		return refreshed
	}
	merged := *refreshed
	if strings.TrimSpace(merged.EnvironmentName) == "" {
		merged.EnvironmentName = previous.EnvironmentName
	}
	if strings.TrimSpace(merged.EnvironmentURI) == "" {
		merged.EnvironmentURI = previous.EnvironmentURI
	}
	return &merged
}

// pageWriteDeleteBaseline is PageBaselineStore.DeleteBaseline: meta.json stays, its baseline goes.
func pageWriteDeleteBaseline(metaPath string) string {
	if strings.TrimSpace(metaPath) == "" || (!pageWriteFileExists(metaPath) && pageWriteAbsenceDefinitive(metaPath)) {
		return ""
	}
	err := pageWriteRewriteMeta(metaPath, func(*pageWriteBaseline) *pageWriteBaseline { return nil }, true)
	if err != nil {
		return fmt.Sprintf("The page was saved, but the now-stale conflict baseline '%s' could not be removed (%s). "+
			"The next save of this page may report a conflict that is not real; re-run get-page to recapture the baseline.", metaPath, err.Error())
	}
	return ""
}

// pageWriteArm is PageBaselineGuard.TryArm: it fills the request's expected checksum / schema from the
// baseline of the same environment, or a conditional baseline for a selector-targeted write.
func pageWriteArm(request *PageUpdateRequest, outputDirectory string) (string, bool, string) {
	request.ExpectedChecksum = strings.TrimSpace(request.ExpectedChecksum)
	pinned := request.ExpectedChecksum != ""
	if strings.TrimSpace(request.TargetPackageUID) != "" || strings.TrimSpace(request.TargetSchemaUID) != "" {
		return pageWriteArmSelector(request, outputDirectory, pinned)
	}
	metaPath, resolveWarning, err := pageWriteMetaPath(outputDirectory, request.BodyFile, request.SchemaName)
	if err != nil {
		detail := redact.Text(err.Error())
		if pinned {
			return "", false, fmt.Sprintf("The checksum pinned for '%s' governs this save but could not be corroborated locally: "+
				"the .clio-pages baseline location could not be resolved (%s). %s", request.SchemaName, detail, pageWritePinnedMergeAdvice)
		}
		return "", false, fmt.Sprintf("External-modification detection is DISARMED for '%s': the .clio-pages baseline location could not be resolved (%s).",
			request.SchemaName, detail)
	}
	baseline, readWarning := pageWriteReadBaseline(metaPath)
	warnings := []string{}
	add := func(text string) {
		if strings.TrimSpace(text) != "" {
			warnings = append(warnings, strings.TrimSpace(text))
		}
	}
	add(readWarning)
	add(redact.Text(resolveWarning))
	if baseline == nil || !pageWriteMatchesEnvironment(baseline, request.EnvironmentName, request.EnvironmentURI) {
		if pinned {
			add(fmt.Sprintf("The checksum pinned for '%s' governs this save but could not be corroborated locally: no .clio-pages "+
				"baseline was found for this anchor and environment. %s", request.SchemaName, pageWritePinnedMergeAdvice))
		}
		return metaPath, false, strings.Join(warnings, " ")
	}
	request.expectedSchemaAbsent = !baseline.EditableSchemaExists && !pinned
	if pinned {
		pageWritePinnedDivergence(add, request, baseline)
		return metaPath, true, strings.Join(warnings, " ")
	}
	request.ExpectedChecksum = baseline.Checksum
	request.expectedSchemaUID = baseline.EditableSchemaUID
	return metaPath, true, strings.Join(warnings, " ")
}

func pageWritePinnedDivergence(add func(string), request *PageUpdateRequest, baseline *pageWriteBaseline) {
	if strings.TrimSpace(baseline.Checksum) == "" || baseline.Checksum == request.ExpectedChecksum {
		return
	}
	add(fmt.Sprintf("The checksum pinned for '%s' differs from the baseline clio last recorded for this page. %s", request.SchemaName, pageWritePinnedMergeAdvice))
	if strings.TrimSpace(baseline.EditableSchemaUID) == "" {
		return
	}
	add(fmt.Sprintf("The schema-identity check is not armed for this pinned save of '%s', and clio last recorded schema %s for this page and "+
		"environment. A checksum is derived from the page content, so a matching pin does not by itself prove the write is landing on that "+
		"same schema; pass target-schema-uid when the target matters.", request.SchemaName, baseline.EditableSchemaUID))
}

func pageWriteArmSelector(request *PageUpdateRequest, outputDirectory string, pinned bool) (string, bool, string) {
	request.expectedSchemaUID = ""
	request.expectedSchemaAbsent = false
	metaPath, _, err := pageWriteMetaPath(outputDirectory, request.BodyFile, request.SchemaName)
	if err != nil {
		metaPath = ""
	}
	warnings := []string{}
	add := func(text string) {
		if strings.TrimSpace(text) != "" {
			warnings = append(warnings, strings.TrimSpace(text))
		}
	}
	var baseline *pageWriteBaseline
	if metaPath != "" {
		var readWarning string
		baseline, readWarning = pageWriteReadBaseline(metaPath)
		add(readWarning)
	}
	known := baseline != nil && pageWriteMatchesEnvironment(baseline, request.EnvironmentName, request.EnvironmentURI) &&
		strings.TrimSpace(baseline.EditableSchemaUID) != ""
	if known {
		request.conditionalSchemaUID = baseline.EditableSchemaUID
		if pinned {
			pageWritePinnedDivergence(add, request, baseline)
			add(fmt.Sprintf("The checksum pinned for '%s' governs this save; the .clio-pages baseline describes schema %s and is used only to "+
				"decide whether it is refreshed afterwards, never to arm a second conflict check. The pin is still compared with the resolved "+
				"target; if it came from another schema, the save is refused. If the write resolves elsewhere, the baseline is left untouched, "+
				"because get-page always reads the automatically resolved schema and has no redirect of its own. %s",
				request.SchemaName, baseline.EditableSchemaUID, pageWritePinnedMergeAdvice))
			return metaPath, false, strings.Join(warnings, " ")
		}
		request.conditionalChecksum = baseline.Checksum
		request.conditionalSchemaAbsent = !baseline.EditableSchemaExists
		add(fmt.Sprintf("target-package-uid / target-schema-uid were supplied for '%s', so the .clio-pages baseline is applied only if the "+
			"write resolves to the schema it describes (%s); if it resolves elsewhere, the write proceeds unchecked, because get-page always "+
			"reads the automatically resolved schema and has no redirect of its own.", request.SchemaName, baseline.EditableSchemaUID))
		return metaPath, false, strings.Join(warnings, " ")
	}
	if pinned {
		add(fmt.Sprintf("The checksum pinned for '%s' governs this save but could not be corroborated locally: the redirect sends the "+
			"write to a schema the .clio-pages baseline does not describe, because get-page always reads the automatically resolved schema "+
			"and has no redirect of its own. The pin is still compared with the resolved target; if it came from another schema, the save "+
			"is refused. %s", request.SchemaName, pageWritePinnedMergeAdvice))
		return "", false, strings.Join(warnings, " ")
	}
	add(fmt.Sprintf("External-modification detection did not run for this save of '%s': target-package-uid / target-schema-uid redirect "+
		"the write to a schema the .clio-pages baseline does not describe, because get-page always reads the automatically resolved "+
		"schema and has no redirect of its own. The write proceeds unchecked.", request.SchemaName))
	return metaPath, false, strings.Join(warnings, " ")
}

// pageWriteRefreshOrDrop is PageBaselineGuard.RefreshOrDrop: the post-save checksum replaces the baseline,
// or the baseline goes when that checksum could not be read.
func pageWriteRefreshOrDrop(metaPath string, request *PageUpdateRequest, response *PageUpdateResponse) string {
	if strings.TrimSpace(response.NewChecksum) == "" {
		return pageWriteDeleteBaseline(metaPath)
	}
	return pageWriteRefreshBaseline(metaPath, &pageWriteBaseline{SchemaName: request.SchemaName,
		EnvironmentName: strings.TrimSpace(request.EnvironmentName), EnvironmentURI: strings.TrimSpace(request.EnvironmentURI),
		EditableSchemaExists: true, EditableSchemaUID: response.SavedSchemaUID, Checksum: response.NewChecksum,
		ModifiedOn: response.NewModifiedOn, CapturedAt: pageWriteRoundTripTime(time.Now())})
}

// pageWriteRoundTripTime is .NET's DateTime.UtcNow.ToString("o").
func pageWriteRoundTripTime(now time.Time) string {
	return now.UTC().Format("2006-01-02T15:04:05.0000000Z")
}
