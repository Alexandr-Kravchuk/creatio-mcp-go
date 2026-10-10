package creatio

// set-background-image: clio's SetBackgroundImageCommand.SetBackground (clio master 914dab286, the same texts
// in 8.1.0.134). The image is uploaded (or an uploaded one is checked), registered in the shell_background
// gallery, CrtBackgroundConfig points at it, UsePanelIconBackground is turned off unless kept, and the four
// parts are bound into the package.

import (
	"context"
	"fmt"
	"strings"
)

const (
	themeWriteShellBackgroundTagID = "273c2402-7cae-456b-a9c4-067d2024f1a7"
	themeWriteBackgroundConfigCode = "CrtBackgroundConfig"
	themeWritePanelIconFeature     = "UsePanelIconBackground"
	// SetBackgroundBothSourcesError is SetBackgroundImageCommand.BothSourcesError.
	SetBackgroundBothSourcesError = "Pass either a file or an image-id, not both."
	// SetBackgroundNoSourceError is SetBackgroundImageCommand.NoSourceError.
	SetBackgroundNoSourceError = "Pass a file to upload (file) or the id of an already-uploaded image (image-id)."
)

// SetBackgroundRequest carries clio's SetBackgroundImageOptions.
type SetBackgroundRequest struct {
	ImageID, File, Package string
	KeepIconBackground     bool
}

// SetBackgroundResult is clio's SetBackgroundResult. Fault is set when clio's command throws (the tool then
// answers only the redacted message); Error is a failure the command reports itself.
type SetBackgroundResult struct {
	Success  bool
	ImageID  string
	Package  string
	Warnings []string
	Bound    []string
	Error    string
	Fault    error
}

// SetBackgroundToolResult is clio's SetBackgroundImageResult.
type SetBackgroundToolResult struct {
	Success  bool     `json:"success"`
	ImageID  string   `json:"image-id,omitempty"`
	Bound    []string `json:"bound,omitempty"`
	Package  string   `json:"package,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
	Error    string   `json:"error,omitempty"`
}

// SetBackgroundToolFailure is SetBackgroundImageResult.Failure with nothing else: blank reads "unknown".
func SetBackgroundToolFailure(message string) SetBackgroundToolResult {
	if strings.TrimSpace(message) == "" {
		message = "unknown"
	}
	return SetBackgroundToolResult{Error: message}
}

// themeWriteGallery is SetBackgroundImageCommand.GalleryMembership.
type themeWriteGallery struct{ rowID, tagID string }

// SetBackground sets the shell background and binds it.
func (c *Client) SetBackground(ctx context.Context, request SetBackgroundRequest) SetBackgroundResult {
	imageID, problem := c.themeWriteResolveImage(ctx, request)
	if problem != "" {
		return SetBackgroundResult{Error: problem}
	}
	gallery, problem := c.themeWriteEnsureGallery(ctx, imageID)
	if problem != "" {
		return SetBackgroundResult{Error: problem}
	}
	config := toJNode(orderedFields{{"imageId", imageID}, {"mode", "Image"}}).stjJSON()
	written, err := c.themeWriteUpdateSysSetting(ctx, themeWriteBackgroundConfigCode, string(config), "Text")
	if err != nil {
		if nonJSON, ok := err.(themeWriteNonJSONError); ok {
			return SetBackgroundResult{Error: "The image is in the background gallery, but writing the " + themeWriteBackgroundConfigCode +
				" setting failed. " + nonJSON.message}
		}
		return SetBackgroundResult{Fault: err}
	}
	if !written {
		return SetBackgroundResult{Error: "The image is in the background gallery, but writing the " + themeWriteBackgroundConfigCode + " setting failed."}
	}
	var warnings []string
	if request.KeepIconBackground {
		warnings = append(warnings, "The "+themeWritePanelIconFeature+" feature was left as is "+
			"(keep-icon-background); while it is on, the panel's icon background can hide the shell background.")
	} else if err := c.themeWriteSetFeatureOff(ctx, themeWritePanelIconFeature); err != nil {
		warnings = append(warnings, "The background image was applied, but turning off the "+themeWritePanelIconFeature+
			" feature failed, so the panel may still hide it: "+err.Error())
	}
	return c.themeWriteBindBackground(ctx, imageID, gallery, request.Package, warnings)
}

// themeWriteResolveImage is TryResolveOrUploadImageId.
func (c *Client) themeWriteResolveImage(ctx context.Context, request SetBackgroundRequest) (string, string) {
	hasFile, hasID := strings.TrimSpace(request.File) != "", strings.TrimSpace(request.ImageID) != ""
	if hasFile && hasID {
		return "", SetBackgroundBothSourcesError
	}
	if !hasFile && !hasID {
		return "", SetBackgroundNoSourceError
	}
	if hasFile {
		upload := c.themeWriteUploadImage(ctx, request.File)
		return upload.ImageID, upload.Error
	}
	imageID, ok := parseGUID(request.ImageID)
	if !ok || imageID == emptyGUID {
		return "", fmt.Sprintf("image-id '%s' is not a valid id. Pass the id printed by upload-image.", request.ImageID)
	}
	existing, problem := c.themeWriteODataSingleID(ctx, "SysImage", "Id eq "+imageID)
	if problem != "" {
		return "", "Could not check the image in the environment: " + problem
	}
	if existing == "" {
		return "", fmt.Sprintf("No uploaded image with id '%s' was found in the environment. "+
			"Upload the file first with upload-image and pass the id it prints.", imageID)
	}
	return imageID, ""
}

// themeWriteGalleryProbe is SetBackgroundImageCommand.GalleryProbe.
type themeWriteGalleryProbe struct{ rowID, hardError, registrationError string }

// themeWriteEnsureGallery is TryEnsureInBackgroundGallery.
func (c *Client) themeWriteEnsureGallery(ctx context.Context, imageID string) (themeWriteGallery, string) {
	probe := c.themeWriteMembershipForTag(ctx, imageID, themeWriteShellBackgroundTagID)
	if probe.hardError != "" {
		return themeWriteGallery{}, probe.hardError
	}
	if probe.rowID != "" {
		return themeWriteGallery{probe.rowID, themeWriteShellBackgroundTagID}, ""
	}
	tagID, problem := c.themeWriteODataSingleID(ctx, "SysImageTag", "Name eq 'shell_background'")
	if problem != "" {
		return themeWriteGallery{}, "Could not resolve the background gallery tag: " + problem
	}
	if tagID == "" {
		return themeWriteGallery{}, "The background gallery tag was not found in the environment, so the image could not " +
			"be registered in the gallery."
	}
	if tagID != themeWriteShellBackgroundTagID {
		probe = c.themeWriteMembershipForTag(ctx, imageID, tagID)
		if probe.hardError != "" {
			return themeWriteGallery{}, probe.hardError
		}
		if probe.rowID != "" {
			return themeWriteGallery{probe.rowID, tagID}, ""
		}
	}
	message := "Registering the image in the background gallery failed."
	if probe.registrationError != "" {
		message += " " + probe.registrationError
	}
	return themeWriteGallery{}, message
}

// themeWriteMembershipForTag is EnsureMembershipForTag: read, register when missing, read again.
func (c *Client) themeWriteMembershipForTag(ctx context.Context, imageID, tagID string) themeWriteGalleryProbe {
	filter := "Entity/Id eq " + imageID + " and Tag/Id eq " + tagID
	rowID, problem := c.themeWriteODataSingleID(ctx, "SysImageInTag", filter)
	if problem != "" {
		return themeWriteGalleryProbe{hardError: "Could not check the background gallery: " + problem}
	}
	if rowID != "" {
		return themeWriteGalleryProbe{rowID: rowID}
	}
	registrationError := ""
	body := toJNode(orderedFields{{"EntityId", imageID}, {"TagId", tagID}}).stjJSON()
	if err := c.themeWriteODataPost(ctx, "SysImageInTag", body); err != nil {
		registrationError = err.Error()
	}
	rowID, problem = c.themeWriteODataSingleID(ctx, "SysImageInTag", filter)
	if problem != "" {
		return themeWriteGalleryProbe{hardError: "Could not verify the background gallery registration: " + problem,
			registrationError: registrationError}
	}
	return themeWriteGalleryProbe{rowID: rowID, registrationError: registrationError}
}

// themeWriteBindBackground is BindBackground with CreateBackgroundDataBindings.
func (c *Client) themeWriteBindBackground(ctx context.Context, imageID string, gallery themeWriteGallery, packageName string,
	warnings []string) SetBackgroundResult {
	binder := c.themeWriteNewBinder(ctx)
	var bound []string
	pkg := ""
	record := func(outcome themeWriteBindingOutcome, part string) {
		warnings = append(warnings, outcome.warnings...)
		if outcome.bound {
			bound = append(bound, part)
		}
	}
	err := func() error {
		name, err := binder.usePackage(packageName)
		if err != nil {
			return err
		}
		pkg = name
		image, err := binder.bindRow("SysImage", "ShellBackground", []string{"Id", "Name", "Data", "MimeType"}, imageID)
		if err != nil {
			return err
		}
		record(image, "image")
		membership, err := c.themeWriteBindGallery(binder, image.bound, gallery)
		if err != nil {
			return err
		}
		record(membership, "gallery-membership")
		config, err := themeWriteBindBackgroundConfig(binder, image.bound)
		if err != nil {
			return err
		}
		record(config, "background-config")
		featureOff, err := binder.bindFeatureOffState(themeWritePanelIconFeature)
		if err != nil {
			return err
		}
		record(featureOff, "panel-icon-off-state")
		return nil
	}()
	if err != nil {
		into, kept := "", ""
		if pkg != "" {
			into = " into package '" + pkg + "'"
		}
		if len(bound) > 0 {
			kept = " Already bound and left in place: " + strings.Join(bound, ", ") + "."
		}
		return SetBackgroundResult{Error: fmt.Sprintf("The background was applied (image %s), but binding it%s failed: %s%s",
			imageID, into, err.Error(), kept), Warnings: warnings, Package: pkg, Bound: bound}
	}
	return SetBackgroundResult{Success: true, ImageID: imageID, Package: pkg, Warnings: warnings, Bound: bound}
}

// themeWriteBindGallery is BindGalleryMembership.
func (c *Client) themeWriteBindGallery(binder *themeWriteBinder, imageBound bool, gallery themeWriteGallery) (themeWriteBindingOutcome, error) {
	folder := "SysImageInTag_ShellBackground"
	if !imageBound {
		dropped, err := binder.removeBinding(folder, "SysImageInTag")
		if err != nil {
			return themeWriteBindingOutcome{}, err
		}
		return themeWriteRefused(append([]string{"background gallery membership: the image row was not bound, so the membership naming it was not " +
			"bound either — a package that ships the membership without the image installs a gallery entry " +
			"pointing at an image the target does not have"}, dropped...)), nil
	}
	if gallery.tagID != themeWriteShellBackgroundTagID {
		dropped, err := binder.removeBinding(folder, "SysImageInTag")
		if err != nil {
			return themeWriteBindingOutcome{}, err
		}
		return themeWriteRefused(append([]string{fmt.Sprintf("background gallery membership: this environment's shell_background tag has a customized "+
			"id (%s) that would not resolve on an install target, so the membership row was not bound", gallery.tagID)}, dropped...)), nil
	}
	return binder.bindRow("SysImageInTag", "ShellBackground", []string{"Id", "Entity", "Tag"}, gallery.rowID)
}

// themeWriteBindBackgroundConfig is BindBackgroundConfig.
func themeWriteBindBackgroundConfig(binder *themeWriteBinder, imageBound bool) (themeWriteBindingOutcome, error) {
	if imageBound {
		return binder.bindSysSettingsValue(themeWriteBackgroundConfigCode)
	}
	dropped, err := binder.removeSysSettingsValue(themeWriteBackgroundConfigCode)
	if err != nil {
		return themeWriteBindingOutcome{}, err
	}
	return themeWriteRefused(append([]string{themeWriteBackgroundConfigCode + ": the image row was not bound, so the configuration naming it was not bound " +
		"either — a package that ships the configuration without the image installs a background the target " +
		"cannot render"}, dropped...)), nil
}
