package mobileguide

import "strings"

// The pure halves of the related-page add-on readers: RelatedPageAddonMetadata, and
// MobileActionTargetProbe.ClassifyRelatedPageMetadata / ExtractDefaultPageSchemaUId / CollectSourceEntityNames.

// Related-page metadata outcomes (RelatedPageAddonMetadata.Status).
const (
	relatedUnparseable = iota
	relatedNoDefault
	relatedDefaultFound
)

// tryFindUntypedDefault is RelatedPageAddonMetadata.TryFindUntypedDefault: the first page marked IsDefault with
// no TypeColumnValue.
func tryFindUntypedDefault(metaData string) (int, *string) {
	if isBlank(metaData) {
		return relatedUnparseable, nil
	}
	root, err := parseSTJ([]byte(metaData))
	if err != nil || !root.isObj() {
		return relatedUnparseable, nil
	}
	pages := root.get("Pages")
	if !pages.isArr() {
		return relatedNoDefault, nil
	}
	for _, page := range pages.arr {
		if page.isObj() && relatedBool(page, "IsDefault") {
			if typeValue := relatedStr(page, "TypeColumnValue"); typeValue == nil || isBlank(*typeValue) {
				return relatedDefaultFound, relatedStr(page, "PageSchemaUId")
			}
		}
	}
	return relatedNoDefault, nil
}

// relatedStr is RelatedPageAddonMetadata.Str: a string value, else the value's JSON with quotes trimmed.
func relatedStr(obj *jn, property string) *string {
	if obj == nil || property == "" {
		return nil
	}
	node, ok := obj.props[property]
	if !ok || node == nil || node.isObj() || node.isArr() {
		return nil
	}
	if node.k == jsString {
		return strp(node.s)
	}
	return strp(strings.Trim(node.toJSON(styleSTJ), `"`))
}

func relatedBool(obj *jn, property string) bool {
	node, ok := obj.props[property]
	if !ok || node == nil || node.isObj() || node.isArr() {
		return false
	}
	if value, ok := node.tryBool(); ok {
		return value
	}
	if node.k == jsString {
		return eqi(dotnetTrim(node.s), "true")
	}
	return false
}

// classifyRelatedPageMetadata is MobileActionTargetProbe.ClassifyRelatedPageMetadata.
func classifyRelatedPageMetadata(metaData string) int {
	switch status, _ := tryFindUntypedDefault(metaData); status {
	case relatedDefaultFound:
		return TargetResolved
	case relatedNoDefault:
		return TargetMissing
	}
	return TargetUnknown
}

// extractDefaultPageSchemaUID is MobileActionTargetProbe.ExtractDefaultPageSchemaUId.
func extractDefaultPageSchemaUID(metaData string) *string {
	if status, uid := tryFindUntypedDefault(metaData); status == relatedDefaultFound {
		return uid
	}
	return nil
}

// collectSourceEntityNames is MobileActionTargetProbe.CollectSourceEntityNames: the primary data source's entity.
func collectSourceEntityNames(modelConfig *jn) *strSet {
	names := newSet(true)
	dataSources := modelConfig.get("dataSources")
	if !dataSources.isObj() {
		return names
	}
	var primary *jn
	if primaryName := relatedStr(modelConfig, "primaryDataSourceName"); primaryName != nil && !isBlank(*primaryName) {
		if candidate := dataSources.get(*primaryName); candidate.isObj() {
			primary = candidate
		}
	}
	if primary == nil {
		if candidate := dataSources.get("PDS"); candidate.isObj() {
			primary = candidate
		}
	}
	if config := primary.get("config"); config.isObj() {
		if entity := relatedStr(config, "entitySchemaName"); entity != nil && !isBlank(*entity) {
			names.add(dotnetTrim(*entity))
		}
	}
	return names
}
