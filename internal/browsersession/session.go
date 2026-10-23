package browsersession

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
)

// The environment's browser session, clio 8.1.0.142's BrowserSessionService over BrowserSessionCache: a forms
// login whose cookies are kept as a Playwright storageState file under <clio home>/sessions and reused while
// the Shell still accepts them. get-browser-session and clear-browser-session use it, and so does page-smoke
// (the page-smoke branch carries the first copy of this file, internal/pagesmoke/session.go; at integration it
// should call this package, so that every tool writes the same file the same way).

const (
	sessionsDirectory = "sessions"
	sessionFileSuffix = ".storageState.json"
	loginTimeout      = 30 * time.Second
	// sessionExpiredScanChars is ReauthExecutor.MaxBodyScanCharacters.
	sessionExpiredScanChars = 4096
)

// BrowserCookie is one cookie in Playwright storageState shape.
type BrowserCookie struct {
	Name     string
	Value    string
	Domain   string
	Path     string
	HTTPOnly bool
	Secure   bool
	SameSite string
	Expires  float64 // Unix seconds, -1 for a session cookie
}

// authenticationError is CreatioAuthenticationException: a sanitized message naming the environment only.
type authenticationError struct{ message string }

func (e *authenticationError) Error() string { return e.message }

func invalidCredentials(uri string) error {
	return &authenticationError{"authentication failed for environment '" + uri + "' — check username and password in env config"}
}

func missingFormsCredentials(uri string) error {
	return &authenticationError{"browser session handoff requires forms-auth credentials (login + password) for environment '" +
		uri + "' — OAuth-only or incomplete environments are not supported"}
}

func connectivity(uri string) error {
	return &authenticationError{"could not reach Creatio at '" + uri + "' — check the environment URL and network connectivity"}
}

func noCookies(uri string) error {
	return &authenticationError{"authentication for environment '" + uri + "' returned no session cookies"}
}

// Environment is what the session is keyed on and logs in with: the registered environment's address,
// forms credentials, OAuth client id (part of the key only) and runtime.
type Environment struct {
	URI, Login, Password, ClientID string
	IsNetCore                      bool
}

// Service is BrowserSessionService without an output path (the MCP tools do not expose one).
type Service struct {
	Env       Environment
	Transport http.RoundTripper
	ClioHome  string // the clio settings folder; sessions live in its "sessions" directory
	Now       func() time.Time
}

func (s Service) clock() time.Time {
	if s.Now == nil {
		return time.Now()
	}
	return s.Now()
}

// IsAuthenticationError reports a CreatioAuthenticationException: a sanitized message naming the environment.
func IsAuthenticationError(err error) bool {
	var target *authenticationError
	return errors.As(err, &target)
}

// SessionPath is GetSessionPathAsync: the storageState file of a live session, logging in when there is no
// cached one, it is stale, or forceRefresh asks for a new login.
func (s Service) SessionPath(ctx context.Context, forceRefresh bool) (string, error) {
	if s.Env.Login == "" || s.Env.Password == "" {
		return "", missingFormsCredentials(s.Env.URI)
	}
	path := s.CachePath()
	if !forceRefresh && fileExists(path) {
		cookies, err := s.tryReuse(ctx, path)
		if err != nil {
			return "", err
		}
		if cookies != nil {
			if err := writeOwnerOnlyAtomic(path, serializeStorageState(cookies)); err != nil {
				return "", err
			}
			return path, nil
		}
		_ = os.Remove(path)
	}
	cookies, err := s.login(ctx)
	if err != nil {
		return "", err
	}
	if err := writeOwnerOnlyAtomic(path, serializeStorageState(cookies)); err != nil {
		return "", err
	}
	return path, nil
}

// Clear is ClearSessionAsync: the cached file is deleted; a missing one is not an error.
func (s Service) Clear() error {
	err := os.Remove(s.CachePath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// CachePath is BrowserSessionCache.GetPath(BuildKey(env)): the environment's address as a slug and the first 16
// hex digits of SHA-256(login|password|client id|IsNetCore).
func (s Service) CachePath() string {
	discriminator := s.Env.Login + "|" + s.Env.Password + "|" + s.Env.ClientID + "|" + dotnetBoolText(s.Env.IsNetCore)
	hash := sha256.Sum256([]byte(discriminator))
	key := sanitizeSlug(stripScheme(s.Env.URI)) + "_" + hex.EncodeToString(hash[:])[:16]
	return filepath.Join(s.ClioHome, sessionsDirectory, key+sessionFileSuffix)
}

func dotnetBoolText(value bool) string {
	if value {
		return "True"
	}
	return "False"
}

func stripScheme(uri string) string {
	if blank(uri) {
		return ""
	}
	if index := strings.Index(uri, "://"); index >= 0 {
		return uri[index+3:]
	}
	return uri
}

func sanitizeSlug(value string) string {
	if blank(value) {
		return "default"
	}
	var builder strings.Builder
	for _, r := range value {
		if r < 0x80 && (unicode.IsLetter(r) || unicode.IsDigit(r)) || r == '.' || r == '_' || r == '-' {
			builder.WriteRune(r)
		} else {
			builder.WriteRune('-')
		}
	}
	sanitized := strings.Trim(builder.String(), "-.")
	if sanitized == "" {
		return "default"
	}
	return strings.ToLower(sanitized)
}

// shellURL is AuthenticatedBrowserLauncher.BuildShellUrl: the Shell honours an injected .ASPXAUTH where the bare
// site root can render the login form.
func shellURL(env Environment) string {
	base := strings.TrimRight(env.URI, "/")
	if env.IsNetCore {
		return base + "/Shell/"
	}
	return base + "/0/Shell/"
}

// login is the forms login clio's CreatioClient performs, with the cookies it leaves exported.
func (s Service) login(ctx context.Context) ([]BrowserCookie, error) {
	app, err := url.Parse(strings.TrimRight(s.Env.URI, "/") + "/")
	if err != nil || app.Host == "" {
		return nil, connectivity(s.Env.URI)
	}
	jar := newCookieRecorder(app)
	client := &http.Client{Transport: s.Transport, Jar: jar, Timeout: loginTimeout}
	_, offset := s.clock().Zone()
	body, _ := json.Marshal(map[string]any{"UserName": s.Env.Login, "UserPassword": s.Env.Password, "TimeZoneOffset": -offset / 60})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, app.String()+"ServiceModel/AuthService.svc/Login", bytes.NewReader(body))
	if err != nil {
		return nil, connectivity(s.Env.URI)
	}
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	response, err := client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, connectivity(s.Env.URI)
	}
	payload, readErr := readLimited(response)
	response.Body.Close()
	if readErr != nil {
		return nil, connectivity(s.Env.URI)
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return nil, invalidCredentials(s.Env.URI)
	}
	var result struct {
		Code *int `json:"Code"`
	}
	if json.Unmarshal(payload, &result) == nil && result.Code != nil && *result.Code != 0 {
		return nil, invalidCredentials(s.Env.URI)
	}
	cookies := jar.export()
	if !hasAuthCookie(cookies) {
		return nil, noCookies(s.Env.URI)
	}
	return cookies, nil
}

func readLimited(response *http.Response) ([]byte, error) {
	var buffer bytes.Buffer
	_, err := buffer.ReadFrom(http.MaxBytesReader(nil, response.Body, 16<<20))
	return buffer.Bytes(), err
}

func hasAuthCookie(cookies []BrowserCookie) bool {
	for _, cookie := range cookies {
		if strings.EqualFold(cookie.Name, ".ASPXAUTH") {
			return true
		}
	}
	return false
}

// tryReuse is TryReuseCachedSessionAsync: the cached cookies when the Shell still accepts them, nil when a new
// login is needed.
func (s Service) tryReuse(ctx context.Context, path string) ([]BrowserCookie, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil
	}
	cached := parseStorageCookies(string(data))
	live := false
	for _, cookie := range cached {
		if strings.EqualFold(cookie.Name, ".ASPXAUTH") && hasLiveExpiry(cookie.Expires, s.clock()) {
			live = true
			break
		}
	}
	app, err := url.Parse(strings.TrimRight(s.Env.URI, "/") + "/")
	if !live || err != nil || !app.IsAbs() || app.Host == "" {
		return nil, nil
	}
	jar := newCookieRecorder(app)
	for _, cookie := range cached {
		if !jar.importCookie(cookie) {
			return nil, nil
		}
	}
	client := &http.Client{Transport: s.Transport, Jar: jar, Timeout: loginTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, shellURL(s.Env), nil)
	if err != nil {
		return nil, nil
	}
	response, err := client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, nil
	}
	payload, readErr := readLimited(response)
	response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden ||
		(response.StatusCode >= 300 && response.StatusCode < 400) || readErr != nil {
		return nil, nil
	}
	if isSessionExpiredResponse(string(payload)) {
		return nil, nil
	}
	cookies := jar.export()
	if !hasAuthCookie(cookies) {
		return nil, noCookies(s.Env.URI)
	}
	return cookies, nil
}

// isSessionExpiredResponse is clio 8.1.0.142's ReauthExecutor.IsSessionExpiredResponse: an HTML body whose
// head names the login route or the login bootstrap, or Creatio's JSON 401 envelope (Message exactly
// "Authentication failed."). Leading white space and a byte-order mark are skipped.
func isSessionExpiredResponse(body string) bool {
	trimmed := strings.TrimLeftFunc(body, func(r rune) bool { return unicode.IsSpace(r) || r == '\uFEFF' })
	if trimmed == "" {
		return false
	}
	switch trimmed[0] {
	case '<':
		head := []rune(trimmed)
		if len(head) > sessionExpiredScanChars {
			head = head[:sessionExpiredScanChars]
		}
		lower := strings.ToLower(string(head))
		return strings.Contains(lower, "/login/") || strings.Contains(lower, `"bootstrap.login"`)
	case '{':
		if !strings.Contains(strings.ToLower(trimmed), "authentication failed") {
			return false
		}
		var envelope map[string]json.RawMessage
		if json.Unmarshal([]byte(trimmed), &envelope) != nil {
			return false
		}
		message, ok := stringMember(envelope, "Message")
		return ok && strings.EqualFold(message, "Authentication failed.")
	}
	return false
}

func hasLiveExpiry(expiry float64, now time.Time) bool {
	if math.IsNaN(expiry) || math.IsInf(expiry, 0) {
		return false
	}
	if expiry < 0 {
		return true
	}
	return expiry <= 253402300799 && expiry > float64(now.Unix())
}

// cookieRecorder is a cookie jar that also keeps each cookie's attributes, which Go's jar does not return, so
// the session can be exported whole (CreatioClient.ExportSessionCookies: the cookies of the application root).
type cookieRecorder struct {
	app     *url.URL
	jar     *cookiejar.Jar
	records map[string]BrowserCookie // by name|domain|path
	order   []string
}

func newCookieRecorder(app *url.URL) *cookieRecorder {
	jar, _ := cookiejar.New(nil)
	return &cookieRecorder{app: app, jar: jar, records: map[string]BrowserCookie{}}
}

func (r *cookieRecorder) SetCookies(target *url.URL, cookies []*http.Cookie) {
	r.jar.SetCookies(target, cookies)
	for _, cookie := range cookies {
		record := BrowserCookie{Name: cookie.Name, Value: cookie.Value, Domain: cookie.Domain, Path: cookie.Path,
			HTTPOnly: cookie.HttpOnly, Secure: cookie.Secure, SameSite: sameSiteText(cookie.SameSite), Expires: -1}
		if record.Domain == "" {
			record.Domain = target.Hostname()
		}
		if record.Path == "" {
			record.Path = defaultCookiePath(target.Path)
		}
		switch {
		case cookie.MaxAge < 0:
			r.remove(record)
			continue
		case cookie.MaxAge > 0:
			record.Expires = float64(time.Now().Add(time.Duration(cookie.MaxAge) * time.Second).Unix())
		case !cookie.Expires.IsZero():
			if cookie.Expires.Before(time.Now()) {
				r.remove(record)
				continue
			}
			record.Expires = float64(cookie.Expires.Unix())
		}
		r.store(record)
	}
}

func (r *cookieRecorder) Cookies(target *url.URL) []*http.Cookie { return r.jar.Cookies(target) }

func cookieKey(cookie BrowserCookie) string {
	return strings.ToLower(cookie.Name) + "|" + strings.ToLower(strings.TrimPrefix(cookie.Domain, ".")) + "|" + cookie.Path
}

func (r *cookieRecorder) store(cookie BrowserCookie) {
	key := cookieKey(cookie)
	if _, ok := r.records[key]; !ok {
		r.order = append(r.order, key)
	}
	r.records[key] = cookie
}

func (r *cookieRecorder) remove(cookie BrowserCookie) { delete(r.records, cookieKey(cookie)) }

// importCookie is CreatioClient.ImportSessionCookies for one cookie: false when it targets a host outside the
// application.
func (r *cookieRecorder) importCookie(cookie BrowserCookie) bool {
	host := r.app.Hostname()
	if cookie.Domain != "" {
		domain := strings.TrimPrefix(cookie.Domain, ".")
		if !strings.EqualFold(host, domain) && !strings.HasSuffix(strings.ToLower(host), "."+strings.ToLower(domain)) {
			return false
		}
	} else {
		cookie.Domain = host
	}
	if cookie.Path == "" {
		cookie.Path = "/"
	}
	native := &http.Cookie{Name: cookie.Name, Value: cookie.Value, Path: cookie.Path, HttpOnly: cookie.HTTPOnly, Secure: cookie.Secure}
	if !strings.EqualFold(cookie.Domain, host) {
		native.Domain = cookie.Domain
	}
	if cookie.Expires >= 0 {
		native.Expires = time.Unix(int64(cookie.Expires), 0)
	}
	r.jar.SetCookies(r.app, []*http.Cookie{native})
	r.store(cookie)
	return true
}

// export lists the kept cookies the application root receives, in the order they were set.
func (r *cookieRecorder) export() []BrowserCookie {
	var cookies []BrowserCookie
	for _, key := range r.order {
		cookie, ok := r.records[key]
		if !ok || !strings.HasPrefix(r.app.Path, cookie.Path) || (cookie.Secure && !strings.EqualFold(r.app.Scheme, "https")) {
			// CookieContainer.GetCookies leaves out a Secure cookie for an http address.
			continue
		}
		cookies = append(cookies, cookie)
	}
	return cookies
}

func defaultCookiePath(requestPath string) string {
	if requestPath == "" || requestPath[0] != '/' {
		return "/"
	}
	last := strings.LastIndex(requestPath, "/")
	if last == 0 {
		return "/"
	}
	return requestPath[:last]
}

func sameSiteText(mode http.SameSite) string {
	switch mode {
	case http.SameSiteStrictMode:
		return "Strict"
	case http.SameSiteNoneMode:
		return "None"
	default:
		return "Lax"
	}
}

// serializeStorageState is StorageStateJson.Serialize: {"cookies":[...],"origins":[]} in Playwright's names, as
// System.Text.Json writes it (default encoder: non-ASCII and HTML-sensitive characters escaped).
func serializeStorageState(cookies []BrowserCookie) string {
	type storedCookie struct {
		Name     string  `json:"name"`
		Value    string  `json:"value"`
		Domain   string  `json:"domain"`
		Path     string  `json:"path"`
		HTTPOnly bool    `json:"httpOnly"`
		Secure   bool    `json:"secure"`
		SameSite string  `json:"sameSite"`
		Expires  float64 `json:"expires"`
	}
	payload := struct {
		Cookies []storedCookie `json:"cookies"`
		Origins []any          `json:"origins"`
	}{Cookies: []storedCookie{}, Origins: []any{}}
	for _, cookie := range cookies {
		payload.Cookies = append(payload.Cookies, storedCookie(cookie))
	}
	text, err := creatio.ClioJSONText(payload)
	if err != nil {
		encoded, _ := json.Marshal(payload)
		return string(encoded)
	}
	return text
}

// parseStorageCookies is StorageStateJson.ParseCookies: missing optional fields default to a session cookie at
// path "/"; a corrupt or cookie-less document gives none.
func parseStorageCookies(text string) []BrowserCookie {
	if blank(text) {
		return nil
	}
	var document struct {
		Cookies []map[string]json.RawMessage `json:"cookies"`
	}
	if json.Unmarshal([]byte(text), &document) != nil {
		return nil
	}
	var cookies []BrowserCookie
	for _, raw := range document.Cookies {
		name, _ := stringMember(raw, "name")
		if name == "" {
			continue
		}
		cookie := BrowserCookie{Name: name, Path: "/", SameSite: "Lax", Expires: -1}
		if value, ok := stringMember(raw, "value"); ok {
			cookie.Value = value
		}
		if domain, ok := stringMember(raw, "domain"); ok {
			cookie.Domain = domain
		}
		if path, ok := stringMember(raw, "path"); ok {
			cookie.Path = path
		}
		if sameSite, ok := stringMember(raw, "sameSite"); ok {
			cookie.SameSite = sameSite
		}
		if value, ok := raw["httpOnly"]; ok {
			_ = json.Unmarshal(value, &cookie.HTTPOnly)
		}
		if value, ok := raw["secure"]; ok {
			_ = json.Unmarshal(value, &cookie.Secure)
		}
		if value, ok := raw["expires"]; ok {
			_ = json.Unmarshal(value, &cookie.Expires)
		}
		cookies = append(cookies, cookie)
	}
	return cookies
}

// writeOwnerOnlyAtomic replaces the file in one step with owner-only permissions (the session is a bearer
// secret, and a reader must never see half of it).
func writeOwnerOnlyAtomic(path, content string) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	_ = os.Chmod(directory, 0o700)
	temporary, err := os.CreateTemp(directory, ".storageState-*.tmp")
	if err != nil {
		return err
	}
	name := temporary.Name()
	if _, err := temporary.WriteString(content); err != nil {
		temporary.Close()
		os.Remove(name)
		return err
	}
	if err := temporary.Chmod(0o600); err != nil && !errors.Is(err, os.ErrInvalid) {
		// Windows has no mode bits to set; the file is in the user's profile.
		_ = err
	}
	if err := temporary.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

// stringMember reads a string member; ok false when it is absent or not a string.
func stringMember(object map[string]json.RawMessage, name string) (string, bool) {
	raw, ok := object[name]
	if !ok {
		return "", false
	}
	var text string
	if json.Unmarshal(raw, &text) != nil {
		return "", false
	}
	return text, true
}

// fileExists is clio's IFileSystem.ExistsFile (File.Exists): an existing file, not a directory.
func fileExists(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func blank(text string) bool { return strings.TrimSpace(text) == "" }
