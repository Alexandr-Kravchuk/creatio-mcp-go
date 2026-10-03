package creatio

// Validation and normalization of clio's entity-schema write tools: the column type vocabulary, the
// localization maps (EntitySchemaDesignerSupport, EntitySchemaLocalizationContract), the default-value
// configuration and the caption culture. Every failure text is clio's.

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

const (
	schemaWriteEntManagerName       = "EntitySchemaManager"
	schemaWriteEntSysImageUID       = "93986bfe-2dbd-46bc-9bf9-d03dfefbf3b8"
	schemaWriteEntSysImageName      = "SysImage"
	schemaWriteEntRequirementNone   = 0
	schemaWriteEntRequirementApp    = 1
	schemaWriteEntTypeGUID          = 0
	schemaWriteEntTypeLookup        = 10
	schemaWriteEntTypeDateTime      = 7
	schemaWriteEntTypeImageLookup   = 16
	schemaWriteEntTypeSecureText    = 24
	schemaWriteEntDefaultParent     = "BaseEntity"
	schemaWriteEntBaseLookup        = "BaseLookup"
	schemaWriteEntReplacementName   = "A replacement schema must have the same name as its parent."
	schemaWriteEntTitleField        = "title-localizations"
	schemaWriteEntDescriptionField  = "description-localizations"
	schemaWriteEntMaskingPattern    = ".*"
	schemaWriteEntMaskingReplace    = "********"
	schemaWriteEntSequencePlaceHold = "{0}"
)

// schemaWriteEntTypes is clio's SupportedDataValueTypes (case-insensitive keys).
var schemaWriteEntTypes = map[string]int{
	"guid": 0, "text": 1, "text50": 27, "text250": 28, "textunlimited": 29, "text500": 30, "phonenumber": 42,
	"weblink": 44, "email": 45, "richtext": 43, "binary": 13, "image": 14, "imagelookup": 16, "file": 25,
	"securetext": 24, "integer": 4, "datetime": 7, "lookup": 10, "boolean": 12, "color": 18,
	"decimal0": 47, "decimal1": 31, "decimal2": 32, "decimal3": 33, "decimal4": 34, "decimal8": 40,
	"currency0": 48, "currency1": 49, "currency2": 6, "currency3": 50,
}

// schemaWriteEntTypeAliases is clio's SupportedDataValueTypeAliases.
var schemaWriteEntTypeAliases = map[string]string{
	"shorttext": "text50", "mediumtext": "text250", "longtext": "text500", "maxsizetext": "textunlimited",
	"emailaddress": "email", "imagelink": "imagelookup", "blob": "binary", "float": "decimal2", "decimal": "decimal2",
	"money": "currency2", "date": "datetime", "time": "datetime", "encrypted": "securetext", "securetext": "securetext",
	"password": "securetext", "float0": "decimal0", "float1": "decimal1", "float2": "decimal2", "float3": "decimal3",
	"float4": "decimal4", "float8": "decimal8", "money0": "currency0", "money1": "currency1", "money3": "currency3",
	"phonetext": "phonenumber", "webtext": "weblink", "emailtext": "email",
}

// schemaWriteEntTypeDisplayNames keeps clio's spelling of the type keys for GetSupportedTypesList.
var schemaWriteEntTypeDisplayNames = []string{
	"guid", "text", "text50", "text250", "textUnlimited", "text500", "phoneNumber", "webLink", "email", "richText",
	"binary", "image", "imageLookup", "file", "secureText", "integer", "datetime", "lookup", "boolean", "color",
	"decimal0", "decimal1", "decimal2", "decimal3", "decimal4", "decimal8", "currency0", "currency1", "currency2",
	"currency3", "shorttext", "mediumtext", "longtext", "maxsizetext", "emailaddress", "imagelink", "blob", "float",
	"decimal", "money", "date", "time", "encrypted", "securetext", "password", "float0", "float1", "float2", "float3",
	"float4", "float8", "money0", "money1", "money3", "phonetext", "webtext", "emailtext",
}

var schemaWriteEntTextTypes = map[int]bool{1: true, 27: true, 28: true, 30: true, 29: true, 42: true, 44: true, 45: true, 43: true}
var schemaWriteEntBinaryTypes = map[int]bool{13: true, 14: true, 25: true}

// schemaWriteEntRuntimeTypeUIDs is clio's RuntimeDataValueTypeUIdMap.
var schemaWriteEntRuntimeTypeUIDs = map[int]string{
	0: "23018567-a13c-4320-8687-fd6f9e3699bd", 1: "8b3f29bb-ea14-4ce5-a5c5-293a929b6ba2",
	4: "6b6b74e2-820d-490e-a017-2b73d4ccf2b0", 6: "969093e2-2b4e-463b-883a-3d3b8c61f0cd",
	7: "d21e9ef4-c064-4012-b286-fa1a8171da44", 10: "b295071f-7ea9-4e62-8d1a-919bf3732ff2",
	12: "90b65bf8-0ffc-4141-8779-2420877af907", 24: "3509b9dd-2c90-4540-b82e-8f6ae85d8248",
	27: "325a73b8-0f47-44a0-8412-7606f78003ac", 28: "ddb3a1ee-07e8-4d62-b7a9-d0e618b00fbd",
	29: "c0f04627-4620-4bc0-84e5-9419dc8516b1", 30: "5ca35f10-a101-4c67-a96a-383da6afacfc",
	31: "07ba84ce-0bf7-44b4-9f2c-7b15032eb98c", 32: "5cc8060d-6d10-4773-89fc-8c12d6f659a6",
	33: "3f62414e-6c25-4182-bcef-a73c9e396f31", 34: "ff22e049-4d16-46ee-a529-92d8808932dc",
	40: "a4aaf398-3531-4a0d-9d75-a587f5b5b59e", 42: "26cba63c-daf1-4f36-b2ea-73c0d675d90c",
	43: "79bccffa-8c8b-4863-b376-a69d2244182b", 44: "26cba64c-daf1-4f36-b2ea-73c0d695d90c",
	45: "66cba64c-daf1-4f36-b8ea-73c0d695d90c", 47: "57ee4c31-5ec4-45fa-b95d-3a2868aa89a8",
	48: "969093e2-2b4e-463b-883a-3d3b8c61f0cd", 49: "969093e2-2b4e-463b-883a-3d3b8c61f0cd",
	50: "969093e2-2b4e-463b-883a-3d3b8c61f0cd",
}

// schemaWriteEntSupportedTypesList is clio's GetSupportedTypesList: every key and alias, distinct
// case-insensitively (first spelling kept), sorted the way .NET's OrderBy(key) sorts with the invariant
// culture.
func schemaWriteEntSupportedTypesList() string {
	seen := map[string]bool{}
	var names []string
	for _, name := range schemaWriteEntTypeDisplayNames {
		if key := strings.ToLower(name); !seen[key] {
			seen[key] = true
			names = append(names, name)
		}
	}
	sort.SliceStable(names, func(i, j int) bool { return schemaWriteEntCultureLess(names[i], names[j]) })
	return strings.Join(names, ", ")
}

// schemaWriteEntCultureLess approximates .NET's culture-aware ordering of ASCII identifiers: letters
// compare case-insensitively first, a lower-case letter sorts before its upper-case twin.
func schemaWriteEntCultureLess(left, right string) bool {
	if l, r := strings.ToLower(left), strings.ToLower(right); l != r {
		return l < r
	}
	for i := 0; i < len(left) && i < len(right); i++ {
		if left[i] != right[i] {
			return unicode.IsLower(rune(left[i]))
		}
	}
	return len(left) < len(right)
}

// schemaWriteEntResolveType is clio's TryResolveDataValueType: non-alphanumerics stripped, case ignored,
// aliases mapped to their canonical type.
func schemaWriteEntResolveType(name string) (int, bool) {
	if strings.TrimSpace(name) == "" {
		return 0, false
	}
	var builder strings.Builder
	for _, r := range strings.TrimSpace(name) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			builder.WriteRune(r)
		}
	}
	key := strings.ToLower(builder.String())
	if alias, ok := schemaWriteEntTypeAliases[key]; ok {
		key = alias
	}
	value, ok := schemaWriteEntTypes[key]
	return value, ok
}

func schemaWriteEntIsLookupTypeName(name string) bool {
	value, ok := schemaWriteEntResolveType(name)
	return ok && value == schemaWriteEntTypeLookup
}

// schemaWriteEntTypesEquivalent is clio's AreColumnTypesEquivalent.
func schemaWriteEntTypesEquivalent(requested, existing string) bool {
	if strings.TrimSpace(requested) == "" || strings.TrimSpace(existing) == "" {
		return true
	}
	ordinal := func(name string) (int, bool) {
		if value, ok := schemaWriteEntResolveType(name); ok {
			return value, true
		}
		value, err := strconv.Atoi(strings.TrimSpace(name))
		return value, err == nil
	}
	left, leftOK := ordinal(requested)
	right, rightOK := ordinal(existing)
	if leftOK && rightOK {
		return left == right
	}
	return strings.EqualFold(strings.TrimSpace(requested), strings.TrimSpace(existing))
}

// schemaWriteEntFriendlyType is clio's GetFriendlyTypeName.
func schemaWriteEntFriendlyType(dataValueType *int) string {
	if dataValueType == nil {
		return "<none>"
	}
	return friendlyDataValueType(*dataValueType)
}

func schemaWriteEntIntPointer(value int) *int { return &value }

// ---------------------------------------------------------------------------------------------- localizations

// schemaWriteEntLocPair is one culture/value entry; schemaWriteEntLocMap keeps them in order with
// case-insensitive keys, as clio's Dictionary(StringComparer.OrdinalIgnoreCase) does.
type schemaWriteEntLocPair struct {
	Key   string
	Value string
}

type schemaWriteEntLocMap []schemaWriteEntLocPair

func (m schemaWriteEntLocMap) get(key string) (string, bool) {
	for _, pair := range m {
		if strings.EqualFold(pair.Key, key) {
			return pair.Value, true
		}
	}
	return "", false
}

// set updates the value under an existing key (keeping the key's first spelling) or appends.
func (m schemaWriteEntLocMap) set(key, value string) schemaWriteEntLocMap {
	for index := range m {
		if strings.EqualFold(m[index].Key, key) {
			m[index].Value = value
			return m
		}
	}
	return append(m, schemaWriteEntLocPair{Key: key, Value: value})
}

func (m schemaWriteEntLocMap) keys() []string {
	keys := make([]string, 0, len(m))
	for _, pair := range m {
		keys = append(keys, pair.Key)
	}
	return keys
}

func (m schemaWriteEntLocMap) values() []string {
	values := make([]string, 0, len(m))
	for _, pair := range m {
		values = append(values, pair.Value)
	}
	return values
}

// schemaWriteEntNormalizeMap is clio's NormalizeLocalizationMap.
func schemaWriteEntNormalizeMap(values schemaWriteEntLocMap, fieldName string, requireDefault bool) (schemaWriteEntLocMap, error) {
	if values == nil {
		return nil, nil
	}
	normalized := schemaWriteEntLocMap{}
	for _, pair := range values {
		culture := strings.TrimSpace(pair.Key)
		if culture == "" {
			return nil, fmt.Errorf("%s must not contain empty culture names.", fieldName)
		}
		if strings.TrimSpace(pair.Value) == "" {
			return nil, fmt.Errorf("%s must not contain empty values.", fieldName)
		}
		normalized = normalized.set(culture, strings.TrimSpace(pair.Value))
	}
	if len(normalized) == 0 {
		return nil, fmt.Errorf("%s must contain at least one localization.", fieldName)
	}
	if requireDefault {
		if _, ok := normalized.get(schemaWriteDefaultCulture); !ok {
			return nil, fmt.Errorf("%s must contain a non-empty '%s' value.", fieldName, schemaWriteDefaultCulture)
		}
	}
	return normalized, nil
}

// schemaWriteEntMapMatchesCulture is CaptionCultureScriptGuard.EnsureLocalizationMapMatchesCulture.
func schemaWriteEntMapMatchesCulture(values schemaWriteEntLocMap, fieldName string) error {
	for _, pair := range values {
		if err := schemaWriteCaptionMatchesCulture(pair.Key, pair.Value, fieldName); err != nil {
			return err
		}
	}
	return nil
}

// schemaWriteEntTitleNormalization is clio's NormalizeTitleLocalizations result.
type schemaWriteEntTitleNormalization struct {
	localizations  schemaWriteEntLocMap
	effectiveTitle string
}

// schemaWriteEntNormalizeTitles is clio's NormalizeTitleLocalizations.
func schemaWriteEntNormalizeTitles(values schemaWriteEntLocMap, fallback, fieldName, culture string) (schemaWriteEntTitleNormalization, error) {
	fallback = strings.TrimSpace(fallback)
	if values == nil {
		return schemaWriteEntTitleNormalization{effectiveTitle: fallback}, nil
	}
	normalized, err := schemaWriteEntNormalizeMap(values, fieldName, true)
	if err != nil {
		return schemaWriteEntTitleNormalization{}, err
	}
	if strings.TrimSpace(culture) == "" {
		culture = schemaWriteDefaultCulture
	}
	effective := ""
	if value, ok := normalized.get(culture); ok && strings.TrimSpace(value) != "" {
		effective = value
	} else if value, ok := normalized.get(schemaWriteDefaultCulture); ok && strings.TrimSpace(value) != "" {
		effective = value
	} else {
		for _, value := range normalized.values() {
			if strings.TrimSpace(value) != "" {
				effective = value
				break
			}
		}
	}
	if effective == "" {
		effective = fallback
	}
	return schemaWriteEntTitleNormalization{localizations: normalized, effectiveTitle: effective}, nil
}

// schemaWriteEntLocalizableStrings is clio's CreateLocalizableStrings: a map becomes a list with en-US first
// and the rest ordered by culture; a lone scalar is anchored to the culture.
func schemaWriteEntLocalizableStrings(values schemaWriteEntLocMap, fallback, culture string) ([]schemaWriteEntLocalizable, error) {
	if values != nil {
		normalized, err := schemaWriteEntNormalizeMap(values, "localizations", true)
		if err != nil {
			return nil, err
		}
		return schemaWriteEntBuildLocalizable(normalized), nil
	}
	if strings.TrimSpace(fallback) == "" {
		return []schemaWriteEntLocalizable{}, nil
	}
	if strings.TrimSpace(culture) == "" {
		culture = schemaWriteDefaultCulture
	}
	return []schemaWriteEntLocalizable{schemaWriteEntLocalized(culture, fallback)}, nil
}

func schemaWriteEntBuildLocalizable(values schemaWriteEntLocMap) []schemaWriteEntLocalizable {
	ordered := append(schemaWriteEntLocMap{}, values...)
	sort.SliceStable(ordered, func(i, j int) bool {
		leftDefault := strings.EqualFold(ordered[i].Key, schemaWriteDefaultCulture)
		rightDefault := strings.EqualFold(ordered[j].Key, schemaWriteDefaultCulture)
		if leftDefault != rightDefault {
			return leftDefault
		}
		return strings.ToUpper(ordered[i].Key) < strings.ToUpper(ordered[j].Key)
	})
	list := make([]schemaWriteEntLocalizable, 0, len(ordered))
	for _, pair := range ordered {
		list = append(list, schemaWriteEntLocalized(pair.Key, pair.Value))
	}
	return list
}

// schemaWriteEntSetLocalizable is clio's SetLocalizableValue: overwrite the culture's entry or append one.
func schemaWriteEntSetLocalizable(values []schemaWriteEntLocalizable, value, culture string) []schemaWriteEntLocalizable {
	if strings.TrimSpace(value) == "" {
		return values
	}
	if strings.TrimSpace(culture) == "" {
		culture = schemaWriteDefaultCulture
	}
	for index := range values {
		if strings.EqualFold(values[index].culture(), culture) {
			text := value
			values[index].Value = &text
			return values
		}
	}
	return append(values, schemaWriteEntLocalized(culture, value))
}

// schemaWriteEntLocalizableValue is clio's GetLocalizableValue: the culture, else en-US, else the first.
func schemaWriteEntLocalizableValue(values []schemaWriteEntLocalizable, culture string) *string {
	if len(values) == 0 {
		return nil
	}
	if strings.TrimSpace(culture) == "" {
		culture = schemaWriteDefaultCulture
	}
	for _, value := range values {
		if strings.EqualFold(value.culture(), culture) {
			return value.Value
		}
	}
	for _, value := range values {
		if strings.EqualFold(value.culture(), schemaWriteDefaultCulture) {
			return value.Value
		}
	}
	return values[0].Value
}

// schemaWriteEntRequiredLocalization is clio's GetRequiredLocalizationValue.
func schemaWriteEntRequiredLocalization(values schemaWriteEntLocMap, fieldName, culture string) (string, error) {
	normalized, err := schemaWriteEntNormalizeMap(values, fieldName, true)
	if err != nil {
		return "", err
	}
	value, ok := normalized.get(culture)
	if !ok || strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("%s must contain a non-empty '%s' value.", fieldName, culture)
	}
	return value, nil
}

// schemaWriteEntCanonicalCulture is CultureInfo.GetCultureInfo(name, predefinedOnly: true).Name, approximated:
// the shape check of canonicalCultureName plus a known language subtag. .NET also rejects a well-formed tag
// whose region or script it has no data for; this does not.
func schemaWriteEntCanonicalCulture(name string) (string, bool) {
	canonical, ok := canonicalCultureName(strings.TrimSpace(name))
	if !ok {
		return "", false
	}
	language := strings.SplitN(canonical, "-", 2)[0]
	if !schemaWriteEntLanguages[language] {
		return "", false
	}
	return canonical, true
}

// schemaWriteEntLanguages lists the ISO 639 language subtags .NET (ICU) ships culture data for.
var schemaWriteEntLanguages = func() map[string]bool {
	set := map[string]bool{}
	for _, code := range strings.Fields("af am ar as az ba be bg bn bo br bs ca co cs cy da de dv dz el en es et eu fa ff fi fo fr fy ga gd gl gn gu ha he hi hr hu hy id ig ii is it iu ja jv ka kk kl km kn ko ks ku ky lb lg ln lo lt lu lv mg mi mk ml mn mr ms mt my nb nd ne nl nn no nr nso oc om or os pa pl ps pt qu rm rn ro ru rw sa sd se sg si sk sl sm sn so sq sr ss st sv sw ta te tg th ti tk tn to tr ts tt ug uk ur uz ve vi wo xh yi yo zh zu " +
		"ast bas bem brx ccp ce cgg chr ckb dav dje dsb dua dyo ebu ewo fil fur gsw guz haw hsb jgo jmc kab kam kde kea khq ki kkj kln kok ksb ksf ksh kw lag lkt lrc luo luy mas mer mfe mgh mgo mua mzn naq nds nmg nnh nus nyn pcm prg rof rwk sah saq sbp seh ses shi smn teo twq tzm vai vun wae xog yav yue zgh") {
		set[code] = true
	}
	return set
}()

// schemaWriteEntNormalizeSchemaCaptions is clio's NormalizeSchemaCaptionLocalizations: en-US not required,
// every culture known and canonicalized, two spellings of one culture with different captions refused, and
// the script guard applied.
func schemaWriteEntNormalizeSchemaCaptions(values schemaWriteEntLocMap, fieldName string) (schemaWriteEntLocMap, error) {
	normalized, err := schemaWriteEntNormalizeMap(values, fieldName, false)
	if err != nil || normalized == nil {
		return nil, err
	}
	canonicalized := schemaWriteEntLocMap{}
	for _, pair := range normalized {
		culture, ok := schemaWriteEntCanonicalCulture(pair.Key)
		if !ok {
			return nil, fmt.Errorf("%s contains an unknown culture name '%s'.", fieldName, pair.Key)
		}
		if existing, found := canonicalized.get(culture); found && existing != pair.Value {
			return nil, fmt.Errorf("%s contains two different captions for culture '%s'.", fieldName, culture)
		}
		canonicalized = canonicalized.set(culture, pair.Value)
	}
	if err := schemaWriteEntMapMatchesCulture(canonicalized, fieldName); err != nil {
		return nil, err
	}
	return canonicalized, nil
}

// ---------------------------------------------------------------------------------------------- MCP contract

// schemaWriteEntRequireTitles is EntitySchemaLocalizationContract.RequireTitleLocalizations: a map without
// en-US gets one derived from the legacy title, the legacy caption or the humanized column name.
func schemaWriteEntRequireTitles(titles schemaWriteEntLocMap, legacyTitle, legacyCaption, columnName, context string) (schemaWriteEntLocMap, error) {
	normalized, err := schemaWriteEntContractNormalize(titles, schemaWriteEntTitleField, context, false)
	if err != nil {
		return nil, err
	}
	if normalized != nil {
		if value, ok := normalized.get(schemaWriteDefaultCulture); ok && strings.TrimSpace(value) != "" {
			return normalized, nil
		}
	}
	derived := ""
	switch {
	case strings.TrimSpace(legacyTitle) != "":
		derived = strings.TrimSpace(legacyTitle)
	case strings.TrimSpace(legacyCaption) != "":
		derived = strings.TrimSpace(legacyCaption)
	default:
		derived = schemaWriteEntHumanize(columnName)
	}
	if strings.TrimSpace(derived) == "" {
		return nil, fmt.Errorf("%s requires '%s' with a non-empty '%s' value.", context, schemaWriteEntTitleField, schemaWriteDefaultCulture)
	}
	merged := schemaWriteEntLocMap{}
	for _, pair := range normalized {
		merged = merged.set(pair.Key, pair.Value)
	}
	merged = merged.set(schemaWriteDefaultCulture, derived)
	return schemaWriteEntContractNormalize(merged, schemaWriteEntTitleField, context, true)
}

// schemaWriteEntHumanize is clio's HumanizeColumnName: drop a leading "Usr", space before each upper-case
// letter that follows a non-upper-case one.
func schemaWriteEntHumanize(columnName string) string {
	trimmed := strings.TrimSpace(columnName)
	if trimmed == "" {
		return ""
	}
	withoutPrefix := trimmed
	if strings.HasPrefix(trimmed, "Usr") && len(trimmed) > 3 {
		withoutPrefix = trimmed[3:]
	}
	runes := []rune(withoutPrefix)
	var builder strings.Builder
	for index, r := range runes {
		if index > 0 && unicode.IsUpper(r) && !unicode.IsUpper(runes[index-1]) {
			builder.WriteRune(' ')
		}
		builder.WriteRune(r)
	}
	humanized := strings.TrimSpace(builder.String())
	if humanized == "" {
		return trimmed
	}
	return humanized
}

// schemaWriteEntContractNormalize is EntitySchemaLocalizationContract.NormalizeLocalizations: the map check
// and the script guard, failures prefixed with the context.
func schemaWriteEntContractNormalize(values schemaWriteEntLocMap, fieldName, context string, requireDefault bool) (schemaWriteEntLocMap, error) {
	if values == nil {
		return nil, nil
	}
	normalized, err := schemaWriteEntNormalizeMap(values, fieldName, requireDefault)
	if err == nil {
		err = schemaWriteEntMapMatchesCulture(normalized, fieldName)
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %s", context, err.Error())
	}
	return normalized, nil
}

func schemaWriteEntRejectLegacy(value, legacyField, replacement, context string) error {
	if strings.TrimSpace(value) != "" {
		return fmt.Errorf("%s does not accept legacy '%s'. Use '%s' instead.", context, legacyField, replacement)
	}
	return nil
}

// schemaWriteEntMutationTitles is NormalizeMutationTitleLocalizations.
func schemaWriteEntMutationTitles(action string, titles schemaWriteEntLocMap, legacyTitle, legacyCaption, columnName, context string) (schemaWriteEntLocMap, error) {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "remove":
		if len(titles) > 0 {
			return nil, fmt.Errorf("%s does not accept '%s' when action is 'remove'.", context, schemaWriteEntTitleField)
		}
		if err := schemaWriteEntRejectLegacy(legacyTitle, "title", schemaWriteEntTitleField, context); err != nil {
			return nil, err
		}
		return nil, schemaWriteEntRejectLegacy(legacyCaption, "caption", schemaWriteEntTitleField, context)
	case "add":
		return schemaWriteEntRequireTitles(titles, legacyTitle, legacyCaption, columnName, context)
	default:
		if err := schemaWriteEntRejectLegacy(legacyTitle, "title", schemaWriteEntTitleField, context); err != nil {
			return nil, err
		}
		if err := schemaWriteEntRejectLegacy(legacyCaption, "caption", schemaWriteEntTitleField, context); err != nil {
			return nil, err
		}
		return schemaWriteEntContractNormalize(titles, schemaWriteEntTitleField, context, true)
	}
}

// schemaWriteEntMutationDescriptions is NormalizeMutationDescriptionLocalizations.
func schemaWriteEntMutationDescriptions(action string, descriptions schemaWriteEntLocMap, legacyDescription, context string) (schemaWriteEntLocMap, error) {
	if strings.EqualFold(strings.TrimSpace(action), "remove") {
		if len(descriptions) > 0 {
			return nil, fmt.Errorf("%s does not accept '%s' when action is 'remove'.", context, schemaWriteEntDescriptionField)
		}
		return nil, schemaWriteEntRejectLegacy(legacyDescription, "description", schemaWriteEntDescriptionField, context)
	}
	if err := schemaWriteEntRejectLegacy(legacyDescription, "description", schemaWriteEntDescriptionField, context); err != nil {
		return nil, err
	}
	return schemaWriteEntContractNormalize(descriptions, schemaWriteEntDescriptionField, context, true)
}

// schemaWriteEntDefaultTitle is EntitySchemaLocalizationContract.GetDefaultTitle.
func schemaWriteEntDefaultTitle(titles schemaWriteEntLocMap, context string) (string, error) {
	value, err := schemaWriteEntRequiredLocalization(titles, schemaWriteEntTitleField, schemaWriteDefaultCulture)
	if err != nil {
		return "", fmt.Errorf("%s: %s", context, err.Error())
	}
	return value, nil
}

// schemaWriteEntColumnIdentity is ColumnIdentityContract.RequireColumnIdentity.
func schemaWriteEntColumnIdentity(name, context, parameter string) (string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", fmt.Errorf("%s is missing the target column. Send it as 'column-name' (or its alias 'name'). (Parameter '%s')", context, parameter)
	}
	return trimmed, nil
}

// ---------------------------------------------------------------------------------------------- default values

// schemaWriteEntDefaultConfig is clio's EntitySchemaDefaultValueConfig as a write tool reads it.
type schemaWriteEntDefaultConfig struct {
	Source                *string         `json:"source"`
	Value                 json.RawMessage `json:"value"`
	ValueSource           *string         `json:"value-source"`
	ResolvedValueSource   *string         `json:"resolved-value-source"`
	SequencePrefix        *string         `json:"sequence-prefix"`
	SequenceNumberOfChars *int            `json:"sequence-number-of-chars"`
	DisplayValue          *string         `json:"display-value"`
	RecordResolution      *string         `json:"record-resolution"`
	SourceResolution      *string         `json:"source-resolution"`
}

func (c *schemaWriteEntDefaultConfig) source() string {
	if c == nil || c.Source == nil {
		return ""
	}
	return *c.Source
}

// schemaWriteEntDefaultSource is clio's ParseDefaultValueSource; -1 means "not given".
func schemaWriteEntDefaultSource(source string) (int, error) {
	if strings.TrimSpace(source) == "" {
		return -1, nil
	}
	switch strings.ToLower(strings.TrimSpace(source)) {
	case "none":
		return defaultSourceNone, nil
	case "const":
		return defaultSourceConst, nil
	case "settings":
		return defaultSourceSettings, nil
	case "systemvalue":
		return defaultSourceSystemValue, nil
	case "sequence":
		return defaultSourceSequence, nil
	}
	return -1, fmt.Errorf("Unsupported default-value-source '%s'. Supported values: None, Const, Settings, SystemValue, Sequence.", source)
}

func schemaWriteEntTextValue(value *string) *string {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func schemaWriteEntPreservePrefix(value *string) *string {
	if value == nil || *value == "" {
		return nil
	}
	copy := *value
	return &copy
}

// schemaWriteEntScalar is clio's NormalizeScalarDefaultValue: null stays null, an object or array fails.
func schemaWriteEntScalar(value json.RawMessage, context string) (json.RawMessage, error) {
	value = schemaWriteEntNullable(value)
	if value == nil {
		return nil, nil
	}
	if !isJSONScalar(value) {
		return nil, fmt.Errorf("%s must be a scalar JSON value.", context)
	}
	return value, nil
}

func schemaWriteEntStringConfig(source string) *schemaWriteEntDefaultConfig {
	return &schemaWriteEntDefaultConfig{Source: &source}
}

// schemaWriteEntResolveDefaultConfig is clio's ResolveDefaultValueConfig: the structured config, or the
// legacy default-value-source/default-value shorthand turned into one.
func schemaWriteEntResolveDefaultConfig(config *schemaWriteEntDefaultConfig, legacySource string, legacyValue *string, context string) (*schemaWriteEntDefaultConfig, error) {
	if config != nil {
		if strings.TrimSpace(legacySource) != "" || legacyValue != nil {
			return nil, fmt.Errorf("%s cannot mix legacy default-value/default-value-source with default-value-config.", context)
		}
		return schemaWriteEntNormalizeDefaultConfig(config, context)
	}
	if strings.TrimSpace(legacySource) == "" && legacyValue == nil {
		return nil, nil
	}
	source := -1
	if strings.TrimSpace(legacySource) != "" {
		parsed, err := schemaWriteEntDefaultSource(legacySource)
		if err != nil {
			return nil, err
		}
		if parsed != defaultSourceConst && parsed != defaultSourceNone {
			return nil, fmt.Errorf("Legacy default-value-source supports only Const or None. Use default-value-config for '%s'.", defaultValueSourceNames[parsed])
		}
		source = parsed
	}
	if source == defaultSourceNone {
		if legacyValue != nil {
			return nil, fmt.Errorf("%s cannot specify default-value when default-value-source is None.", context)
		}
		return schemaWriteEntStringConfig("None"), nil
	}
	if legacyValue == nil {
		return nil, fmt.Errorf("%s requires default-value when legacy default-value-source is Const.", context)
	}
	encoded, _ := schemaWriteEntMarshal(*legacyValue)
	config = schemaWriteEntStringConfig("Const")
	config.Value = encoded
	return config, nil
}

func schemaWriteEntNormalizeDefaultConfig(config *schemaWriteEntDefaultConfig, context string) (*schemaWriteEntDefaultConfig, error) {
	source, err := schemaWriteEntDefaultSource(config.source())
	if err != nil {
		return nil, err
	}
	if source < 0 {
		return nil, fmt.Errorf("%s requires default-value-config.source.", context)
	}
	name := defaultValueSourceNames[source]
	switch source {
	case defaultSourceConst:
		value, err := schemaWriteEntScalar(config.Value, context+" default-value-config.value")
		if err != nil {
			return nil, err
		}
		result := schemaWriteEntStringConfig(name)
		result.Value = value
		return result, nil
	case defaultSourceSettings, defaultSourceSystemValue:
		result := schemaWriteEntStringConfig(name)
		result.ValueSource = schemaWriteEntTextValue(config.ValueSource)
		return result, nil
	case defaultSourceSequence:
		if _, err := schemaWriteEntSequencePrefix(config, context); err != nil {
			return nil, err
		}
		value, err := schemaWriteEntScalar(config.Value, context+" default-value-config.value")
		if err != nil {
			return nil, err
		}
		result := schemaWriteEntStringConfig(name)
		result.Value = value
		result.SequencePrefix = schemaWriteEntPreservePrefix(config.SequencePrefix)
		result.SequenceNumberOfChars = config.SequenceNumberOfChars
		return result, nil
	default:
		return schemaWriteEntStringConfig(name), nil
	}
}

// schemaWriteEntSequencePrefix is clio's ResolveSequencePrefix: an explicit prefix or a "<prefix>{0}" mask.
func schemaWriteEntSequencePrefix(config *schemaWriteEntDefaultConfig, context string) (*string, error) {
	if schemaWriteEntTextValue(config.ValueSource) != nil {
		return nil, fmt.Errorf("%s cannot set default-value-config.value-source when source is Sequence.", context)
	}
	explicit := schemaWriteEntPreservePrefix(config.SequencePrefix)
	mask, err := schemaWriteEntScalar(config.Value, context+" default-value-config.value")
	if err != nil {
		return nil, err
	}
	if mask == nil {
		return explicit, nil
	}
	if explicit != nil {
		return nil, fmt.Errorf("%s cannot combine default-value-config.value and sequence-prefix when source is Sequence. Set the static prefix in one of them.", context)
	}
	var text string
	if json.Unmarshal(mask, &text) != nil {
		return nil, fmt.Errorf("%s default-value-config.value for source Sequence must be a text mask that ends with '{0}' (for example 'LN-{0}'), or use sequence-prefix for the static prefix.", context)
	}
	index := strings.Index(text, schemaWriteEntSequencePlaceHold)
	if index < 0 {
		return nil, fmt.Errorf("%s default-value-config.value '%s' for source Sequence must contain the sequence placeholder '{0}' (for example 'LN-{0}'), or use sequence-prefix for the static prefix.", context, text)
	}
	if index != len(text)-len(schemaWriteEntSequencePlaceHold) {
		return nil, fmt.Errorf("%s default-value-config.value mask '%s' is not supported: sequence defaults apply only a static prefix before a single trailing '{0}' (for example 'LN-{0}' produces LN-00001). Static text after the number cannot be applied.", context, text)
	}
	if index == 0 {
		return nil, nil
	}
	prefix := text[:index]
	return &prefix, nil
}

// schemaWriteEntDefaultDTO is clio's CreateDefaultValueDto.
func schemaWriteEntDefaultDTO(config *schemaWriteEntDefaultConfig, context string) (*schemaWriteEntDefValue, error) {
	source, err := schemaWriteEntDefaultSource(config.source())
	if err != nil {
		return nil, err
	}
	if source < 0 {
		return nil, fmt.Errorf("%s requires default-value-config.source.", context)
	}
	switch source {
	case defaultSourceConst:
		value, err := schemaWriteEntScalar(config.Value, context+" default-value-config.value")
		if err != nil {
			return nil, err
		}
		if value == nil {
			return nil, fmt.Errorf("%s requires default-value-config.value when source is Const.", context)
		}
		return &schemaWriteEntDefValue{ValueSourceType: source, Value: value}, nil
	case defaultSourceSettings, defaultSourceSystemValue:
		selector := schemaWriteEntTextValue(config.ValueSource)
		if selector == nil {
			return nil, fmt.Errorf("%s requires default-value-config.value-source when source is %s.", context, defaultValueSourceNames[source])
		}
		return &schemaWriteEntDefValue{ValueSourceType: source, ValueSource: selector}, nil
	case defaultSourceSequence:
		prefix, err := schemaWriteEntSequencePrefix(config, context)
		if err != nil {
			return nil, err
		}
		if config.SequenceNumberOfChars == nil || *config.SequenceNumberOfChars <= 0 {
			return nil, fmt.Errorf("%s requires default-value-config.sequence-number-of-chars when source is Sequence.", context)
		}
		return &schemaWriteEntDefValue{ValueSourceType: source, SequencePrefix: prefix, SequenceNumberOfChars: *config.SequenceNumberOfChars}, nil
	default:
		return nil, fmt.Errorf("%s must not create a default-value DTO when source is None.", context)
	}
}

// schemaWriteEntValidateDefaultConfig is clio's ValidateDefaultValueConfig.
func schemaWriteEntValidateDefaultConfig(config *schemaWriteEntDefaultConfig, dataValueType int, context string) error {
	if config == nil {
		return nil
	}
	source, err := schemaWriteEntDefaultSource(config.source())
	if err != nil {
		return err
	}
	if source < 0 {
		return fmt.Errorf("%s requires default-value-config.source.", context)
	}
	typeName := schemaWriteEntFriendlyType(&dataValueType)
	if source == defaultSourceConst && schemaWriteEntBinaryTypes[dataValueType] {
		return fmt.Errorf("%s type '%s' does not support default-value-config source Const.", context, typeName)
	}
	if source == defaultSourceNone {
		if schemaWriteEntNullable(config.Value) != nil || schemaWriteEntTextValue(config.ValueSource) != nil ||
			schemaWriteEntTextValue(config.SequencePrefix) != nil || config.SequenceNumberOfChars != nil {
			return fmt.Errorf("%s cannot set value, value-source, or sequence fields when default-value-config source is None.", context)
		}
		return nil
	}
	if source == defaultSourceSequence && !schemaWriteEntTextTypes[dataValueType] {
		return fmt.Errorf("%s type '%s' supports default-value-config source Sequence only for text columns.", context, typeName)
	}
	_, err = schemaWriteEntDefaultDTO(config, context)
	return err
}

// schemaWriteEntLegacyBinaryDefault is clio's UsesUnsupportedLegacyBinaryDefaultValue.
func schemaWriteEntLegacyBinaryDefault(config *schemaWriteEntDefaultConfig, legacySource string, legacyValue *string, dataValueType int) (bool, error) {
	if config != nil || !schemaWriteEntBinaryTypes[dataValueType] {
		return false, nil
	}
	source, err := schemaWriteEntDefaultSource(legacySource)
	if err != nil {
		return false, err
	}
	return legacyValue != nil || source == defaultSourceConst, nil
}

// schemaWriteEntUsageType is clio's TryParseUsageType.
func schemaWriteEntUsageType(name string) (int, bool) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "general":
		return 0, true
	case "advanced":
		return 1, true
	case "none":
		return 2, true
	}
	return 0, false
}

// schemaWriteEntError is a failure clio raises with a fixed text (EntitySchemaDesignerException,
// InvalidOperationException, ArgumentException): the text is the whole message.
func schemaWriteEntError(message string) error { return errors.New(message) }
