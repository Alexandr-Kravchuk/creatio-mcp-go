package creatio

// add-custom-logging: clio's CustomLoggingConfigurator. It edits the NLog files of a registered LOCAL Creatio
// installation (EnvironmentPath): a <logger> for the package's generated Constants.LoggerName in nlog.config,
// placed before the first logger, and a File <target> in nlog.targets.config, placed before the first target.
// Both files keep their encoding (a UTF-8 byte-order mark only when they had one), line endings and
// unrelated content; a change is backed up, verified by re-reading, and rolled back when that fails.

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	pkgWriteNLogConfig        = "nlog.config"
	pkgWriteNLogTargetsConfig = "nlog.targets.config"
	pkgWriteNetFrameworkApp   = "Terrasoft.WebApp"
	pkgWriteXsiNamespace      = "http://www.w3.org/2001/XMLSchema-instance"
	pkgWriteDefaultLayout     = "${DefaultLayout}"
	pkgWriteTodayLogPath      = "${TodayLogPath}"
)

var (
	pkgWriteSafePackageName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]*$`)
	pkgWriteSafeFileName    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
	pkgWriteLoggerConstant  = regexp.MustCompile(`(?m)^[ \t\f\v]*(?:internal\s+)?const\s+string\s+LoggerName\s*=\s*"([A-Za-z_][A-Za-z0-9_.-]*)"\s*;[ \t\f\v]*\r?$`)
	pkgWriteMinLevels       = []string{"Trace", "Debug", "Info", "Warn", "Error", "Fatal", "Off"}
	pkgWriteReservedNames   = map[string]bool{"CON": true, "PRN": true, "AUX": true, "NUL": true,
		"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true, "COM6": true, "COM7": true, "COM8": true, "COM9": true,
		"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true, "LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true}
)

// CustomLoggingResult is CustomLoggingConfigurationResult.
type CustomLoggingResult struct {
	Success                                       bool
	Changed                                       bool
	LoggerName, TargetName, LogPath, ErrorMessage string
}

// CustomLoggingSupportedLevel is CustomLoggingConfigurator.IsSupportedMinLevel.
func CustomLoggingSupportedLevel(value string) bool {
	for _, level := range pkgWriteMinLevels {
		if strings.EqualFold(level, strings.TrimSpace(value)) {
			return true
		}
	}
	return false
}

// CustomLoggingSafePackage is CustomLoggingConfigurator.IsSafePackageName.
func CustomLoggingSafePackage(value string) bool {
	return strings.TrimSpace(value) != "" && pkgWriteSafePackageName.MatchString(value)
}

// CustomLoggingSafeFile is CustomLoggingConfigurator.IsSafeFileName (blank is allowed: the default applies).
func CustomLoggingSafeFile(value string) bool {
	if strings.TrimSpace(value) == "" {
		return true
	}
	return value != "." && value != ".." && pkgWriteSafeFileName.MatchString(value) && !pkgWriteReservedNames[strings.ToUpper(strings.Split(value, ".")[0])]
}

func pkgWriteLoggingFailure(message string) CustomLoggingResult {
	return CustomLoggingResult{ErrorMessage: message}
}

// ConfigureCustomLogging is CustomLoggingConfigurator.Configure.
func ConfigureCustomLogging(environmentPath, packageName, minLevel, fileName string) CustomLoggingResult {
	switch {
	case !CustomLoggingSafePackage(packageName):
		return pkgWriteLoggingFailure("Package name may contain only letters, digits, underscore, dot, and hyphen, and must start with a letter or underscore.")
	case !CustomLoggingSupportedLevel(minLevel):
		return pkgWriteLoggingFailure("Min level must be one of: Trace, Debug, Info, Warn, Error, Fatal, Off.")
	case !CustomLoggingSafeFile(fileName):
		return pkgWriteLoggingFailure("File name must be a simple file name without directories or NLog layout expressions.")
	}
	root, err := pkgWriteApplicationRoot(environmentPath)
	if err != nil {
		return pkgWriteLoggingFailure(err.Error())
	}
	constantsPath := filepath.Join(root, "Terrasoft.Configuration", "Pkg", packageName, "Files", "src", "cs", "Constants.cs")
	constants, err := os.ReadFile(constantsPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return pkgWriteLoggingFailure(fmt.Sprintf("Package '%s' does not contain generated logger constants at '%s'.", packageName, constantsPath))
		}
		return pkgWriteLoggingFailure(err.Error())
	}
	matches := pkgWriteLoggerConstant.FindAllStringSubmatch(string(bytes.TrimPrefix(constants, []byte{0xEF, 0xBB, 0xBF})), -1)
	if len(matches) != 1 {
		return pkgWriteLoggingFailure(fmt.Sprintf("Expected exactly one generated string constant named LoggerName in '%s', found %d.", constantsPath, len(matches)))
	}
	loggerName := matches[0][1]
	baseName := loggerName
	if strings.HasSuffix(loggerName, "App") && len(loggerName) > 3 {
		baseName = loggerName[:len(loggerName)-3]
	}
	targetName := strings.ToLower(baseName[:1]) + baseName[1:] + "Appender"
	effectiveFile := strings.TrimSpace(fileName)
	if effectiveFile == "" {
		effectiveFile = baseName + ".log"
	}
	if !strings.HasSuffix(strings.ToLower(effectiveFile), ".log") {
		effectiveFile += ".log"
	}
	if !CustomLoggingSafeFile(effectiveFile) {
		return pkgWriteLoggingFailure(fmt.Sprintf("Resolved log file name '%s' is reserved or unsafe.", effectiveFile))
	}
	level := ""
	for _, candidate := range pkgWriteMinLevels {
		if strings.EqualFold(candidate, strings.TrimSpace(minLevel)) {
			level = candidate
		}
	}
	logPath := pkgWriteTodayLogPath + "/" + effectiveFile
	rulesPath, targetsPath := filepath.Join(root, pkgWriteNLogConfig), filepath.Join(root, pkgWriteNLogTargetsConfig)
	rulesText, err := pkgWriteReadText(rulesPath)
	if err != nil {
		return pkgWriteLoggingFailure(err.Error())
	}
	rules, err := pkgWriteRulesEdit(rulesPath, rulesText, loggerName, targetName, level)
	if err != nil {
		return pkgWriteLoggingFailure(err.Error())
	}
	targetsText, err := pkgWriteReadText(targetsPath)
	if err != nil {
		return pkgWriteLoggingFailure(err.Error())
	}
	targets, err := pkgWriteTargetsEdit(targetsPath, targetsText, targetName, logPath)
	if err != nil {
		return pkgWriteLoggingFailure(err.Error())
	}
	if message := pkgWriteApplyWithBackups([]pkgWriteFileEdit{targets, rules}, func() error {
		return pkgWriteVerifyLogging(rulesPath, targetsPath, loggerName, targetName, level, logPath)
	}); message != "" {
		return CustomLoggingResult{ErrorMessage: message, LoggerName: loggerName, TargetName: targetName, LogPath: logPath}
	}
	return CustomLoggingResult{Success: true, Changed: rules.changed || targets.changed, LoggerName: loggerName, TargetName: targetName, LogPath: logPath}
}

func pkgWriteApplicationRoot(environmentPath string) (string, error) {
	if strings.TrimSpace(environmentPath) == "" {
		return "", errors.New("The registered environment does not define EnvironmentPath.")
	}
	root, err := filepath.Abs(environmentPath)
	if err != nil {
		return "", err
	}
	hasFiles := func(path string) bool {
		rules, rulesErr := os.Stat(filepath.Join(path, pkgWriteNLogConfig))
		targets, targetsErr := os.Stat(filepath.Join(path, pkgWriteNLogTargetsConfig))
		return rulesErr == nil && !rules.IsDir() && targetsErr == nil && !targets.IsDir()
	}
	if hasFiles(root) {
		return root, nil
	}
	if framework := filepath.Join(root, pkgWriteNetFrameworkApp); hasFiles(framework) {
		return framework, nil
	}
	return "", fmt.Errorf("Could not find both '%s' and '%s' under EnvironmentPath '%s' or its '%s' folder.",
		pkgWriteNLogConfig, pkgWriteNLogTargetsConfig, root, pkgWriteNetFrameworkApp)
}

// pkgWriteText is a configuration file's text and whether it carried a UTF-8 byte-order mark.
type pkgWriteText struct {
	content string
	bom     bool
}

func pkgWriteReadText(path string) (pkgWriteText, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return pkgWriteText{}, err
	}
	bom := bytes.HasPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	if bom {
		data = data[3:]
	}
	if !utf8.Valid(data) {
		return pkgWriteText{}, fmt.Errorf("Unable to translate bytes in '%s' to UTF-8.", path)
	}
	return pkgWriteText{content: string(data), bom: bom}, nil
}

type pkgWriteFileEdit struct {
	path    string
	updated string
	bom     bool
	changed bool
}

// pkgWriteXMLElement is one element of a parsed NLog file: its local name, attributes, the byte offset of its
// '<', and its child elements.
type pkgWriteXMLElement struct {
	tag      string
	attrs    []xml.Attr
	offset   int64
	children []*pkgWriteXMLElement
}

func (e *pkgWriteXMLElement) attr(name string) (string, bool) {
	for _, attribute := range e.attrs {
		if attribute.Name.Space == "" && xmlLocalName(attribute.Name) == name {
			return attribute.Value, true
		}
	}
	return "", false
}

func (e *pkgWriteXMLElement) nsAttr(space, name string) (string, bool) {
	for _, attribute := range e.attrs {
		if attribute.Name.Space == space && xmlLocalName(attribute.Name) == name {
			return attribute.Value, true
		}
	}
	return "", false
}

func (e *pkgWriteXMLElement) elements(tag string) []*pkgWriteXMLElement {
	var found []*pkgWriteXMLElement
	for _, child := range e.children {
		if child.tag == tag {
			found = append(found, child)
		}
	}
	return found
}

// pkgWriteParseNLog is Parse: the document's root, which must be <nlog>.
func pkgWriteParseNLog(path, content string) (*pkgWriteXMLElement, error) {
	decoder := xml.NewDecoder(strings.NewReader(content))
	var root *pkgWriteXMLElement
	var stack []*pkgWriteXMLElement
	for {
		offset := decoder.InputOffset()
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch typed := token.(type) {
		case xml.StartElement:
			element := &pkgWriteXMLElement{tag: xmlLocalName(typed.Name), attrs: typed.Attr, offset: offset}
			if len(stack) > 0 {
				parent := stack[len(stack)-1]
				parent.children = append(parent.children, element)
			} else if root == nil {
				root = element
			}
			stack = append(stack, element)
		case xml.EndElement:
			stack = stack[:len(stack)-1]
		}
	}
	if root == nil || root.tag != "nlog" {
		return nil, fmt.Errorf("'%s' does not contain an nlog root element.", path)
	}
	return root, nil
}

func pkgWriteSingleChild(root *pkgWriteXMLElement, name, path string) (*pkgWriteXMLElement, error) {
	matches := root.elements(name)
	if len(matches) != 1 {
		return nil, fmt.Errorf("Expected one '%s' element in '%s', found %d.", name, path, len(matches))
	}
	return matches[0], nil
}

func pkgWriteLoggerMatches(element *pkgWriteXMLElement, loggerName, targetName, level string) bool {
	name, _ := element.attr("name")
	writeTo, _ := element.attr("writeTo")
	minLevel, _ := element.attr("minlevel")
	final, _ := element.attr("final")
	return name == loggerName && writeTo == targetName && strings.EqualFold(minLevel, level) && strings.EqualFold(final, "true")
}

func pkgWriteTargetMatches(element *pkgWriteXMLElement, targetName, logPath string) bool {
	name, _ := element.attr("name")
	kind, _ := element.nsAttr(pkgWriteXsiNamespace, "type")
	layout, _ := element.attr("layout")
	file, _ := element.attr("fileName")
	return name == targetName && strings.EqualFold(kind, "File") && layout == pkgWriteDefaultLayout && file == logPath
}

func pkgWriteRulesEdit(path string, text pkgWriteText, loggerName, targetName, level string) (pkgWriteFileEdit, error) {
	root, err := pkgWriteParseNLog(path, text.content)
	if err != nil {
		return pkgWriteFileEdit{}, err
	}
	rules, err := pkgWriteSingleChild(root, "rules", path)
	if err != nil {
		return pkgWriteFileEdit{}, err
	}
	loggers := rules.elements("logger")
	defaultIndex, existingIndex := -1, -1
	existing := 0
	for index, logger := range loggers {
		name, _ := logger.attr("name")
		if name == "*" && defaultIndex < 0 {
			defaultIndex = index
		}
		if strings.EqualFold(name, loggerName) {
			existing++
			existingIndex = index
		}
	}
	if defaultIndex < 0 {
		return pkgWriteFileEdit{}, fmt.Errorf("Could not find the default logger (name='*') in '%s'.", path)
	}
	if existing > 1 || existing == 1 && !pkgWriteLoggerMatches(loggers[existingIndex], loggerName, targetName, level) {
		return pkgWriteFileEdit{}, fmt.Errorf("Logger '%s' already exists in '%s' with conflicting or duplicate entries; no files were changed.", loggerName, path)
	}
	if existing == 1 {
		if existingIndex > defaultIndex {
			return pkgWriteFileEdit{}, fmt.Errorf("Logger '%s' exists after the default catch-all logger in '%s'.", loggerName, path)
		}
		return pkgWriteFileEdit{path: path, updated: text.content, bom: text.bom}, nil
	}
	element := fmt.Sprintf(`<logger name="%s" writeTo="%s" minlevel="%s" final="true" />`, loggerName, targetName, level)
	updated, err := pkgWriteInsertBefore(text.content, loggers[0].offset, element)
	if err != nil {
		return pkgWriteFileEdit{}, err
	}
	return pkgWriteFileEdit{path: path, updated: updated, bom: text.bom, changed: true}, nil
}

func pkgWriteTargetsEdit(path string, text pkgWriteText, targetName, logPath string) (pkgWriteFileEdit, error) {
	root, err := pkgWriteParseNLog(path, text.content)
	if err != nil {
		return pkgWriteFileEdit{}, err
	}
	for _, name := range []string{"DefaultLayout", "TodayLogPath"} {
		count, value := 0, ""
		for _, variable := range root.elements("variable") {
			if variableName, _ := variable.attr("name"); variableName == name {
				count++
				value, _ = variable.attr("value")
			}
		}
		if count != 1 || strings.TrimSpace(value) == "" {
			return pkgWriteFileEdit{}, fmt.Errorf("Expected one non-empty '%s' variable in '%s'.", name, path)
		}
	}
	targets, err := pkgWriteSingleChild(root, "targets", path)
	if err != nil {
		return pkgWriteFileEdit{}, err
	}
	var existing []*pkgWriteXMLElement
	for _, target := range targets.elements("target") {
		if name, _ := target.attr("name"); strings.EqualFold(name, targetName) {
			existing = append(existing, target)
		}
	}
	if len(existing) > 1 || len(existing) == 1 && !pkgWriteTargetMatches(existing[0], targetName, logPath) {
		return pkgWriteFileEdit{}, fmt.Errorf("Target '%s' already exists in '%s' with conflicting or duplicate entries; no files were changed.", targetName, path)
	}
	if len(existing) == 1 {
		return pkgWriteFileEdit{path: path, updated: text.content, bom: text.bom}, nil
	}
	anchors := targets.elements("target")
	if len(anchors) == 0 {
		return pkgWriteFileEdit{}, fmt.Errorf("Could not find an existing target in '%s'.", path)
	}
	prefix := ""
	for _, attribute := range root.attrs {
		if attribute.Name.Space == "xmlns" && attribute.Value == pkgWriteXsiNamespace {
			prefix = xmlLocalName(attribute.Name)
			break
		}
	}
	declaration := ""
	if strings.TrimSpace(prefix) == "" {
		declaration, prefix = ` xmlns:xsi="`+pkgWriteXsiNamespace+`"`, "xsi"
	}
	element := fmt.Sprintf(`<target name="%s"%s %s:type="File" layout="%s" fileName="%s" />`, targetName, declaration, prefix, pkgWriteDefaultLayout, logPath)
	updated, err := pkgWriteInsertBefore(text.content, anchors[0].offset, element)
	if err != nil {
		return pkgWriteFileEdit{}, err
	}
	return pkgWriteFileEdit{path: path, updated: updated, bom: text.bom, changed: true}, nil
}

// pkgWriteInsertBefore is InsertBeforeElement: the new element goes where the anchor's '<' is; when only
// white space precedes the anchor on its line, the anchor moves to a new line with the same indentation.
func pkgWriteInsertBefore(content string, offset int64, element string) (string, error) {
	index := int(offset)
	if index < 0 || index > len(content) || !strings.HasPrefix(content[index:], "<") {
		return "", errors.New("Could not determine the XML insertion position.")
	}
	lineStart := strings.LastIndex(content[:index], "\n") + 1
	indentation := content[lineStart:index]
	newline := "\n"
	if strings.Contains(content, "\r\n") {
		newline = "\r\n"
	}
	suffix := ""
	if strings.IndexFunc(indentation, func(r rune) bool { return !unicode.IsSpace(r) }) < 0 {
		suffix = newline + indentation
	}
	updated := content[:index] + element + suffix + content[index:]
	if _, err := pkgWriteParseNLog("updated NLog configuration", updated); err != nil {
		return "", err
	}
	return updated, nil
}

func pkgWriteWriteText(path, content string, bom bool) error {
	data := []byte(content)
	if bom {
		data = append([]byte{0xEF, 0xBB, 0xBF}, data...)
	}
	return os.WriteFile(path, data, 0o644)
}

// pkgWriteApplyWithBackups is ApplyWithBackups: "" when nothing changed or the change was verified, otherwise
// the failure, after the originals were restored.
func pkgWriteApplyWithBackups(edits []pkgWriteFileEdit, verify func() error) string {
	type backup struct{ original, copy string }
	var changed []pkgWriteFileEdit
	for _, edit := range edits {
		if edit.changed {
			changed = append(changed, edit)
		}
	}
	if len(changed) == 0 {
		return ""
	}
	var backups []backup
	failure := func(err error) string {
		var restoreErrors []string
		for index := len(backups) - 1; index >= 0; index-- {
			data, readErr := os.ReadFile(backups[index].copy)
			if readErr == nil {
				readErr = os.WriteFile(backups[index].original, data, 0o644)
			}
			if readErr != nil {
				restoreErrors = append(restoreErrors, fmt.Sprintf("Could not restore '%s' from '%s': %s", backups[index].original, backups[index].copy, readErr.Error()))
				continue
			}
			_ = os.Remove(backups[index].copy)
		}
		if len(restoreErrors) == 0 {
			return fmt.Sprintf("Failed to update NLog configuration: %s. Original files were restored.", err.Error())
		}
		return fmt.Sprintf("Failed to update NLog configuration: %s. Rollback also failed: %s", err.Error(), strings.Join(restoreErrors, "; "))
	}
	for _, edit := range changed {
		random := make([]byte, 16)
		_, _ = rand.Read(random)
		copyPath := edit.path + ".clio-" + hex.EncodeToString(random) + ".bak"
		data, err := os.ReadFile(edit.path)
		if err == nil {
			err = os.WriteFile(copyPath, data, 0o644)
		}
		if err != nil {
			return failure(err)
		}
		backups = append(backups, backup{edit.path, copyPath})
	}
	for _, edit := range changed {
		if err := pkgWriteWriteText(edit.path, edit.updated, edit.bom); err != nil {
			return failure(err)
		}
	}
	if err := verify(); err != nil {
		return failure(err)
	}
	for _, item := range backups {
		_ = os.Remove(item.copy)
	}
	return ""
}

// pkgWriteVerifyLogging is VerifySavedConfiguration.
func pkgWriteVerifyLogging(rulesPath, targetsPath, loggerName, targetName, level, logPath string) error {
	missing := errors.New("The saved NLog configuration does not contain the expected logger and target.")
	read := func(path, child string) (*pkgWriteXMLElement, error) {
		text, err := pkgWriteReadText(path)
		if err != nil {
			return nil, err
		}
		root, err := pkgWriteParseNLog(path, text.content)
		if err != nil {
			return nil, err
		}
		return pkgWriteSingleChild(root, child, path)
	}
	rules, err := read(rulesPath, "rules")
	if err != nil {
		return err
	}
	targets, err := read(targetsPath, "targets")
	if err != nil {
		return err
	}
	var loggers, files []*pkgWriteXMLElement
	for _, logger := range rules.elements("logger") {
		if name, _ := logger.attr("name"); name == loggerName {
			loggers = append(loggers, logger)
		}
	}
	for _, target := range targets.elements("target") {
		if name, _ := target.attr("name"); name == targetName {
			files = append(files, target)
		}
	}
	if len(loggers) != 1 || !pkgWriteLoggerMatches(loggers[0], loggerName, targetName, level) ||
		len(files) != 1 || !pkgWriteTargetMatches(files[0], targetName, logPath) {
		return missing
	}
	return nil
}
