package creatio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/redact"
)

// themeGetMaxCSSBytes is clio's ThemeParameterValidator.MaxCssContentBytes.
const themeGetMaxCSSBytes = 1024 * 1024

// themeGetEmptyCatalogCaveat is clio's ThemeCatalogMessages.EmptyCatalogLicenseCaveat.
const themeGetEmptyCatalogCaveat = "This can also mean the CanCustomizeBranding license is missing (list-themes returns an empty " +
	"catalog in that case) — verify access with check-theming-access."

// ThemeGetResult is clio's get-theme envelope. cssContent is left out when output-file received it;
// everything but success and error is left out on failure.
type ThemeGetResult struct {
	Success          bool   `json:"success"`
	ID               string `json:"id,omitempty"`
	Caption          string `json:"caption,omitempty"`
	CSSClassName     string `json:"cssClassName,omitempty"`
	CSSFilePath      string `json:"cssFilePath,omitempty"`
	CSSContent       string `json:"cssContent,omitempty"`
	CSSContentLength *int   `json:"cssContentLength,omitempty"`
	Error            string `json:"error,omitempty"`
}

// ThemeGetFailure is clio's GetThemeResponse.Failure, with the redaction clio's tool applies to every
// failure message.
func ThemeGetFailure(message string) ThemeGetResult {
	if strings.TrimSpace(message) == "" {
		return ThemeGetResult{Error: "unknown"}
	}
	return ThemeGetResult{Error: redact.Text(message)}
}

// themeGetEntry is one catalog entry as GetAvailableThemes returns it. get-theme reports id, caption and
// cssClassName unchanged, unlike list-themes, which sanitizes them for display.
type themeGetEntry struct {
	ID           string `json:"id"`
	Caption      string `json:"caption"`
	CSSClassName string `json:"cssClassName"`
	CSSFilePath  string `json:"cssFilePath"`
}

// GetTheme reads a custom theme's metadata through the GetAvailableThemes catalog and its CSS from the
// cssFilePath that catalog publishes, as clio's GetThemeCommand does (ThemeService.svc/GetTheme answers an
// empty body). With outputFile set the CSS is written to that confined path instead of being returned.
func (c *Client) GetTheme(ctx context.Context, id, outputFile string) ThemeGetResult {
	requested, ok := parseGUID(id)
	if strings.TrimSpace(id) == "" {
		return ThemeGetFailure("Theme id is required.")
	}
	if !ok {
		return ThemeGetFailure(fmt.Sprintf("Theme id must be a GUID. Received: '%s'.", id))
	}
	resolvedOutput := ""
	if strings.TrimSpace(outputFile) != "" {
		path, err := themeGetResolveOutputFile(outputFile)
		if err != nil {
			return ThemeGetFailure(err.Error())
		}
		resolvedOutput = path
	}
	theme, err := c.themeGetResolve(ctx, id, requested)
	if err != nil {
		return ThemeGetFailure(err.Error())
	}
	css, err := c.themeGetCSS(ctx, theme)
	if err != nil {
		return ThemeGetFailure(err.Error())
	}
	length := utf16Length(css)
	result := ThemeGetResult{Success: true, ID: theme.ID, Caption: theme.Caption, CSSClassName: theme.CSSClassName,
		CSSFilePath: sanitizeThemeText(theme.CSSFilePath, themeDisplayMaxLength), CSSContentLength: &length}
	if resolvedOutput == "" {
		result.CSSContent = css
		return result
	}
	if err := themeGetWriteFile(resolvedOutput, css); err != nil {
		return ThemeGetFailure(err.Error())
	}
	return result
}

func (c *Client) themeGetResolve(ctx context.Context, id, requested string) (themeGetEntry, error) {
	themes, err := c.themeGetCatalog(ctx)
	if err != nil {
		return themeGetEntry{}, err
	}
	if len(themes) == 0 {
		return themeGetEntry{}, fmt.Errorf("Theme '%s' was not found and no custom themes are listed on this environment. %s", id, themeGetEmptyCatalogCaveat)
	}
	// The id passed .NET's Guid.TryParse, which accepts braced and undashed forms, while the catalog
	// publishes the canonical one, so the parsed values are compared.
	matches := []themeGetEntry{}
	for _, theme := range themes {
		if catalogID, ok := parseGUID(theme.ID); ok && catalogID == requested {
			matches = append(matches, theme)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return themeGetEntry{}, fmt.Errorf("Theme '%s' was not found. Run 'clio list-themes' to see the available theme ids.", id)
	}
	return themeGetEntry{}, fmt.Errorf("Theme id '%s' matches more than one theme on the environment; the catalog is inconsistent. Run 'clio list-themes' to inspect it.", id)
}

// themeGetCatalog reads GetAvailableThemes the way clio's theme catalog does and words a failure with
// clio's ThemeServiceResponseParser.DescribeFailure.
func (c *Client) themeGetCatalog(ctx context.Context) ([]themeGetEntry, error) {
	response, err := c.postCreatioServiceJSON(ctx, "ServiceModel/ThemeService.svc/GetAvailableThemes", []byte("{}"), 100*time.Second, maxResponseBytes)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(string(response)) == "" {
		return nil, nil
	}
	var decoded struct {
		Success   *bool `json:"success"`
		ErrorInfo *struct {
			Message string `json:"message"`
		} `json:"errorInfo"`
		Values []*themeGetEntry `json:"values"`
	}
	if err := json.Unmarshal(response, &decoded); err != nil {
		return nil, fmt.Errorf("GetAvailableThemes failed: Unexpected response from server: %s", sanitizeThemeText(string(response), themeDisplayMaxLength))
	}
	if decoded.Success != nil && !*decoded.Success {
		message := ""
		if decoded.ErrorInfo != nil {
			message = sanitizeThemeText(decoded.ErrorInfo.Message, themeDisplayMaxLength)
		}
		if strings.TrimSpace(message) == "" {
			return nil, errors.New("GetAvailableThemes returned success=false. Check the Creatio application logs for details.")
		}
		return nil, fmt.Errorf("GetAvailableThemes failed: %s", message)
	}
	themes := []themeGetEntry{}
	for _, theme := range decoded.Values {
		if theme != nil {
			themes = append(themes, *theme)
		}
	}
	return themes, nil
}

// themeGetCSS fetches the catalog-reported CSS path. Like clio's ExecuteGetRequest it does not trust the
// status code (clio cannot see it): a transport failure reads as an empty body, and the body itself is
// checked for an HTML page, a JSON error envelope, emptiness and the 1 MiB cap.
func (c *Client) themeGetCSS(ctx context.Context, theme themeGetEntry) (string, error) {
	if strings.TrimSpace(theme.CSSFilePath) == "" {
		return "", fmt.Errorf("Theme '%s' has no CSS file path in the theme catalog; there is no content to read.", theme.ID)
	}
	display := sanitizeThemeText(theme.CSSFilePath, themeDisplayMaxLength)
	if !themeGetIsRelativeCSSPath(theme.CSSFilePath) {
		return "", fmt.Errorf("Theme '%s' reports an unexpected CSS file path in the theme catalog ('%s'); refusing to fetch it.", theme.ID, display)
	}
	content := c.themeGetFetch(ctx, strings.TrimPrefix(theme.CSSFilePath, "/"))
	trimmed := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(content), "\uFEFF"))
	if strings.HasPrefix(trimmed, "<") || strings.HasPrefix(trimmed, "{") {
		return "", fmt.Errorf("The environment returned an HTML page or a JSON error envelope instead of the theme CSS for '%s'. "+
			"The CSS file may be missing on the server or the request was redirected.", theme.ID)
	}
	if strings.TrimSpace(content) == "" {
		return "", fmt.Errorf("The environment served no content for the theme CSS of '%s' ('%s'). The file is missing, empty or "+
			"unreadable on the server, or the request failed.", theme.ID, display)
	}
	if len(content) > themeGetMaxCSSBytes {
		return "", fmt.Errorf("The theme CSS for '%s' exceeds the 1 MiB content limit and cannot be read. "+
			"Themes managed through clio are capped at 1 MiB; the served file is not a clio-managed theme CSS.", theme.ID)
	}
	return content, nil
}

// themeGetIsRelativeCSSPath is clio's IsExpectedRelativeCssPath: no scheme, no protocol-relative prefix and
// no ".." segment, so server-reported catalog data cannot steer the GET at another same-host path.
func themeGetIsRelativeCSSPath(path string) bool {
	if strings.Contains(path, "://") || strings.HasPrefix(path, "//") {
		return false
	}
	pathOnly, _, _ := strings.Cut(path, "?")
	for _, segment := range strings.Split(pathOnly, "/") {
		if segment == ".." {
			return false
		}
	}
	return true
}

// themeGetFetch returns the body for any status and "" for a failed request, which is all clio's
// ExecuteGetRequest exposes. A body over the cap is read one byte past it so the cap check can refuse it;
// a UTF-8 byte-order mark is dropped, as .NET's string decoding drops it.
func (c *Client) themeGetFetch(ctx context.Context, path string) string {
	requestCtx, cancel := context.WithTimeout(ctx, 100*time.Second)
	defer cancel()
	response, _, err := c.doAuthenticated(requestCtx, c.requestClient(), func() (*http.Request, error) {
		return http.NewRequestWithContext(requestCtx, http.MethodGet, c.serviceURL(path), nil)
	})
	if err != nil {
		return ""
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, themeGetMaxCSSBytes+4))
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(string(payload), "\uFEFF")
}

// themeGetResolveOutputFile is clio's OutputPathConfinement.Resolve for a caller-supplied output-file: the
// symlink-resolved path must lie in a trusted workspace anchor or the OS temp directory, never in clio's
// own configuration directory, and must not exist yet.
func themeGetResolveOutputFile(outputFile string) (string, error) {
	full, err := filepath.Abs(outputFile)
	if err != nil {
		return "", fmt.Errorf("output-file '%s' resolves outside the allowed locations; it must be inside the workspace or the OS temp directory.", outputFile)
	}
	home, _ := os.UserHomeDir()
	anchor := themeGetOutputAnchor(home)
	unresolvable := fmt.Errorf("output-file '%s' resolves through an unresolvable symbolic link; refusing to continue.", outputFile)
	realPath, err := themeGetRealPath(full)
	if err != nil {
		return "", unresolvable
	}
	tempRoot, err := themeGetRealPath(themeGetAbs(os.TempDir()))
	if err != nil {
		return "", unresolvable
	}
	clioHome, err := themeGetRealPath(themeGetAbs(clioHomeDirectory()))
	if err != nil {
		return "", unresolvable
	}
	realHome := home
	if home != "" {
		if realHome, err = themeGetRealPath(themeGetAbs(home)); err != nil {
			return "", unresolvable
		}
	}
	if anchor != "" {
		if anchor, err = themeGetRealPath(anchor); err != nil {
			return "", unresolvable
		}
	}
	if themeGetWithin(clioHome, realPath) {
		return "", fmt.Errorf("output-file '%s' resolves inside clio's own configuration directory; refusing to continue.", outputFile)
	}
	if !themeGetTrustedAnchor(anchor, realHome) {
		anchor = ""
	}
	if !themeGetWithin(anchor, realPath) && !themeGetWithin(tempRoot, realPath) {
		return "", fmt.Errorf("output-file '%s' resolves outside the allowed locations; it must be inside the workspace or the OS temp directory.", outputFile)
	}
	if _, err := os.Stat(full); err == nil {
		return "", fmt.Errorf("output-file '%s' already exists; refusing to overwrite it. Choose a different path or remove the existing file.", outputFile)
	}
	return full, nil
}

// themeGetOutputAnchor is clio's PageOutputDirectoryResolver.ResolveAnchor with no home fallback: the
// nearest ancestor holding .clio/workspaceSettings.json, else the current directory, else (when the
// current directory is the bare home directory) no anchor at all. pageOutputAnchor substitutes clio's home
// there, which is right for get-page's own default but not as a boundary for a caller-supplied path.
func themeGetOutputAnchor(home string) string {
	current, err := os.Getwd()
	if err != nil {
		return ""
	}
	for directory := current; ; {
		if info, err := os.Stat(filepath.Join(directory, ".clio", "workspaceSettings.json")); err == nil && !info.IsDir() {
			return directory
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			break
		}
		directory = parent
	}
	if home != "" && strings.EqualFold(strings.TrimRight(filepath.Clean(current), `/\`), strings.TrimRight(filepath.Clean(home), `/\`)) {
		return ""
	}
	return current
}

func themeGetAbs(path string) string {
	if full, err := filepath.Abs(path); err == nil {
		return full
	}
	return path
}

// themeGetRealPath resolves symlinks in the deepest existing ancestor of an absolute path (a dangling link
// counts as existing, so its target is checked too) and re-appends the part that does not exist yet.
func themeGetRealPath(full string) (string, error) {
	return themeGetRealPathDepth(full, 0)
}

// themeGetMaxLinkHops bounds dangling-link resolution; a cycle such as a -> a/child would otherwise recurse
// until the stack is exhausted and the server dies. 40 matches the Linux ELOOP limit.
const themeGetMaxLinkHops = 40

func themeGetRealPathDepth(full string, hops int) (string, error) {
	if hops > themeGetMaxLinkHops {
		return "", fmt.Errorf("too many levels of symbolic links resolving %q", full)
	}
	tail := []string{}
	current := full
	for {
		if _, err := os.Lstat(current); err == nil {
			break
		}
		parent := filepath.Dir(current)
		if parent == current {
			return full, nil
		}
		tail = append(tail, filepath.Base(current))
		current = parent
	}
	resolved, err := filepath.EvalSymlinks(current)
	if err != nil {
		if info, statErr := os.Lstat(current); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
			// A dangling link: confine its target lexically, as clio reads LinkTarget without requiring it.
			target, readErr := os.Readlink(current)
			if readErr != nil {
				return "", readErr
			}
			if !filepath.IsAbs(target) {
				target = filepath.Join(filepath.Dir(current), target)
			}
			parentReal, parentErr := themeGetRealPathDepth(filepath.Dir(target), hops+1)
			if parentErr != nil {
				return "", parentErr
			}
			resolved = filepath.Join(parentReal, filepath.Base(target))
		} else {
			return "", err
		}
	}
	for index := len(tail) - 1; index >= 0; index-- {
		resolved = filepath.Join(resolved, tail[index])
	}
	return filepath.Clean(resolved), nil
}

// themeGetTrustedAnchor refuses an anchor that is a filesystem root or the home directory or one of its
// ancestors: an MCP host started in / would otherwise confine writes to the whole volume.
func themeGetTrustedAnchor(anchor, home string) bool {
	if anchor == "" {
		return false
	}
	if filepath.Dir(anchor) == anchor {
		return false
	}
	return home == "" || !themeGetWithin(anchor, home)
}

// themeGetWithin reports whether target is base itself or lies below it, with the platform's case rule.
func themeGetWithin(base, target string) bool {
	if base == "" {
		return false
	}
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		base, target = strings.ToLower(base), strings.ToLower(target)
	}
	relative, err := filepath.Rel(base, target)
	if err != nil {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

// themeGetWriteFile is clio's OutputPathConfinement.WriteAtomic: the body is written to an owner-only
// sibling temporary file and moved onto the target only if the target still does not exist, so the target
// is only ever absent or complete.
func themeGetWriteFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temporary := path + "." + randomHex(16) + ".tmp"
	file, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := file.WriteString(content)
	closeErr := file.Close()
	if writeErr == nil {
		writeErr = closeErr
	}
	if writeErr == nil {
		if _, statErr := os.Lstat(path); statErr == nil {
			writeErr = fmt.Errorf("output-file '%s' already exists; refusing to overwrite it. Choose a different path or remove the existing file.", path)
		} else {
			writeErr = os.Rename(temporary, path)
		}
	}
	if writeErr != nil {
		_ = os.Remove(temporary)
	}
	return writeErr
}
