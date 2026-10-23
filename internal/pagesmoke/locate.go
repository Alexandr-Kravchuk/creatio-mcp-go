package pagesmoke

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

// InstalledBrowser is a Chromium-based browser found on the machine (clio's InstalledBrowser).
type InstalledBrowser struct {
	Name    string
	Version string // "" when it cannot be read without starting the browser
	Path    string
}

// osLayout names the operating system whose install locations and cache folders are searched; tests set it.
type osLayout int

const (
	layoutLinux osLayout = iota
	layoutMac
	layoutWindows
)

func currentLayout() osLayout {
	switch runtime.GOOS {
	case "darwin":
		return layoutMac
	case "windows":
		return layoutWindows
	default:
		return layoutLinux
	}
}

// environmentReader reads one variable; "" when it is not set.
type environmentReader func(string) string

// fileExists is clio's IFileSystem.ExistsFile (File.Exists): an existing file, not a directory.
func fileExists(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func blank(text string) bool { return strings.TrimSpace(text) == "" }

// ---- ChromiumLocator ----

const chromePathVariable = "CHROME_PATH"

var (
	environmentReference = regexp.MustCompile(`%([^%]+)%`)
	bundleVersion        = regexp.MustCompile(`<key>CFBundleShortVersionString</key>\s*<string>([^<]+)</string>`)
)

// chromiumLocator is clio's ChromiumLocator: CHROME_PATH and the standard install locations per OS.
type chromiumLocator struct {
	env    environmentReader
	layout osLayout
}

// chromePathOverride is CHROME_PATH when it names an existing file; otherwise "".
func (l chromiumLocator) chromePathOverride() string {
	fromEnv := l.env(chromePathVariable)
	if !blank(fromEnv) && fileExists(fromEnv) {
		return fromEnv
	}
	return ""
}

// findInstalled is every browser found in the standard locations, in preference order.
func (l chromiumLocator) findInstalled() []InstalledBrowser {
	var found []InstalledBrowser
	seen := map[string]bool{}
	for _, candidate := range l.candidatePaths() {
		if blank(candidate) || !fileExists(candidate) {
			continue
		}
		key := candidate
		if l.layout == layoutWindows {
			key = strings.ToLower(candidate)
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		found = append(found, InstalledBrowser{Name: browserNameOf(candidate), Version: l.readVersion(candidate), Path: candidate})
	}
	return found
}

func browserNameOf(path string) string {
	lower := strings.ToLower(path)
	switch {
	case strings.Contains(lower, "edge"):
		return "Microsoft Edge"
	case strings.Contains(lower, "brave"):
		return "Brave"
	case strings.Contains(lower, "chromium"):
		return "Chromium"
	default:
		return "Google Chrome"
	}
}

// readVersion is the version a user recognises: the app bundle's on macOS, the executable's product version on
// Windows; on Linux it would take starting the browser, so it is left out.
func (l chromiumLocator) readVersion(path string) string {
	switch l.layout {
	case layoutMac:
		app := strings.Index(path, ".app/")
		if app < 0 {
			return ""
		}
		plist := path[:app+4] + "/Contents/Info.plist"
		if !fileExists(plist) {
			return ""
		}
		data, err := os.ReadFile(plist)
		if err != nil {
			return ""
		}
		match := bundleVersion.FindSubmatch(data)
		if match == nil {
			return ""
		}
		return dotnetTrim(string(match[1]))
	case layoutWindows:
		if runtime.GOOS == "windows" {
			return productVersion(path)
		}
	}
	return ""
}

func (l chromiumLocator) candidatePaths() []string {
	switch l.layout {
	case layoutMac:
		return []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
			"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser",
		}
	case layoutWindows:
		return []string{
			l.expand(`%ProgramFiles%\Google\Chrome\Application\chrome.exe`),
			l.expand(`%ProgramFiles(x86)%\Google\Chrome\Application\chrome.exe`),
			l.expand(`%LocalAppData%\Google\Chrome\Application\chrome.exe`),
			l.expand(`%ProgramFiles(x86)%\Microsoft\Edge\Application\msedge.exe`),
			l.expand(`%ProgramFiles%\Microsoft\Edge\Application\msedge.exe`),
		}
	default:
		return []string{
			"/usr/bin/google-chrome",
			"/usr/bin/google-chrome-stable",
			"/usr/bin/chromium",
			"/usr/bin/chromium-browser",
			"/usr/bin/microsoft-edge",
			"/snap/bin/chromium",
		}
	}
}

// expand replaces %NAME% from the environment this locator reads; a variable that is not set is left as written.
func (l chromiumLocator) expand(path string) string {
	return environmentReference.ReplaceAllStringFunc(path, func(match string) string {
		if value := l.env(match[1 : len(match)-1]); value != "" {
			return value
		}
		return match
	})
}

// ---- BrowserCacheLocator ----

const (
	playwrightBrowsersPathVariable = "PLAYWRIGHT_BROWSERS_PATH"
	puppeteerCacheDirVariable      = "PUPPETEER_CACHE_DIR"
	maxCacheSearchDepth            = 4
)

var (
	headlessShellNames = []string{"chrome-headless-shell", "chrome-headless-shell.exe", "headless_shell", "headless_shell.exe"}
	chromiumNames      = []string{"chrome", "chrome.exe", "Chromium", "Google Chrome for Testing"}
)

// cacheLocator is clio's BrowserCacheLocator: the Chromium builds Playwright and Puppeteer already downloaded.
type cacheLocator struct {
	env    environmentReader
	layout osLayout
}

// findCached lists headless shells first (Playwright's, then Puppeteer's), then full Chromium builds; newest
// first within each group.
func (l cacheLocator) findCached() []InstalledBrowser {
	playwright, puppeteer := l.playwrightRoot(), l.puppeteerRoot()
	var found []InstalledBrowser
	found = append(found, l.playwright(playwright, "chromium_headless_shell-", headlessShellNames, "chrome-headless-shell (Playwright cache)")...)
	found = append(found, l.puppeteer(puppeteer, "chrome-headless-shell", headlessShellNames, "chrome-headless-shell (Puppeteer cache)")...)
	found = append(found, l.playwright(playwright, "chromium-", chromiumNames, "Chromium (Playwright cache)")...)
	found = append(found, l.puppeteer(puppeteer, "chrome", chromiumNames, "Chrome for Testing (Puppeteer cache)")...)
	return found
}

func (l cacheLocator) playwrightRoot() string {
	if overridden := l.env(playwrightBrowsersPathVariable); !blank(overridden) {
		if dotnetTrim(overridden) == "0" {
			return ""
		}
		return dotnetTrim(overridden)
	}
	if l.layout == layoutWindows {
		local := l.env("LOCALAPPDATA")
		if blank(local) {
			return ""
		}
		return filepath.Join(local, "ms-playwright")
	}
	home := l.env("HOME")
	if blank(home) {
		return ""
	}
	if l.layout == layoutMac {
		return filepath.Join(home, "Library", "Caches", "ms-playwright")
	}
	cache := l.env("XDG_CACHE_HOME")
	if blank(cache) {
		cache = filepath.Join(home, ".cache")
	}
	return filepath.Join(cache, "ms-playwright")
}

func (l cacheLocator) puppeteerRoot() string {
	if overridden := l.env(puppeteerCacheDirVariable); !blank(overridden) {
		return dotnetTrim(overridden)
	}
	variable := "HOME"
	if l.layout == layoutWindows {
		variable = "USERPROFILE"
	}
	home := l.env(variable)
	if blank(home) {
		return ""
	}
	return filepath.Join(home, ".cache", "puppeteer")
}

// playwright lists <root>/<prefix><revision>/... that Playwright marked complete, newest revision first.
func (l cacheLocator) playwright(root, prefix string, names []string, label string) []InstalledBrowser {
	if root == "" || !directoryExists(root) {
		return nil
	}
	type candidate struct {
		dir      string
		revision int
	}
	var candidates []candidate
	for _, dir := range listDirectories(root) {
		revision, ok := playwrightRevision(filepath.Base(dir), prefix)
		if ok && fileExists(filepath.Join(dir, "INSTALLATION_COMPLETE")) {
			candidates = append(candidates, candidate{dir, revision})
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].revision > candidates[j].revision })
	var found []InstalledBrowser
	for _, item := range candidates {
		if executable := findExecutable(item.dir, names, 0); executable != "" {
			found = append(found, InstalledBrowser{Name: label, Version: "Playwright build " + strconv.Itoa(item.revision), Path: executable})
		}
	}
	return found
}

// puppeteer lists <root>/<product>/<platform>-<version>/..., newest version first.
func (l cacheLocator) puppeteer(root, product string, names []string, label string) []InstalledBrowser {
	if root == "" {
		return nil
	}
	productRoot := filepath.Join(root, product)
	if !directoryExists(productRoot) {
		return nil
	}
	type candidate struct {
		dir     string
		version dotnetVersion
	}
	var candidates []candidate
	for _, dir := range listDirectories(productRoot) {
		name := filepath.Base(dir)
		dash := strings.LastIndex(name, "-")
		if dash < 0 {
			continue
		}
		if version, ok := parseDotnetVersion(name[dash+1:]); ok {
			candidates = append(candidates, candidate{dir, version})
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].version.compare(candidates[j].version) > 0 })
	var found []InstalledBrowser
	for _, item := range candidates {
		if executable := findExecutable(item.dir, names, 0); executable != "" {
			found = append(found, InstalledBrowser{Name: label, Version: item.version.String(), Path: executable})
		}
	}
	return found
}

// playwrightRevision parses <prefix><digits> as int.TryParse with NumberStyles.None does: digits only, in range.
func playwrightRevision(name, prefix string) (int, bool) {
	if !strings.HasPrefix(name, prefix) {
		return 0, false
	}
	digits := name[len(prefix):]
	if digits == "" {
		return 0, false
	}
	for _, r := range digits {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	value, err := strconv.ParseInt(digits, 10, 32)
	if err != nil {
		return 0, false
	}
	return int(value), true
}

// findExecutable looks a few folders down (an app bundle on macOS); the search stays shallow.
func findExecutable(directory string, names []string, depth int) string {
	for _, file := range listFiles(directory) {
		base := filepath.Base(file)
		for _, name := range names {
			if base == name {
				return file
			}
		}
	}
	if depth >= maxCacheSearchDepth {
		return ""
	}
	for _, child := range listDirectories(directory) {
		if executable := findExecutable(child, names, depth+1); executable != "" {
			return executable
		}
	}
	return ""
}

func directoryExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// listDirectories and listFiles are Directory.GetDirectories / GetFiles: full paths, nothing on a read failure. A
// symbolic link counts by what it points to, as .NET's enumeration reports it.
func listDirectories(directory string) []string { return listEntries(directory, true) }

func listFiles(directory string) []string { return listEntries(directory, false) }

func listEntries(directory string, directories bool) []string {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil
	}
	var paths []string
	for _, entry := range entries {
		path := filepath.Join(directory, entry.Name())
		isDir := entry.IsDir()
		if entry.Type()&os.ModeSymlink != 0 {
			info, err := os.Stat(path)
			isDir = err == nil && info.IsDir()
		}
		if isDir == directories {
			paths = append(paths, path)
		}
	}
	return paths
}

// dotnetVersion is System.Version: two to four non-negative components; a missing one sorts below zero.
type dotnetVersion struct{ parts []int }

func parseDotnetVersion(text string) (dotnetVersion, bool) {
	fields := strings.Split(text, ".")
	if len(fields) < 2 || len(fields) > 4 {
		return dotnetVersion{}, false
	}
	parts := make([]int, 0, len(fields))
	for _, field := range fields {
		trimmed := dotnetTrim(field)
		if trimmed == "" {
			return dotnetVersion{}, false
		}
		if trimmed[0] == '+' {
			trimmed = trimmed[1:]
		}
		value, err := strconv.ParseInt(trimmed, 10, 32)
		if err != nil || value < 0 {
			return dotnetVersion{}, false
		}
		parts = append(parts, int(value))
	}
	return dotnetVersion{parts}, true
}

func (v dotnetVersion) component(index int) int {
	if index < len(v.parts) {
		return v.parts[index]
	}
	return -1
}

func (v dotnetVersion) compare(other dotnetVersion) int {
	for index := 0; index < 4; index++ {
		left, right := v.component(index), other.component(index)
		if left != right {
			if left < right {
				return -1
			}
			return 1
		}
	}
	return 0
}

func (v dotnetVersion) String() string {
	texts := make([]string, len(v.parts))
	for i, part := range v.parts {
		texts[i] = strconv.Itoa(part)
	}
	return strings.Join(texts, ".")
}

// dotnetTrim is .NET string.Trim(): Unicode white space at both ends.
func dotnetTrim(text string) string { return strings.TrimSpace(text) }
