package clioconfig

// The shape check clio 8.1.0.142's SettingsBootstrapService runs before it trusts appsettings.json: it binds
// the file to its Settings model one top-level member at a time with Json.NET and records the JSON path of
// every member whose value has the wrong shape. This file is that model's shape, member by member, with
// Json.NET's conversion rules for the member types clio uses; testdata/health142.json is the table of
// answers clio itself gave, which the tests replay.

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/knowledge"
)

type shapeKind int

const (
	kString       shapeKind = iota // string: any scalar converts, an object or array does not
	kURI                           // System.Uri: a string or null
	kBool                          // bool: true/false, a number, a "true"/"false" string; not null or ""
	kNullableBool                  // bool?: as kBool, plus null and ""
	kInt                           // int: an int32 number or numeric string; not null or ""
	kNullableInt                   // int?: as kInt, plus null and ""
	kAuthFlow                      // OAuthFlow: a number, a bool, a member name or a numeric string; not null
	kDateOffset                    // DateTimeOffset?: a date string, null or ""
	kObject                        // a class: an object (members checked) or null
	kDict                          // Dictionary<string, T>: an object whose values are checked, or null
	kArray                         // T[]: an array whose elements are checked, or null
	kAutoupdate                    // AutoUpdateSettings: its converter takes a bool or an object, ignores anything else
	kAny                           // a member whose own converter this check does not model
	kSkip                          // a member Json.NET never binds ([JsonIgnore], getter-only)
)

type shape struct {
	kind   shapeKind
	fields []field // kObject: the declared members
	elem   *shape  // kDict, kArray: the value shape
	// unknown names the section whose unknown members clio counts as evidence of a newer writer ("" for the
	// root object, "autoupdate" for that section); empty when it keeps no such evidence.
	unknown *string
}

type field struct {
	name  string
	shape *shape
}

func scalar(kind shapeKind) *shape             { return &shape{kind: kind} }
func object(fields ...field) *shape            { return &shape{kind: kObject, fields: fields} }
func dict(elem *shape) *shape                  { return &shape{kind: kDict, elem: elem} }
func array(elem *shape) *shape                 { return &shape{kind: kArray, elem: elem} }
func member(name string, s *shape) field       { return field{name: name, shape: s} }
func evidence(s *shape, section string) *shape { s.unknown = &section; return s }

// environmentShape is EnvironmentSettings as Json.NET binds it (the [JsonIgnore] members are skipped).
var environmentShape = object(
	member("IdentityService", object(
		member("EnvironmentPath", scalar(kString)), member("IisTarget", scalar(kString)),
		member("ApplicationPool", scalar(kString)), member("Uri", scalar(kString)),
		member("CrmReferencesCleared", scalar(kBool)), member("IsEmpty", scalar(kSkip)))),
	member("Uri", scalar(kString)), member("DbName", scalar(kString)), member("BackupFilePath", scalar(kString)),
	member("Login", scalar(kString)), member("Password", scalar(kString)), member("Maintainer", scalar(kString)),
	member("IsNetCore", scalar(kBool)), member("ClientId", scalar(kString)), member("DbServerKey", scalar(kString)),
	member("DbServer", scalar(kSkip)), member("ClientSecret", scalar(kString)), member("WorkspacePathes", scalar(kString)),
	member("AuthAppUri", scalar(kString)), member("SimpleloginUri", scalar(kSkip)), member("AccessToken", scalar(kString)),
	member("AccessTokenType", scalar(kSkip)), member("Cookie", scalar(kSkip)), member("AuthFlow", scalar(kAuthFlow)),
	member("RedirectPort", scalar(kNullableInt)), member("RedirectUri", scalar(kString)),
	member("EnvironmentName", scalar(kSkip)), member("ExternalAccessToken", scalar(kSkip)),
	member("Safe", scalar(kNullableBool)), member("DeveloperModeEnabled", scalar(kNullableBool)),
	member("IsDevMode", scalar(kSkip)), member("EnvironmentPath", scalar(kString)),
)

func autoUpdatePolicy() *shape {
	return evidence(object(member("enabled", scalar(kBool)), member("frequency-minutes", scalar(kInt)),
		member("next-run", scalar(kDateOffset))), "autoupdate")
}

// settingsShape is clio's Settings class.
var settingsShape = evidence(object(
	member("creatio-products", scalar(kString)), member("iis-clio-root-path", scalar(kString)),
	member("iis-certificate-thumbprint", scalar(kString)), member("workspaces-root", scalar(kString)),
	member("knowledge", object(
		member("root-path", scalar(kString)),
		member("sources", dict(object(
			member("library-id", scalar(kString)), member("type", scalar(kAny)), member("location", scalar(kString)),
			member("trusted-key-id", scalar(kString)), member("trusted-public-key-path", scalar(kString)),
			member("package-id", scalar(kString)), member("repository-owner", scalar(kString)),
			member("repository-name", scalar(kString)), member("asset-name", scalar(kString)),
			member("branch", scalar(kString)), member("tag", scalar(kString)), member("commit", scalar(kString)),
			member("enabled", scalar(kBool)), member("priority", scalar(kInt)), member("participation", scalar(kAny))))),
		member("topic-pins", dict(scalar(kString))))),
	member("knowledge-feedback", object(
		member("mode", scalar(kString)), member("destination", scalar(kString)),
		member("reporting-scope", scalar(kString)), member("standing-approval", object(member("policy-hash", scalar(kString)))))),
	member("knowledge-root-path", scalar(kString)), member("container-image-cli", scalar(kString)),
	member("$schema", scalar(kSkip)), member("ActiveEnvironmentKey", scalar(kString)),
	member("dbConnectionStringKeys", dict(object(member("Uri", scalar(kURI)), member("WorkingFolder", scalar(kString)),
		member("Password", scalar(kSkip)), member("Login", scalar(kSkip))))),
	member("db", dict(object())), member("redis", dict(object())), member("defaultRedis", scalar(kString)),
	member("telemetry", object(member("enabled", scalar(kNullableBool)), member("endpoint", scalar(kString)),
		member("ingest-key", scalar(kString)))),
	member("deploy-creatio-defaults", object(
		member("db-server-name", scalar(kString)), member("redis-server-name", scalar(kString)),
		member("site-name", scalar(kString)), member("site-port", scalar(kNullableInt)),
		member("site-port-range", array(scalar(kInt))), member("deployment", scalar(kString)), member("IsEmpty", scalar(kSkip)))),
	member("dbhub", object()), member("page-smoke-browser", object()),
	member("autoupdate", &shape{kind: kAutoupdate, elem: evidence(object(
		member("clio", autoUpdatePolicy()), member("knowledge", autoUpdatePolicy()), member("toolkit", autoUpdatePolicy())),
		"autoupdate")}),
	member("SettingsVersion", scalar(kNullableInt)),
	member("Environments", dict(environmentShape)),
	member("features", dict(scalar(kBool))),
	member("RemoteArtefactServerPath", scalar(kString)),
), "")

// bindResult is what binding the file found: the failing paths in order, and the sections that carried
// members the model does not declare.
type bindResult struct {
	failures []string
	unknown  map[string]bool
}

func (r *bindResult) fail(path string) {
	for _, existing := range r.failures {
		if existing == path {
			return
		}
	}
	r.failures = append(r.failures, path)
}

// bindSettings checks every top-level member of root against clio's Settings model.
func bindSettings(root *knowledge.JSONObject) bindResult {
	result := bindResult{unknown: map[string]bool{}}
	bindObject(root, settingsShape, "", &result)
	return result
}

// lookup finds the declared member a JSON name binds to: an exact match first, then one ignoring case, as
// Json.NET's GetClosestMatchProperty does.
func lookup(fields []field, name string) (field, bool) {
	for _, candidate := range fields {
		if candidate.name == name {
			return candidate, true
		}
	}
	for _, candidate := range fields {
		if strings.EqualFold(candidate.name, name) {
			return candidate, true
		}
	}
	return field{}, false
}

func bindObject(object *knowledge.JSONObject, s *shape, path string, result *bindResult) {
	for _, name := range object.Keys() {
		value, _ := object.Get(name)
		declared, ok := lookup(s.fields, name)
		if !ok {
			if s.unknown != nil {
				result.unknown[*s.unknown] = true
			}
			continue
		}
		bindValue(value, declared.shape, childPath(path, name), result)
	}
}

func bindValue(value any, s *shape, path string, result *bindResult) {
	switch s.kind {
	case kSkip, kAny:
		return
	case kAutoupdate:
		if object, ok := value.(*knowledge.JSONObject); ok {
			bindObject(object, s.elem, path, result)
		}
		return
	case kObject:
		switch typed := value.(type) {
		case nil:
		case *knowledge.JSONObject:
			bindObject(typed, s, path, result)
		default:
			result.fail(path)
		}
		return
	case kDict:
		switch typed := value.(type) {
		case nil:
		case *knowledge.JSONObject:
			for _, key := range typed.Keys() {
				entry, _ := typed.Get(key)
				bindValue(entry, s.elem, childPath(path, key), result)
			}
		default:
			result.fail(path)
		}
		return
	case kArray:
		switch typed := value.(type) {
		case nil:
		case []any:
			for index, element := range typed {
				bindValue(element, s.elem, path+"["+strconv.Itoa(index)+"]", result)
			}
		default:
			result.fail(path)
		}
		return
	}
	if !convertible(value, s.kind) {
		result.fail(path)
	}
}

// convertible reports whether Json.NET reads value into a member of the scalar kind.
func convertible(value any, kind shapeKind) bool {
	switch kind {
	case kString:
		switch value.(type) {
		case *knowledge.JSONObject, []any:
			return false
		}
		return true
	case kURI:
		switch value.(type) {
		case nil, string:
			return true
		}
		return false
	case kBool, kNullableBool:
		_, isNull, ok := readBool(value)
		return ok && (!isNull || kind == kNullableBool)
	case kInt, kNullableInt:
		_, isNull, ok := readInt(value)
		return ok && (!isNull || kind == kNullableInt)
	case kAuthFlow:
		_, ok := readAuthFlow(value)
		return ok
	case kDateOffset:
		switch typed := value.(type) {
		case nil:
			return true
		case string:
			return strings.TrimSpace(typed) == "" || parseDateOffset(typed)
		}
		return false
	}
	return true
}

// readBool is JsonReader.ReadAsBoolean: a boolean; a number (non-zero is true); a string bool.TryParse
// accepts, or an empty string, which reads as null.
func readBool(value any) (result, isNull, ok bool) {
	switch typed := value.(type) {
	case nil:
		return false, true, true
	case bool:
		return typed, false, true
	case json.Number:
		number, err := typed.Float64()
		return err == nil && number != 0, false, err == nil
	case string:
		if typed == "" {
			return false, true, true
		}
		switch strings.ToLower(strings.Trim(typed, " \t\r\n\x00")) {
		case "true":
			return true, false, true
		case "false":
			return false, false, true
		}
	}
	return false, false, false
}

// readInt is JsonReader.ReadAsInt32: an int32 number (a fraction is rounded as Convert.ToInt32 does), a
// string int.TryParse accepts, or an empty string, which reads as null.
func readInt(value any) (result int, isNull, ok bool) {
	switch typed := value.(type) {
	case nil:
		return 0, true, true
	case json.Number:
		if whole, err := typed.Int64(); err == nil {
			return int(whole), false, whole >= math.MinInt32 && whole <= math.MaxInt32
		}
		number, err := typed.Float64()
		if err != nil || number < math.MinInt32-0.5 || number >= math.MaxInt32+0.5 {
			return 0, false, false
		}
		return int(math.RoundToEven(number)), false, true
	case string:
		if typed == "" {
			return 0, true, true
		}
		whole, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 32)
		return int(whole), false, err == nil
	}
	return 0, false, false
}

// readAuthFlow is Json.NET's enum conversion for OAuthFlow: a number or bool converts, a member name
// (ignoring case) or a numeric string parses; null and other text do not.
func readAuthFlow(value any) (int, bool) {
	switch typed := value.(type) {
	case bool:
		if typed {
			return 1, true
		}
		return 0, true
	case json.Number:
		if whole, err := typed.Int64(); err == nil && whole >= math.MinInt32 && whole <= math.MaxInt32 {
			return int(whole), true
		}
		return 0, false
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "clientcredentials":
			return 0, true
		case "authorizationcode":
			return 1, true
		}
		whole, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 32)
		return int(whole), err == nil
	}
	return 0, false
}

var dateOffsetLayouts = []string{time.RFC3339Nano, "2006-01-02T15:04:05.9999999", "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02"}

func parseDateOffset(text string) bool {
	for _, layout := range dateOffsetLayouts {
		if _, err := time.Parse(layout, strings.TrimSpace(text)); err == nil {
			return true
		}
	}
	return false
}

// childPath is Json.NET's JsonPosition path: ".name", or "['name']" when the name holds a character a dotted
// path cannot carry; the root's own members have no leading dot.
func childPath(parent, name string) string {
	if strings.ContainsAny(name, ". '/\"[]()\t\n\r\f\b\\\u0085  ") {
		escaped := strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(name)
		return parent + "['" + escaped + "']"
	}
	if parent == "" {
		return name
	}
	return parent + "." + name
}
