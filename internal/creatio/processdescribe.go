package creatio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ProcessDescribeRequest identifies the process describe-business-process reads: exactly one of the three
// identities. Culture is nil when the caller sent none, which clio reads as en-US.
type ProcessDescribeRequest struct {
	ProcessName    string
	ProcessUID     string
	ProcessCaption string
	Culture        *string
}

const (
	processDescribeRoute       = "rest/ProcessDesignService/DescribeProcess"
	processDescribeFamilyCap   = 50
	processDescribeSource      = "process-library-view"
	processDescribeResolverTag = "ResolveProcessByNameOrCaption - "
)

// processDescribeLibRow is one VwProcessLib row with the columns the version facts need.
type processDescribeLibRow struct {
	UID             string
	Name            string
	Caption         *string
	VersionParentID string
	IsActiveVersion *bool
	Version         *int
	PackageUID      string
	Enabled         bool
}

// DescribeBusinessProcess is clio's describe-business-process: the CrtProcessBuilder package gate, the
// one-identity rule, the server-side ProcessDesignService.DescribeProcess read, then the process library's
// version facts overlaid on the graph. Like clio the graph is returned as one indented JSON Info line.
func (c *Client) DescribeBusinessProcess(ctx context.Context, request ProcessDescribeRequest) UserTasksResult {
	installed, err := c.userTasksPackageInstalled(ctx, processBuilderPackageName)
	if err != nil {
		return NewUserTasksResult(-1, "Error", err.Error())
	}
	if !installed {
		return NewUserTasksResult(1, "Error", processBuilderMissingMessage)
	}
	identities := 0
	for _, identity := range []string{request.ProcessName, request.ProcessUID, request.ProcessCaption} {
		if strings.TrimSpace(identity) != "" {
			identities++
		}
	}
	if identities != 1 {
		return NewUserTasksResult(1, "Error", "Error: provide exactly one of --process-name, --process-uid, or --process-caption.")
	}
	culture := "en-US"
	if request.Culture != nil {
		culture = *request.Culture
	}
	text, err := c.describeProcessGraph(ctx, request, culture)
	if err != nil {
		return NewUserTasksResult(1, "Error", "Error: "+err.Error()+".")
	}
	return NewUserTasksResult(0, "Info", text)
}

func (c *Client) describeProcessGraph(ctx context.Context, request ProcessDescribeRequest, culture string) (string, error) {
	identity := newObject()
	var resolved *processDescribeLibRow
	switch {
	case strings.TrimSpace(request.ProcessUID) != "":
		identity.set("uid", newString(strings.TrimSpace(request.ProcessUID)))
	case strings.TrimSpace(request.ProcessName) != "":
		identity.set("name", newString(strings.TrimSpace(request.ProcessName)))
	default:
		row, err := c.processDescribeResolveCaption(ctx, request.ProcessCaption)
		if err != nil {
			return "", err
		}
		resolved = &row
		identity.set("name", newString(row.Name))
	}
	if strings.TrimSpace(culture) != "" {
		identity.set("culture", newString(culture))
	}
	body := newObject()
	body.set("request", identity)
	payload, err := c.postCreatioJSON(ctx, processDescribeRoute, body.newtonsoftJSON(), 10*time.Second, maxResponseBytes)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(string(payload)) == "" {
		return "", errors.New("empty response from the server")
	}
	envelope, err := parseJNode(payload)
	if err != nil {
		return "", fmt.Errorf("could not parse server response: %v", err)
	}
	result := processDescribeProperty(envelope, "DescribeProcessResult")
	if !result.isObject() {
		return "", errors.New("unexpected server response shape")
	}
	if success := processDescribeProperty(result, "success"); success == nil || success.kind != jkBool || !success.flag {
		if message := processDescribeProperty(result, "errorMessage"); message != nil && message.kind == jkString {
			return "", errors.New(message.text)
		}
		return "", errors.New("describe-business-process failed on the server")
	}
	schemaUID := ""
	if value := processDescribeProperty(result, "schemaUId"); value != nil && value.kind == jkString {
		schemaUID = value.text
	}
	var facts processDescribeFacts
	if resolved != nil && normalizeGUID(schemaUID) != "" && normalizeGUID(schemaUID) == resolved.UID {
		facts = c.processDescribeFactsForRow(ctx, *resolved)
	} else {
		facts = c.processDescribeFactsForSchema(ctx, schemaUID)
	}
	return string(processDescribeIndented(processDescribeOutput(result, facts))), nil
}

// processDescribeResolveCaption is clio's ResolveCaption: every VwProcessLib row with that caption, one over
// the 50-row cap so an overflow is refused, ranked by the same policy get-process-signature uses.
func (c *Client) processDescribeResolveCaption(ctx context.Context, caption string) (processDescribeLibRow, error) {
	rows, err := c.processDescribeLibRows(ctx, "Caption", caption, 1, processDescribeFamilyCap+1)
	if err != nil {
		return processDescribeLibRow{}, err
	}
	if len(rows) > processDescribeFamilyCap {
		return processDescribeLibRow{}, fmt.Errorf("caption '%s' matches more than %d schemas, which is more candidates than can be ranked into one version family. Re-run with the exact process code.", caption, processDescribeFamilyCap)
	}
	if len(rows) == 0 {
		return processDescribeLibRow{}, fmt.Errorf("process not found (caption '%s')", caption)
	}
	matches := make([]processLibRow, 0, len(rows))
	for _, row := range rows {
		matches = append(matches, processLibRow{ID: row.UID, Name: row.Name, Caption: row.Caption,
			VersionParentID: row.VersionParentID, IsActiveVersion: row.IsActiveVersion})
	}
	picked, err := resolveProcessByCaption(caption, matches)
	if err != nil {
		// clio reports the resolver's description without its error code.
		return processDescribeLibRow{}, errors.New(strings.TrimPrefix(err.Error(), processDescribeResolverTag))
	}
	for _, row := range rows {
		if row.UID == picked.ID && row.Name == picked.Name {
			return row, nil
		}
	}
	return processDescribeLibRow{}, fmt.Errorf("process not found (caption '%s')", caption)
}

func (c *Client) processDescribeLibRows(ctx context.Context, column, value string, dataValueType, rowCount int) ([]processDescribeLibRow, error) {
	rows, err := c.selectRows(ctx, buildSelectQuery("VwProcessLib", map[string]string{
		"UId": "UId", "Name": "Name", "Caption": "Caption", "VersionParentUId": "VersionParentUId",
		"IsActiveVersion": "IsActiveVersion", "Version": "Version", "PackageUId": "PackageUId", "Enabled": "Enabled",
	}, map[string]any{column: comparisonFilter(column, value, dataValueType, 3)}, rowCount))
	if err != nil {
		return nil, err
	}
	result := make([]processDescribeLibRow, 0, len(rows))
	for _, row := range rows {
		item := processDescribeLibRow{
			UID: guidOrEmpty(rowString(row, "UId")), Name: rowString(row, "Name"), Caption: rowStringPointer(row, "Caption"),
			VersionParentID: guidOrEmpty(rowString(row, "VersionParentUId")), PackageUID: guidOrEmpty(rowString(row, "PackageUId")),
		}
		var active bool
		if raw := row["IsActiveVersion"]; len(raw) > 0 && string(raw) != "null" && json.Unmarshal(raw, &active) == nil {
			item.IsActiveVersion = &active
		}
		var version int
		if raw := row["Version"]; len(raw) > 0 && string(raw) != "null" && json.Unmarshal(raw, &version) == nil {
			item.Version = &version
		}
		_ = json.Unmarshal(row["Enabled"], &item.Enabled)
		result = append(result, item)
	}
	return result, nil
}

// processDescribeFacts is clio's ProcessVersionFacts: what the process library says about the described
// schema's place in its version family.
type processDescribeFacts struct {
	Version                *int
	IsActiveVersion        *bool
	ActiveVersionSchemaUID string
	ActiveVersionName      string
	VersionRootSchemaUID   string
	Versions               []processDescribeLibRow
	PackageNames           map[string]string
	Truncated              bool
	Source                 string
	Warning                string
}

func processDescribeNotEstablished(reason string) processDescribeFacts {
	return processDescribeFacts{Warning: reason + ", so the version facts were not established"}
}

func (c *Client) processDescribeFactsForSchema(ctx context.Context, schemaUID string) processDescribeFacts {
	uid := normalizeGUID(schemaUID)
	if uid == "" {
		return processDescribeNotEstablished(fmt.Sprintf("'%s' is not a schema UId", schemaUID))
	}
	rows, err := c.processDescribeLibRows(ctx, "UId", uid, 0, 1)
	if err != nil {
		return processDescribeNotEstablished("reading the process library failed: " + err.Error())
	}
	if len(rows) == 0 {
		return processDescribeNotEstablished(fmt.Sprintf("the process library has no row for schema '%s'", uid))
	}
	return c.processDescribeFactsForRow(ctx, rows[0])
}

// processDescribeFactsForRow is clio's FactsForRow plus BuildFacts: the family keyed on VersionParentUId,
// ordered by version then name, capped at 50, with the package names read beside it.
func (c *Client) processDescribeFactsForRow(ctx context.Context, row processDescribeLibRow) processDescribeFacts {
	if row.VersionParentID == emptyGUID {
		return processDescribeNotEstablished(fmt.Sprintf("the process library reports no version family key for schema '%s'", row.UID))
	}
	fetched, err := c.processDescribeLibRows(ctx, "VersionParentUId", row.VersionParentID, 0, processDescribeFamilyCap+1)
	if err != nil {
		return processDescribeNotEstablished("reading the process library failed: " + err.Error())
	}
	if len(fetched) == 0 {
		return processDescribeNotEstablished(fmt.Sprintf("the process library returned no version family for root '%s'", row.VersionParentID))
	}
	family := append([]processDescribeLibRow{}, fetched...)
	sort.SliceStable(family, func(i, j int) bool {
		left, right := processDescribeVersionKey(family[i]), processDescribeVersionKey(family[j])
		if left != right {
			return left < right
		}
		return strings.ToLower(family[i].Name) < strings.ToLower(family[j].Name)
	})
	if len(family) > processDescribeFamilyCap {
		family = family[:processDescribeFamilyCap]
	}
	flagged := []processDescribeLibRow{}
	for _, member := range fetched {
		if member.IsActiveVersion != nil && *member.IsActiveVersion {
			flagged = append(flagged, member)
		}
	}
	facts := processDescribeFacts{
		Version: row.Version, IsActiveVersion: row.IsActiveVersion, VersionRootSchemaUID: row.VersionParentID,
		Versions: family, Truncated: len(fetched) > processDescribeFamilyCap, Source: processDescribeSource,
		PackageNames: c.processDescribePackageNames(ctx),
	}
	if len(flagged) == 1 {
		facts.ActiveVersionSchemaUID, facts.ActiveVersionName = flagged[0].UID, flagged[0].Name
	}
	facts.Warning = processDescribeGaps(row, flagged, family, facts.Truncated, facts.PackageNames != nil)
	return facts
}

func processDescribeVersionKey(row processDescribeLibRow) int {
	if row.Version == nil {
		return int(^uint32(0) >> 1)
	}
	return *row.Version
}

// processDescribePackageNames maps every package UId to its name; nil when the read failed or was empty, which
// clio reports as "the package names could not be read".
func (c *Client) processDescribePackageNames(ctx context.Context) map[string]string {
	rows, err := c.selectRows(ctx, buildSelectQuery("SysPackage", map[string]string{"UId": "UId", "Name": "Name"}, nil, -1))
	if err != nil || len(rows) == 0 {
		return nil
	}
	names := map[string]string{}
	for _, row := range rows {
		names[guidOrEmpty(rowString(row, "UId"))] = rowString(row, "Name")
	}
	return names
}

func processDescribeGaps(row processDescribeLibRow, flagged, published []processDescribeLibRow, truncated, packagesRead bool) string {
	gaps := []string{}
	if row.Version == nil {
		gaps = append(gaps, "the view established no version number for this schema")
	}
	if row.IsActiveVersion == nil {
		gaps = append(gaps, "the view established no active-version flag for this schema")
	}
	switch {
	case len(flagged) == 0 && truncated:
		gaps = append(gaps, fmt.Sprintf("the family exceeded the %d-member read cap, so the active version may not have been read", processDescribeFamilyCap))
	case len(flagged) == 0:
		gaps = append(gaps, fmt.Sprintf("the process library flagged no active version in family '%s'", row.VersionParentID))
	case len(flagged) > 1:
		names := make([]string, 0, len(flagged))
		for _, member := range flagged {
			names = append(names, member.Name)
		}
		gaps = append(gaps, fmt.Sprintf("the process library flags %d active versions in family '%s' (%s), and which one the runtime executes is decided by a key this view does not expose", len(flagged), row.VersionParentID, strings.Join(names, ", ")))
	default:
		reported := false
		for _, member := range published {
			if member.UID == flagged[0].UID {
				reported = true
				break
			}
		}
		if !reported {
			gaps = append(gaps, fmt.Sprintf("the active version '%s' fell outside the %d members reported", flagged[0].Name, processDescribeFamilyCap))
		}
	}
	if !packagesRead {
		gaps = append(gaps, "the package names could not be read")
	}
	if len(gaps) == 0 {
		return ""
	}
	return strings.Join(gaps, "; ") + ", so those facts were not established"
}

// processDescribeDeclared is the declaration order of clio's DescribeProcessResult. clio re-serializes the
// server's graph through that type, so its declared members come first and every other server key follows in
// server order (its extension data); the version members are always clio's own.
var processDescribeDeclared = []string{"name", "caption", "schemaUId", "version", "isActiveVersion", "activeVersionSchemaUId",
	"activeVersionName", "versionRootSchemaUId", "activeVersionSource", "versions", "versionsTruncatedAt", "versionReadWarning",
	"elements", "flows", "parameters", "usings", "methods", "compiledMethods", "legacyMethodCount"}

var processDescribeVersionKeys = map[string]bool{"version": true, "isactiveversion": true, "activeversionschemauid": true,
	"activeversionname": true, "versionrootschemauid": true, "activeversionsource": true, "versions": true,
	"versionstruncatedat": true, "versionreadwarning": true}

func processDescribeOutput(result *jnode, facts processDescribeFacts) *jnode {
	versionFields := processDescribeVersionFields(facts)
	output := newObject()
	used := map[string]bool{"success": true, "errormessage": true}
	for _, name := range processDescribeDeclared {
		lower := strings.ToLower(name)
		used[lower] = true
		if processDescribeVersionKeys[lower] {
			if value := versionFields.get(name); value != nil {
				output.set(name, value)
			}
			continue
		}
		if value := processDescribeProperty(result, name); value != nil && value.kind != jkNull {
			output.set(name, processDescribeWithoutNulls(value))
		}
	}
	for _, key := range result.keys {
		if used[strings.ToLower(key)] {
			continue
		}
		output.set(key, result.props[key])
	}
	return output
}

func processDescribeVersionFields(facts processDescribeFacts) *jnode {
	fields := newObject()
	if facts.Version != nil {
		fields.set("version", newInt(*facts.Version))
	}
	if facts.IsActiveVersion != nil {
		fields.set("isActiveVersion", newBool(*facts.IsActiveVersion))
	}
	if facts.ActiveVersionSchemaUID != "" {
		fields.set("activeVersionSchemaUId", newString(facts.ActiveVersionSchemaUID))
	}
	if facts.ActiveVersionName != "" {
		fields.set("activeVersionName", newString(facts.ActiveVersionName))
	}
	if facts.VersionRootSchemaUID != "" {
		fields.set("versionRootSchemaUId", newString(facts.VersionRootSchemaUID))
	}
	if facts.Source != "" {
		fields.set("activeVersionSource", newString(facts.Source))
	}
	if facts.Versions != nil {
		versions := newArray()
		for _, member := range facts.Versions {
			entry := newObject()
			entry.set("schemaUId", newString(member.UID))
			entry.set("name", newString(member.Name))
			if member.Caption != nil {
				entry.set("caption", newString(*member.Caption))
			}
			if member.Version != nil {
				entry.set("version", newInt(*member.Version))
			}
			if member.IsActiveVersion != nil {
				entry.set("isActiveVersion", newBool(*member.IsActiveVersion))
			}
			entry.set("isRoot", newBool(member.UID == member.VersionParentID))
			entry.set("packageUId", newString(member.PackageUID))
			if name, ok := facts.PackageNames[member.PackageUID]; ok && member.PackageUID != emptyGUID {
				entry.set("packageName", newString(name))
			}
			entry.set("enabled", newBool(member.Enabled))
			versions.items = append(versions.items, entry)
		}
		fields.set("versions", versions)
		if facts.Truncated {
			fields.set("versionsTruncatedAt", newInt(len(facts.Versions)))
		}
	}
	if facts.Warning != "" {
		fields.set("versionReadWarning", newString(facts.Warning))
	}
	return fields
}

// processDescribeProperty reads a property case-insensitively, as clio's PropertyNameCaseInsensitive binder does.
func processDescribeProperty(object *jnode, name string) *jnode {
	if !object.isObject() {
		return nil
	}
	if value, ok := object.props[name]; ok {
		return value
	}
	for _, key := range object.keys {
		if strings.EqualFold(key, name) {
			return object.props[key]
		}
	}
	return nil
}

// processDescribeWithoutNulls drops null members, as clio's WhenWritingNull output options do for the graph.
func processDescribeWithoutNulls(node *jnode) *jnode {
	switch node.kind {
	case jkObject:
		copied := newObject()
		for _, key := range node.keys {
			if value := node.props[key]; value.kind != jkNull {
				copied.set(key, processDescribeWithoutNulls(value))
			}
		}
		return copied
	case jkArray:
		copied := newArray()
		for _, item := range node.items {
			copied.items = append(copied.items, processDescribeWithoutNulls(item))
		}
		return copied
	}
	return node
}

// processDescribeIndented writes System.Text.Json's WriteIndented layout with its default encoder: two-space
// indent, "key": value, and empty containers as [] and {}.
func processDescribeIndented(node *jnode) []byte {
	var buffer bytes.Buffer
	processDescribeWriteIndented(&buffer, node, "")
	return buffer.Bytes()
}

func processDescribeWriteIndented(buffer *bytes.Buffer, node *jnode, indent string) {
	inner := indent + "  "
	switch {
	case node.isObject() && len(node.keys) > 0:
		buffer.WriteString("{")
		for index, key := range node.keys {
			if index > 0 {
				buffer.WriteByte(',')
			}
			buffer.WriteString("\n" + inner)
			writeJSONString(buffer, key, true)
			buffer.WriteString(": ")
			processDescribeWriteIndented(buffer, node.props[key], inner)
		}
		buffer.WriteString("\n" + indent + "}")
	case node.isArray() && len(node.items) > 0:
		buffer.WriteString("[")
		for index, item := range node.items {
			if index > 0 {
				buffer.WriteByte(',')
			}
			buffer.WriteString("\n" + inner)
			processDescribeWriteIndented(buffer, item, inner)
		}
		buffer.WriteString("\n" + indent + "]")
	default:
		node.writeJSON(buffer, true)
	}
}
