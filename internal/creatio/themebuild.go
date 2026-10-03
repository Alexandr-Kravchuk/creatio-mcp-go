package creatio

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type ThemeBuildOptions struct {
	Primary, Secondary, Accent, Success, Error string
	CSSClassName, Caption, ID                  string
	HeadingFont, BodyFont                      string
	FontWeights                                []int
	Version, EnvironmentName                   string
}
type ThemeBuildOutcome struct {
	Success    bool     `json:"success"`
	CSS        string   `json:"css,omitempty"`
	Descriptor string   `json:"descriptor,omitempty"`
	Path       string   `json:"path,omitempty"`
	Warnings   []string `json:"warnings,omitempty"`
	Error      string   `json:"error,omitempty"`
}

func (c *Client) BuildThemeForCreate(ctx context.Context, options ThemeBuildOptions, version string) ThemeBuildOutcome {
	options.Version = version
	return BuildTheme(ctx, options)
}

// BuildTheme builds the versioned embedded templates without changing the environment.
func BuildTheme(ctx context.Context, options ThemeBuildOptions) ThemeBuildOutcome {
	fail := func(err error) ThemeBuildOutcome { return ThemeBuildOutcome{Error: err.Error()} }
	class, err := ThemeWriteResolveClassName(options.CSSClassName, options.Caption)
	if err != nil {
		return fail(err)
	}
	if strings.TrimSpace(options.Version) != "" && strings.TrimSpace(options.EnvironmentName) != "" {
		return fail(fmt.Errorf("build-theme: --version and --environment-name are mutually exclusive. Pass one or neither."))
	}
	template, err := themeReadTemplate("theme.css.tpl", options.Version)
	if err != nil {
		return fail(err)
	}
	optional := func(s string) *string {
		if s == "" {
			return nil
		}
		return &s
	}
	fonts := &themeFontsInput{heading: themeNormalizeFontFamily(options.HeadingFont), body: themeNormalizeFontFamily(options.BodyFont), weights: options.FontWeights, suppressed: map[string]bool{}}
	warnings := []string{}
	if len(options.FontWeights) > 0 && fonts.heading == "" && fonts.body == "" {
		warnings = append(warnings, "build-theme: font weights were ignored — they apply only to a custom heading or body font.")
	}
	seen := map[string]bool{}
	for _, family := range []string{fonts.heading, fonts.body} {
		if family == "" || family == themeDefaultFontFamily || seen[family] {
			continue
		}
		seen[family] = true
		if err := themeValidateFontFamily(family); err != nil {
			return fail(err)
		}
		status := themeProbeFont(ctx, family)
		if status == http.StatusNotFound {
			fonts.suppressed[family] = true
			warnings = append(warnings, fmt.Sprintf("build-theme: \"%s\" was not found in Google Fonts — names are case-sensitive (\"Roboto\" resolves where \"roboto\" does not), and families are sometimes renamed (search fonts.google.com for the current name). No web-font import was added: the theme shows \"%s\" only where it is installed locally; everywhere else the text falls back to a generic face. Pick a Google font and restyle if that is not acceptable.", family, family))
		} else if status != http.StatusOK {
			warnings = append(warnings, fmt.Sprintf("build-theme: could not verify \"%s\" against Google Fonts — the web-font import was kept. If \"%s\" is actually a locally installed font, restyle once connectivity is back.", family, family))
		}
	}
	if fonts.heading == "" && fonts.body == "" && len(fonts.weights) == 0 {
		fonts = nil
	}
	css, err := themeBuildCSS(template, themeBuildInput{primary: options.Primary, secondary: optional(options.Secondary), accent: optional(options.Accent), success: optional(options.Success), errs: optional(options.Error), cssClass: class, fonts: fonts})
	if err != nil {
		return fail(err)
	}
	if options.Accent == "" {
		primary, _ := themeMustNormalize(options.Primary, true)
		accent := themeChooseBestAccent(primary, themeAccentCandidates(primary))
		if !themeIsValidAccent(accent.contrast, accent.distance) {
			warnings = append(warnings, fmt.Sprintf("build-theme: the auto-selected accent %s does not meet the accessibility gates (readability on white and distinctness from the primary). Pass --accent to choose one explicitly, or reuse the primary as the accent.", accent.hex))
		}
	}
	descriptor, err := themeReadTemplate("theme.json.tpl", options.Version)
	if err != nil {
		return fail(err)
	}
	id := options.ID
	if id == "" {
		var b [16]byte
		if _, err := rand.Read(b[:]); err != nil {
			return fail(err)
		}
		b[6] = (b[6] & 15) | 64
		b[8] = (b[8] & 63) | 128
		id = fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
	}
	caption := options.Caption
	if caption == "" {
		caption = class
	}
	escape := func(s string) string { b, _ := json.Marshal(s); return string(b[1 : len(b)-1]) }
	descriptor = strings.NewReplacer("<%themeId%>", escape(id), "<%themeCaption%>", escape(caption), "<%themeCssClass%>", escape(class)).Replace(descriptor)
	return ThemeBuildOutcome{Success: true, CSS: css, Descriptor: descriptor, Warnings: warnings}
}

func themeProbeFont(ctx context.Context, family string) int {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://fonts.google.com/metadata/fonts/"+url.PathEscape(family), nil)
	if err != nil {
		return 0
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return res.StatusCode
	}
	if res.StatusCode >= 200 && res.StatusCode < 300 && strings.Contains(strings.ToLower(res.Header.Get("Content-Type")), "json") {
		return http.StatusOK
	}
	return 0
}
