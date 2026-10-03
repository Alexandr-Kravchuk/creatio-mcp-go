package creatio

// The EntitySchemaDesignerService contract of clio's entity-schema write tools (create-entity-schema,
// create-lookup, update-entity-schema, modify-entity-schema-column, set-entity-schema-properties,
// sync-schemas), ported from clio's EntitySchemaDesignerDtos. clio reads every designer answer into these
// typed DTOs and writes them back with Newtonsoft, so a save carries exactly these properties, in this
// order, with nulls and empty lists spelled out; a property the designer sent that the DTO does not know is
// dropped. The Go structs keep the same shape so the request bodies match clio's.

import (
	"bytes"
	"encoding/json"
)

// schemaWriteEntGUID is a .NET Guid as Newtonsoft reads and writes it: any parseable spelling in, the
// lower-case "D" form out, the empty GUID for a missing or unparseable value.
type schemaWriteEntGUID string

func (g schemaWriteEntGUID) String() string {
	if g == "" {
		return emptyGUID
	}
	return string(g)
}

func (g schemaWriteEntGUID) isEmpty() bool { return g == "" || string(g) == emptyGUID }

func (g schemaWriteEntGUID) MarshalJSON() ([]byte, error) { return json.Marshal(g.String()) }

func (g *schemaWriteEntGUID) UnmarshalJSON(data []byte) error {
	var text string
	if json.Unmarshal(data, &text) != nil {
		*g = ""
		return nil
	}
	normalized := pkgWriteNormalizeGUID(text)
	if normalized == emptyGUID {
		*g = ""
		return nil
	}
	*g = schemaWriteEntGUID(normalized)
	return nil
}

func newSchemaWriteEntGUID(text string) schemaWriteEntGUID {
	var g schemaWriteEntGUID
	encoded, _ := json.Marshal(text)
	_ = g.UnmarshalJSON(encoded)
	return g
}

// schemaWriteEntLocalizable is clio's LocalizableStringDto.
type schemaWriteEntLocalizable struct {
	CultureName *string `json:"cultureName"`
	Value       *string `json:"value"`
}

func schemaWriteEntLocalized(culture, value string) schemaWriteEntLocalizable {
	return schemaWriteEntLocalizable{CultureName: &culture, Value: &value}
}

func (l schemaWriteEntLocalizable) culture() string {
	if l.CultureName == nil {
		return ""
	}
	return *l.CultureName
}

func (l schemaWriteEntLocalizable) text() string {
	if l.Value == nil {
		return ""
	}
	return *l.Value
}

// schemaWriteEntPackage is clio's WorkspacePackageDto.
type schemaWriteEntPackage struct {
	UID  schemaWriteEntGUID `json:"uId"`
	Name *string            `json:"name"`
}

// schemaWriteEntDefValue is clio's EntitySchemaColumnDefValueDto. Value is whatever JSON the designer or
// the caller supplied; Newtonsoft writes it back unchanged.
type schemaWriteEntDefValue struct {
	ValueSourceType       int             `json:"valueSourceType"`
	Value                 json.RawMessage `json:"value"`
	ValueSource           *string         `json:"valueSource"`
	SequencePrefix        *string         `json:"sequencePrefix"`
	SequenceNumberOfChars int             `json:"sequenceNumberOfChars"`
}

func (d *schemaWriteEntDefValue) UnmarshalJSON(data []byte) error {
	var raw struct {
		ValueSourceType       json.RawMessage `json:"valueSourceType"`
		Value                 json.RawMessage `json:"value"`
		ValueSource           json.RawMessage `json:"valueSource"`
		SequencePrefix        *string         `json:"sequencePrefix"`
		SequenceNumberOfChars *int            `json:"sequenceNumberOfChars"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*d = schemaWriteEntDefValue{Value: schemaWriteEntNullable(raw.Value), SequencePrefix: raw.SequencePrefix}
	if raw.SequenceNumberOfChars != nil {
		d.SequenceNumberOfChars = *raw.SequenceNumberOfChars
	}
	var number int
	if json.Unmarshal(raw.ValueSourceType, &number) == nil {
		d.ValueSourceType = number
	} else {
		var name string
		if json.Unmarshal(raw.ValueSourceType, &name) == nil {
			for ordinal, known := range defaultValueSourceNames {
				if known == name {
					d.ValueSourceType = ordinal
				}
			}
		}
	}
	if text := rawJSONText(raw.ValueSource); len(bytes.TrimSpace(raw.ValueSource)) > 0 && string(bytes.TrimSpace(raw.ValueSource)) != "null" {
		d.ValueSource = &text
	}
	return nil
}

func (d schemaWriteEntDefValue) MarshalJSON() ([]byte, error) {
	type plain schemaWriteEntDefValue
	copy := plain(d)
	copy.Value = schemaWriteEntNullable(copy.Value)
	if copy.Value == nil {
		copy.Value = json.RawMessage("null")
	}
	return schemaWriteEntMarshal(copy)
}

func schemaWriteEntNullable(value json.RawMessage) json.RawMessage {
	trimmed := bytes.TrimSpace(value)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil
	}
	return value
}

// schemaWriteEntMasking is clio's EntitySchemaColumnValueMaskingSettingsDto.
type schemaWriteEntMasking struct {
	Pattern            *string `json:"pattern"`
	Replacement        *string `json:"replacement"`
	AdminOperationCode *string `json:"adminOperationCode"`
}

// schemaWriteEntColumn is clio's EntitySchemaColumnDto.
type schemaWriteEntColumn struct {
	UID                   schemaWriteEntGUID          `json:"uId"`
	Name                  *string                     `json:"name"`
	Caption               []schemaWriteEntLocalizable `json:"caption"`
	Description           []schemaWriteEntLocalizable `json:"description"`
	DataValueType         *int                        `json:"type"`
	IsInherited           bool                        `json:"isInherited"`
	RequirementType       int                         `json:"requirementType"`
	UsageType             int                         `json:"usageType"`
	DefValue              *schemaWriteEntDefValue     `json:"defValue"`
	IsValueCloneable      bool                        `json:"isValueCloneable"`
	IsTrackChangesInDB    bool                        `json:"isTrackChangesInDB"`
	Indexed               bool                        `json:"indexed"`
	MultiLineText         bool                        `json:"isMultiLineText"`
	AccentInsensitive     bool                        `json:"isAccentInsensitive"`
	LocalizableText       bool                        `json:"isLocalizableText"`
	UseSeconds            bool                        `json:"useSeconds"`
	Masked                bool                        `json:"isMasked"`
	ValueMasked           bool                        `json:"isValueMasked"`
	ValueMaskingSettings  *schemaWriteEntMasking      `json:"valueMaskingSettings"`
	FormatValidated       bool                        `json:"isFormatValidated"`
	ReferenceSchema       *schemaWriteEntSchema       `json:"referenceSchema"`
	List                  bool                        `json:"isSimpleLookup"`
	CascadeConnection     bool                        `json:"isCascade"`
	DoNotControlIntegrity bool                        `json:"doNotControlIntegrity"`
	SensitiveData         bool                        `json:"isSensitiveData"`
}

func (c *schemaWriteEntColumn) name() string {
	if c == nil || c.Name == nil {
		return ""
	}
	return *c.Name
}

func (c *schemaWriteEntColumn) typeIs(dataValueType int) bool {
	return c != nil && c.DataValueType != nil && *c.DataValueType == dataValueType
}

func (c *schemaWriteEntColumn) UnmarshalJSON(data []byte) error {
	type plain schemaWriteEntColumn
	value := plain{Caption: []schemaWriteEntLocalizable{}, Description: []schemaWriteEntLocalizable{}}
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*c = schemaWriteEntColumn(value)
	if c.Caption == nil {
		c.Caption = []schemaWriteEntLocalizable{}
	}
	if c.Description == nil {
		c.Description = []schemaWriteEntLocalizable{}
	}
	return nil
}

// schemaWriteEntSchema is clio's EntityDesignSchemaDto.
type schemaWriteEntSchema struct {
	ID                        schemaWriteEntGUID          `json:"id"`
	UID                       schemaWriteEntGUID          `json:"uId"`
	Name                      *string                     `json:"name"`
	Package                   *schemaWriteEntPackage      `json:"package"`
	Caption                   []schemaWriteEntLocalizable `json:"caption"`
	Description               []schemaWriteEntLocalizable `json:"description"`
	Columns                   []*schemaWriteEntColumn     `json:"columns"`
	InheritedColumns          []*schemaWriteEntColumn     `json:"inheritedColumns"`
	Indexes                   []json.RawMessage           `json:"indexes"`
	PrimaryColumn             *schemaWriteEntColumn       `json:"primaryColumn"`
	PrimaryDisplayColumn      *schemaWriteEntColumn       `json:"primaryDisplayColumn"`
	PrimaryImageColumn        *schemaWriteEntColumn       `json:"primaryImageColumn"`
	PrimaryColorColumn        *schemaWriteEntColumn       `json:"primaryColorColumn"`
	PrimaryOrderColumn        *schemaWriteEntColumn       `json:"primaryOrderColumn"`
	HierarchyColumn           *schemaWriteEntColumn       `json:"hierarchyColumn"`
	OwnerColumn               *schemaWriteEntColumn       `json:"ownerColumn"`
	ParentSchema              *schemaWriteEntSchema       `json:"parentSchema"`
	ExtendParent              bool                        `json:"extendParent"`
	IsDBView                  bool                        `json:"isDBView"`
	IsSSPAvailable            bool                        `json:"isSSPAvailable"`
	IsVirtual                 bool                        `json:"isVirtual"`
	UseRecordDeactivation     bool                        `json:"useRecordDeactivation"`
	ShowInAdvancedMode        bool                        `json:"showInAdvancedMode"`
	IsTrackChangesInDB        bool                        `json:"isTrackChangesInDB"`
	AdministratedByOperations bool                        `json:"administratedByOperations"`
	AdministratedByColumns    bool                        `json:"administratedByColumns"`
	AdministratedByRecords    bool                        `json:"administratedByRecords"`
	UseDenyRecordRights       bool                        `json:"useDenyRecordRights"`
	UseLiveEditing            bool                        `json:"useLiveEditing"`
	TrackChangesSchemaName    *string                     `json:"trackChangesSchemaName"`
	RightSchemaName           *string                     `json:"rightSchemaName"`
	LocalizationSchemaName    *string                     `json:"localizationSchemaName"`
	MasterRecordColumn        *schemaWriteEntColumn       `json:"masterRecordColumn"`
	CreatedByColumn           *schemaWriteEntColumn       `json:"createdByColumn"`
	CreatedOnColumn           *schemaWriteEntColumn       `json:"createdOnColumn"`
	ModifiedByColumn          *schemaWriteEntColumn       `json:"modifiedByColumn"`
	ModifiedOnColumn          *schemaWriteEntColumn       `json:"modifiedOnColumn"`
	UseFullHierarchy          bool                        `json:"useFullHierarchy"`
}

func newSchemaWriteEntSchema() *schemaWriteEntSchema {
	return &schemaWriteEntSchema{Caption: []schemaWriteEntLocalizable{}, Description: []schemaWriteEntLocalizable{},
		Columns: []*schemaWriteEntColumn{}, InheritedColumns: []*schemaWriteEntColumn{}, Indexes: []json.RawMessage{}}
}

func (s *schemaWriteEntSchema) name() string {
	if s == nil || s.Name == nil {
		return ""
	}
	return *s.Name
}

// hasValue is clio's HasValue: a schema reference with a non-empty UId.
func (s *schemaWriteEntSchema) hasValue() bool { return s != nil && !s.UID.isEmpty() }

func (s *schemaWriteEntSchema) UnmarshalJSON(data []byte) error {
	type plain schemaWriteEntSchema
	value := plain(*newSchemaWriteEntSchema())
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*s = schemaWriteEntSchema(value)
	if s.Caption == nil {
		s.Caption = []schemaWriteEntLocalizable{}
	}
	if s.Description == nil {
		s.Description = []schemaWriteEntLocalizable{}
	}
	return nil
}

// allColumns is clio's GetAllColumns: own columns, then inherited ones.
func (s *schemaWriteEntSchema) allColumns() []*schemaWriteEntColumn {
	columns := append([]*schemaWriteEntColumn{}, s.Columns...)
	return append(columns, s.InheritedColumns...)
}

// normalizeLists is what LoadSchema does after a read: null lists become empty ones.
func (s *schemaWriteEntSchema) normalizeLists() {
	if s.Columns == nil {
		s.Columns = []*schemaWriteEntColumn{}
	}
	if s.InheritedColumns == nil {
		s.InheritedColumns = []*schemaWriteEntColumn{}
	}
	if s.Indexes == nil {
		s.Indexes = []json.RawMessage{}
	}
}

// schemaWriteEntManagerItem is clio's ManagerItemDto.
type schemaWriteEntManagerItem struct {
	UID     schemaWriteEntGUID `json:"uId"`
	Name    string             `json:"name"`
	Caption *string            `json:"caption"`
}

// schemaWriteEntErrorInfo is the errorInfo of clio's BaseResponse.
type schemaWriteEntErrorInfo struct {
	ErrorCode *string `json:"errorCode"`
	Message   *string `json:"message"`
}

// schemaWriteEntBase is clio's BaseResponse.
type schemaWriteEntBase struct {
	Success   bool                     `json:"success"`
	ErrorInfo *schemaWriteEntErrorInfo `json:"errorInfo"`
}

func (b schemaWriteEntBase) base() schemaWriteEntBase { return b }

type schemaWriteEntResponse interface{ base() schemaWriteEntBase }

type schemaWriteEntDesignerResponse struct {
	schemaWriteEntBase
	Schema *schemaWriteEntSchema `json:"schema"`
}

type schemaWriteEntAvailableResponse struct {
	schemaWriteEntBase
	Items []schemaWriteEntManagerItem `json:"items"`
}

type schemaWriteEntSaveResponse struct {
	schemaWriteEntBase
	SchemaUID schemaWriteEntGUID `json:"schemaUid"`
}

type schemaWriteEntBoolResponse struct {
	schemaWriteEntBase
	Value bool `json:"value"`
}

type schemaWriteEntPlainResponse struct {
	schemaWriteEntBase
}

type schemaWriteEntRuntimeResponse struct {
	schemaWriteEntBase
	Schema *struct {
		UID  schemaWriteEntGUID `json:"uId"`
		Name string             `json:"name"`
	} `json:"schema"`
}

type schemaWriteEntSystemValue struct {
	Value        schemaWriteEntGUID `json:"value"`
	DisplayValue string             `json:"displayValue"`
}

type schemaWriteEntSystemValuesResponse struct {
	schemaWriteEntBase
	Items []schemaWriteEntSystemValue `json:"items"`
}

// schemaWriteEntDesignRequest is clio's GetSchemaDesignItemRequestDto.
type schemaWriteEntDesignRequest struct {
	Name             string             `json:"name"`
	PackageUID       schemaWriteEntGUID `json:"packageUId"`
	UseFullHierarchy bool               `json:"useFullHierarchy"`
	Cultures         []string           `json:"cultures"`
}

// schemaWriteEntAvailableRequest is clio's GetAvailableSchemasRequestDto.
type schemaWriteEntAvailableRequest struct {
	PackageUID       schemaWriteEntGUID `json:"packageUId"`
	UseFullHierarchy bool               `json:"useFullHierarchy"`
	AllowVirtual     bool               `json:"allowVirtual"`
}

// schemaWriteEntDesignerRequest is clio's SchemaDesignerRequestDto.
type schemaWriteEntDesignerRequest struct {
	SaveSchemaDBStructure     []schemaWriteEntGUID `json:"saveSchemaDBStructure"`
	BuildWorkspace            bool                 `json:"buildWorkspace"`
	BuildChangedConfiguration bool                 `json:"buildChangedConfiguration"`
}

// schemaWriteEntNewtonsoft serializes a request the way clio's IJsonConverter does (Newtonsoft, indented).
func schemaWriteEntNewtonsoft(value any) []byte {
	encoded, err := schemaWriteEntMarshal(value)
	if err != nil {
		return []byte("{}")
	}
	var indented bytes.Buffer
	if json.Indent(&indented, encoded, "", "  ") != nil {
		return encoded
	}
	return indented.Bytes()
}

// schemaWriteEntMarshal is json.Marshal without HTML escaping: Newtonsoft and System.Text.Json's relaxed
// encoder write '<', '>' and '&' as they are.
func schemaWriteEntMarshal(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buffer.Bytes(), "\n"), nil
}
