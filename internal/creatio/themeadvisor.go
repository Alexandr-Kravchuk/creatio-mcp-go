package creatio

import (
	"fmt"
	"strconv"
	"strings"
)

type ThemeAdvisorOptions struct {
	Operation, Primary, Role, Color, Secondary, Accent, Success, Error, Version string
	Colors, CandidateHexes                                                      []string
	FullStops                                                                   bool
}

func themeAdvisorWarning(role string, value float64) map[string]any {
	severity := "warning"
	message := fmt.Sprintf("This %s colour is hard to read on white.", role)
	if role == "primary" {
		severity = "strong"
		message = "This colour is hard to read on white; a darker variant is recommended."
	}
	return map[string]any{"code": strings.ToUpper(role) + "_LOW_CONTRAST_ON_WHITE", "severity": severity, "message": message, "values": map[string]any{"contrastOnWhite": value}}
}
func themeAdvisorSimilarity(severity string, distance float64) map[string]any {
	return map[string]any{"code": "ACCENT_TOO_SIMILAR_TO_PRIMARY", "severity": severity, "message": "This accent is very close to the primary colour.", "values": map[string]any{"distanceFromPrimary": distance}}
}
func AdviseThemePalette(o ThemeAdvisorOptions) map[string]any {
	fail := func(message string) map[string]any { return map[string]any{"success": false, "error": message} }
	normalize := func(s string) (string, string) {
		hex, code := themeNormalizeColor(s, true)
		if code != "" {
			return "", fmt.Sprintf("%s: \"%s\"", code, s)
		}
		return hex, ""
	}
	converted := func(raw, hex string) bool { return !strings.EqualFold(dotnetTrim(raw), hex) }
	result := map[string]any{"success": true}
	if o.Operation == "triage" {
		if len(o.Colors) == 0 {
			return fail("INVALID_COLOR: at least one colour is required for triage.")
		}
		colors := []any{}
		accepted, passing := 0, 0
		best := ""
		highest := -1.0
		for _, raw := range o.Colors {
			hex, code := themeNormalizeColor(raw, true)
			item := map[string]any{"input": raw, "accepted": code == ""}
			if code != "" {
				item["rejectionCode"] = code
			} else {
				contrast := themeContrastRatio(hex, themeColorWhite)
				item["normalizedHex"] = hex
				item["wasConverted"] = converted(raw, hex)
				item["contrastOnWhite"] = contrast
				item["passesNonTextContrast"] = themeMeetsMinContrast(hex)
				accepted++
				if themeMeetsMinContrast(hex) {
					passing++
				}
				if contrast > highest {
					highest = contrast
					best = hex
				}
			}
			colors = append(colors, item)
		}
		result["colors"] = colors
		result["acceptedCount"] = accepted
		result["passingCount"] = passing
		if best != "" {
			result["highestContrastHex"] = best
		}
		return result
	}
	if o.Operation == "validate-color" || o.Operation == "accent-validate-manual" {
		role := o.Role
		if o.Operation == "accent-validate-manual" {
			role = "accent"
		}
		if role != "primary" && role != "secondary" && role != "accent" && role != "success" && role != "error" {
			return fail("INVALID_ROLE: role must be primary, secondary, accent, success, or error.")
		}
		color, err := normalize(o.Color)
		if err != "" {
			return fail(err)
		}
		result["normalizedColor"] = color
		result["wasConverted"] = converted(o.Color, color)
		contrast := themeContrastRatio(color, themeColorWhite)
		if role == "accent" {
			primary, code := themeNormalizeColor(o.Primary, true)
			if code != "" {
				return fail("INVALID_COLOR: a valid primary is required to validate an accent.")
			}
			distance := themeDistanceOklab(primary, color)
			band := themeSimilarityBand(distance)
			if band == "strong" {
				result["verdict"] = "strong"
				result["warning"] = themeAdvisorSimilarity("strong", distance)
				return result
			}
			if !themeMeetsMinContrast(color) {
				result["verdict"] = "warn"
				result["warning"] = themeAdvisorWarning(role, contrast)
				return result
			}
			if band == "warn" {
				result["verdict"] = "warn"
				result["warning"] = themeAdvisorSimilarity("warning", distance)
				return result
			}
		} else if !themeMeetsMinContrast(color) {
			verdict := "warn"
			if role == "primary" {
				verdict = "strong"
			}
			result["verdict"] = verdict
			result["warning"] = themeAdvisorWarning(role, contrast)
			return result
		}
		result["verdict"] = "pass"
		return result
	}
	primary, err := normalize(o.Primary)
	if err != "" {
		return fail(err)
	}
	switch o.Operation {
	case "adapt-primary":
		adapted := themeAdaptPrimary(primary)
		result["adaptationState"] = adapted.outcome
		if adapted.outcome != "compliant" {
			result["original500"] = primary
			result["originalContrastOnWhite"] = adapted.originalContrast
			result["warning"] = themeAdvisorWarning("primary", adapted.originalContrast)
		}
		if adapted.outcome == "adapted" {
			result["adapted500"] = adapted.adapted
			result["adaptedContrastOnWhite"] = adapted.adaptedContrast
			result["distanceFromOriginal"] = adapted.distance
		}
	case "derive-secondary":
		result["derivedSecondary"] = themeDeriveSecondary(primary)
		if strings.TrimSpace(o.Secondary) != "" {
			secondary, err := normalize(o.Secondary)
			if err != "" {
				return fail(err)
			}
			readable := themeMeetsMinContrast(secondary)
			result["secondaryHex"] = secondary
			result["secondaryWasConverted"] = converted(o.Secondary, secondary)
			result["secondaryReadable"] = readable
			result["secondaryContrastOnWhite"] = themeContrastRatio(secondary, themeColorWhite)
			if !readable {
				result["warning"] = themeAdvisorWarning("secondary", themeContrastRatio(secondary, themeColorWhite))
			}
		}
	case "accent-evaluate-stored":
		candidates := []any{}
		for _, raw := range o.CandidateHexes {
			hex, code := themeNormalizeColor(raw, true)
			if code != "" {
				continue
			}
			distance := themeDistanceOklab(primary, hex)
			band := themeSimilarityBand(distance)
			candidate := map[string]any{"hex": hex, "distanceFromPrimary": distance, "similarityBand": band, "recommend": band != "strong"}
			if band == "warn" {
				candidate["warning"] = themeAdvisorSimilarity("warning", distance)
			}
			candidates = append(candidates, candidate)
		}
		result["evaluatedCandidates"] = candidates
	case "accent-suggest":
		best, count, scored := themeSelectBestValidAccent(primary, themeAccentCandidates(primary))
		candidates := []any{}
		for _, c := range scored {
			candidates = append(candidates, map[string]any{"hex": c.hex, "offset": c.offset, "contrastOnWhite": c.contrast, "distanceFromPrimary": c.distance, "valid": themeIsValidAccent(c.contrast, c.distance), "isBest": best != nil && c.hex == best.hex})
		}
		result["suggestedCandidates"] = candidates
		result["validCandidateCount"] = count
		result["primaryAsAccentAvailable"] = count <= 1
		if best != nil {
			result["bestCandidateHex"] = best.hex
		}
	case "preview":
		secondary, err := normalize(o.Secondary)
		if err != "" {
			return fail(err)
		}
		accent, err := normalize(o.Accent)
		if err != "" {
			return fail(err)
		}
		version, e := themeResolveTemplateVersion(o.Version)
		if e != nil {
			return fail(fmt.Sprintf("VERSION_NOT_SUPPORTED: \"%s\"", o.Version))
		}
		template, _ := themeReadTemplate("theme.css.tpl", version)
		anchors := map[string]string{"primary": primary, "secondary": secondary, "accent": accent}
		warnings := []string{}
		for _, role := range []string{"success", "error"} {
			raw := o.Success
			if role == "error" {
				raw = o.Error
			}
			source := "template-default"
			hex, ok := themeTemplatePaletteBase(template, role)
			if strings.TrimSpace(raw) != "" {
				source = "user-override"
				var err string
				hex, err = normalize(raw)
				if err != "" {
					return fail(err)
				}
				prefix := role
				result["normalized"+strings.ToUpper(prefix[:1])+prefix[1:]] = hex
				result[prefix+"WasConverted"] = converted(raw, hex)
				readable := themeMeetsMinContrast(hex)
				result[prefix+"ContrastVerdict"] = readable
				result[prefix+"ContrastOnWhite"] = themeContrastRatio(hex, themeColorWhite)
				if !readable {
					warnings = append(warnings, strings.ToUpper(role)+"_LOW_CONTRAST_ON_WHITE")
				}
			} else if !ok {
				return fail(fmt.Sprintf("TEMPLATE_DEFAULT_MISSING: \"%s@%s\"", role, version))
			}
			anchors[role] = hex
			result[role+"Source"] = source
		}
		palettes := map[string]any{}
		for role, anchor := range anchors {
			stops := map[string]string{}
			scale := themeGenerateScale(anchor)
			if o.FullStops {
				for step, hex := range scale {
					stops[strconv.Itoa(step)] = hex
				}
			} else {
				stops["500"] = scale[500]
			}
			palettes[role] = stops
		}
		result["palettes"] = palettes
		result["resolvedVersion"] = version
		if len(warnings) > 0 {
			result["warnings"] = warnings
		}
	default:
		return fail(fmt.Sprintf("operation \"%s\" is not recognized.", o.Operation))
	}
	return result
}
