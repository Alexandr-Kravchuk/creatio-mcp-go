package creatio

// The theme colour engine of clio master 914dab286 (clio/Theming: ColorSpace, ColorNormalizer,
// PaletteGenerator, ColorMetrics, TextTokenResolver, ThemeCssBuilder, FontImportBuilder, FontFamilyName,
// ThemeTemplateDefaults; clio/Command/Theming/ThemeTemplateProvider). build-theme, create-theme's brand
// mode and advise-theme-palette share it. The arithmetic follows clio's statement by statement so the
// generated hex values agree.

import (
	"embed"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

//go:embed themetemplates
var themeTemplateFiles embed.FS

const (
	themePaletteAnchorStep  = 500
	themeDefaultFontFamily  = "Montserrat"
	themeColorWhite         = "#ffffff"
	themeColorDark          = "#181818"
	themeMinContrastOnWhite = 3.0
	themeTextContrastMin    = 4.5
	themeAccentSimilarWarn  = 0.10
	themeAccentSimilarStrng = 0.07
)

// themePaletteSteps is PaletteGenerator.Steps.
var themePaletteSteps = []int{10, 25, 50, 100, 200, 300, 400, 500, 600, 700, 800, 900}

// themeGeneratedPalettes is ThemeCssBuilder.GeneratedPaletteNames.
var themeGeneratedPalettes = []string{"primary", "secondary", "accent", "success", "error"}

// ---------------------------------------------------------------------------------------------- colour space

type themePaletteMode int

const (
	themeModeStandard themePaletteMode = iota
	themeModeLight
	themeModeDark
	themeModeYellow
)

type themeRGB struct{ r, g, b float64 }

func themeHexToRGB(hex string) themeRGB {
	normalized := strings.ReplaceAll(hex, "#", "")
	channel := func(start int) float64 {
		value, _ := strconv.ParseUint(normalized[start:start+2], 16, 8)
		return float64(value) / 255.0
	}
	return themeRGB{channel(0), channel(2), channel(4)}
}

func themeLinearize(value float64) float64 {
	if value <= 0.04045 {
		return value / 12.92
	}
	return math.Pow((value+0.055)/1.055, 2.4)
}

func themeDelinearize(value float64) float64 {
	if value <= 0.0031308 {
		return 12.92 * value
	}
	return 1.055*math.Pow(value, 1.0/2.4) - 0.055
}

func themeHexToOklab(hex string) (float64, float64, float64) {
	rgb := themeHexToRGB(hex)
	rl, gl, bl := themeLinearize(rgb.r), themeLinearize(rgb.g), themeLinearize(rgb.b)
	ox := 0.4122214708*rl + 0.5363325363*gl + 0.0514459929*bl
	oy := 0.2119034982*rl + 0.6806995451*gl + 0.1073969566*bl
	oz := 0.0883024619*rl + 0.2817188376*gl + 0.6299787005*bl
	lv, mv, sv := math.Cbrt(ox), math.Cbrt(oy), math.Cbrt(oz)
	l := 0.2104542553*lv + 0.793617785*mv - 0.0040720468*sv
	a := 1.9779984951*lv - 2.428592205*mv + 0.4505937099*sv
	b := 0.0259040371*lv + 0.7827717662*mv - 0.808675766*sv
	return l, a, b
}

func themeHexToOklch(hex string) (float64, float64, float64) {
	l, a, b := themeHexToOklab(hex)
	c := math.Sqrt(a*a + b*b)
	h := math.Mod(math.Atan2(b, a)*180.0/math.Pi+360.0, 360.0)
	return l, c, h
}

func themeOklchToRGB(l, c, h float64) themeRGB {
	hr := h * math.Pi / 180.0
	a := c * math.Cos(hr)
	b := c * math.Sin(hr)
	lv := l + 0.3963377774*a + 0.2158037573*b
	mv := l - 0.1055613458*a - 0.0638541728*b
	sv := l - 0.0894841775*a - 1.291485548*b
	lc, mc, sc := math.Pow(lv, 3), math.Pow(mv, 3), math.Pow(sv, 3)
	return themeRGB{
		themeDelinearize(4.0767416621*lc - 3.3077115913*mc + 0.2309699292*sc),
		themeDelinearize(-1.2684380046*lc + 2.6097574011*mc - 0.3413193965*sc),
		themeDelinearize(-0.0041960863*lc - 0.7034186147*mc + 1.707614701*sc),
	}
}

func themeRoundHalfUp(x float64) float64 {
	floor := math.Floor(x)
	if x-floor < 0.5 {
		return floor
	}
	return floor + 1.0
}

func themeHexChannel(channel float64) string {
	rounded := int(math.Max(0.0, math.Min(255.0, themeRoundHalfUp(channel*255.0))))
	return fmt.Sprintf("%02x", rounded)
}

func themeRGBToHex(rgb themeRGB) string {
	return "#" + themeHexChannel(rgb.r) + themeHexChannel(rgb.g) + themeHexChannel(rgb.b)
}

func themeInGamut(channel float64) bool {
	return channel >= -0.001 && channel <= 1.001
}

func themeMaxChromaInGamut(l, h float64) float64 {
	lo, hi := 0.0, 0.4
	for i := 0; i < 24; i++ {
		mid := (lo + hi) / 2.0
		rgb := themeOklchToRGB(l, mid, h)
		if themeInGamut(rgb.r) && themeInGamut(rgb.g) && themeInGamut(rgb.b) {
			lo = mid
		} else {
			hi = mid
		}
	}
	return lo
}

func themeOklchToHex(l, c, h float64) string {
	safeHue := math.Mod(math.Mod(h, 360.0)+360.0, 360.0)
	safeChroma := math.Min(c, themeMaxChromaInGamut(l, safeHue))
	return themeRGBToHex(themeOklchToRGB(l, safeChroma, safeHue))
}

func themeDetectMode(l, c, h float64) themePaletteMode {
	switch {
	case h >= 80 && h <= 105 && c > 0.06:
		return themeModeYellow
	case l < 0.38:
		return themeModeDark
	case l > 0.78:
		return themeModeLight
	}
	return themeModeStandard
}

// ---------------------------------------------------------------------------------------------- normalizer

var (
	themeHex8 = regexp.MustCompile(`^#?[0-9a-f]{8}$`)
	themeHex4 = regexp.MustCompile(`^#?[0-9a-f]{4}$`)
	themeHex3 = regexp.MustCompile(`^#?([0-9a-f])([0-9a-f])([0-9a-f])$`)
	themeHex6 = regexp.MustCompile(`^#?([0-9a-f]{6})$`)
	themeRGBx = regexp.MustCompile(`^rgb\(\s*(\d{1,3})\s*,\s*(\d{1,3})\s*,\s*(\d{1,3})\s*\)$`)
	themeHSLx = regexp.MustCompile(`^hsl\(\s*(\d{1,3})\s*,\s*(\d{1,3})%\s*,\s*(\d{1,3})%\s*\)$`)
)

const (
	themeAlphaNotSupported = "ALPHA_NOT_SUPPORTED"
	themeInvalidColor      = "INVALID_COLOR"
)

// themeNormalizeColor is ColorNormalizer.TryNormalize: a CSS colour (name, #rgb, #rrggbb, rgb(), hsl())
// as lower-case #rrggbb, or the rejection code. present=false is a null input.
func themeNormalizeColor(input string, present bool) (string, string) {
	if !present {
		return "", themeInvalidColor
	}
	value := strings.ToLower(dotnetTrim(input))
	if themeHex8.MatchString(value) || themeHex4.MatchString(value) || strings.HasPrefix(value, "rgba") || strings.HasPrefix(value, "hsla") {
		return "", themeAlphaNotSupported
	}
	if named, ok := themeNamedColors[value]; ok {
		return named, ""
	}
	if match := themeHex3.FindStringSubmatch(value); match != nil {
		return "#" + match[1] + match[1] + match[2] + match[2] + match[3] + match[3], ""
	}
	if match := themeHex6.FindStringSubmatch(value); match != nil {
		return "#" + match[1], ""
	}
	if match := themeRGBx.FindStringSubmatch(value); match != nil {
		r, _ := strconv.Atoi(match[1])
		g, _ := strconv.Atoi(match[2])
		b, _ := strconv.Atoi(match[3])
		if r <= 255 && g <= 255 && b <= 255 {
			return themeRGBToHex(themeRGB{float64(r) / 255.0, float64(g) / 255.0, float64(b) / 255.0}), ""
		}
	}
	if match := themeHSLx.FindStringSubmatch(value); match != nil {
		hue, _ := strconv.Atoi(match[1])
		saturation, _ := strconv.Atoi(match[2])
		lightness, _ := strconv.Atoi(match[3])
		if saturation <= 100 && lightness <= 100 {
			return themeHSLToHex(float64(hue%360), float64(saturation)/100.0, float64(lightness)/100.0), ""
		}
	}
	return "", themeInvalidColor
}

// themeMustNormalize is ColorNormalizer.Normalize: a rejection is the ArgumentException text.
func themeMustNormalize(input string, present bool) (string, error) {
	hex, code := themeNormalizeColor(input, present)
	if code != "" {
		return "", fmt.Errorf("%s: \"%s\" (Parameter 'input')", code, input)
	}
	return hex, nil
}

func themeHSLToHex(h, s, l float64) string {
	c := (1 - math.Abs(2*l-1)) * s
	x := c * (1 - math.Abs(math.Mod(h/60, 2)-1))
	m := l - c/2
	var r, g, b float64
	switch {
	case h < 60:
		r, g, b = c, x, 0
	case h < 120:
		r, g, b = x, c, 0
	case h < 180:
		r, g, b = 0, c, x
	case h < 240:
		r, g, b = 0, x, c
	case h < 300:
		r, g, b = x, 0, c
	default:
		r, g, b = c, 0, x
	}
	return themeRGBToHex(themeRGB{r + m, g + m, b + m})
}

// ---------------------------------------------------------------------------------------------- palette

// themeGenerateScale is PaletteGenerator.GenerateScale: twelve stops around the 500 anchor.
func themeGenerateScale(hex500 string) map[int]string {
	l, c, h := themeHexToOklch(hex500)
	mode := themeDetectMode(l, c, h)
	palette := map[int]string{themePaletteAnchorStep: strings.ToLower(hex500)}
	var lighter, darker []int
	for _, step := range themePaletteSteps {
		if step < themePaletteAnchorStep {
			lighter = append(lighter, step)
		} else if step > themePaletteAnchorStep {
			darker = append(darker, step)
		}
	}
	for _, step := range lighter {
		t := float64(themePaletteAnchorStep-step) / themePaletteAnchorStep
		ls := l + (0.99-l)*t
		var cs, hs float64
		if mode == themeModeYellow {
			cs = c * math.Pow(1-t, 0.8)
			hs = h + 4*t
		} else {
			cs = c * (1 - t)
			hs = h
		}
		palette[step] = themeOklchToHex(ls, cs, hs)
	}
	lbot := 0.2
	if mode == themeModeDark {
		lbot = math.Max(0.06, math.Min(0.2, l-0.035*float64(len(darker)+1)))
	}
	if mode == themeModeLight {
		lbot = 0.24
	}
	lbot = math.Min(lbot, l)
	for i, step := range darker {
		f := float64(i+1) / float64(len(darker)+1)
		ls := l - (l-lbot)*f
		var cs, hs float64
		if mode == themeModeYellow {
			hs = h - 24*math.Sqrt(f)
			rel := math.Min(1, c/math.Max(themeMaxChromaInGamut(l, h), 1e-6))
			cs = themeMaxChromaInGamut(ls, hs) * math.Max(rel, 0.85)
		} else {
			hs = h
			cs = c
		}
		palette[step] = themeOklchToHex(ls, cs, hs)
	}
	return palette
}

// themeDeriveSecondary is PaletteGenerator.DeriveSecondary.
func themeDeriveSecondary(primaryHex string) string {
	l, c, h := themeHexToOklch(primaryHex)
	ls := math.Min(l*0.61, 0.3)
	hs := math.Mod(h-11+360, 360)
	d := math.Min(math.Abs(hs-20), math.Abs(hs-(20+360)))
	dip := math.Exp(-(d * d) / (2 * 40 * 40))
	mult := 0.319 - 0.113*dip
	cs := math.Min(math.Max(c*mult, 0.015), themeMaxChromaInGamut(ls, hs))
	return themeOklchToHex(ls, cs, hs)
}

func themeFindCuspLightness(h float64) float64 {
	bestL, bestC := 0.6, 0.0
	for l := 0.35; l <= 0.85; l += 0.01 {
		if c := themeMaxChromaInGamut(l, h); c > bestC {
			bestC, bestL = c, l
		}
	}
	return bestL
}

type themeAccentCandidate struct {
	hex    string
	offset int
}

// themeAccentCandidates is PaletteGenerator.GenerateAccentCandidates: the cusp colours at +135, +180 and
// +225 degrees of the primary's hue.
func themeAccentCandidates(primaryHex string) []themeAccentCandidate {
	_, _, h := themeHexToOklch(primaryHex)
	candidates := []themeAccentCandidate{}
	for _, offset := range []int{135, 180, 225} {
		ha := math.Mod(h+float64(offset), 360)
		la := math.Min(math.Max(themeFindCuspLightness(ha), 0.55), 0.72)
		ca := themeMaxChromaInGamut(la, ha) * 0.97
		candidates = append(candidates, themeAccentCandidate{themeOklchToHex(la, ca, ha), offset})
	}
	return candidates
}

// ---------------------------------------------------------------------------------------------- metrics

func themeRelativeLuminance(hex string) float64 {
	rgb := themeHexToRGB(hex)
	return 0.2126*themeLinearize(rgb.r) + 0.7152*themeLinearize(rgb.g) + 0.0722*themeLinearize(rgb.b)
}

func themeContrastRatio(a, b string) float64 {
	l1, l2 := themeRelativeLuminance(a), themeRelativeLuminance(b)
	return (math.Max(l1, l2) + 0.05) / (math.Min(l1, l2) + 0.05)
}

func themeDistanceOklab(a, b string) float64 {
	l1, a1, b1 := themeHexToOklab(a)
	l2, a2, b2 := themeHexToOklab(b)
	return math.Sqrt(math.Pow(l1-l2, 2) + math.Pow(a1-a2, 2) + math.Pow(b1-b2, 2))
}

func themeMeetsMinContrast(hex string) bool {
	return themeContrastRatio(hex, themeColorWhite) >= themeMinContrastOnWhite
}

func themeIsValidAccent(contrast, distance float64) bool {
	return contrast >= themeMinContrastOnWhite && distance >= themeAccentSimilarStrng
}

type themeScoredAccent struct {
	hex      string
	offset   int
	contrast float64
	distance float64
}

func themeScoreAccents(primaryHex string, candidates []themeAccentCandidate) []themeScoredAccent {
	scored := make([]themeScoredAccent, 0, len(candidates))
	for _, candidate := range candidates {
		scored = append(scored, themeScoredAccent{candidate.hex, candidate.offset,
			themeContrastRatio(candidate.hex, themeColorWhite), themeDistanceOklab(primaryHex, candidate.hex)})
	}
	return scored
}

// themeChooseBestAccent is ColorMetrics.ChooseBestAccent: the farthest readable candidate, else the most
// readable one (a stable order keeps the first of equals).
func themeChooseBestAccent(primaryHex string, candidates []themeAccentCandidate) themeScoredAccent {
	scored := themeScoreAccents(primaryHex, candidates)
	best, found := themeScoredAccent{}, false
	for _, candidate := range scored {
		if candidate.contrast >= themeMinContrastOnWhite && (!found || candidate.distance > best.distance) {
			best, found = candidate, true
		}
	}
	if found {
		return best
	}
	best = scored[0]
	for _, candidate := range scored[1:] {
		if candidate.contrast > best.contrast {
			best = candidate
		}
	}
	return best
}

// themeSelectBestValidAccent is ColorMetrics.SelectBestValidAccent.
func themeSelectBestValidAccent(primaryHex string, candidates []themeAccentCandidate) (*themeScoredAccent, int, []themeScoredAccent) {
	scored := themeScoreAccents(primaryHex, candidates)
	var best *themeScoredAccent
	valid := 0
	for index := range scored {
		candidate := scored[index]
		if !themeIsValidAccent(candidate.contrast, candidate.distance) {
			continue
		}
		valid++
		if best == nil || candidate.distance > best.distance {
			best = &scored[index]
		}
	}
	return best, valid, scored
}

func themeSimilarityBand(distance float64) string {
	if distance >= themeAccentSimilarWarn {
		return "clean"
	}
	if distance >= themeAccentSimilarStrng {
		return "warn"
	}
	return "strong"
}

type themeAdaptedPrimary struct {
	outcome          string
	originalContrast float64
	adapted          string
	adaptedContrast  float64
	distance         float64
}

// themeAdaptPrimary is ColorMetrics.AdaptPrimary500: darken in OKLCH until the colour reads on white.
func themeAdaptPrimary(primaryHex string) themeAdaptedPrimary {
	original := themeContrastRatio(primaryHex, themeColorWhite)
	if original >= themeMinContrastOnWhite {
		return themeAdaptedPrimary{outcome: "compliant", originalContrast: original}
	}
	l, c, h := themeHexToOklch(primaryHex)
	for ls := l; ls >= 0.05; ls -= 0.005 {
		safeChroma := math.Min(c, themeMaxChromaInGamut(ls, h))
		adapted := themeOklchToHex(ls, safeChroma, h)
		if contrast := themeContrastRatio(adapted, themeColorWhite); contrast >= themeMinContrastOnWhite {
			return themeAdaptedPrimary{outcome: "adapted", originalContrast: original, adapted: adapted,
				adaptedContrast: contrast, distance: themeDistanceOklab(primaryHex, adapted)}
		}
	}
	return themeAdaptedPrimary{outcome: "could-not-adapt", originalContrast: original}
}

// ---------------------------------------------------------------------------------------------- text tokens

type themeTokenPalette struct{ token, palette string }

var themeTextTokens = []themeTokenPalette{
	{"text-heading", "secondary"}, {"text-action", "secondary"}, {"text-action-hover", "primary"},
	{"text-link", "primary"}, {"text-primary", "primary"}, {"text-secondary", "secondary"},
	{"text-accent", "accent"}, {"text-error", "error"}, {"text-success", "success"},
}

var themeTextOnColorTokens = []themeTokenPalette{
	{"text-on-primary", "primary"}, {"text-on-primary-subtle", "primary"}, {"text-on-primary-soft", "primary"},
	{"text-on-secondary", "secondary"}, {"text-on-secondary-subtle", "secondary"}, {"text-on-secondary-soft", "secondary"},
	{"text-on-accent", "accent"}, {"text-on-accent-subtle", "accent"}, {"text-on-accent-soft", "accent"},
	{"text-on-error", "error"}, {"text-on-error-subtle", "error"}, {"text-on-error-soft", "error"},
	{"text-on-success", "success"}, {"text-on-success-subtle", "success"}, {"text-on-success-soft", "success"},
}

var themeAscendingSteps = []int{500, 600, 700, 800, 900}

func themeResolveTextToken(palette map[int]string, startStep int) int {
	start := -1
	for index, step := range themeAscendingSteps {
		if step == startStep {
			start = index
			break
		}
	}
	steps := themeAscendingSteps
	if start >= 0 {
		steps = themeAscendingSteps[start:]
	}
	for _, step := range steps {
		if themeContrastRatio(palette[step], themeColorWhite) >= themeTextContrastMin {
			return step
		}
	}
	return 900
}

func themeLinkHoverStep(linkStep int) int {
	for index, step := range themeAscendingSteps {
		if step == linkStep && index < len(themeAscendingSteps)-1 {
			return themeAscendingSteps[index+1]
		}
	}
	return 900
}

// themeTextOnColor is TextTokenResolver.ResolveTextOnColorToken: white, then the palette's 900, then the
// dark base, whichever first reaches 4.5:1 on the background; else the highest contrast.
func themeTextOnColor(paletteName, background string, palettes map[string]map[int]string) string {
	type candidate struct {
		value    string
		contrast float64
	}
	candidates := []candidate{
		{"var(--crt-color-base-light)", themeContrastRatio(themeColorWhite, background)},
		{fmt.Sprintf("var(--crt-palette-%s-900)", paletteName), themeContrastRatio(palettes[paletteName][900], background)},
		{"var(--crt-color-base-dark)", themeContrastRatio(themeColorDark, background)},
	}
	for _, item := range candidates {
		if item.contrast >= themeTextContrastMin {
			return item.value
		}
	}
	best := candidates[0]
	for _, item := range candidates[1:] {
		if item.contrast > best.contrast {
			best = item
		}
	}
	return best.value
}

// ---------------------------------------------------------------------------------------------- CSS builder

// themeBuildInput is clio's BuildThemeInput; a nil colour is "not supplied" (derived or defaulted).
type themeBuildInput struct {
	primary                          string
	secondary, accent, success, errs *string
	cssClass                         string
	fonts                            *themeFontsInput
}

type themeFontsInput struct {
	heading, body string
	weights       []int // nil: the default weights
	suppressed    map[string]bool
}

var (
	themePaletteRef      = regexp.MustCompile(`var\(--crt-palette-([a-z]+)-(\d+)\)`)
	themeColorDecl       = regexp.MustCompile(`(--crt-color-[a-z0-9-]+):\s*([^;]+);`)
	themeTemplateDefault = regexp.MustCompile(`(?i)--crt-palette-(primary|secondary|accent|success|error)-500\s*:\s*(#[0-9a-f]{6})`)
)

const themeTemplateCommentMarker = "Creatio custom theme template"

// themeStripTemplateComment removes the first /* ... */ comment that carries the template marker, with
// one following newline, as ThemeCssBuilder.CommentStripRegex does.
func themeStripTemplateComment(css string) string {
	for start := 0; ; {
		open := strings.Index(css[start:], "/*")
		if open < 0 {
			return css
		}
		open += start
		end := strings.Index(css[open+2:], "*/")
		if end < 0 {
			return css
		}
		end += open + 2
		if strings.Contains(css[open+2:end], themeTemplateCommentMarker) {
			stop := end + 2
			if stop < len(css) && css[stop] == '\n' {
				stop++
			}
			return css[:open] + css[stop:]
		}
		start = open + 1
	}
}

// themeReplaceFirst replaces the first match of pattern, building the replacement from the submatches.
func themeReplaceFirst(pattern *regexp.Regexp, text string, replacement func(groups []string) string) string {
	location := pattern.FindStringSubmatchIndex(text)
	if location == nil {
		return text
	}
	groups := make([]string, len(location)/2)
	for index := range groups {
		if location[2*index] >= 0 {
			groups[index] = text[location[2*index]:location[2*index+1]]
		}
	}
	return text[:location[0]] + replacement(groups) + text[location[1]:]
}

func themeDeclaration(name string) *regexp.Regexp {
	return regexp.MustCompile(`(` + regexp.QuoteMeta(name) + `:\s*)[^;]+(;)`)
}

// themeTemplatePaletteBase is ThemeTemplateDefaults.TryGetPaletteBase.
func themeTemplatePaletteBase(templateCSS, role string) (string, bool) {
	pattern := regexp.MustCompile(`(?i)--crt-palette-` + regexp.QuoteMeta(role) + `-500\s*:\s*(#[0-9a-f]{6})`)
	match := pattern.FindStringSubmatch(templateCSS)
	if match == nil {
		return "", false
	}
	return strings.ToLower(match[1]), true
}

// themeBuildCSS is ThemeCssBuilder.Build.
func themeBuildCSS(templateCSS string, input themeBuildInput) (string, error) {
	if input.primary == "" {
		return "", errors.New("PRIMARY_REQUIRED: a primary color is required. (Parameter 'options')")
	}
	if input.cssClass == "" {
		return "", errors.New("THEME_CSS_CLASS_REQUIRED: a themeCssClass is required. (Parameter 'options')")
	}
	if !themeWriteValidClassName(input.cssClass) {
		return "", fmt.Errorf("INVALID_THEME_CSS_CLASS: \"%s\" (Parameter 'options')", input.cssClass)
	}
	templateCSS = strings.ReplaceAll(strings.ReplaceAll(templateCSS, "\r\n", "\n"), "\r", "\n")
	primary, err := themeMustNormalize(input.primary, true)
	if err != nil {
		return "", err
	}
	secondary, err := themeColorOr(input.secondary, func() string { return themeDeriveSecondary(primary) })
	if err != nil {
		return "", err
	}
	accent, err := themeColorOr(input.accent, func() string {
		return themeChooseBestAccent(primary, themeAccentCandidates(primary)).hex
	})
	if err != nil {
		return "", err
	}
	systemDefault := func(role string) (string, error) {
		hex, ok := themeTemplatePaletteBase(templateCSS, role)
		if !ok {
			return "", fmt.Errorf("The theme template does not define a default --crt-palette-%s-500 colour.", role)
		}
		return hex, nil
	}
	success, err := themeSystemColor(input.success, func() (string, error) { return systemDefault("success") })
	if err != nil {
		return "", err
	}
	errorColor, err := themeSystemColor(input.errs, func() (string, error) { return systemDefault("error") })
	if err != nil {
		return "", err
	}
	palettes := map[string]map[int]string{
		"primary": themeGenerateScale(primary), "secondary": themeGenerateScale(secondary),
		"accent": themeGenerateScale(accent), "success": themeGenerateScale(success), "error": themeGenerateScale(errorColor),
	}
	css := themeStripTemplateComment(templateCSS)
	css = strings.ReplaceAll(css, "<%themeCssClass%>", input.cssClass)
	for _, name := range themeGeneratedPalettes {
		for _, step := range themePaletteSteps {
			pattern := regexp.MustCompile(fmt.Sprintf(`(--crt-palette-%s-%d:\s*)#[0-9a-f]{6}(;)`, name, step))
			css = themeReplaceFirst(pattern, css, func(groups []string) string { return groups[1] + palettes[name][step] + groups[2] })
		}
	}
	css = themeFinalizeTextTokens(css, palettes)
	css, err = themeApplyFonts(css, input.fonts)
	if err != nil {
		return "", err
	}
	if strings.Contains(css, "<%") {
		return "", errors.New("Theme template fill left an unresolved '<%…%>' placeholder — the template does not match the expected contract.")
	}
	for _, name := range themeGeneratedPalettes {
		for _, step := range themePaletteSteps {
			pattern := regexp.MustCompile(fmt.Sprintf(`--crt-palette-%s-%d:\s*([^;]+);`, name, step))
			match := pattern.FindStringSubmatch(css)
			if match == nil || dotnetTrim(match[1]) != palettes[name][step] {
				return "", fmt.Errorf("Theme template fill did not substitute '--crt-palette-%s-%d' — the template is missing the expected declaration.", name, step)
			}
		}
	}
	return css, nil
}

func themeColorOr(value *string, derive func() string) (string, error) {
	if value != nil {
		return themeMustNormalize(*value, true)
	}
	return themeMustNormalize(derive(), true)
}

func themeSystemColor(value *string, fallback func() (string, error)) (string, error) {
	if value != nil {
		return themeMustNormalize(*value, true)
	}
	hex, err := fallback()
	if err != nil {
		return "", err
	}
	return themeMustNormalize(hex, true)
}

func themeFinalizeTextTokens(css string, palettes map[string]map[int]string) string {
	declarations := map[string]string{}
	for _, match := range themeColorDecl.FindAllStringSubmatch(css, -1) {
		declarations[match[1]] = dotnetTrim(match[2])
	}
	paletteRef := func(name string) (string, int, bool) {
		value, ok := declarations[name]
		if !ok {
			return "", 0, false
		}
		match := themePaletteRef.FindStringSubmatch(value)
		if match == nil {
			return "", 0, false
		}
		step, err := strconv.Atoi(match[2])
		if err != nil {
			return "", 0, false
		}
		return match[1], step, true
	}
	setColor := func(text, token, value string) string {
		return themeReplaceFirst(themeDeclaration("--crt-color-"+token), text, func(groups []string) string {
			return groups[1] + value + groups[2]
		})
	}
	next := css
	resolved := map[string]int{}
	for _, entry := range themeTextTokens {
		start := 500
		if _, step, ok := paletteRef("--crt-color-" + entry.token); ok {
			start = step
		}
		step := themeResolveTextToken(palettes[entry.palette], start)
		resolved[entry.token] = step
		next = setColor(next, entry.token, fmt.Sprintf("var(--crt-palette-%s-%d)", entry.palette, step))
	}
	next = setColor(next, "text-link-hover", fmt.Sprintf("var(--crt-palette-primary-%d)", themeLinkHoverStep(resolved["text-link"])))
	for _, entry := range themeTextOnColorTokens {
		name, step, ok := paletteRef("--crt-color-background-" + strings.TrimPrefix(entry.token, "text-on-"))
		if !ok {
			continue
		}
		palette, ok := palettes[name]
		if !ok {
			continue
		}
		next = setColor(next, entry.token, themeTextOnColor(entry.palette, palette[step], palettes))
	}
	return next
}

var themeFontFamilyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 -]*$`)

// themeNormalizeFontFamily is FontFamilyName.Normalize: trimmed, runs of white space collapsed.
func themeNormalizeFontFamily(family string) string {
	if strings.TrimSpace(family) == "" {
		return family
	}
	return strings.Join(strings.FieldsFunc(dotnetTrim(family), unicode.IsSpace), " ")
}

func themeValidFontFamily(family string) bool {
	return family != "" && family == dotnetTrim(family) && utf16Length(family) <= 100 && themeFontFamilyPattern.MatchString(family)
}

func themeValidateFontFamily(family string) error {
	if !themeValidFontFamily(family) {
		return fmt.Errorf("INVALID_FONT_FAMILY: \"%s\" (Parameter 'family')", family)
	}
	return nil
}

func themeApplyFonts(css string, fonts *themeFontsInput) (string, error) {
	heading, body := themeDefaultFontFamily, themeDefaultFontFamily
	if fonts != nil && fonts.heading != "" {
		heading = fonts.heading
	}
	if fonts != nil && fonts.body != "" {
		body = fonts.body
	}
	if heading == themeDefaultFontFamily && body == themeDefaultFontFamily {
		return css, nil
	}
	for _, family := range []string{heading, body} {
		if family != themeDefaultFontFamily {
			if err := themeValidateFontFamily(family); err != nil {
				return "", err
			}
		}
	}
	needsImport := func(family string) bool { return fonts == nil || !fonts.suppressed[family] }
	var families []string
	if heading != themeDefaultFontFamily && needsImport(heading) {
		families = append(families, heading)
	}
	if body != themeDefaultFontFamily && body != heading && needsImport(body) {
		families = append(families, body)
	}
	replace := func(text, which, family string) string {
		return themeReplaceFirst(themeDeclaration("--crt-font-family-"+which), text, func(groups []string) string {
			return groups[1] + "'" + family + "', sans-serif" + groups[2]
		})
	}
	next := replace(css, "heading", heading)
	next = replace(next, "body", body)
	if len(families) == 0 {
		return next, nil
	}
	var weights []int
	if fonts != nil {
		weights = fonts.weights
	}
	rule, err := themeFontImportRule(families, weights)
	if err != nil {
		return "", err
	}
	return rule + "\n" + next, nil
}

// themeFontImportRule is FontImportBuilder.BuildRule: one Google Fonts css2 import for the families, each
// with the distinct sorted weights (400, 500, 600 when none were given; none at all for an empty list).
func themeFontImportRule(families []string, weights []int) (string, error) {
	if weights == nil {
		weights = []int{400, 500, 600}
	}
	distinct := map[int]bool{}
	sorted := []int{}
	for _, weight := range weights {
		if !distinct[weight] {
			distinct[weight] = true
			sorted = append(sorted, weight)
		}
	}
	sort.Ints(sorted)
	params := make([]string, 0, len(families))
	for _, family := range families {
		trimmed := dotnetTrim(family)
		if err := themeValidateFontFamily(trimmed); err != nil {
			return "", err
		}
		name := strings.Join(strings.FieldsFunc(trimmed, unicode.IsSpace), "+")
		if len(sorted) > 0 {
			parts := make([]string, len(sorted))
			for index, weight := range sorted {
				parts[index] = strconv.Itoa(weight)
			}
			name += ":wght@" + strings.Join(parts, ";")
		}
		params = append(params, "family="+name)
	}
	return "@import url('https://fonts.googleapis.com/css2?" + strings.Join(params, "&") + "&display=swap');", nil
}

// ---------------------------------------------------------------------------------------------- templates

// themeTemplateVersions are the bundled template directories, oldest first.
func themeTemplateVersions() []themeWriteVersion {
	entries, _ := themeTemplateFiles.ReadDir("themetemplates")
	versions := []themeWriteVersion{}
	for _, entry := range entries {
		if version, ok := themeWriteParseVersion(entry.Name()); ok && entry.IsDir() {
			versions = append(versions, version)
		}
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i].less(versions[j]) })
	return versions
}

// themeResolveTemplateVersion is ThemeTemplateProvider.ResolveCompatibleVersion: the newest bundled
// template not newer than the requested version, or the newest one when none is requested. The error is
// the ArgumentException message.
func themeResolveTemplateVersion(requested string) (string, error) {
	available := themeTemplateVersions()
	if len(available) == 0 {
		return "", errors.New("No bundled theme templates were found.")
	}
	if strings.TrimSpace(requested) == "" {
		return available[len(available)-1].String(), nil
	}
	target, ok := themeWriteParseVersion(requested)
	if !ok {
		return "", fmt.Errorf("Invalid Creatio version '%s'. (Parameter 'creatioVersion')", requested)
	}
	compatible := ""
	for _, version := range available {
		if !target.less(version) {
			compatible = version.String()
		}
	}
	if compatible == "" {
		return "", fmt.Errorf("Themes require Creatio %s or newer; version %s is not supported. (Parameter 'creatioVersion')",
			available[0].String(), target.String())
	}
	return compatible, nil
}

func themeReadTemplate(file, requested string) (string, error) {
	version, err := themeResolveTemplateVersion(requested)
	if err != nil {
		return "", err
	}
	content, err := themeTemplateFiles.ReadFile("themetemplates/" + version + "/" + file)
	if err != nil {
		return "", fmt.Errorf("Theme template '%s' is missing for Creatio version %s.", file, version)
	}
	return strings.TrimPrefix(string(content), "\uFEFF"), nil
}
