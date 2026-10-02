package creatio

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// TargetPackageResult is clio's get-target-package envelope. ResolutionFailed is true when Creatio answered
// and named no usable package, false when Creatio could not be asked, and absent on success.
type TargetPackageResult struct {
	Success          bool   `json:"success"`
	ResolutionFailed *bool  `json:"resolutionFailed,omitempty"`
	Error            string `json:"error,omitempty"`
	PackageName      string `json:"package-name,omitempty"`
}

type targetPackageRow struct {
	Name        string
	UID         string
	InstallType *int
}

// GetTargetPackage resolves the package design-time writes land in and refuses a locked one: the named
// package when one is given, otherwise the package the CurrentPackageId system setting points at.
func (c *Client) GetTargetPackage(ctx context.Context, packageName string) TargetPackageResult {
	if strings.TrimSpace(packageName) == "" {
		return c.resolveCurrentTargetPackage(ctx)
	}
	return c.resolveNamedTargetPackage(ctx, strings.TrimSpace(packageName))
}

func (c *Client) resolveNamedTargetPackage(ctx context.Context, packageName string) TargetPackageResult {
	rows, err := c.selectTargetPackages(ctx, nil)
	if err != nil {
		return targetPackageUnavailable(err)
	}
	for _, row := range rows {
		if !strings.EqualFold(row.Name, packageName) {
			continue
		}
		if row.locked() {
			return targetPackageUnresolvable(fmt.Sprintf("Package '%s' is locked, so it cannot receive design-time writes. Unlock it with unlock-package, or name another package.", row.Name))
		}
		return materializeTargetPackage(row, fmt.Sprintf("Package '%s'", row.Name))
	}
	return targetPackageUnresolvable(fmt.Sprintf("Package '%s' was not found in the environment. Check the name against list-packages.", packageName))
}

func (c *Client) resolveCurrentTargetPackage(ctx context.Context) TargetPackageResult {
	currentPackageID, err := c.readSysSettingValue(ctx, currentPackageSettingCode)
	if err != nil {
		return targetPackageUnavailable(err)
	}
	packageID, ok := parseGUID(currentPackageID)
	if !ok || packageID == emptyGUID {
		return targetPackageUnresolvable(fmt.Sprintf("No package was named, and the environment's %s system setting does not point at one, so there is nowhere to deliver the package data. Name the package explicitly (see list-packages for the available names).", currentPackageSettingCode))
	}
	rows, err := c.selectTargetPackages(ctx, map[string]any{"Id": comparisonFilter("Id", packageID, 0, 3)})
	if err != nil {
		return targetPackageUnavailable(err)
	}
	if len(rows) > 1 {
		return targetPackageUnresolvable(fmt.Sprintf("The environment's %s system setting points at package '%s', which matched %d packages, so the delivery target cannot be told apart. Name the package explicitly (see list-packages for the available names).", currentPackageSettingCode, currentPackageID, len(rows)))
	}
	if len(rows) == 0 || strings.TrimSpace(rows[0].Name) == "" {
		return targetPackageUnresolvable(fmt.Sprintf("The environment's %s system setting points at package '%s', which could not be resolved to a usable package. Name the package explicitly (see list-packages for the available names).", currentPackageSettingCode, currentPackageID))
	}
	row := rows[0]
	if row.locked() {
		return targetPackageUnresolvable(fmt.Sprintf("The environment's %s system setting points at package '%s', which is locked, so it cannot receive design-time writes. Unlock it with unlock-package, or name another package.", currentPackageSettingCode, row.Name))
	}
	return materializeTargetPackage(row, fmt.Sprintf("The %s package '%s'", currentPackageSettingCode, row.Name))
}

func (c *Client) selectTargetPackages(ctx context.Context, filters map[string]any) ([]targetPackageRow, error) {
	rows, err := c.selectRows(ctx, buildSelectQuery("SysPackage", map[string]string{
		"Name": "Name", "UId": "UId", "InstallType": "InstallType",
	}, filters, 10000))
	if err != nil {
		return nil, err
	}
	packages := make([]targetPackageRow, 0, len(rows))
	for _, row := range rows {
		item := targetPackageRow{Name: rowString(row, "Name"), UID: rowString(row, "UId")}
		var installType int
		if raw := row["InstallType"]; len(raw) > 0 && string(raw) != "null" && json.Unmarshal(raw, &installType) == nil {
			item.InstallType = &installType
		}
		packages = append(packages, item)
	}
	return packages, nil
}

func (row targetPackageRow) locked() bool { return row.InstallType != nil && *row.InstallType != 0 }

func materializeTargetPackage(row targetPackageRow, subject string) TargetPackageResult {
	if uid, ok := parseGUID(row.UID); !ok || uid == emptyGUID {
		return targetPackageUnresolvable(subject + " has no usable UId in the environment, so package data cannot be addressed to it. Name another package (see list-packages for the available names).")
	}
	return TargetPackageResult{Success: true, PackageName: row.Name}
}

func targetPackageUnresolvable(message string) TargetPackageResult {
	resolutionFailed := true
	return TargetPackageResult{ResolutionFailed: &resolutionFailed, Error: message}
}

func targetPackageUnavailable(err error) TargetPackageResult {
	resolutionFailed := false
	return TargetPackageResult{ResolutionFailed: &resolutionFailed,
		Error: "The environment could not be asked which package to deliver the data into: " + err.Error()}
}

// parseGUID accepts the lexical forms .NET Guid.TryParse accepts for 32 hex digits (plain, hyphenated,
// braced or parenthesised) and returns the lower-case hyphenated form.
func parseGUID(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if len(value) >= 2 && ((value[0] == '{' && value[len(value)-1] == '}') || (value[0] == '(' && value[len(value)-1] == ')')) {
		value = value[1 : len(value)-1]
	}
	if len(value) == 36 {
		if value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
			return "", false
		}
		value = strings.ReplaceAll(value, "-", "")
	}
	if len(value) != 32 {
		return "", false
	}
	for _, r := range value {
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			return "", false
		}
	}
	value = strings.ToLower(value)
	return value[0:8] + "-" + value[8:12] + "-" + value[12:16] + "-" + value[16:20] + "-" + value[20:], true
}
