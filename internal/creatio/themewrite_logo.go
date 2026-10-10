package creatio

// set-logo: clio's SetLogoCommand.ApplyLogos (clio master 914dab286, the same texts in 8.1.0.134). Each
// image goes to its Binary setting, the companions (HideSplashScreenLogoImage, UseFaviconFromSysSettings) are
// turned on, and everything applied is bound into the package as SysSettingsValue data bindings. A refused
// image does not undo the ones that applied: the run still binds them and reports a failure naming the rest.

import (
	"context"
	"fmt"
	"os"
	"strings"
)

// SetLogoRequest carries clio's SetLogoOptions.
type SetLogoRequest struct {
	Logo, LoginLogo, MenuLogo, ConfigurationLogo, DarkLogo, Favicon, Package string
}

// SetLogoResult is clio's SetLogoResult: Error is the unredacted failure; the tool redacts it.
type SetLogoResult struct {
	Success  bool
	Applied  []string
	Bound    []string
	Package  string
	Warnings []string
	Error    string
}

// SetLogoNoImageError is SetLogoCommand.NoImageError.
const SetLogoNoImageError = "Pass at least one image file: logo (every slot at once), login-logo, menu-logo, configuration-logo, " +
	"dark-logo, or favicon."

var (
	themeWriteLogoFormats    = []string{"png", "jpg", "jpeg", "gif", "bmp", "webp", "svg"}
	themeWriteFaviconFormats = []string{"png", "jpg", "jpeg", "gif", "bmp", "webp", "svg", "ico"}
)

// themeWriteCompanion is SetLogoCommand.BrandingCompanion.
type themeWriteCompanion struct {
	code, failure string
	required      bool
}

var (
	themeWriteSplashSuppression = &themeWriteCompanion{"HideSplashScreenLogoImage",
		"The logos were applied, but setting HideSplashScreenLogoImage failed, so the stock splash logo may still " +
			"flash during load", false}
	themeWriteFaviconGate = &themeWriteCompanion{"UseFaviconFromSysSettings",
		"The favicon image was written, but turning UseFaviconFromSysSettings on failed, so the platform ignores the " +
			"image and keeps the stock icon", true}
)

// themeWriteBrandingImage is SetLogoCommand.BrandingImage.
type themeWriteBrandingImage struct {
	label, code, file string
	formats           []string
	companion         *themeWriteCompanion
}

type themeWriteRefusedImage struct {
	image themeWriteBrandingImage
	error string
}

// SetLogo applies and binds the requested logos.
func (c *Client) SetLogo(ctx context.Context, request SetLogoRequest) SetLogoResult {
	slot := func(override string) string {
		if strings.TrimSpace(override) == "" {
			return request.Logo
		}
		return override
	}
	candidates := []themeWriteBrandingImage{
		{"login-logo", "LogoImage", slot(request.LoginLogo), themeWriteLogoFormats, themeWriteSplashSuppression},
		{"menu-logo", "MenuLogoImage", slot(request.MenuLogo), themeWriteLogoFormats, themeWriteSplashSuppression},
		{"configuration-logo", "ConfigurationPageLogoImage", slot(request.ConfigurationLogo), themeWriteLogoFormats, themeWriteSplashSuppression},
		{"dark-logo", "CrtAppToolbarLogo", slot(request.DarkLogo), themeWriteLogoFormats, themeWriteSplashSuppression},
		{"favicon", "FaviconImage", request.Favicon, themeWriteFaviconFormats, themeWriteFaviconGate},
	}
	var requested []themeWriteBrandingImage
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate.file) != "" {
			requested = append(requested, candidate)
		}
	}
	if len(requested) == 0 {
		return SetLogoResult{Error: SetLogoNoImageError}
	}
	var missing []string
	for _, image := range requested {
		if info, err := os.Stat(image.file); err != nil || info.IsDir() {
			missing = append(missing, fmt.Sprintf("'%s' (passed for %s)", image.file, image.label))
		}
	}
	if len(missing) > 0 {
		return SetLogoResult{Error: "Files not found: " + strings.Join(missing, "; ") + "."}
	}
	var supported []themeWriteBrandingImage
	var refused []themeWriteRefusedImage
	for _, image := range requested {
		if problem := themeWriteUnsupportedFormat(image); problem != "" {
			refused = append(refused, themeWriteRefusedImage{image, problem})
		} else {
			supported = append(supported, image)
		}
	}
	var applied []themeWriteBrandingImage
	for _, image := range supported {
		if failure := c.themeWriteTryUpdateSetting(ctx, image.code, "", image.file); failure != "" {
			refused = append(refused, themeWriteRefusedImage{image, failure})
		} else {
			applied = append(applied, image)
		}
	}
	if len(applied) == 0 {
		return SetLogoResult{Error: themeWriteDescribeRefused(refused)}
	}
	// TurnCompanionsOn: each distinct companion of an applied image, in order.
	var companions []*themeWriteCompanion
	for _, image := range applied {
		if image.companion != nil && !themeWriteHasCompanion(companions, image.companion) {
			companions = append(companions, image.companion)
		}
	}
	var companionCodes []string
	type companionFailure struct {
		fatal   bool
		message string
	}
	var companionRefused []struct {
		companion *themeWriteCompanion
		error     string
	}
	for _, companion := range companions {
		if failure := c.themeWriteTryUpdateSetting(ctx, companion.code, "true", ""); failure != "" {
			companionRefused = append(companionRefused, struct {
				companion *themeWriteCompanion
				error     string
			}{companion, failure})
		} else {
			companionCodes = append(companionCodes, companion.code)
		}
	}
	var companionFailures []companionFailure
	for _, refusal := range companionRefused {
		if c.themeWriteSettingIsOn(ctx, refusal.companion.code) {
			companionCodes = append(companionCodes, refusal.companion.code)
		} else {
			companionFailures = append(companionFailures, companionFailure{refusal.companion.required,
				refusal.companion.failure + ": " + refusal.error})
		}
	}
	// BindAppliedImages.
	binder := c.themeWriteNewBinder(ctx)
	var deliveryWarnings, boundCodes []string
	packageName := ""
	var deliveryFailure error
	if name, err := binder.usePackage(request.Package); err != nil {
		deliveryFailure = err
	} else {
		packageName = name
		codes := make([]string, 0, len(applied)+len(companionCodes))
		for _, image := range applied {
			codes = append(codes, image.code)
		}
		codes = append(codes, companionCodes...)
		for _, code := range codes {
			outcome, err := binder.bindSysSettingsValue(code)
			if err != nil {
				deliveryFailure = err
				break
			}
			deliveryWarnings = append(deliveryWarnings, outcome.warnings...)
			if outcome.bound {
				boundCodes = append(boundCodes, code)
			}
		}
	}
	// BuildResult.
	labels := make([]string, 0, len(applied))
	for _, image := range applied {
		labels = append(labels, image.label)
	}
	var warnings, errorsList []string
	for _, failure := range companionFailures {
		if !failure.fatal {
			warnings = append(warnings, failure.message)
		}
	}
	warnings = append(warnings, deliveryWarnings...)
	if len(refused) > 0 {
		errorsList = append(errorsList, themeWriteDescribeRefused(refused))
	}
	for _, failure := range companionFailures {
		if failure.fatal {
			errorsList = append(errorsList, failure.message)
		}
	}
	if deliveryFailure != nil {
		into := ""
		if packageName != "" {
			into = " into package '" + packageName + "'"
		}
		errorsList = append(errorsList, "The applied branding could not be bound"+into+": "+deliveryFailure.Error())
	}
	result := SetLogoResult{Success: len(errorsList) == 0, Applied: labels, Bound: boundCodes, Package: packageName, Warnings: warnings}
	if !result.Success {
		result.Error = strings.Join(errorsList, " ")
	}
	return result
}

func themeWriteHasCompanion(companions []*themeWriteCompanion, wanted *themeWriteCompanion) bool {
	for _, companion := range companions {
		if companion == wanted {
			return true
		}
	}
	return false
}

// themeWriteUnsupportedFormat is SetLogoCommand.FindUnsupportedFormatError.
func themeWriteUnsupportedFormat(image themeWriteBrandingImage) string {
	list := strings.Join(image.formats, ", ")
	extension := strings.TrimLeft(themeWriteExtension(image.file), ".")
	if extension == "" {
		return fmt.Sprintf("the file has no extension, so it cannot be verified as an image (%s accepts: %s)", image.label, list)
	}
	for _, format := range image.formats {
		if strings.EqualFold(format, extension) {
			return ""
		}
	}
	return fmt.Sprintf("'.%s' is not a supported image format (%s accepts: %s)", extension, image.label, list)
}

// themeWriteDescribeRefused is SetLogoCommand.DescribeRefusedImages.
func themeWriteDescribeRefused(refused []themeWriteRefusedImage) string {
	details := make([]string, 0, len(refused))
	for _, refusal := range refused {
		details = append(details, fmt.Sprintf("%s (%s) from '%s': %s", refusal.image.label, refusal.image.code, refusal.image.file, refusal.error))
	}
	return fmt.Sprintf("Applying %d image(s) failed: %s.", len(refused), strings.Join(details, "; "))
}

// SetLogoToolResult is clio's SetLogoToolResult: empty lists and an unresolved package are omitted.
type SetLogoToolResult struct {
	Success  bool     `json:"success"`
	Applied  []string `json:"applied,omitempty"`
	Bound    []string `json:"bound,omitempty"`
	Package  string   `json:"package,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
	Error    string   `json:"error,omitempty"`
}

// SetLogoToolFailure is SetLogoToolResult.Failure with nothing applied: a blank message reads "unknown".
func SetLogoToolFailure(message string) SetLogoToolResult {
	if strings.TrimSpace(message) == "" {
		message = "unknown"
	}
	return SetLogoToolResult{Error: message}
}
