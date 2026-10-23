package creatio

// The Creatio side of the link-from-repository tools (clio's Link4RepoCommand): the Maintainer system setting
// read and write of SysSettingsManager, PackageLockManager.Unlock with a package list,
// FileDesignModePackages.LoadPackagesToFileSystem and ApplicationPackageListProvider.GetPackages with the
// {"isCustomer": true} filter (SysPackage rows whose InstallType is 0).

import (
	"context"
	"encoding/json"
	"fmt"
)

// WorkspaceLinkPackage is the part of a SysPackage row the unlocked flow reads: Descriptor.Name and
// Descriptor.PackageVersion, a missing value read as "".
type WorkspaceLinkPackage struct {
	Name    string
	Version string
}

// WorkspaceLinkSite runs the link commands' remote calls over one environment's client.
type WorkspaceLinkSite struct{ client *Client }

// WorkspaceLinkSite returns the link commands' view of this client.
func (c *Client) WorkspaceLinkSite() WorkspaceLinkSite { return WorkspaceLinkSite{client: c} }

// SysSettingValue is SysSettingsManager.GetSysSettingValueByCode.
func (s WorkspaceLinkSite) SysSettingValue(ctx context.Context, code string) (string, error) {
	return s.client.readSysSettingValue(ctx, code)
}

// UpdateSysSetting is SysSettingsManager.UpdateSysSetting(code, value) with its default type name, Text.
func (s WorkspaceLinkSite) UpdateSysSetting(ctx context.Context, code, value string) (bool, error) {
	return s.client.adminWriteUpdateValue(ctx, code, value, "Text")
}

// UnlockPackages is PackageLockManager.Unlock(packages): ClioGate's UnlockPackages with the names, or null
// for none, and clio's three texts for an answer that is not true.
func (s WorkspaceLinkSite) UnlockPackages(ctx context.Context, names []string) error {
	const route = "UnlockPackages"
	var list any
	if len(names) > 0 {
		items := make([]any, 0, len(names))
		for _, name := range names {
			items = append(items, name)
		}
		list = items
	}
	body := toJNode(orderedFields{{"unlockPackages", list}}).stjJSON()
	call := serviceCall{Route: "rest/CreatioApiGateway/UnlockPackages", Body: body, Label: "ClioGate"}
	response, err := s.client.serviceRequest(ctx, call)
	if err != nil {
		return err
	}
	if response.status < 200 || response.status >= 300 {
		return fmt.Errorf("%s %s returned HTTP %d", call.label(), call.Route, response.status)
	}
	// A 2xx answer is judged by its body, as clio's CallGate does: an HTML page is a non-JSON response.
	payload := response.payload
	if len(payload) == 0 {
		return fmt.Errorf("ClioGate %s returned an empty response. Check the Creatio application logs (Error.log) and verify the installed cliogate version is current.", route)
	}
	var success bool
	if err := json.Unmarshal(payload, &success); err != nil {
		return fmt.Errorf("ClioGate %s returned a non-JSON response (likely an HTTP error page). Check the Creatio application logs (Error.log) and verify the installed cliogate version is current.", route)
	}
	if !success {
		return fmt.Errorf("ClioGate %s returned false. Check the Creatio application logs for details.", route)
	}
	return nil
}

// LoadPackagesToFileSystem is FileDesignModePackages.LoadPackagesToFileSystem: the log it writes and its
// outcome, "completed", "disabled", "unknown" or "refused".
func (s WorkspaceLinkSite) LoadPackagesToFileSystem(ctx context.Context) ([]LogMessage, string, error) {
	return s.client.pkgWriteLoad(ctx, "file system", pkgWriteToFSRoute)
}

// UnlockedPackages is ApplicationPackageListProvider.GetPackages("{\"isCustomer\": true}"): the SysPackage
// rows (Name, UId, Maintainer, Version) whose InstallType is 0, in the order the site returns them.
func (s WorkspaceLinkSite) UnlockedPackages(ctx context.Context) ([]WorkspaceLinkPackage, error) {
	const intDataValueType = 4
	rows, err := s.client.clioSelectRows(ctx, "SysPackage",
		[]rightsColumn{{path: "Name", alias: "Name"}, {path: "UId", alias: "UId"}, {path: "Maintainer", alias: "Maintainer"},
			{path: "Version", alias: "Version"}},
		[]rightsFilter{{column: "InstallType", value: 0, dataValueType: intDataValueType}})
	if err != nil {
		return nil, err
	}
	packages := make([]WorkspaceLinkPackage, 0, len(rows))
	for _, row := range rows {
		packages = append(packages, WorkspaceLinkPackage{Name: wpRowText(row, "Name"), Version: wpRowText(row, "Version")})
	}
	return packages, nil
}
