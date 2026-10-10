package creatio

// execute-sql-script: clio's SqlScriptCommand over SqlScriptExecutor. The script goes to ClioGate's
// rest/CreatioApiGateway/ExecuteSqlScript; the answer (rows as a JSON array, an affected-row count, or an
// "ExecuteSQL ERROR: " text) is rendered the way the command renders it: a ConsoleTables table (40 characters per
// displayed cell line), the raw JSON, or a CSV/XLSX file, plus "Results saved to" and "Done" log lines.

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"
)

const (
	dataWriteSQLRoute            = "rest/CreatioApiGateway/ExecuteSqlScript"
	dataWriteSQLErrorPrefix      = "ExecuteSQL ERROR: "
	dataWriteSQLMinClioGate      = "2.0.0.41"
	dataWriteSQLTimeout          = 24 * time.Hour
	dataWriteConsoleTableMaxCell = 40
)

// DataWriteSQLRequest is ExecuteSqlScriptOptions as the MCP tool fills it. Destination is nil when the caller
// passed no destination-path (clio's null), and points at "" for an empty one.
type DataWriteSQLRequest struct {
	Script      string
	File        string
	View        string
	Destination *string
	Silent      bool
}

// DataWriteSQLRequirement is the [RequiresPackage("cliogate", "2.0.0.41")] gate clio checks after resolving the
// environment and before running the command; "" when it is met.
func (c *Client) DataWriteSQLRequirement(ctx context.Context) string {
	version, found, err := c.lowestClioGateVersion(ctx)
	if err == nil && found && describeCompareVersions(version, dataWriteSQLMinClioGate) >= 0 {
		return ""
	}
	return "To use this command, you need to install the cliogate package version " + dataWriteSQLMinClioGate +
		" or higher. Install or update the package in the target environment and retry.\n" +
		"Run 'clio install-gate -e <environment>' (or call the install-gate MCP tool) to install/update cliogate."
}

// ExecuteSQLScript is SqlScriptCommand.Execute.
func (c *Client) ExecuteSQLScript(ctx context.Context, request DataWriteSQLRequest) CommandResult {
	lines := []LogMessage{}
	fail := func(message string) CommandResult {
		return CommandResult{ExitCode: 1, Messages: append(lines, LogMessage{MessageType: "Error", Value: message})}
	}
	if strings.EqualFold(request.View, "csv") && (request.Destination == nil || strings.TrimSpace(*request.Destination) == "") {
		return fail("-d/--destination-path is required when -v csv is used.")
	}
	script := request.Script
	if script == "" {
		content, err := os.ReadFile(request.File)
		if err != nil {
			return fail(dataWriteFileError(request.File, err))
		}
		text := strings.TrimPrefix(string(content), "\uFEFF")
		if !request.Silent {
			lines = append(lines, LogMessage{MessageType: "None", Value: text})
		}
		script = strings.ReplaceAll(text, dataWriteNewline(), "|nl|")
	}
	body := toJNode(orderedFields{{"script", script}}).newtonsoftJSON()
	payload, err := c.callService(ctx, serviceCall{Route: dataWriteSQLRoute, Body: body, Timeout: dataWriteSQLTimeout, Label: "ClioGate"})
	if err != nil {
		return fail(err.Error())
	}
	response := string(payload)
	if strings.HasPrefix(strings.TrimLeft(response, " \t\r\n"), "\"") {
		var unquoted string
		if err := json.Unmarshal([]byte(strings.TrimSpace(response)), &unquoted); err != nil {
			return fail(err.Error())
		}
		response = unquoted
	}
	if strings.HasPrefix(response, dataWriteSQLErrorPrefix) {
		return fail(response[len(dataWriteSQLErrorPrefix):])
	}
	result, err := dataWriteSQLRender(response, strings.ToLower(request.View), request.Destination)
	if err != nil {
		return fail(err.Error())
	}
	if request.Destination != nil {
		full, err := filepath.Abs(*request.Destination)
		if err != nil {
			return fail(err.Error())
		}
		lines = append(lines, LogMessage{MessageType: "Info", Value: "Results saved to: " + full})
	}
	if !request.Silent {
		lines = append(lines, LogMessage{MessageType: "None", Value: result})
	}
	lines = append(lines, LogMessage{MessageType: "Info", Value: "Done"})
	return CommandResult{ExitCode: 0, Messages: lines}
}

// dataWriteFileError is the message of .NET's FileNotFoundException / DirectoryNotFoundException for a read.
func dataWriteFileError(path string, err error) string {
	full, _ := filepath.Abs(path)
	if errors.Is(err, fs.ErrNotExist) {
		if _, statErr := os.Stat(filepath.Dir(full)); statErr != nil {
			return fmt.Sprintf("Could not find a part of the path '%s'.", full)
		}
		return fmt.Sprintf("Could not find file '%s'.", full)
	}
	if errors.Is(err, fs.ErrPermission) {
		return fmt.Sprintf("Access to the path '%s' is denied.", full)
	}
	return err.Error()
}

func dataWriteNewline() string {
	if os.PathSeparator == '\\' {
		return "\r\n"
	}
	return "\n"
}

// dataWriteSQLRender is GetSqlScriptResult.
func dataWriteSQLRender(response, view string, destination *string) (string, error) {
	if view == "json" {
		node, err := parseJNode([]byte(response))
		if err != nil {
			return "", fmt.Errorf("Unexpected character encountered while parsing value: %s. Path '', line 0, position 0.", dataWriteFirstChar(response))
		}
		if node.kind != jkArray && node.kind != jkInteger {
			return "", errors.New("SQL response must contain a JSON row array or an affected-row count.")
		}
		if destination != nil {
			if err := dataWriteWriteFile(*destination, []byte(response)); err != nil {
				return "", err
			}
		}
		return response, nil
	}
	if count, ok := dataWriteParseInt32(response); ok {
		if destination != nil {
			return "", errors.New("Use -v json to export an affected-row count.")
		}
		return fmt.Sprintf("(%d rows affected)", count), nil
	}
	table, err := dataWriteParseDataTable(response)
	if err != nil {
		return "", err
	}
	formatted := ""
	if len(table.rows) > 0 {
		formatted = dataWriteConsoleTable(table)
	}
	switch view {
	case "table":
		if destination != nil {
			if err := dataWriteWriteFile(*destination, []byte(formatted)); err != nil {
				return "", err
			}
		}
	case "csv":
		if err := dataWriteWriteFile(*destination, []byte(dataWriteCSV(table))); err != nil {
			return "", err
		}
	case "xlsx":
		content, err := dataWriteXLSX(table)
		if err != nil {
			return "", err
		}
		if err := dataWriteWriteFile(*destination, content); err != nil {
			return "", err
		}
	}
	return formatted, nil
}

func dataWriteFirstChar(text string) string {
	trimmed := strings.TrimLeft(text, " \t\r\n")
	if trimmed == "" {
		return ""
	}
	return string([]rune(trimmed)[:1])
}

func dataWriteWriteFile(path string, content []byte) error {
	if path == "" {
		return errors.New("The value cannot be an empty string. (Parameter 'path')")
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		return errors.New(dataWriteFileError(path, err))
	}
	return nil
}

// dataWriteParseInt32 is int.TryParse with the invariant integer style: optional surrounding white space and
// a leading sign.
func dataWriteParseInt32(text string) (int, bool) {
	value, err := strconv.ParseInt(strings.Trim(text, " \t\n\v\f\r"), 10, 32)
	return int(value), err == nil
}

// dataWriteTable is the DataTable Newtonsoft's DataTableConverter builds: columns in first-seen order, every
// cell rendered as the CLR value's ToString() (DBNull as "").
type dataWriteTable struct {
	columns []string
	rows    [][]string
}

func dataWriteParseDataTable(response string) (dataWriteTable, error) {
	node, err := parseJNode([]byte(response))
	if err != nil {
		return dataWriteTable{}, fmt.Errorf("Unexpected character encountered while parsing value: %s. Path '', line 0, position 0.", dataWriteFirstChar(response))
	}
	if !node.isArray() {
		return dataWriteTable{}, errors.New("Unexpected JSON token when reading DataTable. Expected StartArray, got " + dataWriteNewtonsoftToken(node) + ". Path '', line 1, position 1.")
	}
	table := dataWriteTable{}
	index := map[string]int{}
	for _, item := range node.items {
		if !item.isObject() {
			return dataWriteTable{}, errors.New("Unexpected JSON token when reading DataTable: " + dataWriteNewtonsoftToken(item) + ". Path '[0]'.")
		}
		row := make([]string, len(table.columns))
		for _, key := range item.keys {
			position, ok := index[key]
			if !ok {
				position = len(table.columns)
				index[key] = position
				table.columns = append(table.columns, key)
				for i := range table.rows {
					table.rows[i] = append(table.rows[i], "")
				}
				row = append(row, "")
			}
			row[position] = dataWriteCellText(item.props[key])
		}
		table.rows = append(table.rows, row)
	}
	return table, nil
}

func dataWriteNewtonsoftToken(node *jnode) string {
	switch node.kind {
	case jkObject:
		return "StartObject"
	case jkArray:
		return "StartArray"
	case jkString:
		return "String"
	case jkInteger:
		return "Integer"
	case jkFloat:
		return "Float"
	case jkBool:
		return "Boolean"
	}
	return "Null"
}

var dataWriteISODate = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d{1,7})?(Z|[+-]\d{2}:\d{2})?$`)

// dataWriteCellText is a DataTable cell's ToString(): a long or double in .NET's invariant text, a bool as
// True/False, an ISO date string (which Newtonsoft reads as a DateTime) in the process culture's general
// date-time pattern, other strings as they are, a null as "".
func dataWriteCellText(node *jnode) string {
	switch node.kind {
	case jkNull:
		return ""
	case jkBool:
		if node.flag {
			return "True"
		}
		return "False"
	case jkInteger:
		return node.text
	case jkFloat:
		value, err := strconv.ParseFloat(node.text, 64)
		if err != nil {
			return node.text
		}
		return dataWriteDoubleText(value)
	case jkString:
		if dataWriteISODate.MatchString(node.text) {
			if text, ok := dataWriteDateText(node.text); ok {
				return text
			}
		}
		return node.text
	}
	return string(node.stjJSON())
}

// dataWriteDateText renders an ISO date as DateTime.ToString() does in the invariant culture
// ("MM/dd/yyyy HH:mm:ss"). clio uses the process culture, which on macOS follows the system locale (a
// recorded gap, as for the decimal separator of numbers). A value with an offset is converted to local time, as Newtonsoft's RoundtripKind does.
func dataWriteDateText(text string) (string, bool) {
	layouts := []string{"2006-01-02T15:04:05.9999999Z07:00", "2006-01-02T15:04:05.9999999"}
	var parsed time.Time
	var err error
	for _, layout := range layouts {
		if parsed, err = time.Parse(layout, text); err == nil {
			break
		}
	}
	if err != nil {
		return "", false
	}
	if !strings.HasSuffix(text, "Z") && (strings.Contains(text[19:], "+") || strings.Contains(text[19:], "-")) {
		parsed = parsed.In(time.Now().Location())
	}
	return parsed.Format("01/02/2006 15:04:05"), true
}

// dataWriteDoubleText is double.ToString() in the invariant culture: the shortest round-trip digits,
// scientific from 1E+15 and below 1E-04 (0.0001 stays fixed, 0.00001 is 1E-05).
func dataWriteDoubleText(value float64) string {
	absolute := math.Abs(value)
	if absolute != 0 && absolute < 1e-4 && absolute >= 1e-5 {
		text := strconv.FormatFloat(value, 'E', -1, 64)
		mantissa, exponent, _ := strings.Cut(text, "E")
		digits := strings.TrimLeft(exponent[1:], "0")
		for len(digits) < 2 {
			digits = "0" + digits
		}
		return mantissa + "E" + exponent[:1] + digits
	}
	return strings.TrimSuffix(dotnetDouble(value), ".0")
}

// ---------------------------------------------------------------------------------------------- ConsoleTables

// dataWriteConsoleTable is ConsoleTables 2.7.0's ConsoleTable.ToString() for a table built from DataTable rows:
// column widths from the unwrapped values, then every cell longer than 40 UTF-16 units wrapped onto
// continuation rows at the last space, a dashed divider between rows, and the " Count: N" footer (N counting
// the continuation rows).
func dataWriteConsoleTable(table dataWriteTable) string {
	columnCount := len(table.columns)
	lengths := make([]int, columnCount)
	for i, column := range table.columns {
		lengths[i] = dataWriteTextWidth(column)
		for _, row := range table.rows {
			if width := dataWriteTextWidth(row[i]); width > lengths[i] {
				lengths[i] = width
			}
		}
	}
	rows := make([][]*string, 0, len(table.rows))
	for _, row := range table.rows {
		cells := make([]*string, columnCount)
		for i := range row {
			value := row[i]
			cells[i] = &value
		}
		rows = append(rows, cells)
	}
	for i := 0; i < len(rows); i++ {
		long := false
		for _, cell := range rows[i] {
			if cell != nil && dataWriteUTF16Len(*cell) > dataWriteConsoleTableMaxCell {
				long = true
				break
			}
		}
		if !long {
			continue
		}
		next := make([]*string, columnCount)
		for j, cell := range rows[i] {
			text := ""
			if cell != nil {
				text = *cell
			}
			units := utf16.Encode([]rune(text))
			if len(units) <= dataWriteConsoleTableMaxCell {
				continue
			}
			head, rest := units[:dataWriteConsoleTableMaxCell], units[dataWriteConsoleTableMaxCell:]
			if head[len(head)-1] != ' ' && head[0] != ' ' {
				if cut := dataWriteLastSpace(head); cut > 0 {
					head, rest = units[:cut], units[cut:]
				}
			}
			restText := dataWriteDotNetTrim(string(utf16.Decode(rest)))
			headText := dataWriteDotNetTrim(string(utf16.Decode(head)))
			next[j] = &restText
			rows[i][j] = &headText
		}
		rows = append(rows[:i+1], append([][]*string{next}, rows[i+1:]...)...)
	}
	format := func(cells []*string, corrected bool) string {
		var builder strings.Builder
		for i := 0; i < columnCount; i++ {
			text := ""
			if cells[i] != nil {
				text = *cells[i]
			}
			width := lengths[i]
			if corrected {
				width = lengths[i] - (dataWriteTextWidth(text) - dataWriteUTF16Len(text))
			}
			builder.WriteString(" | ")
			builder.WriteString(text)
			if pad := width - dataWriteUTF16Len(text); pad > 0 {
				builder.WriteString(strings.Repeat(" ", pad))
			}
		}
		builder.WriteString(" |")
		return builder.String()
	}
	header := make([]*string, columnCount)
	for i := range table.columns {
		header[i] = &table.columns[i]
	}
	longest := 0
	for _, row := range rows {
		if length := dataWriteUTF16Len(format(row, false)); length > longest {
			longest = length
		}
	}
	headerLine := format(header, true)
	if length := dataWriteUTF16Len(headerLine); length > longest {
		longest = length
	}
	divider := " " + strings.Repeat("-", max(longest-1, 0)) + " "
	newline := dataWriteNewline()
	var builder strings.Builder
	builder.WriteString(divider + newline)
	builder.WriteString(headerLine + newline)
	for _, row := range rows {
		builder.WriteString(divider + newline)
		builder.WriteString(format(row, true) + newline)
	}
	builder.WriteString(divider + newline)
	builder.WriteString(newline)
	builder.WriteString(fmt.Sprintf(" Count: %d", len(rows)))
	return builder.String()
}

func dataWriteUTF16Len(text string) int { return len(utf16.Encode([]rune(text))) }

func dataWriteLastSpace(units []uint16) int {
	for i := len(units) - 1; i >= 0; i-- {
		if units[i] == ' ' {
			return i
		}
	}
	return -1
}

// dataWriteDotNetTrim is String.Trim(): leading and trailing char.IsWhiteSpace characters.
func dataWriteDotNetTrim(text string) string {
	return strings.TrimFunc(text, unicode.IsSpace)
}

// dataWriteTextWidth is ConsoleTable.GetTextWidth: the sum of Wcwidth's UnicodeCalculator.GetWidth over the
// UTF-16 units — 0 for NUL and zero-width marks, -1 for other control characters, 2 for East Asian wide and
// full-width characters, 1 otherwise.
func dataWriteTextWidth(text string) int {
	width := 0
	for _, unit := range utf16.Encode([]rune(text)) {
		width += dataWriteUnitWidth(rune(unit))
	}
	return width
}

func dataWriteUnitWidth(r rune) int {
	switch {
	case r == 0:
		return 0
	case r < 32 || (r >= 0x7f && r < 0xa0):
		return -1
	case r >= 0xD800 && r <= 0xDFFF:
		return 1
	case unicode.In(r, unicode.Mn, unicode.Me) || r == 0x200B || (r >= 0x1160 && r <= 0x11FF):
		return 0
	case dataWriteWide(r):
		return 2
	}
	return 1
}

func dataWriteWide(r rune) bool {
	return (r >= 0x1100 && r <= 0x115F) || (r >= 0x2E80 && r <= 0x303E) || (r >= 0x3041 && r <= 0x33FF) ||
		(r >= 0x3400 && r <= 0x4DBF) || (r >= 0x4E00 && r <= 0x9FFF) || (r >= 0xA000 && r <= 0xA4CF) ||
		(r >= 0xAC00 && r <= 0xD7A3) || (r >= 0xF900 && r <= 0xFAFF) || (r >= 0xFE30 && r <= 0xFE4F) ||
		(r >= 0xFF00 && r <= 0xFF60) || (r >= 0xFFE0 && r <= 0xFFE6)
}

// ---------------------------------------------------------------------------------------------- files

// dataWriteCSV is SaveDataTableToCsv: ';' between values, no quoting, the platform newline after each line.
func dataWriteCSV(table dataWriteTable) string {
	newline := dataWriteNewline()
	var builder strings.Builder
	builder.WriteString(strings.Join(table.columns, ";") + newline)
	for _, row := range table.rows {
		builder.WriteString(strings.Join(row, ";") + newline)
	}
	return builder.String()
}

// dataWriteXLSX is SaveDataTableToXlsx: one worksheet "Sheet1", a header row, every cell a string.
func dataWriteXLSX(table dataWriteTable) ([]byte, error) {
	var sheet strings.Builder
	sheet.WriteString(`<?xml version="1.0" encoding="utf-8"?><x:worksheet xmlns:x="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><x:sheetData>`)
	writeRow := func(values []string) {
		sheet.WriteString("<x:row>")
		for _, value := range values {
			sheet.WriteString(`<x:c t="str"><x:v>`)
			sheet.WriteString(dataWriteXMLText(value))
			sheet.WriteString("</x:v></x:c>")
		}
		sheet.WriteString("</x:row>")
	}
	writeRow(table.columns)
	for _, row := range table.rows {
		writeRow(row)
	}
	sheet.WriteString("</x:sheetData></x:worksheet>")
	parts := []struct{ name, content string }{
		{"[Content_Types].xml", `<?xml version="1.0" encoding="utf-8"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
			`<Default Extension="xml" ContentType="application/xml"/><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>` +
			`<Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>` +
			`<Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/></Types>`},
		{"_rels/.rels", `<?xml version="1.0" encoding="utf-8"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
			`<Relationship Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="/xl/workbook.xml" Id="R1"/></Relationships>`},
		{"xl/workbook.xml", `<?xml version="1.0" encoding="utf-8"?><x:workbook xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" ` +
			`xmlns:x="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><x:sheets><x:sheet name="Sheet1" sheetId="1" r:id="R2"/></x:sheets></x:workbook>`},
		{"xl/_rels/workbook.xml.rels", `<?xml version="1.0" encoding="utf-8"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
			`<Relationship Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="/xl/worksheets/sheet1.xml" Id="R2"/></Relationships>`},
		{"xl/worksheets/sheet1.xml", sheet.String()},
	}
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	for _, part := range parts {
		writer, err := archive.Create(part.name)
		if err != nil {
			return nil, err
		}
		if _, err := writer.Write([]byte(part.content)); err != nil {
			return nil, err
		}
	}
	if err := archive.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func dataWriteXMLText(value string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(value)
}
