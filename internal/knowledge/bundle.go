// Package knowledge reads and verifies the clio-knowledge bundles that clio installs, so this server
// serves the same guidance from the same local cache (decision D2 in docs/migration-plan.md).
//
// It is a port of clio's Clio.Command.McpServer.Knowledge runtime: the bundle contract, the trust
// stores, the verification order and the rejection codes follow KnowledgeBundleRuntime.cs and
// KnowledgeBundleContracts.cs. Nothing here weakens a signature, digest or size check.
package knowledge

import (
	"archive/zip"
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/x509"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

// Size and shape limits, identical to KnowledgeBundleRuntime.
const (
	maxManifestBytes          = 1024 * 1024
	maxSignatureBytes         = 1024
	maxResourceBytes          = 4 * 1024 * 1024
	maxBundleResourceBytes    = 32 * 1024 * 1024
	maxArchiveBytes           = 40 * 1024 * 1024
	maxArchiveEntries         = 1024
	maxCentralDirectoryBytes  = 2 * 1024 * 1024
	endOfCentralDirectory     = 0x06054b50
	legacyContractVersion     = "0.1.0"
	multiSourceContract       = "1.0.0"
	legacyLibraryID           = "com.creatio.clio"
	signatureAlgorithm        = "ECDSA-P256-SHA256"
	digestAlgorithm           = "SHA-256"
	namespacedURIPrefix       = "docs://knowledge/"
	defaultRole               = "guidance"
	referenceRole             = "reference"
	referenceExampleRole      = "reference-example"
	maxDiscoveryTitleLength   = 160
	maxDiscoveryDescriptionLn = 1000
)

var (
	versionPattern  = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
	stableIDPattern = regexp.MustCompile(`^[a-z0-9]+(?:[.-][a-z0-9]+)*$`)
	allowedRoles    = map[string]bool{"guidance": true, "reference": true, "advisory": true, "capability": true, "reference-example": true}
)

// RejectionCode mirrors clio's KnowledgeBundleRejectionCode names.
type RejectionCode string

// The rejection codes clio reports.
const (
	RejectMalformed           RejectionCode = "Malformed"
	RejectUnsupportedContract RejectionCode = "UnsupportedContract"
	RejectUntrustedKey        RejectionCode = "UntrustedKey"
	RejectInvalidSignature    RejectionCode = "InvalidSignature"
	RejectIncompatible        RejectionCode = "Incompatible"
	RejectMissingCapability   RejectionCode = "MissingCapability"
	RejectInvalidContent      RejectionCode = "InvalidContent"
	RejectSequenceNotForward  RejectionCode = "SequenceNotForward"
)

// Rejection is a refused bundle: the code, the candidate sequence when the manifest was readable, and
// clio's diagnostic text.
type Rejection struct {
	Code     RejectionCode
	Sequence *uint64
	Message  string
}

func (r *Rejection) Error() string { return r.Message }

func reject(code RejectionCode, sequence *uint64, format string, args ...any) *Rejection {
	return &Rejection{Code: code, Sequence: sequence, Message: fmt.Sprintf(format, args...)}
}

// Version is a three-part version, compared component by component like System.Version.
type Version [3]int

// Compare returns -1, 0 or 1.
func (v Version) Compare(other Version) int {
	for i := range v {
		if v[i] != other[i] {
			if v[i] < other[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

// Capabilities is what this host offers a knowledge producer, like clio's KnowledgeBundleClientCapabilities.
type Capabilities struct {
	ClioVersion            Version
	McpToolContractVersion Version
	Tools                  map[string]bool
}

// Article is one verified knowledge item.
type Article struct {
	Name             string
	URI              string
	Text             string
	LibraryID        string
	ItemID           string
	TopicID          string
	Role             string
	LocalPath        string
	LegacyURIs       []string
	Title            string
	Description      string
	MediaType        string
	RequiredFeatures []string
}

// PreparedBundle is a bundle that passed every check.
type PreparedBundle struct {
	LibraryID      string
	LibraryVersion string
	Sequence       uint64
	BundleDigest   string
	SourceCommit   string
	Articles       []Article
}

// TrustStore answers which public key may sign a library. multiSource selects the library-scoped
// lookup; the legacy contract only names a key ID.
type TrustStore interface {
	PublicKey(libraryID, keyID string, multiSource bool) (*ecdsa.PublicKey, bool)
}

// Verifier validates bundles the way KnowledgeBundleRuntime.Prepare does.
type Verifier struct {
	Trust        TrustStore
	Capabilities Capabilities
}

type manifestRange struct {
	Min *string `json:"min"`
	Max *string `json:"max"`
}

type manifestResource struct {
	ID               *string   `json:"id"`
	ItemID           *string   `json:"itemId"`
	TopicID          *string   `json:"topicId"`
	Role             *string   `json:"role"`
	Title            *string   `json:"title"`
	Description      *string   `json:"description"`
	RequiredFeatures *[]string `json:"requiredFeatures"`
	URI              *string   `json:"uri"`
	LegacyURIs       *[]string `json:"legacyUris"`
	Path             *string   `json:"path"`
	MediaType        *string   `json:"mediaType"`
	Length           int64     `json:"length"`
	Digest           *string   `json:"digest"`
}

type manifest struct {
	ContractVersion     *string `json:"contractVersion"`
	BundleSchemaVersion *string `json:"bundleSchemaVersion"`
	LibraryID           *string `json:"libraryId"`
	LibraryVersion      *string `json:"libraryVersion"`
	Sequence            uint64  `json:"sequence"`
	BundleVersion       *string `json:"bundleVersion"`
	IssuedAt            *string `json:"issuedAt"`
	Source              *struct {
		Repository *string `json:"repository"`
		Commit     *string `json:"commit"`
	} `json:"source"`
	Compatibility *struct {
		Clio            *manifestRange `json:"clio"`
		McpToolContract *manifestRange `json:"mcpToolContract"`
	} `json:"compatibility"`
	Requirements *struct {
		Tools        *[]string `json:"tools"`
		GuidanceIDs  *[]string `json:"guidanceIds"`
		ItemIDs      *[]string `json:"itemIds"`
		ResourceURIs *[]string `json:"resourceUris"`
	} `json:"requirements"`
	DigestAlg *string `json:"digestAlg"`
	Signature *struct {
		Algorithm *string `json:"algorithm"`
		KeyID     *string `json:"keyId"`
	} `json:"signature"`
	Resources *[]*manifestResource `json:"resources"`
}

func str(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func (m *manifest) multiSource() bool { return str(m.ContractVersion) == multiSourceContract }

// The exact member names System.Text.Json accepts for each manifest object; any other name is an
// unmapped member, which clio's source-generated context disallows.
var manifestShape = map[string]any{
	"contractVersion": nil, "bundleSchemaVersion": nil, "libraryId": nil, "libraryVersion": nil,
	"sequence": nil, "bundleVersion": nil, "issuedAt": nil,
	"source":        map[string]any{"repository": nil, "commit": nil},
	"compatibility": map[string]any{"clio": map[string]any{"min": nil, "max": nil}, "mcpToolContract": map[string]any{"min": nil, "max": nil}},
	"requirements":  map[string]any{"tools": nil, "guidanceIds": nil, "itemIds": nil, "resourceUris": nil},
	"digestAlg":     nil,
	"signature":     map[string]any{"algorithm": nil, "keyId": nil},
	"resources": []any{map[string]any{
		"id": nil, "itemId": nil, "topicId": nil, "role": nil, "title": nil, "description": nil,
		"requiredFeatures": nil, "uri": nil, "legacyUris": nil, "path": nil, "mediaType": nil,
		"length": nil, "digest": nil,
	}},
}

// Prepare verifies one candidate archive. expectedVersion and expectedLibraryID are empty when the
// caller has no expectation.
func (v *Verifier) Prepare(candidate []byte, expectedVersion, expectedLibraryID string) (*PreparedBundle, *Rejection) {
	prepared, err := v.prepare(candidate, expectedVersion, expectedLibraryID)
	if err == nil {
		return prepared, nil
	}
	var rejection *Rejection
	if errors.As(err, &rejection) {
		return nil, rejection
	}
	return nil, &Rejection{Code: RejectMalformed, Message: err.Error()}
}

func (v *Verifier) prepare(candidate []byte, expectedVersion, expectedLibraryID string) (*PreparedBundle, error) {
	if len(candidate) > maxArchiveBytes {
		return nil, reject(RejectInvalidContent, nil, "Bundle archive exceeds the %d-byte compressed size limit.", maxArchiveBytes)
	}
	digest := sha256.Sum256(candidate)
	bundleDigest := hex.EncodeToString(digest[:])
	if err := validateCentralDirectory(candidate); err != nil {
		return nil, err
	}
	archive, err := zip.NewReader(bytes.NewReader(candidate), int64(len(candidate)))
	if err != nil {
		return nil, err
	}
	entries, err := readEntryIndex(archive)
	if err != nil {
		return nil, err
	}
	manifestBytes, err := readRequiredEntry(entries, "manifest.json", maxManifestBytes)
	if err != nil {
		return nil, err
	}
	if err := rejectDuplicateJSONProperties(manifestBytes); err != nil {
		return nil, err
	}
	if err := checkJSONShape(manifestBytes, manifestShape); err != nil {
		return nil, err
	}
	var m manifest
	if err := json.Unmarshal(manifestBytes, &m); err != nil {
		return nil, err
	}
	if string(bytes.TrimSpace(manifestBytes)) == "null" {
		return nil, reject(RejectMalformed, nil, "Bundle manifest is empty.")
	}
	seq := m.Sequence
	if err := validateManifestEnvelope(&m); err != nil {
		return nil, err
	}
	if expectedLibraryID != "" && !m.multiSource() {
		return nil, reject(RejectUnsupportedContract, &seq, "Configured knowledge sources require the multi-source bundle contract.")
	}
	if expectedLibraryID != "" && str(m.LibraryID) != expectedLibraryID {
		return nil, reject(RejectInvalidContent, &seq, "Candidate library '%s' does not match configured library '%s'.", str(m.LibraryID), expectedLibraryID)
	}
	if err := v.verifySignature(&m, manifestBytes, entries); err != nil {
		return nil, err
	}
	libraryVersion := str(m.BundleVersion)
	if m.multiSource() {
		libraryVersion = str(m.LibraryVersion)
	}
	if expectedVersion != "" && libraryVersion != expectedVersion {
		return nil, reject(RejectInvalidContent, &seq, "Signed bundle version does not match its immutable package version.")
	}
	if !isCompatible(m.Compatibility.Clio, v.Capabilities.ClioVersion) || !isCompatible(m.Compatibility.McpToolContract, v.Capabilities.McpToolContractVersion) {
		return nil, reject(RejectIncompatible, &seq, "Bundle compatibility ranges do not include this Clio runtime.")
	}
	if err := v.validateRequirements(&m); err != nil {
		return nil, err
	}
	articles, err := readAndValidateResources(&m, entries)
	if err != nil {
		return nil, err
	}
	libraryID := legacyLibraryID
	if m.multiSource() {
		libraryID = str(m.LibraryID)
	}
	return &PreparedBundle{LibraryID: libraryID, LibraryVersion: libraryVersion, Sequence: seq,
		BundleDigest: bundleDigest, SourceCommit: str(m.Source.Commit), Articles: articles}, nil
}

func validateCentralDirectory(candidate []byte) error {
	const minimumRecord = 22
	tailLength := len(candidate)
	if tailLength > minimumRecord+0xFFFF {
		tailLength = minimumRecord + 0xFFFF
	}
	if tailLength < minimumRecord {
		return reject(RejectInvalidContent, nil, "Bundle archive is missing its central directory.")
	}
	tail := candidate[len(candidate)-tailLength:]
	for index := len(tail) - minimumRecord; index >= 0; index-- {
		record := tail[index:]
		if binary.LittleEndian.Uint32(record) != endOfCentralDirectory {
			continue
		}
		commentLength := int(binary.LittleEndian.Uint16(record[20:]))
		if index+minimumRecord+commentLength != len(tail) {
			continue
		}
		diskNumber := binary.LittleEndian.Uint16(record[4:])
		centralDisk := binary.LittleEndian.Uint16(record[6:])
		entriesOnDisk := binary.LittleEndian.Uint16(record[8:])
		totalEntries := binary.LittleEndian.Uint16(record[10:])
		size := binary.LittleEndian.Uint32(record[12:])
		offset := binary.LittleEndian.Uint32(record[16:])
		recordOffset := int64(len(candidate) - tailLength + index)
		if diskNumber != 0 || centralDisk != 0 || entriesOnDisk != totalEntries || totalEntries == 0xFFFF ||
			size == 0xFFFFFFFF || offset == 0xFFFFFFFF || totalEntries > maxArchiveEntries ||
			size > maxCentralDirectoryBytes || int64(offset)+int64(size) != recordOffset {
			return reject(RejectInvalidContent, nil, "Bundle archive central directory exceeds the supported v0 bounds.")
		}
		return nil
	}
	return reject(RejectInvalidContent, nil, "Bundle archive is missing its central directory.")
}

func readEntryIndex(archive *zip.Reader) (map[string]*zip.File, error) {
	if len(archive.File) > maxArchiveEntries {
		return nil, reject(RejectInvalidContent, nil, "Bundle contains too many archive entries.")
	}
	entries := make(map[string]*zip.File, len(archive.File))
	for _, entry := range archive.File {
		name := entry.Name
		_, duplicate := entries[name]
		if strings.TrimSpace(name) == "" || strings.HasSuffix(name, "/") || strings.Contains(name, `\`) || duplicate {
			return nil, reject(RejectInvalidContent, nil, "Bundle contains an invalid or duplicate entry '%s'.", name)
		}
		entries[name] = entry
	}
	return entries, nil
}

func readRequiredEntry(entries map[string]*zip.File, path string, maximum int) ([]byte, error) {
	entry, ok := entries[path]
	if !ok || entry.UncompressedSize64 > uint64(maximum) {
		return nil, reject(RejectInvalidContent, nil, "Required bundle entry '%s' is missing or too large.", path)
	}
	reader, err := entry.Open()
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	data, err := io.ReadAll(io.LimitReader(reader, int64(maximum)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maximum {
		return nil, reject(RejectInvalidContent, nil, "Bundle entry '%s' exceeds its size limit.", path)
	}
	return data, nil
}

// rejectDuplicateJSONProperties walks the manifest the way clio's Utf8JsonReader pass does.
func rejectDuplicateJSONProperties(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	type frame struct {
		object bool
		keys   map[string]bool
		expect bool // the next string token in an object is a property name
	}
	var stack []*frame
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		var top *frame
		if len(stack) > 0 {
			top = stack[len(stack)-1]
		}
		switch t := token.(type) {
		case json.Delim:
			switch t {
			case '{':
				if top != nil && top.object {
					top.expect = true
				}
				stack = append(stack, &frame{object: true, keys: map[string]bool{}, expect: true})
			case '[':
				if top != nil && top.object {
					top.expect = true
				}
				stack = append(stack, &frame{})
			case '}', ']':
				stack = stack[:len(stack)-1]
			}
		case string:
			if top != nil && top.object && top.expect {
				if top.keys[t] {
					return reject(RejectMalformed, nil, "Bundle manifest contains duplicate JSON property '%s'.", t)
				}
				top.keys[t] = true
				top.expect = false
				continue
			}
			if top != nil && top.object {
				top.expect = true
			}
		default:
			if top != nil && top.object {
				top.expect = true
			}
		}
	}
}

// checkJSONShape refuses member names the manifest contract does not declare. shape maps a member to
// nil (scalar or free array), a nested shape, or a one-element []any whose element shapes each item.
func checkJSONShape(data []byte, shape map[string]any) error {
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	return checkValueShape(value, shape)
}

func checkValueShape(value any, shape any) error {
	switch expected := shape.(type) {
	case map[string]any:
		object, ok := value.(map[string]any)
		if !ok {
			return nil // a type mismatch is reported by the typed decode
		}
		for key, item := range object {
			child, known := expected[key]
			if !known {
				return reject(RejectMalformed, nil, "The JSON property '%s' could not be mapped to any .NET member contained in type 'KnowledgeBundleManifestDto'.", key)
			}
			if child != nil {
				if err := checkValueShape(item, child); err != nil {
					return err
				}
			}
		}
	case []any:
		items, ok := value.([]any)
		if !ok {
			return nil
		}
		for _, item := range items {
			if err := checkValueShape(item, expected[0]); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateManifestEnvelope(m *manifest) error {
	seq := m.Sequence
	legacy := str(m.ContractVersion) == legacyContractVersion && str(m.BundleSchemaVersion) == legacyContractVersion
	multi := str(m.ContractVersion) == multiSourceContract && str(m.BundleSchemaVersion) == multiSourceContract
	if !legacy && !multi {
		return reject(RejectUnsupportedContract, &seq, "Only bundle contracts %s and %s are supported.", legacyContractVersion, multiSourceContract)
	}
	issued := false
	if m.IssuedAt != nil {
		if parsed, ok := parseDateTimeOffset(*m.IssuedAt); ok && !parsed.IsZero() {
			issued = true
		}
	}
	missing := seq == 0 ||
		(legacy && blank(str(m.BundleVersion))) ||
		(multi && (blank(str(m.LibraryID)) || blank(str(m.LibraryVersion)))) ||
		(legacy && !issued) ||
		m.Source == nil || blank(str(m.Source.Repository)) || blank(str(m.Source.Commit)) ||
		m.Signature == nil || str(m.Signature.Algorithm) != signatureAlgorithm || blank(str(m.Signature.KeyID)) ||
		str(m.DigestAlg) != digestAlgorithm ||
		m.Compatibility == nil || m.Requirements == nil || m.Resources == nil || len(*m.Resources) == 0
	if !missing {
		for _, resource := range *m.Resources {
			if resource == nil {
				missing = true
				break
			}
		}
	}
	if missing {
		return reject(RejectMalformed, &seq, "Bundle manifest is missing required values.")
	}
	if multi {
		commit := str(m.Source.Commit)
		if (len(commit) != 40 && len(commit) != 64) || !isHex(commit) {
			return reject(RejectMalformed, &seq, "Bundle source commit must be a complete 40- or 64-character hexadecimal object ID.")
		}
	}
	return nil
}

func parseDateTimeOffset(value string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.9999999", "2006-01-02T15:04:05", "2006-01-02"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}

func (v *Verifier) verifySignature(m *manifest, manifestBytes []byte, entries map[string]*zip.File) error {
	seq := m.Sequence
	keyID := str(m.Signature.KeyID)
	var key *ecdsa.PublicKey
	var trusted bool
	if v.Trust != nil {
		key, trusted = v.Trust.PublicKey(str(m.LibraryID), keyID, m.multiSource())
	}
	if !trusted || key == nil {
		return reject(RejectUntrustedKey, &seq, "Bundle signing key '%s' is not trusted.", keyID)
	}
	signature, err := readRequiredEntry(entries, "manifest.sig", maxSignatureBytes)
	if err != nil {
		return err
	}
	if key.Curve != elliptic.P256() {
		return reject(RejectUntrustedKey, &seq, "Bundle trust material is not a supported ECDSA P-256 public key.")
	}
	// .NET's ECDsa.VerifyData uses the IEEE P1363 encoding: r and s, 32 bytes each, no DER wrapper.
	hash := sha256.Sum256(manifestBytes)
	if len(signature) != 64 || !ecdsa.Verify(key, hash[:], new(big.Int).SetBytes(signature[:32]), new(big.Int).SetBytes(signature[32:])) {
		return reject(RejectInvalidSignature, &seq, "Bundle manifest signature is invalid.")
	}
	return nil
}

func isCompatible(r *manifestRange, current Version) bool {
	if r == nil {
		return false
	}
	minimum, ok := parseExactVersion(str(r.Min), r.Min != nil)
	if !ok {
		return false
	}
	maximum, ok := parseExactVersion(str(r.Max), r.Max != nil)
	if !ok || minimum.Compare(maximum) > 0 {
		return false
	}
	return current.Compare(minimum) >= 0 && current.Compare(maximum) <= 0
}

func parseExactVersion(value string, present bool) (Version, bool) {
	var version Version
	if !present || len(value) > 32 || !versionPattern.MatchString(value) {
		return version, false
	}
	for i, part := range strings.Split(value, ".") {
		number, err := strconv.ParseInt(part, 10, 32)
		if err != nil {
			return version, false
		}
		version[i] = int(number)
	}
	return version, true
}

// ParseVersion reads "major.minor[.build[.revision]]"; missing parts are zero and a revision is ignored,
// like clio's normalization of its own assembly version.
func ParseVersion(value string) (Version, error) {
	var version Version
	parts := strings.Split(strings.TrimSpace(value), ".")
	if len(parts) < 2 || len(parts) > 4 {
		return version, fmt.Errorf("invalid version %q", value)
	}
	for i := 0; i < 3 && i < len(parts); i++ {
		number, err := strconv.Atoi(parts[i])
		if err != nil || number < 0 {
			return version, fmt.Errorf("invalid version %q", value)
		}
		version[i] = number
	}
	return version, nil
}

func (v *Verifier) validateRequirements(m *manifest) error {
	seq := m.Sequence
	r := m.Requirements
	ids := r.ItemIDs
	if !m.multiSource() {
		ids = r.GuidanceIDs
	}
	if r.Tools == nil || r.ResourceURIs == nil || ids == nil {
		return reject(RejectMissingCapability, &seq, "Bundle requires an MCP tool capability that is not available.")
	}
	for _, tool := range *r.Tools {
		if !v.Capabilities.Tools[tool] {
			return reject(RejectMissingCapability, &seq, "Bundle requires an MCP tool capability that is not available.")
		}
	}
	if err := ensureUnique(*r.Tools, "required tool", seq); err != nil {
		return err
	}
	if err := ensureUnique(*ids, "required item id", seq); err != nil {
		return err
	}
	return ensureUnique(*r.ResourceURIs, "required resource URI", seq)
}

func ensureUnique(values []string, label string, seq uint64) error {
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		if blank(value) || seen[value] {
			return reject(RejectInvalidContent, &seq, "Every %s must be non-empty and unique.", label)
		}
		seen[value] = true
	}
	return nil
}

func resourceID(resource *manifestResource, m *manifest) string {
	if m.multiSource() {
		return str(resource.ItemID)
	}
	return str(resource.ID)
}

func readAndValidateResources(m *manifest, entries map[string]*zip.File) ([]Article, error) {
	seq := m.Sequence
	resources := *m.Resources
	collect := func(pick func(*manifestResource) string) []string {
		values := make([]string, 0, len(resources))
		for _, resource := range resources {
			values = append(values, pick(resource))
		}
		return values
	}
	if err := ensureUnique(collect(func(r *manifestResource) string { return resourceID(r, m) }), "resource id", seq); err != nil {
		return nil, err
	}
	if err := ensureUnique(collect(func(r *manifestResource) string { return str(r.URI) }), "resource URI", seq); err != nil {
		return nil, err
	}
	if err := ensureUnique(collect(func(r *manifestResource) string { return str(r.Path) }), "resource path", seq); err != nil {
		return nil, err
	}
	if m.multiSource() {
		if err := ensureUnique(collect(func(r *manifestResource) string { return str(r.TopicID) + "\x00" + str(r.Role) }), "topic and role", seq); err != nil {
			return nil, err
		}
		var legacy []string
		for _, resource := range resources {
			if resource.LegacyURIs != nil {
				legacy = append(legacy, *resource.LegacyURIs...)
			}
		}
		if err := ensureUnique(legacy, "legacy resource URI", seq); err != nil {
			return nil, err
		}
		canonical := map[string]bool{}
		for _, resource := range resources {
			canonical[str(resource.URI)] = true
		}
		for _, uri := range legacy {
			if canonical[uri] {
				return nil, reject(RejectInvalidContent, &seq, "Legacy resource URIs must not collide with canonical resource URIs.")
			}
		}
	}
	required := m.Requirements.ItemIDs
	if !m.multiSource() {
		required = m.Requirements.GuidanceIDs
	}
	if !sameSet(collect(func(r *manifestResource) string { return resourceID(r, m) }), *required) ||
		!sameSet(collect(func(r *manifestResource) string { return str(r.URI) }), *m.Requirements.ResourceURIs) {
		return nil, reject(RejectInvalidContent, &seq, "Declared requirements and bundle resources must describe the same complete item set.")
	}
	expected := map[string]bool{"manifest.json": true, "manifest.sig": true}
	articles := make([]Article, 0, len(resources))
	var total int64
	for _, resource := range resources {
		if err := validateResourceDescriptor(m, resource); err != nil {
			return nil, err
		}
		itemID := resourceID(resource, m)
		expected[str(resource.Path)] = true
		data, err := readRequiredEntry(entries, str(resource.Path), maxResourceBytes)
		if err != nil {
			return nil, err
		}
		total += int64(len(data))
		sum := sha256.Sum256(data)
		if total > maxBundleResourceBytes || resource.Length != int64(len(data)) || str(resource.Digest) != hex.EncodeToString(sum[:]) {
			return nil, reject(RejectInvalidContent, &seq, "Resource '%s' failed length or digest validation.", itemID)
		}
		if !utf8.Valid(data) {
			return nil, reject(RejectMalformed, nil, "Unable to translate bytes to their Unicode equivalent in resource '%s'.", itemID)
		}
		articles = append(articles, createArticle(m, resource, itemID, string(data)))
	}
	if len(expected) != len(entries) {
		return nil, reject(RejectInvalidContent, &seq, "Bundle contains missing or unexpected entries.")
	}
	for name := range entries {
		if !expected[name] {
			return nil, reject(RejectInvalidContent, &seq, "Bundle contains missing or unexpected entries.")
		}
	}
	return articles, nil
}

func sameSet(left, right []string) bool {
	a := map[string]bool{}
	for _, value := range left {
		a[value] = true
	}
	b := map[string]bool{}
	for _, value := range right {
		b[value] = true
	}
	if len(a) != len(b) {
		return false
	}
	for value := range a {
		if !b[value] {
			return false
		}
	}
	return true
}

func createArticle(m *manifest, resource *manifestResource, itemID, text string) Article {
	article := Article{
		Name: itemID, URI: str(resource.URI), Text: text, LibraryID: legacyLibraryID, ItemID: itemID,
		TopicID: itemID, Role: defaultRole, LocalPath: str(resource.Path), LegacyURIs: []string{},
		Title: itemID, Description: str(resource.Description), MediaType: str(resource.MediaType),
		RequiredFeatures: []string{},
	}
	if m.multiSource() {
		article.LibraryID = str(m.LibraryID)
		article.TopicID = str(resource.TopicID)
		article.Role = str(resource.Role)
	}
	if resource.LegacyURIs != nil {
		article.LegacyURIs = append([]string{}, *resource.LegacyURIs...)
	}
	if resource.Title != nil {
		article.Title = *resource.Title
	}
	if resource.RequiredFeatures != nil {
		article.RequiredFeatures = append([]string{}, *resource.RequiredFeatures...)
	}
	return article
}

func validateResourceDescriptor(m *manifest, resource *manifestResource) error {
	seq := m.Sequence
	itemID := resourceID(resource, m)
	multi := m.multiSource()
	uri := str(resource.URI)
	path := str(resource.Path)
	digest := str(resource.Digest)
	invalid := blank(itemID)
	if !invalid && multi {
		canonical := namespacedURIPrefix + EscapeDataString(str(m.LibraryID)) + "/" + EscapeDataString(itemID)
		featuresValid := true
		if resource.RequiredFeatures != nil {
			for _, feature := range *resource.RequiredFeatures {
				if !validStableID(feature) {
					featuresValid = false
				}
			}
		}
		invalid = !validStableID(itemID) || !validStableID(str(resource.TopicID)) || !allowedRoles[str(resource.Role)] ||
			!featuresValid || !validDiscoveryText(resource.Title, maxDiscoveryTitleLength) ||
			!validDiscoveryText(resource.Description, maxDiscoveryDescriptionLn) || uri != canonical
	}
	invalid = invalid || blank(uri) || !strings.HasPrefix(uri, "docs://") || blank(path) ||
		!strings.HasPrefix(path, "resources/") || strings.Contains(path, "..") || strings.Contains(path, `\`) ||
		blank(str(resource.MediaType)) || !strings.HasPrefix(str(resource.MediaType), "text/") || resource.Length < 0 ||
		blank(digest) || len(digest) != 64 || !isHex(digest)
	if invalid {
		return reject(RejectInvalidContent, &seq, "Resource '%s' has an invalid descriptor.", itemID)
	}
	if resource.LegacyURIs != nil {
		if err := ensureUnique(*resource.LegacyURIs, "legacy resource URI", seq); err != nil {
			return err
		}
		for _, legacy := range *resource.LegacyURIs {
			if !strings.HasPrefix(legacy, "docs://") {
				return reject(RejectInvalidContent, &seq, "Resource '%s' has an invalid legacy URI.", itemID)
			}
		}
	}
	if resource.RequiredFeatures != nil {
		return ensureUnique(*resource.RequiredFeatures, "required feature", seq)
	}
	return nil
}

func validStableID(value string) bool {
	return len(value) >= 1 && len(value) <= 160 && stableIDPattern.MatchString(value)
}

func validDiscoveryText(value *string, maximum int) bool {
	if value == nil || blank(*value) || len(utf16.Encode([]rune(*value))) > maximum || *value != trimDotNet(*value) {
		return false
	}
	for _, r := range *value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// trimDotNet trims what .NET's string.Trim trims: Unicode white space.
func trimDotNet(value string) string { return strings.TrimFunc(value, unicode.IsSpace) }

func blank(value string) bool { return strings.TrimFunc(value, unicode.IsSpace) == "" }

func isHex(value string) bool {
	for _, c := range value {
		if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
			return false
		}
	}
	return true
}

// EscapeDataString percent-encodes everything except RFC 3986 unreserved characters, like .NET's
// Uri.EscapeDataString.
func EscapeDataString(value string) string {
	var builder strings.Builder
	for _, b := range []byte(value) {
		if (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9') || b == '-' || b == '.' || b == '_' || b == '~' {
			builder.WriteByte(b)
			continue
		}
		fmt.Fprintf(&builder, "%%%02X", b)
	}
	return builder.String()
}

// UnescapeDataString reverses EscapeDataString; an invalid escape is kept literally, as .NET does.
func UnescapeDataString(value string) string {
	var out []byte
	for i := 0; i < len(value); i++ {
		if value[i] == '%' && i+2 < len(value) {
			if decoded, err := hex.DecodeString(value[i+1 : i+3]); err == nil {
				out = append(out, decoded[0])
				i += 2
				continue
			}
		}
		out = append(out, value[i])
	}
	return string(out)
}

// ParseP256PublicKeyPEM accepts exactly one "PUBLIC KEY" PEM block holding a P-256 key, with nothing
// but white space around it, as clio's trust stores require.
func ParseP256PublicKeyPEM(text string) (*ecdsa.PublicKey, []byte, bool) {
	block, rest := pem.Decode([]byte(text))
	if block == nil || block.Type != "PUBLIC KEY" || strings.TrimSpace(string(rest)) != "" {
		return nil, nil, false
	}
	start := strings.Index(text, "-----BEGIN")
	if start < 0 || strings.TrimSpace(text[:start]) != "" {
		return nil, nil, false
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, nil, false
	}
	key, ok := parsed.(*ecdsa.PublicKey)
	if !ok || key.Curve != elliptic.P256() {
		return nil, nil, false
	}
	return key, block.Bytes, true
}

func sortedStrings(values map[string]bool) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
