package creatio

// install-application: clio's InstallApplicationCommand over ApplicationInstaller / BasePackageInstaller —
// pack a package folder into clio's .gz format (PackageArchiver, PackageUtilities, CompressionUtilities),
// upload it in 1 MB chunks (Creatio.Client UploadFile), back it up, install it with AppInstallerService
// InstallAppFromFile while the installation log is followed every 3 s, judge the outcome (InstallLogAnalyzer),
// write the report file, and in developer mode unlock packages and restart the application.

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
)

// AppInstallRequest is clio's InstallApplicationOptions plus what the installer reads from the environment.
type AppInstallRequest struct {
	// Name is the package file (.gz) or package folder; empty means the current directory, as in clio.
	Name                   string
	ReportPath             string
	CheckCompilationErrors *bool
	// EnvironmentURI is EnvironmentSettings.Uri, which the installer logs.
	EnvironmentURI string
	// DeveloperMode is EnvironmentSettings.DeveloperModeEnabled.
	DeveloperMode bool
}

// appWriteInstallTimings are the installer's waits; tests shorten them.
var appWriteInstallTimings = struct {
	logInterval, installTimeout, uploadTimeout time.Duration
	chunk                                      int
}{logInterval: 3 * time.Second, installTimeout: 6 * time.Hour, uploadTimeout: 100 * time.Second, chunk: 1 << 20}

// appWriteLog is the command's execution log; the log listener writes to it concurrently.
type appWriteLog struct {
	mu       sync.Mutex
	messages []LogMessage
}

func (l *appWriteLog) add(kind, value string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.messages = append(l.messages, LogMessage{MessageType: kind, Value: value})
}

func (l *appWriteLog) line(value string)    { l.add("None", value) }
func (l *appWriteLog) info(value string)    { l.add("Info", value) }
func (l *appWriteLog) warning(value string) { l.add("Warning", value) }
func (l *appWriteLog) error(value string)   { l.add("Error", value) }

func (l *appWriteLog) result(exitCode int) CommandResult {
	l.mu.Lock()
	defer l.mu.Unlock()
	return CommandResult{ExitCode: exitCode, Messages: append([]LogMessage{}, l.messages...)}
}

// appWriteInvalidArchive is InvalidGZipArchiveInstallException (exit code 5).
type appWriteInvalidArchive struct{ message string }

func (e appWriteInvalidArchive) Error() string { return e.message }

// InstallApplication is InstallApplicationCommand.Execute.
func (c *Client) InstallApplication(ctx context.Context, request AppInstallRequest) CommandResult {
	log := &appWriteLog{}
	success, err := c.appWriteInstall(ctx, request, log)
	var invalid appWriteInvalidArchive
	switch {
	case errors.As(err, &invalid):
		log.error(invalid.message)
		return log.result(5)
	case err != nil:
		log.error(err.Error())
		return log.result(1)
	case success:
		log.info("Done")
		return log.result(0)
	}
	message := "Package installation failed."
	if strings.TrimSpace(request.Name) != "" {
		message = fmt.Sprintf("Failed package: \"%s\".", request.Name)
	}
	log.error(message)
	return log.result(1)
}

// appWriteInstall is BasePackageInstaller.InternalInstall.
func (c *Client) appWriteInstall(ctx context.Context, request AppInstallRequest, log *appWriteLog) (bool, error) {
	path := request.Name
	if strings.TrimSpace(path) == "" {
		var err error
		if path, err = os.Getwd(); err != nil {
			return false, err
		}
	}
	var success bool
	var logText string
	info, statErr := os.Stat(path)
	switch {
	case statErr == nil && !info.IsDir():
		var err error
		if success, logText, err = c.appWriteInstallPacked(ctx, path, request, log); err != nil {
			return false, err
		}
	case statErr == nil && info.IsDir():
		packed := path + ".gz"
		if err := appWritePack(path, packed, log); err != nil {
			return false, err
		}
		var err error
		success, logText, err = c.appWriteInstallPacked(ctx, packed, request, log)
		_ = os.Remove(packed)
		if err != nil {
			return false, err
		}
	default:
		log.line("Specified package not found by path " + path)
	}
	if err := appWriteSaveLog(logText, request.ReportPath); err != nil {
		return false, err
	}
	return success, nil
}

// appWriteSaveLog is SaveLogFile: an existing report file is replaced, a directory gets cliolog.txt; UTF-8
// with a byte-order mark, as .NET's Encoding.UTF8 writes it.
func appWriteSaveLog(text, reportPath string) error {
	if reportPath == "" || strings.TrimSpace(text) == "" {
		return nil
	}
	if info, err := os.Stat(reportPath); err == nil {
		if info.IsDir() {
			reportPath = filepath.Join(reportPath, "cliolog.txt")
		} else if err := os.Remove(reportPath); err != nil {
			return err
		}
	}
	return os.WriteFile(reportPath, append([]byte{0xEF, 0xBB, 0xBF}, text...), 0o644)
}

// appWritePackageElements are PackageUtilities.PackageElementNames.
var appWritePackageElements = []string{"Assemblies", "Bin", "Data", "Files", "Resources", "Schemas", "SqlScripts"}

// appWritePack is PackageArchiver.Pack (skipPdb false, overwrite true): the package's element folders and
// descriptor.json copied to a temporary folder, then written as clio's .gz stream. A .clioignore file is
// not applied.
func appWritePack(packagePath, packedPath string, log *appWriteLog) error {
	if err := os.Remove(packedPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	log.info("Creating temporary directory...")
	temp, err := os.MkdirTemp(filepath.Join(os.TempDir()), "clio-")
	if err != nil {
		return err
	}
	log.info("Created temporary directory " + temp)
	defer func() {
		_ = os.RemoveAll(temp)
		log.info("Deleted temporary directory " + temp)
	}()
	content, err := appWritePackageContent(packagePath)
	if err != nil {
		return err
	}
	for _, element := range appWritePackageElements {
		source := filepath.Join(content, element)
		if info, err := os.Stat(source); err == nil && info.IsDir() {
			if err := appWriteCopyTree(source, filepath.Join(temp, element)); err != nil {
				return err
			}
		}
	}
	descriptor := filepath.Join(content, "descriptor.json")
	data, err := os.ReadFile(descriptor)
	if err != nil {
		return fmt.Errorf("Could not find file '%s'.", descriptor)
	}
	if err := os.WriteFile(filepath.Join(temp, "descriptor.json"), data, 0o644); err != nil {
		return err
	}
	return appWriteGzip(temp, packedPath)
}

// appWritePackageContent is GetPackageContentFolderPath: a repository package keeps its content in its only
// branches/<version> folder.
func appWritePackageContent(packagePath string) (string, error) {
	branches := filepath.Join(packagePath, "branches")
	info, err := os.Stat(branches)
	if err != nil || !info.IsDir() {
		return packagePath, nil
	}
	entries, err := os.ReadDir(branches)
	if err != nil {
		return "", err
	}
	var folders []string
	for _, entry := range entries {
		if entry.IsDir() {
			folders = append(folders, entry.Name())
		}
	}
	if len(folders) == 1 {
		return filepath.Join(branches, folders[0]), nil
	}
	return "", fmt.Errorf("Unsupported package folder structure.Expected structure contains one package version in folder '%s'.", branches)
}

func appWriteCopyTree(source, target string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, _ := filepath.Rel(source, path)
		destination := filepath.Join(target, relative)
		if entry.IsDir() {
			return os.MkdirAll(destination, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(destination, data, 0o644)
	})
}

// appWriteGzip is CompressionUtilities.PackToGZip: for every file, the relative path as an int32 count of
// UTF-16 units and the units, then the content as an int32 length and the bytes, all little-endian, gzipped.
func appWriteGzip(root, packedPath string) error {
	var files []string
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err == nil && !entry.IsDir() {
			files = append(files, path)
		}
		return err
	}); err != nil {
		return err
	}
	sort.Strings(files)
	output, err := os.Create(packedPath)
	if err != nil {
		return err
	}
	defer output.Close()
	writer := gzip.NewWriter(output)
	for _, file := range files {
		relative := strings.TrimLeft(strings.TrimPrefix(file, strings.TrimRight(root, string(os.PathSeparator))), string(os.PathSeparator))
		units := utf16.Encode([]rune(relative))
		_ = binary.Write(writer, binary.LittleEndian, int32(len(units)))
		_ = binary.Write(writer, binary.LittleEndian, units)
		data, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		_ = binary.Write(writer, binary.LittleEndian, int32(len(data)))
		if _, err := writer.Write(data); err != nil {
			return err
		}
	}
	return writer.Close()
}

// appWriteInstallPacked is InstallPackedPackage.
func (c *Client) appWriteInstallPacked(ctx context.Context, filePath string, request AppInstallRequest, log *appWriteLog) (bool, string, error) {
	log.line("Uploading...")
	fileName := filepath.Base(filePath)
	if err := c.appWriteUpload(ctx, filePath); err != nil {
		return false, "", err
	}
	log.line("Uploaded")
	code := strings.Split(fileName, ".")[0]
	log.info(request.EnvironmentURI)
	log.line("Backup process...")
	backup := fmt.Sprintf(`{"Name":"%s","Code":"%s","ZipPackageName":"%s","LastUpdate":0}`, code, code, fileName)
	if _, err := c.callService(ctx, serviceCall{Route: "ServiceModel/PackageInstallerService.svc/CreatePackageBackup", Body: []byte(backup), Label: "PackageInstallerService"}); err != nil {
		return false, "Dont created backup.", nil
	}
	log.line("Backup completed")
	success, logText, err := c.appWriteInstallWithLog(ctx, fileName, code, request, log)
	if err != nil {
		return false, "", err
	}
	if request.DeveloperMode {
		if err := c.appWriteUnlockPackages(ctx); err != nil {
			return false, "", err
		}
	}
	if request.DeveloperMode || c.config.IsNetCore {
		log.line("Restart application...")
		route := "ServiceModel/AppInstallerService.svc/UnloadAppDomain"
		if c.config.IsNetCore {
			route = "ServiceModel/AppInstallerService.svc/RestartApp"
		}
		if _, err := c.callService(ctx, serviceCall{Route: route, Body: []byte("{}"), Timeout: appWriteInstallTimings.installTimeout}); err != nil {
			log.line("Error while restarting application: " + err.Error())
		}
	}
	return success, logText, nil
}

// appWriteUnlockPackages is PackageLockManager.Unlock() through ClioGate.
func (c *Client) appWriteUnlockPackages(ctx context.Context) error {
	const route = "UnlockPackages"
	payload, err := c.callService(ctx, serviceCall{Route: "rest/CreatioApiGateway/UnlockPackages", Body: []byte(`{"unlockPackages":null}`), Label: "ClioGate"})
	if err != nil {
		return err
	}
	if len(payload) == 0 {
		return fmt.Errorf("ClioGate %s returned an empty response. Check the Creatio application logs (Error.log) and verify the installed cliogate version is current.", route)
	}
	var success bool
	if err := json.Unmarshal(payload, &success); err != nil {
		return fmt.Errorf("ClioGate %s returned a non-JSON response (likely an HTTP error page). Check the Creatio application logs (Error.log) and verify the installed cliogate version is current.", route)
	}
	if !success {
		return fmt.Errorf("ClioGate %s returned false. Check the Creatio application logs for details.", route)
	}
	return nil
}

// appWriteUploadMime is Creatio.Client's GetMimeTypeFromFileExtension for the package extensions.
func appWriteUploadMime(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".zip":
		return "application/x-zip-compressed"
	case ".gz":
		return "application/gzip"
	case ".json":
		return "application/json"
	case ".xml":
		return "application/xml"
	default:
		return "application/octet-stream"
	}
}

// appWriteUpload is Creatio.Client.UploadFile: 1 MB chunks posted to PackageInstallerService UploadPackage
// with the file's total length, name and type in the query and the chunk's range in Content-Range.
func (c *Client) appWriteUpload(ctx context.Context, filePath string) error {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return err
	}
	name := filepath.Base(filePath)
	mime := appWriteUploadMime(name)
	query := "?totalFileLength=" + strconv.Itoa(len(data)) + "&fileName=" + name + "&mimeType=" + url.QueryEscape(mime)
	for start := 0; start < len(data); start += appWriteInstallTimings.chunk {
		end := min(start+appWriteInstallTimings.chunk, len(data))
		chunk := data[start:end]
		requestCtx, cancel := context.WithTimeout(ctx, appWriteInstallTimings.uploadTimeout)
		response, _, err := c.doAuthenticated(requestCtx, c.requestClient(), func() (*http.Request, error) {
			request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, c.serviceURL("ServiceModel/PackageInstallerService.svc/UploadPackage")+query, bytes.NewReader(chunk))
			if err != nil {
				return nil, err
			}
			request.Header.Set("Content-Type", mime)
			request.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end-1, len(data)))
			request.Header.Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s", name))
			return request, nil
		})
		if err != nil {
			cancel()
			return err
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
		response.Body.Close()
		cancel()
		if readErr != nil {
			return readErr
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return fmt.Errorf("Response status code does not indicate success: %d (%s).", response.StatusCode, http.StatusText(response.StatusCode))
		}
		var answer struct {
			Success bool `json:"success"`
		}
		if json.Unmarshal(body, &answer) != nil {
			return errors.New("Error deserializing upload response (Parameter 'response')")
		}
	}
	return nil
}

// appWriteInstallLog is ApplicationLogProvider.GetInstallationLog: the log file, or "" when it cannot be read.
func (c *Client) appWriteInstallLog(ctx context.Context) string {
	response, err := c.serviceRequest(ctx, serviceCall{Method: http.MethodGet, Route: "ServiceModel/PackageInstallerService.svc/GetLogFile"})
	if err != nil || response.status < 200 || response.status >= 300 {
		return ""
	}
	return string(response.payload)
}

func appWriteLogDiff(current, complete string) string {
	if strings.TrimSpace(complete) == "" || len(complete) <= len(current) {
		return ""
	}
	return complete[len(current):]
}

// appWriteInstallResponse is clio's BaseResponse.
type appWriteInstallResponse struct {
	Success   bool `json:"success"`
	ErrorInfo *struct {
		ErrorCode  string `json:"errorCode"`
		Message    string `json:"message"`
		StackTrace string `json:"stackTrace"`
	} `json:"errorInfo"`
}

// appWriteInstallWithLog is InstallPackageOnServerWithLogListener.
func (c *Client) appWriteInstallWithLog(ctx context.Context, fileName, code string, request AppInstallRequest, log *appWriteLog) (bool, string, error) {
	log.line("Install " + fileName + " ...")
	log.line("Installation log:")
	initial := c.appWriteInstallLog(ctx)
	listenCtx, stop := context.WithCancel(ctx)
	var listened string
	done := make(chan struct{})
	go func() {
		defer close(done)
		current := initial
		for listenCtx.Err() == nil {
			complete := c.appWriteInstallLog(listenCtx)
			if output := appWriteLogDiff(current, complete); strings.TrimSpace(output) != "" {
				log.line(output)
				current = complete
				if request.ReportPath != "" {
					_ = appWriteSaveLog(current, request.ReportPath)
				}
			}
			appWriteSleep(listenCtx, appWriteInstallTimings.logInterval)
		}
		listened = current
	}()
	body := map[string]any{"Name": code, "Code": code, "ZipPackageName": fileName, "LastUpdateString": 0}
	if request.CheckCompilationErrors != nil {
		body["CheckCompilationErrors"] = *request.CheckCompilationErrors
	}
	encoded, _ := json.Marshal(body)
	payload, err := c.callService(ctx, serviceCall{Route: "ServiceModel/AppInstallerService.svc/InstallAppFromFile", Body: encoded,
		Timeout: appWriteInstallTimings.installTimeout, Label: "AppInstallerService"})
	stop()
	<-done
	if err != nil {
		return false, "", err
	}
	var response *appWriteInstallResponse
	if trimmed := bytes.TrimSpace(payload); len(trimmed) > 0 {
		if err := json.Unmarshal(trimmed, &response); err != nil {
			return false, "", fmt.Errorf("InstallAppFromFile returned an unreadable response: %w", err)
		}
	}
	complete := c.appWriteInstallLog(ctx)
	current := appWriteLogDiff(initial, complete)
	log.line(appWriteLogDiff(listened, complete))
	success := response == nil || response.Success
	appWriteReportLocallyModified(current, log)
	if !success && appWriteInvalidGzip(response, current) {
		_ = appWriteSaveLog(complete, request.ReportPath)
		message := "The package archive is invalid or corrupted."
		if response != nil && response.ErrorInfo != nil && response.ErrorInfo.Message != "" {
			message = response.ErrorInfo.Message
		} else if line := appWriteInvalidGzipLine(current); line != "" {
			message = line
		}
		return false, "", appWriteInvalidArchive{message: message}
	}
	if !success {
		if initial == "" || !strings.HasPrefix(complete, initial) {
			log.warning("The installation log could not be attributed to this run, so the reported failure is " +
				"kept. Re-run the command to get a usable log.")
		} else if appWriteTreatAsSuccess(response, current) {
			log.warning("The installation service reported a failure, but the installation finished and the only " +
				"problem reported in this run's log was a locally modified schema. Treating the " +
				"installation as successful.")
			success = true
		}
	}
	if !success {
		log.error("Package installation failed: " + appWriteDescribeFailure(response, current))
	}
	return success, complete, nil
}

var (
	appWriteLocallyModifiedSchema = regexp.MustCompile(`(?i)Unable to install\s+\w+\s+"(?P<schema>[^"]+)"`)
	appWriteCompilationError      = regexp.MustCompile(`\berror CS[0-9]+\b`)
)

func appWriteLogLines(text string) []string {
	return strings.Split(strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n"), "\n")
}

// appWriteLocallyModifiedLines is InstallLogAnalyzer.GetLocallyModifiedSchemaLines.
func appWriteLocallyModifiedLines(log string) []string {
	var lines []string
	if strings.TrimSpace(log) == "" {
		return lines
	}
	for _, line := range appWriteLogLines(log) {
		if strings.Contains(strings.ToLower(line), "has been modified locally") {
			lines = append(lines, strings.TrimSpace(line))
		}
	}
	return lines
}

// appWriteReportLocallyModified is ReportLocallyModifiedSchemas.
func appWriteReportLocallyModified(current string, log *appWriteLog) {
	lines := appWriteLocallyModifiedLines(current)
	if len(lines) == 0 {
		return
	}
	var names []string
	seen := map[string]bool{}
	for _, line := range lines {
		log.warning(line)
		if match := appWriteLocallyModifiedSchema.FindStringSubmatch(line); match != nil && strings.TrimSpace(match[1]) != "" && !seen[strings.ToLower(match[1])] {
			seen[strings.ToLower(match[1])] = true
			names = append(names, match[1])
		}
	}
	list := "see the messages above"
	if len(names) > 0 {
		list = strings.Join(names, ", ")
	}
	log.warning(fmt.Sprintf("%d schema(s) skipped because they were modified locally: %s. "+
		"Resolve the conflict on the environment and mark the elements as unchanged to install them.", len(lines), list))
}

func appWriteGenericFailure(response *appWriteInstallResponse) bool {
	if response == nil || response.ErrorInfo == nil || strings.TrimSpace(response.ErrorInfo.Message) == "" {
		return false
	}
	normalized := strings.TrimSpace(strings.TrimRight(strings.TrimSpace(response.ErrorInfo.Message), "."))
	return strings.EqualFold(normalized, "Packages installation failed")
}

// appWriteTreatAsSuccess is InstallLogAnalyzer.ShouldTreatAsSuccess with FailOnError off.
func appWriteTreatAsSuccess(response *appWriteInstallResponse, log string) bool {
	return strings.Contains(strings.ToLower(log), "package installation finished") && len(appWriteLocallyModifiedLines(log)) > 0 &&
		appWriteGenericFailure(response) && !appWriteCompilationError.MatchString(log)
}

// appWriteDescribeFailure is InstallLogAnalyzer.DescribeFailure.
func appWriteDescribeFailure(response *appWriteInstallResponse, log string) string {
	if appWriteGenericFailure(response) {
		for _, line := range appWriteLogLines(log) {
			if appWriteCompilationError.MatchString(line) {
				return "the configuration failed to compile. " + strings.TrimSpace(line)
			}
		}
	}
	if response != nil && response.ErrorInfo != nil && strings.TrimSpace(response.ErrorInfo.Message) != "" {
		if strings.TrimSpace(response.ErrorInfo.ErrorCode) == "" {
			return strings.TrimSpace(response.ErrorInfo.Message)
		}
		return strings.TrimSpace(response.ErrorInfo.ErrorCode) + ": " + strings.TrimSpace(response.ErrorInfo.Message)
	}
	var meaningful []string
	for _, line := range appWriteLogLines(log) {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			meaningful = append(meaningful, trimmed)
		}
	}
	if len(meaningful) == 0 {
		return "the installation service reported a failure without an error message."
	}
	return "the installation service reported a failure without an error message. Last log lines: " +
		strings.Join(meaningful[max(0, len(meaningful)-3):], " | ")
}

func appWriteInvalidGzip(response *appWriteInstallResponse, log string) bool {
	contains := func(value string) bool {
		return strings.Contains(strings.ToLower(value), "invalidgziparchiveexception")
	}
	if response != nil && response.ErrorInfo != nil &&
		(contains(response.ErrorInfo.ErrorCode) || contains(response.ErrorInfo.Message) || contains(response.ErrorInfo.StackTrace)) {
		return true
	}
	return contains(log)
}

func appWriteInvalidGzipLine(log string) string {
	for _, line := range appWriteLogLines(log) {
		if strings.Contains(strings.ToLower(line), "invalidgziparchiveexception") {
			return strings.TrimSpace(line)
		}
	}
	return ""
}
