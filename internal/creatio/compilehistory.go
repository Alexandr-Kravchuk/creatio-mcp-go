package creatio

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// clio 8.1.0.142's CompilationHistoryReader (ENG-102333): the newest CompilationHistory rows with the time each
// was written in UTC. compile-status lists them when this server holds no record of the compile asked about.
//
// DataService returns CreatedOn in the session user's time zone with no offset marker, so the time is converted
// with the offset GetApplicationInfo reports for the same session (applicationInfo.sysValues.userTimezoneOffset).

const (
	compilationHistoryUnreadableAnswer = "The environment did not answer with compilation-history rows."
	compilationHistoryUnreadableOffset = "The environment did not report the session's time-zone offset."

	// CompilationHistoryRowCount is how many rows a not-found compile-status answer lists in full.
	CompilationHistoryRowCount = 10
	// CompilationHistoryFetchCount is how many rows are read, so older failed rows can be reported.
	CompilationHistoryFetchCount = 100
	// CompilationHistoryOlderFailureCount is how many failed rows older than the listed ones are reported.
	CompilationHistoryOlderFailureCount = 5
	// compilationHistoryErrorsPerRow is how many errors of one row an answer carries.
	compilationHistoryErrorsPerRow = 5
	// CompilationHistoryRequestTimeout is the timeout of each of the read's two requests.
	CompilationHistoryRequestTimeout = 10 * time.Second
	// CompilationHistoryReadBudget is the wall-clock bound on the whole read, login included.
	CompilationHistoryReadBudget     = 40 * time.Second
	compilationHistoryMaxProjectName = 64
)

// CompilationHistoryRow is one project of one build.
type CompilationHistoryRow struct {
	ProjectName     *string
	FinishedUTC     time.Time
	DurationSeconds int
	Succeeded       bool
	diagnostics     []pkgWriteDiagnostic
}

// CompilationHistoryReading is the newest rows, newest first, and this host's UTC clock right after the read.
type CompilationHistoryReading struct {
	CheckedUTC time.Time
	Rows       []CompilationHistoryRow
}

// CompileHistoryEntry is clio's CompileHistoryEntry: one row as a compile-status not-found answer carries it.
type CompileHistoryEntry struct {
	ProjectName        *string   `json:"project-name"`
	FinishedUTC        time.Time `json:"-"`
	FinishedSecondsAgo int64     `json:"finished-seconds-ago"`
	DurationSeconds    int       `json:"duration-seconds"`
	Succeeded          bool      `json:"succeeded"`
	ErrorCount         int       `json:"error-count"`
	Errors             []string  `json:"errors"`
}

// MarshalJSON keeps clio's key order and writes finished-utc the way System.Text.Json writes a UTC DateTime.
func (e CompileHistoryEntry) MarshalJSON() ([]byte, error) {
	type ordered struct {
		ProjectName        *string  `json:"project-name"`
		FinishedUTC        string   `json:"finished-utc"`
		FinishedSecondsAgo int64    `json:"finished-seconds-ago"`
		DurationSeconds    int      `json:"duration-seconds"`
		Succeeded          bool     `json:"succeeded"`
		ErrorCount         int      `json:"error-count"`
		Errors             []string `json:"errors"`
	}
	errorsList := e.Errors
	if errorsList == nil {
		errorsList = []string{}
	}
	return json.Marshal(ordered{e.ProjectName, FormatClioUTC(e.FinishedUTC), e.FinishedSecondsAgo, e.DurationSeconds,
		e.Succeeded, e.ErrorCount, errorsList})
}

// FormatClioUTC writes a UTC time the way System.Text.Json writes a DateTime of kind Utc: up to seven
// fractional digits, trailing zeros dropped.
func FormatClioUTC(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.9999999Z")
}

// ReadCompilationHistory is CompilationHistoryReader.ReadLatest. Both requests use this client's session, so
// the offset and the rows come from the same time zone.
func (c *Client) ReadCompilationHistory(ctx context.Context, count int, requestTimeout time.Duration) (*CompilationHistoryReading, error) {
	info, err := c.postCreatioServiceJSON(ctx, applicationInfoRoute, []byte("{}"), requestTimeout, maxResponseBytes)
	if err != nil {
		return nil, err
	}
	offset, err := parseCompilationHistoryOffset(info)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(compilationHistoryQuery(count))
	if err != nil {
		return nil, err
	}
	answer, err := c.postDataServiceJSON(ctx, "SelectQuery", body, requestTimeout, maxResponseBytes)
	if err != nil {
		return nil, err
	}
	rows, err := parseCompilationHistoryRows(answer, offset)
	if err != nil {
		return nil, err
	}
	return &CompilationHistoryReading{CheckedUTC: time.Now().UTC(), Rows: rows}, nil
}

// compilationHistoryQuery is CompilationHistoryReader.BuildSelectQuery: newest first by CreatedOn.
func compilationHistoryQuery(count int) map[string]any {
	column := func(path string, direction, position int) map[string]any {
		return map[string]any{"expression": map[string]any{"expressionType": 0, "columnPath": path},
			"orderDirection": direction, "orderPosition": position, "isVisible": true}
	}
	return map[string]any{"rootSchemaName": "CompilationHistory", "operationType": 0, "allColumns": false,
		"isDistinct": false, "ignoreDisplayValues": false, "rowCount": count, "rowsOffset": -1, "isPageable": false,
		"columns": map[string]any{"items": map[string]any{
			"CreatedOn": column("CreatedOn", 2, 0), "ProjectName": column("ProjectName", 0, -1),
			"Result": column("Result", 0, -1), "DurationInSeconds": column("DurationInSeconds", 0, -1),
			"ErrorsWarnings": column("ErrorsWarnings", 0, -1)}},
		"filters": map[string]any{"filterType": 6, "isEnabled": true, "trimDateTimeParameterToDate": false,
			"logicalOperation": 0, "items": map[string]any{}}}
}

// parseCompilationHistoryOffset is CompilationHistoryReader.ParseSessionOffset.
func parseCompilationHistoryOffset(body []byte) (time.Duration, error) {
	var root struct {
		ApplicationInfo *struct {
			SysValues *struct {
				UserTimezoneOffset json.RawMessage `json:"userTimezoneOffset"`
			} `json:"sysValues"`
		} `json:"applicationInfo"`
	}
	if json.Unmarshal(body, &root) == nil && root.ApplicationInfo != nil && root.ApplicationInfo.SysValues != nil {
		if minutes, err := strconv.ParseInt(string(root.ApplicationInfo.SysValues.UserTimezoneOffset), 10, 32); err == nil {
			return time.Duration(minutes) * time.Minute, nil
		}
	}
	return 0, errors.New(compilationHistoryUnreadableOffset)
}

// compilationHistoryTimeLayouts are the zone-less forms DataService writes CreatedOn in. A time that carries
// its own zone is refused, as clio refuses a DateTime whose Kind is not Unspecified.
var compilationHistoryTimeLayouts = []string{"2006-01-02T15:04:05.9999999", "2006-01-02 15:04:05.9999999"}

// parseCompilationHistoryRows is CompilationHistoryReader.ParseRows: a row without a usable time makes the
// whole answer unreadable rather than being skipped.
func parseCompilationHistoryRows(body []byte, offset time.Duration) ([]CompilationHistoryRow, error) {
	unreadable := errors.New(compilationHistoryUnreadableAnswer)
	var root map[string]json.RawMessage
	if json.Unmarshal(body, &root) != nil || root == nil {
		return nil, unreadable
	}
	if success, ok := root["success"]; ok && string(success) == "false" {
		return nil, unreadable
	}
	var rows []json.RawMessage
	if json.Unmarshal(root["rows"], &rows) != nil || rows == nil {
		return nil, unreadable
	}
	result := make([]CompilationHistoryRow, 0, len(rows))
	for _, raw := range rows {
		var row map[string]json.RawMessage
		if json.Unmarshal(raw, &row) != nil || row == nil {
			return nil, unreadable
		}
		var created string
		if json.Unmarshal(row["CreatedOn"], &created) != nil {
			return nil, unreadable
		}
		var sessionTime time.Time
		parsed := false
		for _, layout := range compilationHistoryTimeLayouts {
			if value, err := time.Parse(layout, created); err == nil {
				sessionTime, parsed = value, true
				break
			}
		}
		if !parsed {
			return nil, unreadable
		}
		entry := CompilationHistoryRow{FinishedUTC: sessionTime.Add(-offset).UTC()}
		if name, ok := compilationHistoryString(row, "ProjectName"); ok {
			entry.ProjectName = &name
		}
		if seconds, err := strconv.ParseInt(string(row["DurationInSeconds"]), 10, 32); err == nil {
			entry.DurationSeconds = int(seconds)
		}
		entry.Succeeded = string(row["Result"]) == "true"
		if errorsWarnings, ok := compilationHistoryString(row, "ErrorsWarnings"); ok {
			entry.diagnostics = pkgWriteHistoryDiagnostics(errorsWarnings)
		}
		result = append(result, entry)
	}
	return result, nil
}

func compilationHistoryString(row map[string]json.RawMessage, key string) (string, bool) {
	var value string
	raw, ok := row[key]
	if !ok || json.Unmarshal(raw, &value) != nil || string(raw) == "null" {
		return "", false
	}
	return value, true
}

// compilationHistoryProjectName is the project-name pattern the platform writes; anything else is fenced.
var compilationHistoryProjectName = regexp.MustCompile(`^[A-Za-z0-9_]+(\.[A-Za-z0-9_]+){0,6}\.csproj$`)

// CompileHistoryEntries is CompileStatusTool.BuildNotFound's split of a reading: the newest rows in full, and
// failed rows from further back.
func CompileHistoryEntries(reading *CompilationHistoryReading) (listed, olderFailures []CompileHistoryEntry) {
	listed, olderFailures = []CompileHistoryEntry{}, []CompileHistoryEntry{}
	for index, row := range reading.Rows {
		switch {
		case index < CompilationHistoryRowCount:
			listed = append(listed, compileHistoryEntry(row, reading.CheckedUTC))
		case !row.Succeeded && len(olderFailures) < CompilationHistoryOlderFailureCount:
			olderFailures = append(olderFailures, compileHistoryEntry(row, reading.CheckedUTC))
		}
	}
	return listed, olderFailures
}

// compileHistoryEntry is CompileStatusTool.ToHistoryEntry.
func compileHistoryEntry(row CompilationHistoryRow, checked time.Time) CompileHistoryEntry {
	errorsOnly := make([]pkgWriteDiagnostic, 0, len(row.diagnostics))
	for _, diagnostic := range row.diagnostics {
		if !diagnostic.IsWarning {
			errorsOnly = append(errorsOnly, diagnostic)
		}
	}
	rendered := []string{}
	plain := func(text string) string { return text }
	for _, diagnostic := range errorsOnly[:min(len(errorsOnly), compilationHistoryErrorsPerRow)] {
		if fenced := DataWriteUntrusted(diagnostic.format(plain, compilationHistoryFileNameOnly)); fenced != nil {
			rendered = append(rendered, *fenced)
		}
	}
	// Two clocks: the environment's wrote the row and this host's read it, so a small skew must not read as a
	// row from the future.
	ago := int64(checked.Sub(row.FinishedUTC) / time.Second)
	if ago < 0 {
		ago = 0
	}
	return CompileHistoryEntry{ProjectName: compilationHistoryRenderProject(row.ProjectName), FinishedUTC: row.FinishedUTC,
		FinishedSecondsAgo: ago, DurationSeconds: row.DurationSeconds, Succeeded: row.Succeeded,
		ErrorCount: len(errorsOnly), Errors: rendered}
}

func compilationHistoryRenderProject(name *string) *string {
	if name == nil || (len([]rune(*name)) <= compilationHistoryMaxProjectName && compilationHistoryProjectName.MatchString(*name)) {
		return name
	}
	return DataWriteUntrusted(*name)
}

// compilationHistoryFileNameOnly cuts the environment's path to its file name, Windows separators included.
func compilationHistoryFileNameOnly(path string) string {
	if strings.TrimSpace(path) == "" {
		return path
	}
	return path[strings.LastIndexAny(path, `/\`)+1:]
}
