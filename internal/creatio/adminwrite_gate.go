package creatio

// The two requirement gates clio's BaseTool runs after resolving the environment and before a command:
// RequiresCreatioVersion (CreatioVersionChecker) and RequiresPackage (RequiredPackageChecker). clio's third
// check, bundled-package convergence (refusing a cliogate older than the one that clio release ships), is
// not ported: this server ships no cliogate to compare with.

import (
	"context"
	"strings"
)

// AdminWriteRequirementError is an unmet requirement. Version refusals carry clio's stable code and exit
// code 78; package refusals exit 1 (FromValidationError).
type AdminWriteRequirementError struct {
	Message string
	Code    string // version refusals only: version-too-old, version-undeterminable, version-check-failed
}

func (e *AdminWriteRequirementError) Error() string {
	if e.Code == "" {
		return e.Message
	}
	return e.Message + " [" + e.Code + "]"
}

// AdminWriteRequirementFailure is the command envelope of a refused requirement.
func AdminWriteRequirementFailure(err *AdminWriteRequirementError) CommandResult {
	if err.Code != "" {
		return NewCommandResult(themeWriteVersionExitCode, "Error", err.Error())
	}
	return CommandFailure(err.Message)
}

// AdminWriteEnsureVersion is CreatioVersionChecker.EnsureRequirements for one triggered requirement.
func (c *Client) AdminWriteEnsureVersion(ctx context.Context, floor, hint string, allowDevelopmentBuild bool) *AdminWriteRequirementError {
	required, _ := themeWriteParseVersion(floor)
	version, status := c.themeWriteResolveVersion(ctx)
	switch status {
	case themeWriteProbeFailed:
		return &AdminWriteRequirementError{Code: "version-check-failed", Message: "Could not perform the Creatio platform version check " +
			"for the target environment (it could not be reached, or access was denied). This command requires " + floor +
			" or later — verify connectivity and permissions, then retry."}
	case themeWriteReachableWithoutVersion:
		return &AdminWriteRequirementError{Code: "version-undeterminable", Message: "Could not determine the Creatio platform version " +
			"of the target environment; this command requires " + floor + " or later."}
	}
	if version.isDevBuild() && allowDevelopmentBuild {
		return nil
	}
	if version.less(required) {
		message := "This command requires Creatio " + floor + " or later. The target environment runs " + version.String() +
			". Update Creatio and retry."
		if hint != "" {
			message += "\n" + hint
		}
		return &AdminWriteRequirementError{Code: "version-too-old", Message: message}
	}
	return nil
}

// AdminWriteEnsurePackage is RequiredPackageChecker.EnsureRequirements for one triggered requirement: the
// package (cliogate also under cliogate_netcore, the lowest installed version counting) must be installed,
// at minVersion or higher when one is given. A failed package read is returned as is.
func (c *Client) AdminWriteEnsurePackage(ctx context.Context, name, minVersion, hint string) (*AdminWriteRequirementError, error) {
	packages, err := c.pkgWriteInstalledPackages(ctx)
	if err != nil {
		return nil, err
	}
	names := []string{name}
	if strings.EqualFold(name, "cliogate") {
		names = append(names, "cliogate_netcore")
	}
	var lowest *themeWriteVersion
	found := false
	for _, item := range packages {
		for _, candidate := range names {
			if !strings.EqualFold(item.Name, candidate) {
				continue
			}
			found = true
			// PackageVersion keeps only the numeric part before a '-' suffix.
			parsed, ok := themeWriteParseVersion(strings.SplitN(item.Version, "-", 2)[0])
			if !ok {
				parsed = themeWriteVersion{0, 0, -1, -1}
			}
			if lowest == nil || parsed.less(*lowest) {
				copied := parsed
				lowest = &copied
			}
		}
	}
	withHint := func(message string) *AdminWriteRequirementError {
		if hint != "" {
			message += "\n" + hint
		}
		return &AdminWriteRequirementError{Message: message}
	}
	if minVersion == "" {
		if !found {
			return withHint("To use this command, you need to install the " + name + " package. " +
				"Install the package in the target environment and retry."), nil
		}
		return nil, nil
	}
	required, _ := themeWriteParseVersion(minVersion)
	if !found || adminWriteVersionLess(*lowest, required) {
		return withHint("To use this command, you need to install the " + name + " package version " + minVersion +
			" or higher. Install or update the package in the target environment and retry."), nil
	}
	return nil, nil
}

// adminWriteVersionLess compares as PackageVersion does: an absent component counts as zero.
func adminWriteVersionLess(left, right themeWriteVersion) bool {
	for index := range left {
		a, b := left[index], right[index]
		if a < 0 {
			a = 0
		}
		if b < 0 {
			b = 0
		}
		if a != b {
			return a < b
		}
	}
	return false
}
