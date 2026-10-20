package creatio

// clio 8.1.0.142's ODataReadColumnValidation: an invalid-query rejection of either read tool is explained
// from the entity's OData metadata ($metadata, one 10 s attempt): each select / expand / order-by / filter
// name the entity does not have is listed with up to three closest valid names, and the entity's column count
// is added. Unavailable metadata leaves the rejection as it was.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	odataExplainMaxSuggestions = 3
	odataExplainMaxReported    = 20
	odataExplainMinContainment = 4
	odataExplainMinPrefix      = 3
)

// odataNameSet is a case-insensitive set that hands back the stored spelling (HashSet.TryGetValue under
// StringComparer.OrdinalIgnoreCase).
type odataNameSet map[string]string

func (s odataNameSet) add(name string) {
	if _, ok := s[strings.ToLower(name)]; !ok {
		s[strings.ToLower(name)] = name
	}
}

func (s odataNameSet) lookup(name string) (string, bool) {
	stored, ok := s[strings.ToLower(name)]
	return stored, ok
}

func (s odataNameSet) has(name string) bool { _, ok := s.lookup(name); return ok }

type odataTypeShape struct {
	properties  odataNameSet
	navigations odataNameSet
}

type odataEntityShape struct {
	root    odataTypeShape
	targets map[string]odataTypeShape // keyed by lower-cased navigation name
}

func (s odataEntityShape) target(navigation string) (odataTypeShape, bool) {
	shape, ok := s.targets[strings.ToLower(navigation)]
	return shape, ok
}

func odataShapeOf(typ *odataCSDLType) odataTypeShape {
	shape := odataTypeShape{properties: odataNameSet{}, navigations: odataNameSet{}}
	for _, name := range odataSortedKeys(typ.properties) {
		shape.properties.add(name)
	}
	for _, name := range odataSortedStringKeys(typ.navigations) {
		shape.navigations.add(name)
	}
	return shape
}

func odataSortedKeys(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func odataSortedStringKeys(set map[string]string) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// odataEntityShapeOf is ODataFieldValidation.TryGetEntityShape: nil unless the CSDL resolves the entity.
func (c *Client) odataEntityShapeOf(ctx context.Context, entity string) *odataEntityShape {
	body := c.dataWriteGet(ctx, "odata/$metadata", 10*time.Second)
	if !strings.HasPrefix(strings.TrimLeft(body, " \t\r\n\v\f\u0085\u00a0"), "<") {
		return nil
	}
	types, order, err := odataParseCSDL(body)
	if err != nil {
		return nil
	}
	var target *odataCSDLType
	for _, name := range order {
		if strings.EqualFold(name, dataWriteTrim(entity)) {
			target = types[name]
			break
		}
	}
	if target == nil || !odataCollectInherited(target, types) {
		return nil
	}
	shape := &odataEntityShape{root: odataShapeOf(target), targets: map[string]odataTypeShape{}}
	for _, navigation := range odataSortedStringKeys(target.navigations) {
		if typ, ok := types[target.navigations[navigation]]; ok && odataCollectInherited(typ, types) {
			shape.targets[strings.ToLower(navigation)] = odataShapeOf(typ)
		}
	}
	return shape
}

type odataReference struct {
	argument       string
	path           string
	navigationOnly bool
}

// odataExplainColumns is ODataReadColumnValidation.Explain.
func (c *Client) odataExplainColumns(ctx context.Context, response ODataReadResponse, args ODataReadArgs, selectColumns, expandColumns []string) ODataReadResponse {
	if response.Success || response.ErrorCode == nil || *response.ErrorCode != odataCodeInvalidQuery {
		return response
	}
	references := odataCollectReferences(args, selectColumns, expandColumns)
	if len(references) == 0 {
		return response
	}
	entity := dataWriteTrim(args.Entity)
	shape := c.odataEntityShapeOf(ctx, entity)
	if shape == nil {
		return response
	}
	unknown := odataFindUnknown(*shape, references)
	plainID := odataPlainIDColumns(*shape, references)
	if len(unknown) == 0 && len(plainID) == 0 {
		return response
	}
	text, members, columns := odataBuildExplanation(*shape, unknown, plainID, entity)
	explained := response
	message := *response.Error + " " + text
	explained.Error = &message
	if len(members) > 0 {
		explained.UnknownMembers = members
		explained.ValidColumnCount = &columns
	}
	return explained
}

func odataCollectReferences(args ODataReadArgs, selectColumns, expandColumns []string) []odataReference {
	var references []odataReference
	for _, name := range selectColumns {
		references = append(references, odataReference{"select", name, false})
	}
	for _, name := range expandColumns {
		references = append(references, odataReference{"expand", name, true})
	}
	if !dataWriteBlank(args.OrderBy) {
		for _, clause := range strings.Split(args.OrderBy, ",") {
			if parts := odataSplitTrimmed(clause, " "); len(parts) > 0 {
				references = append(references, odataReference{"order-by", parts[0], false})
			}
		}
	}
	if args.Filters != nil {
		for _, group := range [][]*ODataFilterCondition{args.Filters.All, args.Filters.Any} {
			for _, condition := range group {
				if condition != nil && !dataWriteBlank(condition.Field) {
					references = append(references, odataReference{"filters", dataWriteTrim(condition.Field), false})
				}
			}
		}
	}
	return references
}

func odataPlainIDColumns(shape odataEntityShape, references []odataReference) []string {
	seen := map[string]bool{}
	var names []string
	for _, reference := range references {
		if reference.argument == "filters" && !strings.Contains(reference.path, "/") && len(reference.path) > 2 &&
			odataIsIdish(reference.path) && shape.root.properties.has(reference.path) &&
			!odataTypeNeedsNavigationPath(shape.root, reference.path) && !seen[strings.ToLower(reference.path)] {
			seen[strings.ToLower(reference.path)] = true
			names = append(names, reference.path)
		}
	}
	return names
}

func odataFindUnknown(shape odataEntityShape, references []odataReference) []odataReference {
	seen := map[string]bool{}
	var unknown []odataReference
	for _, reference := range references {
		if odataExistsOnType(shape, reference) && !odataNeedsNavigationPath(shape, reference) {
			continue
		}
		key := reference.argument + "\x00" + reference.path
		if !seen[key] {
			seen[key] = true
			unknown = append(unknown, reference)
		}
	}
	return unknown
}

func odataExistsOnType(shape odataEntityShape, reference odataReference) bool {
	segments := strings.Split(reference.path, "/")
	first := segments[0]
	if len(segments) == 1 {
		return shape.root.navigations.has(first) || (!reference.navigationOnly && shape.root.properties.has(first))
	}
	if !shape.root.navigations.has(first) {
		return false
	}
	target, ok := shape.target(first)
	if !ok {
		return true
	}
	return target.navigations.has(segments[1]) || (!reference.navigationOnly && target.properties.has(segments[1]))
}

func odataIsPathOnly(reference odataReference) bool {
	return reference.argument == "filters" || reference.argument == "order-by"
}

func odataNeedsNavigationPath(shape odataEntityShape, reference odataReference) bool {
	if !odataIsPathOnly(reference) {
		return false
	}
	segments := strings.Split(reference.path, "/")
	if len(segments) == 1 {
		return odataTypeNeedsNavigationPath(shape.root, segments[0])
	}
	if len(segments) != 2 {
		return false
	}
	target, ok := shape.target(segments[0])
	return ok && odataTypeNeedsNavigationPath(target, segments[1])
}

func odataTypeNeedsNavigationPath(typ odataTypeShape, name string) bool {
	return !strings.EqualFold(odataPathOnlyForm(typ, name), name)
}

func odataPathOnlyForm(typ odataTypeShape, name string) string {
	if len(name) > 2 && strings.HasSuffix(strings.ToLower(name), "id") && typ.properties.has(name) {
		if lookup, ok := typ.navigations.lookup(name[:len(name)-2]); ok {
			return lookup + "/Id"
		}
	}
	if !typ.properties.has(name) {
		if navigation, ok := typ.navigations.lookup(name); ok {
			return navigation + "/Id"
		}
	}
	return name
}

func odataBuildExplanation(shape odataEntityShape, unknown []odataReference, plainID []string, entity string) (string, []ODataUnknownMember, int) {
	reported := unknown
	if len(reported) > odataExplainMaxReported {
		reported = reported[:odataExplainMaxReported]
	}
	members := make([]ODataUnknownMember, 0, len(reported))
	details := make([]string, 0, len(reported))
	for _, reference := range reported {
		member := ODataUnknownMember{Argument: reference.argument, Name: odataDescribeCallerKey(reference.path),
			Suggestions: odataSuggest(shape, reference, entity)}
		members = append(members, member)
		details = append(details, odataDescribeUnknown(member, odataExistsOnType(shape, reference)))
	}
	more := ""
	if len(unknown) > len(reported) {
		more = fmt.Sprintf(" and %d more", len(unknown)-len(reported))
	}
	columns := len(shape.root.properties)
	text := "Checked against the OData metadata of " + entity + ":"
	if len(unknown) > 0 {
		text += fmt.Sprintf(" %d name(s) cannot be used as sent%s: %s. ", len(unknown), more, strings.Join(details, "; ")) +
			fmt.Sprintf("%s has %d columns. Use one of the suggested names (a lookup is filtered and ordered through its ", entity, columns) +
			"navigation path, for example AccountId -> Account/Id)."
	}
	if len(plainID) > 0 {
		quoted := make([]string, len(plainID))
		for index, name := range plainID {
			quoted[index] = "'" + odataDescribeCallerKey(name) + "'"
		}
		verb := "are ordinary columns"
		if len(plainID) == 1 {
			verb = "is an ordinary column"
		}
		text += " " + strings.Join(quoted, ", ") + " " + verb + " of " + entity + ", " +
			"not a lookup: keep the name as sent and ignore the navigation-path rewrite suggested for it above"
		if len(unknown) == 0 {
			text += "; the rejection has another cause."
		} else {
			text += "."
		}
	}
	return text, members, columns
}

func odataDescribeUnknown(member ODataUnknownMember, wrongForm bool) string {
	if wrongForm {
		use := "its navigation path"
		if len(member.Suggestions) > 0 {
			use = member.Suggestions[0]
		}
		return fmt.Sprintf("%s '%s' (a lookup on the type, but not usable in %s: use %s)", member.Argument, member.Name, member.Argument, use)
	}
	if len(member.Suggestions) > 0 {
		return fmt.Sprintf("%s '%s' (not on the type; closest: %s)", member.Argument, member.Name, strings.Join(member.Suggestions, ", "))
	}
	return fmt.Sprintf("%s '%s' (not on the type; no close match)", member.Argument, member.Name)
}

type odataRanked struct {
	tier, distance int
	name           string
}

func odataSuggest(shape odataEntityShape, reference odataReference, entity string) []string {
	segments := strings.Split(reference.path, "/")
	result := []string{}
	if len(segments) > 1 {
		if navigation, ok := shape.root.navigations.lookup(segments[0]); ok {
			if target, ok := shape.target(navigation); ok {
				pathOnly := odataIsPathOnly(reference) && len(segments) == 2
				odataAddRanked(&result, odataRank(odataMembersOf(target, reference.navigationOnly), segments[1], entity), func(name string) string {
					if pathOnly {
						return navigation + "/" + odataPathOnlyForm(target, name)
					}
					return navigation + "/" + name
				})
				return result
			}
		}
		pathOnly := odataIsPathOnly(reference)
		if len(segments) == 2 && !pathOnly && !reference.navigationOnly && strings.EqualFold(segments[1], "Id") {
			if column, ok := shape.root.properties.lookup(segments[0] + "Id"); ok {
				result = append(result, column)
			}
		}
		tail := strings.Join(segments[1:], "/")
		fallback := ""
		if pathOnly {
			fallback = "/Id"
		}
		odataAddRanked(&result, odataRank(odataNavigationNames(shape.root), segments[0], entity), func(name string) string {
			if corrected, ok := odataCorrectTail(shape, name, tail, reference.navigationOnly, pathOnly && len(segments) == 2); ok {
				return name + "/" + corrected
			}
			return name + fallback
		})
		return result
	}
	subject := segments[0]
	pathOnly := odataIsPathOnly(reference)
	if pathOnly && len(subject) > 2 && strings.HasSuffix(strings.ToLower(subject), "id") {
		if stem, ok := shape.root.navigations.lookup(subject[:len(subject)-2]); ok {
			result = append(result, stem+"/Id")
		}
	}
	odataAddRanked(&result, odataRank(odataMembersOf(shape.root, reference.navigationOnly), subject, entity), func(name string) string {
		if pathOnly {
			return odataPathOnlyForm(shape.root, name)
		}
		return name
	})
	return result
}

func odataNavigationNames(typ odataTypeShape) map[string]bool {
	names := map[string]bool{}
	for _, name := range typ.navigations {
		names[name] = true
	}
	return names
}

func odataMembersOf(typ odataTypeShape, navigationOnly bool) map[string]bool {
	names := odataNavigationNames(typ)
	if !navigationOnly {
		for _, name := range typ.properties {
			names[name] = true
		}
	}
	return names
}

func odataOrdered(ranked []odataRanked) []string {
	sort.Slice(ranked, func(i, j int) bool {
		a, b := ranked[i], ranked[j]
		if a.tier != b.tier {
			return a.tier < b.tier
		}
		if a.distance != b.distance {
			return a.distance < b.distance
		}
		if toFileUTF16Len(a.name) != toFileUTF16Len(b.name) {
			return toFileUTF16Len(a.name) < toFileUTF16Len(b.name)
		}
		return a.name < b.name
	})
	names := make([]string, len(ranked))
	for index, entry := range ranked {
		names[index] = entry.name
	}
	return names
}

func odataAddRanked(result *[]string, ranked []odataRanked, format func(string) string) {
	for _, name := range odataOrdered(ranked) {
		if len(*result) >= odataExplainMaxSuggestions {
			return
		}
		full := format(name)
		present := false
		for _, existing := range *result {
			if existing == full {
				present = true
			}
		}
		if !present {
			*result = append(*result, full)
		}
	}
}

func odataCorrectTail(shape odataEntityShape, navigation, tail string, navigationOnly, pathOnly bool) (string, bool) {
	target, ok := shape.target(navigation)
	if !ok {
		return tail, true
	}
	parts := strings.Split(tail, "/")
	ordered := odataOrdered(odataRank(odataMembersOf(target, navigationOnly), parts[0], ""))
	if len(ordered) == 0 {
		return "", false
	}
	best := ordered[0]
	if pathOnly {
		best = odataPathOnlyForm(target, best)
	}
	parts[0] = best
	return strings.Join(parts, "/"), true
}

func odataRank(pool map[string]bool, subject, entity string) []odataRanked {
	var ranked []odataRanked
	for candidate := range pool {
		tier, distance, ok := odataScore(subject, candidate)
		if stripped, has := odataStripEntityPrefix(subject, entity); has {
			if strippedTier, strippedDistance, strippedOK := odataScore(stripped, candidate); strippedOK &&
				(!ok || strippedTier < tier || (strippedTier == tier && strippedDistance < distance)) {
				tier, distance, ok = strippedTier, strippedDistance, true
			}
		}
		if ok {
			ranked = append(ranked, odataRanked{tier, distance, candidate})
		}
	}
	return ranked
}

func odataStripEntityPrefix(name, entity string) (string, bool) {
	if entity != "" && len(name) > len(entity) && strings.HasPrefix(strings.ToLower(name), strings.ToLower(entity)) {
		return name[len(entity):], true
	}
	return "", false
}

func odataScore(name, candidate string) (int, int, bool) {
	left, right := strings.ToLower(name), strings.ToLower(candidate)
	if left == right {
		return 0, 0, true
	}
	shorter, longer := left, right
	if toFileUTF16Len(left) > toFileUTF16Len(right) {
		shorter, longer = right, left
	}
	if (toFileUTF16Len(left) >= odataExplainMinPrefix && strings.HasPrefix(right, left)) ||
		(toFileUTF16Len(shorter) >= odataExplainMinContainment && strings.Contains(longer, shorter)) {
		return 1, toFileUTF16Len(longer) - toFileUTF16Len(shorter), true
	}
	distance := componentInfoLevenshtein(left, right)
	limit := toFileUTF16Len(left) / 4
	if limit < 2 {
		limit = 2
	}
	if distance <= limit {
		return 2, distance, true
	}
	return 0, 0, false
}
