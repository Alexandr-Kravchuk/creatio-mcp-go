package clioconfig

// One "Environments" entry as clio 8.1.0.142's EnvironmentSettings reads it from appsettings.json and writes
// it back: the members in the class's declaration order, null members left out, IdentityService always
// written, AuthFlow as its number, AuthAppUri through its getter (which derives the identity-server token
// endpoint of a *.creatio.com site), and members the class does not declare kept after the rest, as
// Json.NET's extension data is.

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/knowledge"
)

// identityMembers are IdentityServiceAttachment's members in order; the last one is a bool.
var identityMembers = []string{"EnvironmentPath", "IisTarget", "ApplicationPool", "Uri", "CrmReferencesCleared"}

// Environment is EnvironmentSettings. A nil pointer is a null member, which clio does not write.
type Environment struct {
	identity        []any // IdentityService members by identityMembers; nil entries are null
	URI             *string
	DBName          *string
	BackupFilePath  *string
	Login           *string
	Password        *string
	Maintainer      *string
	IsNetCore       bool
	ClientID        *string
	DBServerKey     *string
	ClientSecret    *string
	WorkspacePathes *string
	authAppURI      *string // the stored _authAppUri; AuthAppURI() is what clio reads and writes
	accessToken     *string // StoredAccessToken, bound from "AccessToken"
	AuthFlow        int
	RedirectPort    *int
	RedirectURI     *string
	Safe            *bool
	DevMode         *bool
	EnvironmentPath *string
	extra           []extraMember
}

type extraMember struct {
	name  string
	value any
}

// NewEnvironment is `new EnvironmentSettings()`: an empty IdentityService and EnvironmentPath "".
func NewEnvironment() Environment {
	empty := ""
	return Environment{identity: defaultIdentity(), EnvironmentPath: &empty}
}

func defaultIdentity() []any { return []any{"", "", "", "", false} }

// SetAuthAppURI sets the stored _authAppUri.
func (e *Environment) SetAuthAppURI(value *string) { e.authAppURI = value }

// AuthAppURI is EnvironmentSettings.AuthAppUri's getter: an empty value on a *.creatio.com site becomes
// that site's identity-server token endpoint.
func (e Environment) AuthAppURI() *string {
	if e.authAppURI == nil || *e.authAppURI == "" {
		if e.URI != nil && strings.Contains(strings.ToLower(*e.URI), ".creatio.com") {
			derived := strings.ReplaceAll(strings.ToLower(*e.URI), ".creatio.com", "-is.creatio.com/connect/token")
			return &derived
		}
	}
	return e.authAppURI
}

// identityEmpty is IdentityServiceAttachment.IsEmpty.
func (e Environment) identityEmpty() bool {
	for index, value := range e.identity {
		if index == len(identityMembers)-1 {
			if flag, _ := value.(bool); flag {
				return false
			}
			continue
		}
		if text, _ := value.(string); strings.TrimSpace(text) != "" {
			return false
		}
	}
	return true
}

// ParseEnvironment binds a stored entry the way Json.NET does. The entry has already passed the shape
// check, so every value converts; a null entry is a default EnvironmentSettings.
func ParseEnvironment(value any) Environment {
	environment := NewEnvironment()
	object, ok := value.(*knowledge.JSONObject)
	if !ok {
		return environment
	}
	text := func(member any) *string {
		converted, isNull := stringValue(member)
		if isNull {
			return nil
		}
		return &converted
	}
	for _, name := range object.Keys() {
		member, _ := object.Get(name)
		setText := func(target **string) { *target = text(member) }
		declared, known := lookup(environmentShape.fields, name)
		if !known {
			environment.extra = append(environment.extra, extraMember{name, member})
			continue
		}
		switch declared.name {
		case "IdentityService":
			environment.identity = parseIdentity(member)
		case "Uri":
			environment.URI = text(member)
		case "DbName":
			environment.DBName = text(member)
		case "BackupFilePath":
			environment.BackupFilePath = text(member)
		case "Login":
			environment.Login = text(member)
		case "Password":
			setText(&environment.Password)
		case "Maintainer":
			environment.Maintainer = text(member)
		case "IsNetCore":
			environment.IsNetCore, _, _ = readBool(member)
		case "ClientId":
			environment.ClientID = text(member)
		case "DbServerKey":
			environment.DBServerKey = text(member)
		case "ClientSecret":
			setText(&environment.ClientSecret)
		case "WorkspacePathes":
			environment.WorkspacePathes = text(member)
		case "AuthAppUri":
			environment.authAppURI = text(member)
		case "AccessToken":
			setText(&environment.accessToken)
		case "AuthFlow":
			environment.AuthFlow, _ = readAuthFlow(member)
		case "RedirectPort":
			if port, isNull, ok := readInt(member); ok && !isNull {
				environment.RedirectPort = &port
			} else {
				environment.RedirectPort = nil
			}
		case "RedirectUri":
			environment.RedirectURI = text(member)
		case "Safe":
			environment.Safe = nullableBool(member)
		case "DeveloperModeEnabled":
			environment.DevMode = nullableBool(member)
		case "EnvironmentPath":
			environment.EnvironmentPath = text(member)
		default:
			// [JsonIgnore] members: Json.NET keeps them with the extension data.
			environment.extra = append(environment.extra, extraMember{name, member})
		}
	}
	return environment
}

func nullableBool(member any) *bool {
	flag, isNull, ok := readBool(member)
	if !ok || isNull {
		return nil
	}
	return &flag
}

// parseIdentity binds IdentityServiceAttachment: a null value becomes a new attachment (its setter does
// that), a member left out keeps its default, a null member is null.
func parseIdentity(value any) []any {
	identity := defaultIdentity()
	object, ok := value.(*knowledge.JSONObject)
	if !ok {
		return identity
	}
	for index, name := range identityMembers {
		_, member, found := memberFold(object, name)
		if !found {
			continue
		}
		if index == len(identityMembers)-1 {
			flag, _, _ := readBool(member)
			identity[index] = flag
			continue
		}
		if converted, isNull := stringValue(member); isNull {
			identity[index] = nil
		} else {
			identity[index] = converted
		}
	}
	return identity
}

// Object writes the entry as clio's settings serializer does.
func (e Environment) Object() *knowledge.JSONObject {
	object := knowledge.NewJSONObject()
	identity := knowledge.NewJSONObject()
	for index, name := range identityMembers {
		if e.identity[index] != nil {
			identity.Set(name, e.identity[index])
		}
	}
	object.Set("IdentityService", identity)
	text := func(name string, value *string) {
		if value != nil {
			object.Set(name, *value)
		}
	}
	text("Uri", e.URI)
	text("DbName", e.DBName)
	text("BackupFilePath", e.BackupFilePath)
	text("Login", e.Login)
	text("Password", e.Password)
	text("Maintainer", e.Maintainer)
	object.Set("IsNetCore", e.IsNetCore)
	text("ClientId", e.ClientID)
	text("DbServerKey", e.DBServerKey)
	text("ClientSecret", e.ClientSecret)
	text("WorkspacePathes", e.WorkspacePathes)
	text("AuthAppUri", e.AuthAppURI())
	text("AccessToken", e.accessToken)
	object.Set("AuthFlow", json.Number(strconv.Itoa(e.AuthFlow)))
	if e.RedirectPort != nil {
		object.Set("RedirectPort", json.Number(strconv.Itoa(*e.RedirectPort)))
	}
	text("RedirectUri", e.RedirectURI)
	if e.Safe != nil {
		object.Set("Safe", *e.Safe)
	}
	if e.DevMode != nil {
		object.Set("DeveloperModeEnabled", *e.DevMode)
	}
	text("EnvironmentPath", e.EnvironmentPath)
	for _, member := range e.extra {
		if _, taken := object.Get(member.name); !taken {
			object.Set(member.name, member.value)
		}
	}
	return object
}

// Merge is EnvironmentSettings.Merge: what reg-web-app changes on an environment that is already registered.
func (e *Environment) Merge(update Environment) {
	if !update.identityEmpty() {
		e.identity = update.identity
	}
	keep := func(target **string, value *string) {
		if value != nil && *value != "" {
			*target = value
		}
	}
	keep(&e.Login, update.Login)
	keep(&e.URI, update.URI)
	keep(&e.Password, update.Password)
	keep(&e.Maintainer, update.Maintainer)
	if update.Safe != nil {
		e.Safe = update.Safe
	}
	if update.DevMode != nil {
		e.DevMode = update.DevMode
	}
	e.IsNetCore = update.IsNetCore
	e.ClientSecret, e.ClientID, e.authAppURI = update.ClientSecret, update.ClientID, update.AuthAppURI()
	e.AuthFlow = update.AuthFlow
	e.RedirectPort = update.RedirectPort
	e.RedirectURI = update.RedirectURI
	e.WorkspacePathes = update.WorkspacePathes
	if update.EnvironmentPath != nil && strings.TrimSpace(*update.EnvironmentPath) != "" {
		e.EnvironmentPath = update.EnvironmentPath
	}
	keep(&e.DBName, update.DBName)
	keep(&e.DBServerKey, update.DBServerKey)
	keep(&e.BackupFilePath, update.BackupFilePath)
}
