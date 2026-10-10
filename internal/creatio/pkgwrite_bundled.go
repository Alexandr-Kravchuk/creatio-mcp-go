package creatio

// install-gate and install-dashboards-migrator install a package clio ships next to its executable:
// clio's InstallGateCommand and InstallDashboardsMigratorCommand (InstallBundledPackageCommand) over
// PackageInstaller (BasePackageInstaller with PackageInstallerService InstallPackage / CreateBackup). This
// server ships no package archives; the tool finds them in the newest clio installed as a .NET tool and passes
// the archive path in.

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
)

const (
	// DashboardsMigratorPackage is BundledPackages.DashboardsMigratorPackageName.
	DashboardsMigratorPackage   = "CrtDashboardsMigratorApp"
	pkgWriteDashboardsPingRoute = "rest/DashboardsMigratorPingService/Ping"
	pkgWriteDashboardsVerb      = "install-dashboards-migrator"
	pkgWriteAppDescriptorEntry  = "Files/app-descriptor.json"
)

// pkgWriteBundledTimings are the waits around a bundled install; tests shorten them.
var pkgWriteBundledTimings = struct {
	readinessTimeout, pingTimeout, pingDelay, restartTimeout time.Duration
	pingAttempts                                             int
}{readinessTimeout: 600 * time.Second, pingTimeout: 15 * time.Second, pingDelay: 5 * time.Second,
	restartTimeout: 10 * time.Minute, pingAttempts: 3}

// ---- clio's package archive (CompressionUtilities) ----

// pkgWriteReadGzipEntry is CompressionUtilities.TryReadFileFromGZip: the content of one entry of clio's .gz
// package format; found is false when the archive was read to its end without that entry.
func pkgWriteReadGzipEntry(archivePath, entryPath string) (content []byte, found bool, err error) {
	file, err := os.Open(archivePath)
	if err != nil {
		return nil, false, err
	}
	defer file.Close()
	reader, err := gzip.NewReader(file)
	if err != nil {
		return nil, false, err
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, false, err
	}
	normalize := func(path string) string { return strings.Trim(strings.ReplaceAll(path, `\`, "/"), "/") }
	wanted := normalize(entryPath)
	position := 0
	readLength := func() (int, bool) {
		if len(data)-position < 4 {
			return 0, false
		}
		value := int(int32(binary.LittleEndian.Uint32(data[position:])))
		position += 4
		return value, true
	}
	for {
		nameLength, ok := readLength()
		if !ok || nameLength < 0 || nameLength*2 > len(data)-position {
			if ok || position < len(data) {
				return nil, false, fmt.Errorf("'%s' is not a readable package archive: the entry starting at offset %d declares an impossible name length.", archivePath, position-4)
			}
			return nil, false, nil
		}
		units := make([]uint16, nameLength)
		for index := range units {
			units[index] = binary.LittleEndian.Uint16(data[position+2*index:])
		}
		position += 2 * nameLength
		name := string(utf16.Decode(units))
		if nameLength == 0 {
			if position < len(data) {
				return nil, false, fmt.Errorf("'%s' is not a readable package archive: the entry starting at offset %d declares an impossible name length.", archivePath, position-4)
			}
			return nil, false, nil
		}
		contentLength, ok := readLength()
		if !ok || contentLength < 0 || contentLength > len(data)-position {
			if strings.EqualFold(normalize(name), wanted) {
				return nil, false, fmt.Errorf("'%s' is not a readable package archive: entry '%s' is truncated.", archivePath, name)
			}
			return nil, false, fmt.Errorf("'%s' is not a readable package archive: entry '%s' declares a content length that does not fit in the remaining bytes.", archivePath, name)
		}
		if strings.EqualFold(normalize(name), wanted) {
			return data[position : position+contentLength], true, nil
		}
		position += contentLength
	}
}

// ---- package versions (PackageVersion over System.Version) ----

// pkgWritePackageVersion is clio's PackageVersion: a System.Version (2 to 4 components) and a suffix.
type pkgWritePackageVersion struct {
	parts  []int
	suffix string
}

// pkgWriteParsePackageVersion is PackageVersion.TryParseVersion.
func pkgWriteParsePackageVersion(text string) (pkgWritePackageVersion, bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return pkgWritePackageVersion{}, false
	}
	version, suffix := text, ""
	if index := strings.Index(text, "-"); index > 0 {
		version, suffix = text[:index], strings.TrimSpace(text[index+1:])
	}
	fields := strings.Split(strings.TrimSpace(version), ".")
	if len(fields) < 2 || len(fields) > 4 {
		return pkgWritePackageVersion{}, false
	}
	parts := make([]int, len(fields))
	for index, field := range fields {
		number, err := strconv.Atoi(strings.TrimSpace(field))
		if err != nil || number < 0 {
			return pkgWritePackageVersion{}, false
		}
		parts[index] = number
	}
	return pkgWritePackageVersion{parts: parts, suffix: suffix}, true
}

// compare is System.Version.CompareTo: an absent component sorts below 0.
func (v pkgWritePackageVersion) compare(other pkgWritePackageVersion) int {
	for index := 0; index < 4; index++ {
		a, b := -1, -1
		if index < len(v.parts) {
			a = v.parts[index]
		}
		if index < len(other.parts) {
			b = other.parts[index]
		}
		if a != b {
			if a < b {
				return -1
			}
			return 1
		}
	}
	return 0
}

func (v pkgWritePackageVersion) number() string {
	texts := make([]string, len(v.parts))
	for index, part := range v.parts {
		texts[index] = strconv.Itoa(part)
	}
	return strings.Join(texts, ".")
}

// display is TextUtilities.SanitizeVersionForDisplay: the suffix only when it is short and version-shaped.
func (v pkgWritePackageVersion) display() string {
	if strings.TrimSpace(v.suffix) == "" {
		return v.number()
	}
	if len(v.suffix) <= 16 && pkgWriteVersionShapedSuffix(v.suffix) {
		return v.number() + "-" + v.suffix
	}
	return v.number()
}

func pkgWriteVersionShapedSuffix(suffix string) bool {
	separator := func(r byte) bool { return r == '.' || r == '-' }
	if suffix == "" || separator(suffix[0]) || separator(suffix[len(suffix)-1]) {
		return false
	}
	previous := false
	for index := 0; index < len(suffix); index++ {
		character := suffix[index]
		isSeparator := separator(character)
		if isSeparator && previous {
			return false
		}
		alphanumeric := character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z' || character >= '0' && character <= '9'
		if !isSeparator && !alphanumeric {
			return false
		}
		previous = isSeparator
	}
	return true
}

// ---- PackageInstaller ----

// pkgWriteInstallPackageFile is PackageInstaller.Install(path, settings with DeveloperModeEnabled = false,
// no options, no report, createBackup: true) for a packed package file.
func (c *Client) pkgWriteInstallPackageFile(ctx context.Context, archivePath string, log *appWriteLog) (bool, error) {
	if info, err := os.Stat(archivePath); err != nil || info.IsDir() {
		log.line("Specified package not found by path " + archivePath)
		return false, nil
	}
	log.line("Uploading...")
	if err := c.appWriteUpload(ctx, archivePath); err != nil {
		return false, err
	}
	log.line("Uploaded")
	fileName := filepath.Base(archivePath)
	code := strings.Split(fileName, ".")[0]
	log.info(c.config.BaseURL)
	log.line("Backup process...")
	backup := fmt.Sprintf(`{"Name":"%s","Code":"%s","ZipPackageName":"%s","LastUpdate":0}`, code, code, fileName)
	if _, err := c.callService(ctx, serviceCall{Route: "ServiceModel/PackageInstallerService.svc/CreateBackup", Body: []byte(backup), Label: "PackageInstallerService"}); err != nil {
		return false, nil
	}
	log.line("Backup completed")
	success, err := c.pkgWriteInstallWithLog(ctx, fileName, log)
	if err != nil {
		return false, err
	}
	// Developer mode is forced off for a bundled install, so only a .NET Core host is restarted here.
	if c.config.IsNetCore {
		if err := c.pkgWriteApplicationRestart(ctx, log); err != nil {
			log.line("Error while restarting application: " + err.Error())
		}
	}
	return success, nil
}

// pkgWriteApplicationRestart is Application.Restart.
func (c *Client) pkgWriteApplicationRestart(ctx context.Context, log *appWriteLog) error {
	log.line("Restart application...")
	route := pkgWriteRestartFrameworkRoute
	if c.config.IsNetCore {
		route = pkgWriteRestartNetCoreRoute
	}
	_, err := c.callService(ctx, serviceCall{Route: route, Body: []byte("{}"), Timeout: pkgWriteBundledTimings.restartTimeout})
	return err
}

// pkgWriteInstallWithLog is InstallPackageOnServerWithLogListener for PackageInstaller: InstallPackage with
// the archive name as a JSON string, the installation log followed every 3 s while it runs.
func (c *Client) pkgWriteInstallWithLog(ctx context.Context, fileName string, log *appWriteLog) (bool, error) {
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
			}
			appWriteSleep(listenCtx, appWriteInstallTimings.logInterval)
		}
		listened = current
	}()
	body, _ := json.Marshal(fileName)
	payload, err := c.callService(ctx, serviceCall{Route: "ServiceModel/PackageInstallerService.svc/InstallPackage", Body: body,
		Timeout: appWriteInstallTimings.installTimeout, Label: "PackageInstallerService"})
	stop()
	<-done
	if err != nil {
		return false, err
	}
	var response *appWriteInstallResponse
	if trimmed := bytes.TrimSpace(payload); len(trimmed) > 0 {
		if err := json.Unmarshal(trimmed, &response); err != nil {
			return false, fmt.Errorf("InstallPackage returned an unreadable response: %w", err)
		}
	}
	complete := c.appWriteInstallLog(ctx)
	current := appWriteLogDiff(initial, complete)
	log.line(appWriteLogDiff(listened, complete))
	success := response == nil || response.Success
	appWriteReportLocallyModified(current, log)
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
	return success, nil
}

// ---- install-gate ----

// InstallGate is InstallGateCommand.Execute with the bundled cliogate archive (cliogate_netcore.gz on .NET
// Core). The command's catch also prints the exception's stack trace; that line is not reproduced.
func (c *Client) InstallGate(ctx context.Context, archivePath string) CommandResult {
	log := &appWriteLog{}
	success, err := c.pkgWriteInstallPackageFile(ctx, archivePath, log)
	if err != nil {
		log.error(err.Error())
		return log.result(1)
	}
	if !success {
		log.error("Error")
		return log.result(1)
	}
	log.line("Done")
	if err := c.pkgWriteApplicationRestart(ctx, log); err != nil {
		log.error(err.Error())
		return log.result(1)
	}
	return log.result(0)
}

// GateArchiveName is the cliogate archive InstallGateCommand picks for the environment.
func (c *Client) GateArchiveName() string {
	if c.config.IsNetCore {
		return "cliogate_netcore.gz"
	}
	return "cliogate.gz"
}

// ---- install-dashboards-migrator ----

// pkgWriteBundledVersion is BundledPackageCatalog.TryGetVersion for an app archive: Version in
// Files/app-descriptor.json.
func pkgWriteBundledVersion(packageName, archivePath string) (pkgWritePackageVersion, string) {
	if info, err := os.Stat(archivePath); err != nil || info.IsDir() {
		return pkgWritePackageVersion{}, fmt.Sprintf("This clio installation does not carry the bundled %s archive — it was expected at '%s'. Reinstall or update clio itself.", packageName, archivePath)
	}
	content, found, err := pkgWriteReadGzipEntry(archivePath, pkgWriteAppDescriptorEntry)
	if err != nil {
		flattened := strings.NewReplacer("\r", " ", "\n", " ").Replace(err.Error())
		return pkgWritePackageVersion{}, fmt.Sprintf("The bundled %s archive at '%s' could not be read (%s). Reinstall or update clio itself.", packageName, archivePath, flattened)
	}
	if !found {
		return pkgWritePackageVersion{}, fmt.Sprintf("The bundled %s archive at '%s' contains no %s, so the version it carries cannot be determined. Reinstall or update clio itself.", packageName, archivePath, pkgWriteAppDescriptorEntry)
	}
	content = bytes.TrimPrefix(content, []byte{0xEF, 0xBB, 0xBF})
	var descriptor map[string]json.RawMessage
	if err := json.Unmarshal(content, &descriptor); err != nil {
		if !json.Valid(content) {
			return pkgWritePackageVersion{}, fmt.Sprintf("The bundled %s archive at '%s' has a malformed %s (%s). Reinstall or update clio itself.", packageName, archivePath, pkgWriteAppDescriptorEntry, err.Error())
		}
	}
	var text string
	if raw, ok := descriptor["Version"]; ok && json.Unmarshal(raw, &text) == nil {
		if version, ok := pkgWriteParsePackageVersion(text); ok {
			return version, ""
		}
	}
	return pkgWritePackageVersion{}, fmt.Sprintf("The bundled %s archive at '%s' has a %s without a readable Version. Reinstall or update clio itself.", packageName, archivePath, pkgWriteAppDescriptorEntry)
}

// pkgWriteInstalledVersion is RequiredPackageChecker.GetInstalledVersion: the lowest version recorded for
// the package, nil when it is not installed.
func (c *Client) pkgWriteInstalledVersion(ctx context.Context, packageName string) (*pkgWritePackageVersion, error) {
	packages, err := c.pkgWriteInstalledPackages(ctx)
	if err != nil {
		return nil, err
	}
	var lowest *pkgWritePackageVersion
	for _, item := range packages {
		if !strings.EqualFold(item.Name, packageName) {
			continue
		}
		if version, ok := pkgWriteParsePackageVersion(item.Version); ok && (lowest == nil || version.compare(*lowest) < 0) {
			copied := version
			lowest = &copied
		}
	}
	return lowest, nil
}

// pkgWriteRefuseBundledInstall is InstallBundledPackageCommand.ShouldRefuseInstall.
func (c *Client) pkgWriteRefuseBundledInstall(ctx context.Context, packageName, archivePath string, log *appWriteLog) string {
	overrideHint := fmt.Sprintf("A command-line override exists for the case where the rollback is what you want; see 'clio %s -H'.", pkgWriteDashboardsVerb)
	shipped, diagnosis := pkgWriteBundledVersion(packageName, archivePath)
	if diagnosis != "" {
		log.warning(diagnosis + " Installing anyway: without a version to compare, a downgrade cannot be ruled in " +
			"or out, and refusing would block the install over clio's own defect.")
		return ""
	}
	if strings.TrimSpace(shipped.suffix) != "" {
		return fmt.Sprintf("Refusing: this clio distribution declares %s %s, but a bundled package version "+
			"must be a plain four-part number with no pre-release suffix — clio compares bundled versions "+
			"numerically, and installing a suffixed one could move the target environment's recorded "+
			"version backwards for everyone using it without being detected. Reinstall or update clio "+
			"itself, or re-run the rebundle with a four-part version if you produced this archive. ", packageName, shipped.display()) + overrideHint
	}
	installed, err := c.pkgWriteInstalledVersion(ctx, packageName)
	if err != nil {
		log.warning("Could not read the version currently installed in the environment " +
			"(" + ThemeWriteSanitize(err.Error(), 300) + "). Installing anyway; if the environment is " +
			"genuinely unreachable the install itself will say so.")
		return ""
	}
	if installed == nil || installed.compare(shipped) <= 0 {
		return ""
	}
	return fmt.Sprintf("Refusing: the environment carries %s %s, and this clio ships %s — installing would move that "+
		"environment's recorded version BACKWARDS for everyone using it, and nothing downstream would "+
		"report it: the gate would see a present package and the convergence check compares the recorded "+
		"version, which the rollback has just rewritten. Update clio instead. ", packageName, installed.display(), shipped.display()) + overrideHint
}

// pkgWritePingOperational is BundledPackagePingOutcomeVerifier.IsPackageOperational: the diagnosis, "" when
// the package's Ping answered with success. A Ping that does not answer logs its own error and has no
// diagnosis (ok false, diagnosis "").
func (c *Client) pkgWritePingOperational(ctx context.Context, packageName string, log *appWriteLog) (bool, string) {
	url := c.serviceURL(pkgWriteDashboardsPingRoute)
	var response serviceResponse
	var err error
	for attempt := 1; attempt <= pkgWriteBundledTimings.pingAttempts; attempt++ {
		response, err = c.serviceRequest(ctx, serviceCall{Route: pkgWriteDashboardsPingRoute, Body: []byte("{}"), Timeout: pkgWriteBundledTimings.pingTimeout})
		if err == nil || attempt == pkgWriteBundledTimings.pingAttempts || !appWriteSleep(ctx, pkgWriteBundledTimings.pingDelay) {
			break
		}
	}
	if err != nil {
		log.error(fmt.Sprintf("%s Ping did not answer: %s", packageName, err.Error()))
		return false, ""
	}
	body := string(response.payload)
	truncated := "(empty)"
	if body != "" {
		truncated = ThemeWriteSanitize(body, 200)
	}
	if pkgWriteSessionExpired(body) {
		return false, fmt.Sprintf("%s was installed, but %s answered with a login page: the session expired and "+
			"automatic re-authentication did not restore it. The configuration build is NOT implicated "+
			"and the package's state is UNKNOWN — check the environment's credentials, then verify "+
			"with 'clio call-service --service-path %s -m POST -b {} -e <environment>'.", packageName, url, pkgWriteDashboardsPingRoute)
	}
	root, ok := dataWriteParseObject([]byte(body))
	if !ok || !json.Valid([]byte(body)) {
		log.error(fmt.Sprintf("%s Ping did not answer: the answer is not valid JSON.", packageName))
		return false, ""
	}
	if _, present := root.raw("PingResult"); !present {
		return false, fmt.Sprintf("%s was installed, but %s answered with a JSON body carrying no PingResult, "+
			"so something other than this package is serving that route — a reverse proxy, an "+
			"authenticating gateway or an expired-session redirect. The configuration build is NOT "+
			"implicated; check what sits in front of the environment. First 200 characters of the "+
			"answer: %s", packageName, url, truncated)
	}
	result, ok := root.object("PingResult")
	if !ok {
		log.error(fmt.Sprintf("%s Ping did not answer: PingResult is not an object.", packageName))
		return false, ""
	}
	if raw, present := result.raw("success"); !present || dataWriteJSONKind(raw) != 't' {
		return false, fmt.Sprintf("%s's Ping route answered, but not with success — so the package is serving and "+
			"is reporting a problem, which the shipped build cannot do (its Ping returns a constant). "+
			"Either the serving build is not the one clio ships, or something is answering in this "+
			"package's envelope. First 200 characters of the answer: %s", packageName, truncated)
	}
	return true, ""
}

// InstallDashboardsMigrator is InstallDashboardsMigratorCommand.Execute (no --force) with the bundled archive
// at archivePath. The catch's stack-trace line is not reproduced.
func (c *Client) InstallDashboardsMigrator(ctx context.Context, archivePath string) CommandResult {
	const packageName = DashboardsMigratorPackage
	log := &appWriteLog{}
	if info, err := os.Stat(archivePath); err != nil || info.IsDir() {
		log.error(fmt.Sprintf("The bundled %s package was not found at '%s'. This clio installation does not carry the "+
			"package archive, so retrying will not help — reinstall or update clio itself.", packageName, archivePath))
		return log.result(1)
	}
	if refusal := c.pkgWriteRefuseBundledInstall(ctx, packageName, archivePath, log); refusal != "" {
		log.error(refusal)
		return log.result(1)
	}
	success, err := c.pkgWriteInstallPackageFile(ctx, archivePath, log)
	if err != nil {
		log.error(err.Error())
		return log.result(1)
	}
	if !success {
		log.error(fmt.Sprintf("Failed to install the bundled %s package.", packageName))
		return log.result(1)
	}
	if !c.pkgWriteWaitReady(ctx, pkgWriteBundledTimings.readinessTimeout, false, log) {
		log.error(fmt.Sprintf("%s was installed, but the environment did not become ready within the timeout after "+
			"the platform's post-install restart. Check the instance, then verify with 'clio call-service "+
			"--service-path %s -m POST -b {} -e <environment>'.", packageName, pkgWriteDashboardsPingRoute))
		return log.result(1)
	}
	if ok, diagnosis := c.pkgWritePingOperational(ctx, packageName, log); !ok {
		if diagnosis == "" {
			diagnosis = fmt.Sprintf("%s was installed, but its Ping route (%s) does not answer, which means the "+
				"environment did not compile the package. Check the environment's configuration build log, and "+
				"verify the bundled archive still contains its Source Code schema — without it the package "+
				"installs but is never compiled.", packageName, pkgWriteDashboardsPingRoute)
		}
		log.error(diagnosis)
		return log.result(1)
	}
	log.line("Done")
	return log.result(0)
}

// PkgWriteNoClioError is the failure a bundled-package tool reports when no clio installation carries the archive.
func PkgWriteNoClioError(packageName string) CommandResult {
	return CommandFailure(fmt.Sprintf("The bundled %s package ships with clio, not with this server, and %s "+
		"(~/.dotnet/tools/.store/clio). Install clio as a .NET tool (dotnet tool install -g clio) so its bundled "+
		"packages can be read, then retry.", packageName, "no clio .NET tool installation was found"))
}
