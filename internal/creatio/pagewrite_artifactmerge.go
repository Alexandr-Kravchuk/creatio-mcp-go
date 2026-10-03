package creatio

// merge-creatio-artifact: clio's CreatioArtifactMergeService up to the resolver. Request validation, the
// path-only terminals and the classification of every artifact shape are ported with clio's texts; the
// semantic three-way merge itself (clio's Creatio.ConflictResolver strategies) is not, so a shape clio would
// merge is answered "not-implemented" here and the caller resolves the conflict another way.

import (
	"encoding/json"
	"fmt"
	"regexp"
	"runtime"
	"strings"
)

const (
	pageWriteMergeMaxPathBytes    = 4096
	pageWriteMergeMaxContentBytes = 4 * 1024 * 1024
	pageWriteMergeUnknownKind     = "unknown-artifact"
)

// ArtifactMergeRequest is clio's CreatioArtifactMergeArgs.
type ArtifactMergeRequest struct {
	ArtifactPath      string  `json:"artifact-path"`
	BaseContent       string  `json:"base-content"`
	OursContent       string  `json:"ours-content"`
	TheirsContent     string  `json:"theirs-content"`
	DescriptorContent *string `json:"descriptor-content"`
}

// ArtifactMergeResult is clio's CreatioArtifactMergeResult.
type ArtifactMergeResult struct {
	Status          string              `json:"status"`
	ArtifactKind    string              `json:"artifact-kind"`
	ResolverVersion string              `json:"resolver-version"`
	Content         *string             `json:"content,omitempty"`
	Report          ArtifactMergeReport `json:"report"`
	Diagnostics     []string            `json:"diagnostics"`
}

// ArtifactMergeReport is clio's CreatioArtifactMergeReport; a terminal answer carries the empty one.
type ArtifactMergeReport struct {
	ResolutionType     string   `json:"resolution-type"`
	WinnerPolicy       string   `json:"winner-policy"`
	VerificationPassed bool     `json:"verification-passed"`
	LocalAdditions     []string `json:"local-additions"`
	RemoteAdditions    []string `json:"remote-additions"`
	LocalDeletions     []string `json:"local-deletions"`
	RemoteDeletions    []string `json:"remote-deletions"`
	TrueConflicts      []string `json:"true-conflicts"`
}

func pageWriteMergeResult(status, kind, diagnostic, version string) ArtifactMergeResult {
	return ArtifactMergeResult{Status: status, ArtifactKind: kind, ResolverVersion: version, Diagnostics: []string{diagnostic},
		Report: ArtifactMergeReport{LocalAdditions: []string{}, RemoteAdditions: []string{}, LocalDeletions: []string{},
			RemoteDeletions: []string{}, TrueConflicts: []string{}}}
}

func pageWriteMergeNotImplemented(kind string) string {
	return fmt.Sprintf("Merge for %s is not implemented yet.", kind)
}

// MergeCreatioArtifact classifies one artifact as clio does and answers its terminal status; resolverVersion
// names this server's (classification-only) resolver.
func MergeCreatioArtifact(request ArtifactMergeRequest, resolverVersion string) ArtifactMergeResult {
	result := func(status, kind, diagnostic string) ArtifactMergeResult {
		return pageWriteMergeResult(status, kind, diagnostic, resolverVersion)
	}
	descriptor := ""
	if request.DescriptorContent != nil {
		descriptor = *request.DescriptorContent
	}
	if len(request.ArtifactPath) > pageWriteMergeMaxPathBytes {
		return result("invalid-input", pageWriteMergeUnknownKind, "artifact-path exceeds the 4096-byte limit.")
	}
	if !pageWriteMergeSafePath(request.ArtifactPath) {
		return result("invalid-input", pageWriteMergeUnknownKind, "artifact-path must be a safe repository-relative path.")
	}
	if strings.TrimSpace(request.BaseContent) == "" || strings.TrimSpace(request.OursContent) == "" || strings.TrimSpace(request.TheirsContent) == "" {
		return result("invalid-input", pageWriteMergeUnknownKind, "base-content, ours-content, and theirs-content are required.")
	}
	if len(request.ArtifactPath)+len(request.BaseContent)+len(request.OursContent)+len(request.TheirsContent)+len(descriptor) > pageWriteMergeMaxContentBytes {
		return result("invalid-input", pageWriteMergeUnknownKind, "Combined merge content exceeds the 4 MiB limit.")
	}
	if kind, ok := pageWriteMergePathTerminal(request.ArtifactPath); ok {
		return result("not-implemented", kind, pageWriteMergeNotImplemented(kind))
	}
	status, kind, diagnostic := pageWriteMergeClassify(request, descriptor)
	if status == "" {
		// A shape clio merges semantically: this server has no resolver for it.
		status, diagnostic = "not-implemented", pageWriteMergeNotImplemented(kind)
	}
	return result(status, kind, diagnostic)
}

// pageWriteMergeSafePath is IsSafeRelativePath.
func pageWriteMergeSafePath(path string) bool {
	if strings.TrimSpace(path) == "" || strings.ContainsRune(path, 0) {
		return false
	}
	normalized := strings.ReplaceAll(path, `\`, "/")
	if strings.HasPrefix(normalized, "/") || (len(normalized) >= 2 && pageWriteASCIILetter(normalized[0]) && normalized[1] == ':') {
		return false
	}
	for _, segment := range strings.Split(normalized, "/") {
		if strings.TrimSpace(segment) == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

func pageWriteASCIILetter(b byte) bool { return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' }

// pageWriteMergePathTerminal is ClassifyPathOnlyTerminal: C#, SQL and process resources/metadata by path.
func pageWriteMergePathTerminal(path string) (string, bool) {
	normalized := strings.ReplaceAll(path, `\`, "/")
	segments := strings.Split(normalized, "/")
	fileName := strings.ToLower(segments[len(segments)-1])
	switch {
	case strings.HasSuffix(fileName, ".cs"):
		return "csharp-source", true
	case strings.HasSuffix(fileName, ".sql"):
		return "sql-script", true
	}
	parent := ""
	if len(segments) > 1 {
		parent = strings.ToLower(segments[len(segments)-2])
	}
	if strings.HasSuffix(parent, ".process") {
		if strings.HasSuffix(fileName, ".xml") {
			return "process-resource", true
		}
		if fileName == "metadata.json" {
			return "process-schema-metadata", true
		}
	}
	return "", false
}

// pageWriteMergeFileName is Path.GetFileName on the clio host: '/' separates (and '\' too on Windows).
func pageWriteMergeFileName(path string) string {
	cut := strings.LastIndex(path, "/")
	if runtime.GOOS == "windows" {
		cut = max(cut, strings.LastIndex(path, `\`))
	}
	return path[cut+1:]
}

var pageWriteMergeProcessManager = regexp.MustCompile(`"ManagerName"\s*:\s*"ProcessSchemaManager"`)

// pageWriteMergeClassify is MergeRequest.TryDetectFileTypeFromPath plus CreatioArtifactMergeService.Classify.
// An empty status means a shape clio merges semantically.
func pageWriteMergeClassify(request ArtifactMergeRequest, descriptor string) (string, string, string) {
	unsupported := func() (string, string, string) {
		return "unsupported", pageWriteMergeUnknownKind, "The artifact path is not a supported Creatio merge shape."
	}
	name := pageWriteMergeFileName(request.ArtifactPath)
	lower := strings.ToLower(name)
	switch {
	case strings.TrimSpace(name) == "":
		return unsupported()
	case lower == "data.json" || (strings.HasPrefix(lower, "data.") && strings.HasSuffix(lower, ".json") && len(name) > len("data..json") &&
		strings.TrimSpace(name[len("data."):len(name)-len(".json")]) != ""):
		if _, ok := pageWriteMergeDescriptor(descriptor); ok {
			return "", "data-binding", ""
		}
		return "invalid-input", "data-binding", "A valid, marker-free descriptor-content is required for data-binding merge."
	case lower == "properties.json":
		return "", "properties", ""
	case lower == "descriptor.json":
		for _, content := range []string{request.BaseContent, request.OursContent, request.TheirsContent} {
			if identity, ok := pageWriteMergeDescriptor(content); ok && pageWriteMergeText(identity.manager) == "ProcessSchemaManager" {
				return "not-implemented", "process-schema-descriptor", pageWriteMergeNotImplemented("process-schema-descriptor")
			}
		}
		return "", "descriptor", ""
	case lower == "metadata.json":
		if strings.Contains(strings.ReplaceAll(request.ArtifactPath, `\`, "/"), "/") && descriptor != "" &&
			pageWriteMergeProcessManager.MatchString(descriptor) {
			return "not-implemented", "process-schema-metadata", pageWriteMergeNotImplemented("process-schema-metadata")
		}
		return pageWriteMergeClassifyMetadata(request, descriptor)
	case strings.HasPrefix(lower, "resource.") && strings.HasSuffix(lower, ".xml") && len(name) > len("resource..xml"):
		if pageWriteMergeParentFolder(request.ArtifactPath, ".process") {
			return "not-implemented", "process-resource", pageWriteMergeNotImplemented("process-resource")
		}
		return "", "resource", ""
	case strings.HasSuffix(lower, ".js"):
		normalized := strings.ToLower(strings.ReplaceAll(request.ArtifactPath, `\`, "/"))
		if strings.Contains(normalized, "/schemas/") || strings.HasPrefix(normalized, "schemas/") {
			return "", "client-unit-source", ""
		}
	case strings.HasSuffix(lower, ".sql"):
		return "not-implemented", "sql-script", pageWriteMergeNotImplemented("sql-script")
	case strings.HasSuffix(lower, ".cs"):
		return "not-implemented", "csharp-source", pageWriteMergeNotImplemented("csharp-source")
	}
	return unsupported()
}

func pageWriteMergeParentFolder(path, suffix string) bool {
	normalized := strings.ReplaceAll(path, `\`, "/")
	last := strings.LastIndex(normalized, "/")
	if last <= 0 {
		return false
	}
	directory := normalized[:last]
	folder := directory[strings.LastIndex(directory, "/")+1:]
	return folder != "" && strings.HasSuffix(strings.ToLower(folder), suffix)
}

type pageWriteArtifactIdentity struct {
	uid, name, manager, subtype *string
}

func pageWriteMergeHasMarker(content string) bool {
	return strings.Contains(content, "<<<<<<<") || strings.Contains(content, "=======") || strings.Contains(content, ">>>>>>>")
}

func pageWriteMergeString(object map[string]json.RawMessage, key string) *string {
	raw, ok := object[key]
	if !ok {
		return nil
	}
	var text string
	if json.Unmarshal(raw, &text) != nil {
		return nil
	}
	return &text
}

// pageWriteMergeDescriptor is TryParseDescriptor: the Descriptor identity of a marker-free descriptor.json.
func pageWriteMergeDescriptor(content string) (pageWriteArtifactIdentity, bool) {
	if strings.TrimSpace(content) == "" || pageWriteMergeHasMarker(content) {
		return pageWriteArtifactIdentity{}, false
	}
	var root map[string]json.RawMessage
	if json.Unmarshal([]byte(strings.TrimLeft(content, "\ufeff")), &root) != nil {
		return pageWriteArtifactIdentity{}, false
	}
	var descriptor map[string]json.RawMessage
	if raw, ok := root["Descriptor"]; !ok || json.Unmarshal(raw, &descriptor) != nil || descriptor == nil {
		return pageWriteArtifactIdentity{}, false
	}
	return pageWriteArtifactIdentity{uid: pageWriteMergeString(descriptor, "UId"), name: pageWriteMergeString(descriptor, "Name"),
		manager: pageWriteMergeString(descriptor, "ManagerName")}, true
}

var pageWriteMergeFlatIdentity = regexp.MustCompile(`(?m)^\s*[=+~]\s+MetaData\.Schema\.(UId|A2|ManagerName|AD3)\s+"([^"]+)"\s*$`)

// pageWriteMergeMetadataIdentity is ParseMetadataIdentity: JSON MetaData.Schema, or the flat-diff assignments.
func pageWriteMergeMetadataIdentity(content string) pageWriteArtifactIdentity {
	var root map[string]json.RawMessage
	if json.Unmarshal([]byte(strings.TrimLeft(content, "\ufeff")), &root) == nil {
		var metaData map[string]json.RawMessage
		var schema map[string]json.RawMessage
		if json.Unmarshal(root["MetaData"], &metaData) == nil && json.Unmarshal(metaData["Schema"], &schema) == nil && schema != nil {
			return pageWriteArtifactIdentity{uid: pageWriteMergeString(schema, "UId"), name: pageWriteMergeString(schema, "A2"),
				manager: pageWriteMergeString(schema, "ManagerName"), subtype: pageWriteMergeString(schema, "AD3")}
		}
	}
	// Creatio also stores metadata in flat-diff syntax; its identity assignments are read from the text.
	identity := pageWriteArtifactIdentity{}
	for _, match := range pageWriteMergeFlatIdentity.FindAllStringSubmatch(content, -1) {
		value := match[2]
		switch match[1] {
		case "UId":
			if identity.uid == nil {
				identity.uid = &value
			}
		case "A2":
			if identity.name == nil {
				identity.name = &value
			}
		case "ManagerName":
			if identity.manager == nil {
				identity.manager = &value
			}
		case "AD3":
			if identity.subtype == nil {
				identity.subtype = &value
			}
		}
	}
	return identity
}

func pageWriteMergeText(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func pageWriteMergeClassifyMetadata(request ArtifactMergeRequest, descriptorContent string) (string, string, string) {
	descriptor, ok := pageWriteMergeDescriptor(descriptorContent)
	if !ok {
		return "invalid-input", "unknown-schema-metadata", "A valid, marker-free descriptor-content is required for metadata merge."
	}
	identities := []pageWriteArtifactIdentity{pageWriteMergeMetadataIdentity(request.BaseContent),
		pageWriteMergeMetadataIdentity(request.OursContent), pageWriteMergeMetadataIdentity(request.TheirsContent)}
	for _, identity := range identities {
		valid := strings.TrimSpace(pageWriteMergeText(identity.uid)) != "" && strings.TrimSpace(pageWriteMergeText(identity.name)) != ""
		matches := strings.EqualFold(pageWriteMergeText(identity.uid), pageWriteMergeText(descriptor.uid)) &&
			pageWriteMergeText(identity.name) == pageWriteMergeText(descriptor.name) &&
			(strings.TrimSpace(pageWriteMergeText(identity.manager)) == "" || pageWriteMergeText(identity.manager) == pageWriteMergeText(descriptor.manager))
		if !valid || !matches {
			return "invalid-input", "unknown-schema-metadata", "Metadata identity does not match descriptor-content."
		}
	}
	switch pageWriteMergeText(descriptor.manager) {
	case "EntitySchemaManager":
		return "", "entity-schema-metadata", ""
	case "ClientUnitSchemaManager":
		return "", "client-unit-metadata", ""
	case "ServiceSchemaManager":
		return "", "service-schema-metadata", ""
	case "ProcessSchemaManager":
		return "not-implemented", "process-schema-metadata", pageWriteMergeNotImplemented("process-schema-metadata")
	case "AddonSchemaManager":
		subtype := identities[0].subtype
		for _, identity := range identities {
			if subtype == nil || strings.TrimSpace(*subtype) == "" || identity.subtype == nil || *identity.subtype != *subtype {
				return "invalid-input", "unknown-schema-metadata", "Addon metadata subtype does not match across merge inputs."
			}
		}
		switch *subtype {
		case "AppearanceSettings":
			return "", "addon-appearance-settings-metadata", ""
		case "BusinessRule":
			return "", "addon-business-rule-metadata", ""
		case "RelatedPage":
			return "", "addon-related-page-metadata", ""
		case "TimelineEntity":
			return "", "addon-timeline-entity-metadata", ""
		}
	}
	return "unsupported", "unknown-schema-metadata", "Merge for unknown-schema-metadata is unsupported."
}
