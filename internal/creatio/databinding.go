package creatio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ReadDataBinding is clio's read-data-binding-db: the binding's entity schema, UId, bound column set and each
// row's values, read through SysPackageSchemaData and SchemaDataDesignerService.GetBoundSchemaData. Like clio
// it answers with its command log; any failure is one Error line with exit code 1.
func (c *Client) ReadDataBinding(ctx context.Context, packageName, bindingName string) CommandResult {
	lines, err := c.readDataBindingLines(ctx, packageName, bindingName)
	if err != nil {
		return NewCommandResult(1, "Error", err.Error())
	}
	messages := make([]LogMessage, 0, len(lines))
	for _, line := range lines {
		messages = append(messages, LogMessage{MessageType: "Info", Value: line})
	}
	return CommandResult{ExitCode: 0, Messages: messages}
}

func (c *Client) readDataBindingLines(ctx context.Context, packageName, bindingName string) ([]string, error) {
	packageUID, err := c.dataBindingPackageUID(ctx, packageName)
	if err != nil {
		return nil, err
	}
	bindingUID, entitySchemaName, err := c.dataBindingFind(ctx, packageUID, bindingName)
	if err != nil {
		return nil, err
	}
	rows, err := c.dataBindingRows(ctx, bindingUID)
	if err != nil {
		return nil, err
	}
	columnSet := map[string]bool{}
	for _, row := range rows {
		for _, name := range row.keys {
			columnSet[name] = true
		}
	}
	columns := make([]string, 0, len(columnSet))
	for name := range columnSet {
		columns = append(columns, name)
	}
	sort.Strings(columns)
	lines := []string{
		"binding: " + bindingName,
		"schema:  " + entitySchemaName,
		"uId:     " + bindingUID,
		fmt.Sprintf("rows:    %d", len(rows)),
		fmt.Sprintf("columns (%d): %s", len(columns), strings.Join(columns, ", ")),
	}
	for index, row := range rows {
		// A repeated key keeps its last value, as the dictionary clio builds does.
		values := map[string]string{}
		for _, name := range row.keys {
			values[name] = dataBindingValue(row.props[name])
		}
		names := make([]string, 0, len(values))
		for name := range values {
			names = append(names, name)
		}
		sort.Strings(names)
		pairs := make([]string, 0, len(names))
		for _, name := range names {
			pairs = append(pairs, name+"="+values[name])
		}
		lines = append(lines, fmt.Sprintf("row[%d]: %s", index, strings.Join(pairs, ", ")))
	}
	return lines, nil
}

// dataBindingPackageUID is clio's PackageTargetResolver for a named package without the editable requirement:
// a locked package can still be read.
func (c *Client) dataBindingPackageUID(ctx context.Context, packageName string) (string, error) {
	if strings.TrimSpace(packageName) == "" {
		return "", errors.New("Package name is required to write a package data binding.")
	}
	packageName = strings.TrimSpace(packageName)
	rows, err := c.selectTargetPackages(ctx, nil)
	if err != nil {
		return "", errors.New("The environment could not be asked which package to deliver the data into: " + err.Error())
	}
	for _, row := range rows {
		if !strings.EqualFold(row.Name, packageName) {
			continue
		}
		uid, ok := parseGUID(row.UID)
		if !ok || uid == emptyGUID {
			return "", fmt.Errorf("Package '%s' has no usable UId in the environment, so package data cannot be addressed to it. Name another package (see list-packages for the available names).", row.Name)
		}
		return uid, nil
	}
	return "", fmt.Errorf("Package '%s' was not found in the environment. Check the name against list-packages.", packageName)
}

// dataBindingFind is clio's FindBinding plus LookupBindingInfo: the one SysPackageSchemaData row of that name
// in the package, its entity schema falling back to the binding name.
func (c *Client) dataBindingFind(ctx context.Context, packageUID, bindingName string) (string, string, error) {
	rows, err := c.selectRows(ctx, buildSelectQuery("SysPackageSchemaData", map[string]string{"UId": "UId", "EntitySchemaName": "SysSchema.Name"}, map[string]any{
		"filter0": comparisonFilter("Name", bindingName, 1, 3),
		"filter1": comparisonFilter("SysPackage.UId", packageUID, 0, 3),
	}, 10000))
	if err != nil {
		return "", "", err
	}
	if len(rows) > 1 {
		return "", "", fmt.Errorf("Package data binding '%s' has multiple registrations in package '%s'.", bindingName, packageUID)
	}
	if len(rows) == 0 {
		return "", "", fmt.Errorf("Binding '%s' was not found in the remote environment.", bindingName)
	}
	uid, ok := parseGUID(rowString(rows[0], "UId"))
	if !ok || uid == emptyGUID {
		return "", "", fmt.Errorf("Package data binding '%s' in package '%s' carries an unusable UId '%s', so a re-save could not update it in place.", bindingName, packageUID, rowString(rows[0], "UId"))
	}
	entity := rowString(rows[0], "EntitySchemaName")
	if strings.TrimSpace(entity) == "" {
		entity = bindingName
	}
	return uid, entity, nil
}

// dataBindingRows reads the bound rows; items is either an inline array or a JSON-encoded string of one.
func (c *Client) dataBindingRows(ctx context.Context, bindingUID string) ([]*jnode, error) {
	body, _ := json.Marshal(map[string]string{"uId": bindingUID})
	payload, err := c.postCreatioServiceJSON(ctx, "ServiceModel/SchemaDataDesignerService.svc/GetBoundSchemaData", body, 45*time.Second, maxResponseBytes)
	if err != nil {
		return nil, err
	}
	root, err := parseJNode(payload)
	if err != nil {
		return nil, fmt.Errorf("GetBoundSchemaData returned invalid JSON: %w", err)
	}
	items := root.get("items")
	if items != nil && items.kind == jkString {
		if strings.TrimSpace(items.text) == "" {
			return []*jnode{}, nil
		}
		if items, err = parseJNode([]byte(items.text)); err != nil {
			return nil, fmt.Errorf("GetBoundSchemaData returned invalid items JSON: %w", err)
		}
	}
	rows := []*jnode{}
	if !items.isArray() {
		return rows, nil
	}
	for _, item := range items.items {
		if item.isObject() {
			rows = append(rows, item)
		} else {
			rows = append(rows, newObject())
		}
	}
	return rows, nil
}

// dataBindingValue renders a bound value as clio's FormatBoundValue does: a lookup envelope collapses to
// "displayValue (value)", a scalar prints as JsonNode.ToString.
func dataBindingValue(value *jnode) string {
	if value == nil || value.kind == jkNull {
		return ""
	}
	if value.isObject() {
		display, hasDisplay := value.props["displayValue"]
		raw, hasRaw := value.props["value"]
		if !hasDisplay && !hasRaw {
			return string(value.stjJSON())
		}
		displayText, rawText := dataBindingValue(display), dataBindingValue(raw)
		switch {
		case displayText == "":
			return rawText
		case rawText == "":
			return displayText
		default:
			return displayText + " (" + rawText + ")"
		}
	}
	switch value.kind {
	case jkString:
		return value.text
	case jkArray:
		return string(processDescribeIndented(value))
	default:
		return string(value.stjJSON())
	}
}
