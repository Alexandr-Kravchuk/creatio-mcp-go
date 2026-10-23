package pagesmoke

import (
	"archive/zip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
)

// The chrome-headless-shell build page-smoke downloads when the user chooses a download (HeadlessShellRelease):
// one Chrome for Testing release, pinned with each archive's size and SHA-256, measured by clio when it pinned
// the version (Chrome for Testing publishes no checksums).
const (
	headlessShellVersion     = "155.0.8059.79"
	headlessShellMirrorVar   = "CLIO_CHROME_FOR_TESTING_MIRROR"
	headlessShellDefaultBase = "https://storage.googleapis.com/chrome-for-testing-public"
	headlessShellDisplayName = "chrome-headless-shell from Google's Chrome for Testing"
)

type shellArchive struct {
	Size   int64
	Sha256 string
}

var headlessShellArchives = map[string]shellArchive{
	"linux-arm64": {124_715_442, "72d938cabad9674610939c72ef2d3faf20214a4418c3b4e94c1cebca683e767b"},
	"linux64":     {124_406_717, "4cea4c32b501aa43cc2b81eb45734af991a6a82feded48380d6dfaf96642a1c1"},
	"mac-arm64":   {102_390_341, "5f51504ead3d41815111cd73e79c4979df3a4e6aedc7cf6fe2b042ae037363b5"},
	"mac-x64":     {108_042_306, "36a1da728fc936770a0edc52989b74d8d3a194e8747f490e372a210853ad831c"},
	"win32":       {118_265_419, "6b008fa3838b64166c27202f2074843eb59d1e21d97d6fdb3eb8de53f43983a9"},
	"win64":       {125_212_001, "c4cc49fa7908cf018600b04c95918d336565d90f9c72241fd83dacd9ec41855c"},
}

// platformKey is HeadlessShellRelease.PlatformKey for an OS and architecture; "" when none is published.
// Windows on Arm runs the x64 build under emulation.
func platformKey(goos, goarch string) string {
	switch goos {
	case "darwin":
		if goarch == "arm64" {
			return "mac-arm64"
		}
		return "mac-x64"
	case "windows":
		if goarch == "386" {
			return "win32"
		}
		return "win64"
	case "linux":
		switch goarch {
		case "amd64":
			return "linux64"
		case "arm64":
			return "linux-arm64"
		}
		return ""
	default:
		// clio treats every other Unix as Linux.
		switch goarch {
		case "amd64":
			return "linux64"
		case "arm64":
			return "linux-arm64"
		}
		return ""
	}
}

// archiveURL is {base}/{version}/{platform}/chrome-headless-shell-{platform}.zip.
func archiveURL(base, key string) string {
	return strings.TrimRight(base, "/") + "/" + headlessShellVersion + "/" + key + "/chrome-headless-shell-" + key + ".zip"
}

// executableRelativePath is the executable inside the extracted archive.
func executableRelativePath(key string) string {
	name := "chrome-headless-shell"
	if strings.HasPrefix(key, "win") {
		name += ".exe"
	}
	return filepath.Join("chrome-headless-shell-"+key, name)
}

// installError is HeadlessShellInstallException; incomplete is set for HeadlessShellDownloadIncompleteException.
type installError struct {
	message    string
	incomplete bool
	downloaded int64
	total      int64
}

func (e *installError) Error() string { return e.message }

func installFailure(message string) *installError { return &installError{message: message} }

// headlessShellInstaller is clio's HeadlessShellInstaller over ChromeForTestingHeadlessShellSource: it downloads
// the pinned build into <clio home>/browsers/<version>/, verified by size and SHA-256, and finds it there.
type headlessShellInstaller struct {
	root     string // <clio home>/browsers
	key      string // the platform key; "" when no build is published
	env      environmentReader
	client   func() *http.Client
	now      func() time.Time
	budget   time.Duration
	osString string // RuntimeInformation.OSDescription and architecture, for the no-build refusal
}

const (
	downloadBudget     = 90 * time.Second
	leftoverAge        = 10 * time.Minute
	copyBufferBytes    = 81_920
	proxyLoginHintText = "The proxy asked for a login (HTTP 407): set HTTPS_PROXY to the proxy with its credentials " +
		"(https_proxy=http://user:password@proxy:port); clio does not send the Windows login to a system or auto-detected proxy."
)

func newHeadlessShellInstaller(clioHome string, env environmentReader) *headlessShellInstaller {
	return &headlessShellInstaller{
		root:     filepath.Join(clioHome, "browsers"),
		key:      platformKey(runtime.GOOS, runtime.GOARCH),
		env:      env,
		client:   func() *http.Client { return &http.Client{Transport: downloadTransport(env)} },
		now:      time.Now,
		budget:   downloadBudget,
		osString: runtime.GOOS + " (" + runtime.GOARCH + ")",
	}
}

func (i *headlessShellInstaller) installDirectory() string {
	return filepath.Join(i.root, headlessShellVersion)
}

// downloadArchive is the pinned archive of this platform; ok false when none is published.
func (i *headlessShellInstaller) downloadArchive() (shellArchive, bool) {
	if i.key == "" {
		return shellArchive{}, false
	}
	archive, ok := headlessShellArchives[i.key]
	return archive, ok
}

// findInstalled is the executable of the pinned version when it is installed; otherwise "".
func (i *headlessShellInstaller) findInstalled() string {
	if i.key == "" {
		return ""
	}
	executable := filepath.Join(i.installDirectory(), executableRelativePath(i.key))
	if fileExists(executable) {
		return executable
	}
	return ""
}

// sourceURL is ChromeForTestingHeadlessShellSource.ArchiveUrl: the default base, or the mirror.
func (i *headlessShellInstaller) sourceURL() (*url.URL, error) {
	mirror := i.env(headlessShellMirrorVar)
	base := headlessShellDefaultBase
	if !blank(mirror) {
		parsed, err := url.Parse(dotnetTrim(mirror))
		if err != nil || !parsed.IsAbs() || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
			return nil, installFailure(headlessShellMirrorVar + " must be an absolute http(s) URL, the base the archives sit under (" +
				headlessShellDefaultBase + " is the default); fix or unset it, then call page-smoke again.")
		}
		base = absoluteURI(parsed)
	}
	return url.Parse(archiveURL(base, i.key))
}

// absoluteURI is Uri.AbsoluteUri for an http(s) base: the path is at least "/".
func absoluteURI(parsed *url.URL) string {
	copied := *parsed
	if copied.Path == "" {
		copied.Path = "/"
	}
	return copied.String()
}

// hostOf is HeadlessShellInstaller.HostOf: scheme, host and port only, never the user info.
func hostOf(target *url.URL) string {
	host := target.Hostname()
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port := target.Port(); port != "" && port != defaultPort(target.Scheme) {
		return target.Scheme + "://" + host + ":" + port
	}
	return target.Scheme + "://" + host
}

func defaultPort(scheme string) string {
	if scheme == "https" {
		return "443"
	}
	return "80"
}

var urlUserInfo = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.\-]*://)[^/@\s'"]+@`)

// withoutURLCredentials drops a scheme://user:password@ anywhere in a transport message.
func withoutURLCredentials(text string) string { return urlUserInfo.ReplaceAllString(text, "$1") }

func megabytesFloor(bytes int64) int64 { return bytes / (1024 * 1024) }

func networkHint() string {
	return "The download goes through HTTPS_PROXY / HTTP_PROXY when they are set and the system proxy (PAC included) " +
		"otherwise, with NO_PROXY honoured. On a network that reaches only a mirror of Chrome for Testing, set " +
		headlessShellMirrorVar + " to its base URL."
}

func (i *headlessShellInstaller) partialPath() string {
	return filepath.Join(i.root, ".partial-"+headlessShellVersion+"-"+i.key+".zip")
}

type downloadProgress struct {
	bytes     int64
	added     int64
	receiving bool
}

// install downloads, verifies and installs the pinned version unless it is already installed.
func (i *headlessShellInstaller) install(ctx context.Context) (string, error) {
	if installed := i.findInstalled(); installed != "" {
		return installed, nil
	}
	archive, ok := i.downloadArchive()
	if !ok {
		return "", installFailure(fmt.Sprintf("There is no %s build for %s; install a Chromium-based browser and choose it instead.",
			headlessShellDisplayName, i.osString))
	}
	budget, cancel := context.WithTimeout(ctx, i.budget)
	defer cancel()
	partial := i.partialPath()
	extract := filepath.Join(i.root, ".extract-"+newHexID())
	defer os.RemoveAll(extract)
	host := headlessShellDisplayName
	progress := &downloadProgress{}
	var keptBefore int64
	fail := func(err error) (string, error) {
		var install *installError
		if errors.As(err, &install) {
			return "", install
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if budget.Err() != nil {
			return "", i.stopped(progress, keptBefore, archive, host,
				fmt.Sprintf("the call's %d s ran out", int(i.budget/time.Second)), nil)
		}
		var transfer *transferError
		if errors.As(err, &transfer) {
			if transfer.receiving {
				return "", i.stopped(progress, keptBefore, archive, host, "the transfer from "+host+" broke off ("+transfer.Error()+")", transfer)
			}
			return "", i.stopped(progress, keptBefore, archive, host, "the connection to "+host+" failed ("+transfer.Error()+")", transfer)
		}
		var unpack *unpackError
		if errors.As(err, &unpack) {
			_ = os.Remove(partial)
			return "", installFailure(fmt.Sprintf("%s %s could not be unpacked: %s The download was deleted; nothing was installed; "+
				"call page-smoke again to retry.", headlessShellDisplayName, headlessShellVersion, unpack.Error()))
		}
		return "", installFailure(fmt.Sprintf("%s %s could not be installed into %s: %s Nothing was installed; call page-smoke again to retry.",
			headlessShellDisplayName, headlessShellVersion, i.root, err.Error()))
	}
	source, err := i.sourceURL()
	if err != nil {
		return fail(err)
	}
	host = hostOf(source)
	if err := os.MkdirAll(i.root, 0o777); err != nil {
		return fail(err)
	}
	i.deleteLeftovers(partial)
	keptBefore = keptBytes(partial)
	if err := i.download(budget, source, host, archive, partial, progress); err != nil {
		return fail(err)
	}
	if err := i.verify(partial, archive); err != nil {
		return fail(err)
	}
	if err := extractArchive(budget, partial, extract); err != nil {
		return fail(err)
	}
	relative := executableRelativePath(i.key)
	if !fileExists(filepath.Join(extract, relative)) {
		_ = os.Remove(partial)
		return fail(installFailure("The downloaded archive has no " + relative + "."))
	}
	if err := i.publish(extract); err != nil {
		return fail(err)
	}
	_ = os.Remove(partial)
	if installed := i.findInstalled(); installed != "" {
		return installed, nil
	}
	return fail(installFailure("The headless browser was extracted but is not at " + i.installDirectory() + "."))
}

// stopped is HeadlessShellInstaller.Stopped: progress made on this call is resumable work (browser-downloading);
// a call that added nothing is a failure even with an older partial kept.
func (i *headlessShellInstaller) stopped(progress *downloadProgress, keptBefore int64, archive shellArchive, host, reason string, cause error) error {
	reason = withoutURLCredentials(reason)
	if progress.added > 0 {
		return &installError{incomplete: true, downloaded: progress.bytes, total: archive.Size,
			message: fmt.Sprintf("Downloaded %d of %d MB of %s %s before %s. What arrived is kept and the next call resumes from there: "+
				"call page-smoke again with the same arguments, without browser, to continue.",
				megabytesFloor(progress.bytes), megabytesFloor(archive.Size), headlessShellDisplayName, headlessShellVersion, reason)}
	}
	kept := ""
	if keptBefore > 0 {
		kept = fmt.Sprintf(" %d MB from an earlier call are kept for when it works again.", megabytesFloor(keptBefore))
	}
	advice := " The machine may be offline or the host blocked." + kept + " " + networkHint()
	var transfer *transferError
	if errors.As(cause, &transfer) && transfer.proxyLogin {
		advice = kept + " " + proxyLoginHintText
	}
	return installFailure(fmt.Sprintf("%s could not be downloaded from %s: %s; nothing arrived on this call.%s",
		headlessShellDisplayName, host, reason, advice))
}

// transferError is a failed connection or transfer (HttpRequestException, or an IOException while receiving).
type transferError struct {
	message    string
	receiving  bool
	proxyLogin bool
}

func (e *transferError) Error() string { return e.message }

// unpackError is an archive that is not a readable zip (InvalidDataException).
type unpackError struct{ message string }

func (e *unpackError) Error() string { return e.message }

func keptBytes(path string) int64 {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return 0
	}
	return info.Size()
}

// download appends to the kept partial file, resuming at its length when the server honours Range (206) and
// starting over when it does not (200). The size is checked as bytes arrive.
func (i *headlessShellInstaller) download(ctx context.Context, source *url.URL, host string, archive shellArchive, partial string,
	progress *downloadProgress) error {
	target, err := os.OpenFile(partial, os.O_RDWR|os.O_CREATE, 0o666)
	if err != nil {
		return err
	}
	defer target.Close()
	info, err := target.Stat()
	if err != nil {
		return err
	}
	existing := info.Size()
	if existing > archive.Size {
		if err := target.Truncate(0); err != nil {
			return err
		}
		existing = 0
	}
	progress.bytes = existing
	if existing == archive.Size {
		return nil
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, source.String(), nil)
	if err != nil {
		return &transferError{message: err.Error()}
	}
	if existing > 0 {
		request.Header.Set("Range", "bytes="+strconv.FormatInt(existing, 10)+"-")
	}
	response, err := i.client().Do(request)
	if err != nil {
		return describeTransportFailure(err)
	}
	defer response.Body.Close()
	resumeAt, err := i.resumeOffset(response, existing, archive, host, target)
	if err != nil {
		return err
	}
	progress.bytes = resumeAt
	if _, err := target.Seek(resumeAt, io.SeekStart); err != nil {
		return err
	}
	return copyBody(response.Body, target, archive, progress)
}

func (i *headlessShellInstaller) resumeOffset(response *http.Response, existing int64, archive shellArchive, host string, target *os.File) (int64, error) {
	if response.StatusCode == http.StatusPartialContent && existing > 0 {
		from, length, ok := parseContentRange(response.Header.Get("Content-Range"))
		if !ok || from != existing || length != archive.Size {
			_ = target.Truncate(0)
			return 0, i.mismatch(fmt.Sprintf("the server resumed it as %s, expected bytes %d-/%d",
				response.Header.Get("Content-Range"), existing, archive.Size))
		}
		return existing, nil
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		_ = target.Truncate(0)
		hint := networkHint()
		if response.StatusCode == http.StatusProxyAuthRequired {
			hint = proxyLoginHintText
		}
		return 0, installFailure(fmt.Sprintf("Downloading %s %s from %s failed: HTTP %d %s. %s", headlessShellDisplayName,
			headlessShellVersion, host, response.StatusCode, reasonPhrase(response), hint))
	}
	if err := target.Truncate(0); err != nil {
		return 0, err
	}
	if response.ContentLength >= 0 && response.ContentLength != archive.Size {
		return 0, i.mismatch(fmt.Sprintf("its size is %d bytes, expected %d", response.ContentLength, archive.Size))
	}
	return 0, nil
}

// reasonPhrase is HttpResponseMessage.ReasonPhrase: the status line's text.
func reasonPhrase(response *http.Response) string {
	status := response.Status
	if space := strings.IndexByte(status, ' '); space >= 0 {
		return status[space+1:]
	}
	return http.StatusText(response.StatusCode)
}

// parseContentRange reads "bytes <from>-<to>/<length>".
func parseContentRange(header string) (int64, int64, bool) {
	header = strings.TrimSpace(header)
	if !strings.HasPrefix(header, "bytes ") {
		return 0, 0, false
	}
	rangePart, lengthPart, ok := strings.Cut(header[len("bytes "):], "/")
	if !ok {
		return 0, 0, false
	}
	fromText, _, ok := strings.Cut(rangePart, "-")
	if !ok {
		return 0, 0, false
	}
	from, err1 := strconv.ParseInt(strings.TrimSpace(fromText), 10, 64)
	length, err2 := strconv.ParseInt(strings.TrimSpace(lengthPart), 10, 64)
	return from, length, err1 == nil && err2 == nil
}

func copyBody(source io.Reader, target *os.File, archive shellArchive, progress *downloadProgress) error {
	buffer := make([]byte, copyBufferBytes)
	progress.receiving = true
	for {
		read, err := source.Read(buffer)
		if read > 0 {
			if progress.bytes+int64(read) > archive.Size {
				_ = target.Truncate(0)
				return mismatchError(fmt.Sprintf("it is larger than the expected %d bytes", archive.Size))
			}
			if _, writeErr := target.Write(buffer[:read]); writeErr != nil {
				return writeErr
			}
			progress.bytes += int64(read)
			progress.added += int64(read)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return &transferError{message: err.Error(), receiving: true}
		}
	}
	progress.receiving = false
	if progress.bytes != archive.Size {
		return &transferError{message: fmt.Sprintf("the server ended the body at %d of %d bytes", progress.bytes, archive.Size)}
	}
	return nil
}

// describeTransportFailure words a failed request the way .NET's HttpRequestException reads where the cause is
// a refused connection or an unknown host; any other cause keeps Go's text.
func describeTransportFailure(err error) error {
	failure := &transferError{message: creatio.ClioTransportText(err)}
	var urlError *url.Error
	if errors.As(err, &urlError) && failure.message == urlError.Error() {
		failure.message = urlError.Err.Error()
	}
	failure.proxyLogin = strings.Contains(err.Error(), "407 Proxy Authentication Required")
	return failure
}

func (i *headlessShellInstaller) mismatch(detail string) error { return mismatchError(detail) }

func mismatchError(detail string) error {
	return installFailure(fmt.Sprintf("The downloaded %s %s archive did not verify: %s. It was deleted and nothing was installed; "+
		"calling page-smoke again downloads it from the start.", headlessShellDisplayName, headlessShellVersion, detail))
}

func (i *headlessShellInstaller) verify(partial string, archive shellArchive) error {
	file, err := os.Open(partial)
	if err != nil {
		return err
	}
	hash := sha256.New()
	_, err = io.Copy(hash, file)
	file.Close()
	if err != nil {
		return err
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if actual != archive.Sha256 {
		_ = os.Remove(partial)
		return i.mismatch(fmt.Sprintf("its SHA-256 is %s, expected %s", actual, archive.Sha256))
	}
	return nil
}

// extractArchive lands every entry inside the extraction directory (no "..", no absolute path) and restores the
// executable bits recorded in the archive on Unix.
func extractArchive(ctx context.Context, archivePath, directory string) error {
	root, err := filepath.Abs(directory)
	if err != nil {
		return err
	}
	root += string(filepath.Separator)
	if err := os.MkdirAll(directory, 0o777); err != nil {
		return err
	}
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return &unpackError{message: err.Error()}
	}
	defer reader.Close()
	for _, entry := range reader.File {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		destination, err := filepath.Abs(filepath.Join(directory, entry.Name))
		if err != nil {
			return err
		}
		if !strings.HasPrefix(destination, root) {
			return installFailure("The chrome-headless-shell archive has an entry outside its folder: " + entry.Name + ".")
		}
		if strings.HasSuffix(entry.Name, "/") {
			if err := os.MkdirAll(destination, 0o777); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0o777); err != nil {
			return err
		}
		if err := extractEntry(entry, destination); err != nil {
			return err
		}
		mode := (entry.ExternalAttrs >> 16) & 0o777
		if mode != 0 && runtime.GOOS != "windows" {
			if err := os.Chmod(destination, os.FileMode(mode)); err != nil {
				return err
			}
		}
	}
	return nil
}

func extractEntry(entry *zip.File, destination string) error {
	source, err := entry.Open()
	if err != nil {
		return &unpackError{message: err.Error()}
	}
	defer source.Close()
	target, err := os.Create(destination)
	if err != nil {
		return err
	}
	if _, err := io.Copy(target, source); err != nil {
		target.Close()
		if errors.Is(err, zip.ErrChecksum) || errors.Is(err, zip.ErrFormat) {
			return &unpackError{message: err.Error()}
		}
		return err
	}
	return target.Close()
}

// publish makes the version directory appear whole or not at all; another call that installed it first wins, and
// a version directory without the executable is replaced.
func (i *headlessShellInstaller) publish(extract string) error {
	if directoryExists(i.installDirectory()) {
		if i.findInstalled() != "" {
			return nil
		}
		if err := os.RemoveAll(i.installDirectory()); err != nil {
			return err
		}
	}
	if err := os.Rename(extract, i.installDirectory()); err != nil {
		if directoryExists(i.installDirectory()) {
			return nil
		}
		return err
	}
	return nil
}

// deleteLeftovers removes extraction folders a killed run left and partial downloads of another version or platform.
func (i *headlessShellInstaller) deleteLeftovers(partial string) {
	entries, err := os.ReadDir(i.root)
	if err != nil {
		return
	}
	cutoff := i.now().Add(-leftoverAge)
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, ".") || !strings.Contains(name[1:], "-") {
			continue
		}
		path := filepath.Join(i.root, name)
		staleExtract := false
		if strings.HasPrefix(name, ".extract-") {
			if info, err := os.Stat(path); err == nil && info.ModTime().Before(cutoff) {
				staleExtract = true
			}
		}
		otherPartial := strings.HasPrefix(name, ".partial-") && path != partial
		if staleExtract || otherPartial {
			_ = os.RemoveAll(path)
		}
	}
}

func newHexID() string {
	buffer := make([]byte, 16)
	_, _ = rand.Read(buffer)
	return hex.EncodeToString(buffer)
}
