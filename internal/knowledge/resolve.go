package knowledge

import (
	"fmt"
	"sort"
	"strings"
)

// resolve is clio's KnowledgeResolver.Find: an exact namespaced URI, then a publisher-declared legacy
// URI, then a topic or item ID resolved by pin, participation and priority.
func resolve(identifier string, libraries []*Library, pins map[string]string, eligible func(*Article) bool) Lookup {
	if libraryID, itemID, ok := parseNamespacedURI(identifier); ok {
		return findExact(libraryID, itemID, libraries, eligible)
	}
	if legacy := findLegacyURI(identifier, libraries, eligible); legacy.Status != LookupNotFound {
		return legacy
	}
	return findTopic(identifier, libraries, pins, eligible)
}

func parseNamespacedURI(identifier string) (string, string, bool) {
	if !strings.HasPrefix(identifier, namespacedURIPrefix) {
		return "", "", false
	}
	remainder := identifier[len(namespacedURIPrefix):]
	separator := strings.Index(remainder, "/")
	if separator <= 0 || separator == len(remainder)-1 || strings.Contains(remainder[separator+1:], "/") {
		return "", "", false
	}
	libraryID := UnescapeDataString(remainder[:separator])
	itemID := UnescapeDataString(remainder[separator+1:])
	return libraryID, itemID, !blank(libraryID) && !blank(itemID)
}

func findExact(libraryID, itemID string, libraries []*Library, eligible func(*Article) bool) Lookup {
	var library *Library
	for _, candidate := range libraries {
		if candidate.LibraryID == libraryID {
			library = candidate
		}
	}
	if library == nil {
		return Lookup{Status: LookupNotFound}
	}
	for i := range library.Articles {
		article := &library.Articles[i]
		if article.ItemID == itemID {
			if !eligible(article) {
				return Lookup{Status: LookupNotFound}
			}
			return active(article, library)
		}
	}
	return Lookup{Status: LookupNotFound}
}

type match struct {
	library *Library
	article *Article
}

func findLegacyURI(identifier string, libraries []*Library, eligible func(*Article) bool) Lookup {
	var matches []match
	for _, library := range libraries {
		if library.Participation == ParticipationIsolated {
			continue
		}
		for i := range library.Articles {
			article := &library.Articles[i]
			for _, legacy := range article.LegacyURIs {
				if legacy == identifier && eligible(article) {
					matches = append(matches, match{library, article})
					break
				}
			}
		}
	}
	switch len(matches) {
	case 0:
		return Lookup{Status: LookupNotFound}
	case 1:
		return active(matches[0].article, matches[0].library)
	}
	ids := map[string]bool{}
	for _, m := range matches {
		ids[m.library.LibraryID] = true
	}
	return Lookup{Status: LookupAmbiguous, Diagnostic: fmt.Sprintf(
		"Legacy knowledge URI '%s' is ambiguous between libraries: %s. Use a namespaced knowledge URI.",
		identifier, strings.Join(sortedStrings(ids), ", "))}
}

func findTopic(identifier string, libraries []*Library, pins map[string]string, eligible func(*Article) bool) Lookup {
	var named []match
	for _, library := range libraries {
		if library.Participation == ParticipationIsolated {
			continue
		}
		for i := range library.Articles {
			article := &library.Articles[i]
			if article.Role == defaultRole && (article.TopicID == identifier || article.ItemID == identifier) && eligible(article) {
				named = append(named, match{library, article})
			}
		}
	}
	if len(named) == 0 {
		return Lookup{Status: LookupNotFound}
	}
	topics := map[string]bool{}
	for _, m := range named {
		if !blank(m.article.TopicID) {
			topics[m.article.TopicID] = true
		}
	}
	if len(topics) != 1 {
		return Lookup{Status: LookupAmbiguous, Diagnostic: fmt.Sprintf(
			"Knowledge name '%s' maps to multiple logical topics: %s. Use a namespaced knowledge URI.",
			identifier, strings.Join(sortedStrings(topics), ", "))}
	}
	canonical := sortedStrings(topics)[0]
	var matches []match
	perLibrary := map[string]int{}
	for _, library := range libraries {
		if library.Participation == ParticipationIsolated {
			continue
		}
		for i := range library.Articles {
			article := &library.Articles[i]
			if article.Role == defaultRole && article.TopicID == canonical && eligible(article) {
				matches = append(matches, match{library, article})
				perLibrary[library.LibraryID]++
			}
		}
	}
	duplicates := map[string]bool{}
	for id, count := range perLibrary {
		if count > 1 {
			duplicates[id] = true
		}
	}
	if len(duplicates) > 0 {
		return Lookup{Status: LookupAmbiguous, Diagnostic: fmt.Sprintf(
			"Knowledge name '%s' matches multiple guidance items in libraries: %s. Use a namespaced knowledge URI.",
			identifier, strings.Join(sortedStrings(duplicates), ", "))}
	}
	pinned, hasPin := pins[canonical]
	if !hasPin && canonical != identifier {
		pinned, hasPin = pins[identifier]
	}
	if hasPin {
		for _, m := range matches {
			if m.library.LibraryID == pinned {
				return active(m.article, m.library)
			}
		}
		return Lookup{Status: LookupAmbiguous, Diagnostic: fmt.Sprintf(
			"Knowledge topic '%s' is pinned to unavailable or ineligible library '%s'.", canonical, pinned)}
	}
	var eligibleMatches []match
	for _, m := range matches {
		if m.library.Participation == ParticipationAuthoritative {
			eligibleMatches = append(eligibleMatches, m)
		}
	}
	if len(eligibleMatches) == 0 {
		for _, m := range matches {
			if m.library.Participation == ParticipationSupplement {
				eligibleMatches = append(eligibleMatches, m)
			}
		}
	}
	if len(eligibleMatches) == 0 {
		// clio's Max over an empty sequence throws here; the guidance source reports it as a failure.
		return Lookup{Status: LookupNotFound}
	}
	highest := eligibleMatches[0].library.Priority
	for _, m := range eligibleMatches {
		if m.library.Priority > highest {
			highest = m.library.Priority
		}
	}
	var winners []match
	for _, m := range eligibleMatches {
		if m.library.Priority == highest {
			winners = append(winners, m)
		}
	}
	if len(winners) != 1 {
		var ids []string
		for _, w := range winners {
			ids = append(ids, w.library.LibraryID)
		}
		sort.Strings(ids)
		return Lookup{Status: LookupAmbiguous, Diagnostic: fmt.Sprintf(
			"Knowledge topic '%s' is ambiguous between equally prioritized libraries: %s. Configure a topic pin or distinct priorities.",
			canonical, strings.Join(ids, ", "))}
	}
	return active(winners[0].article, winners[0].library)
}

func active(article *Article, library *Library) Lookup {
	return Lookup{Status: LookupActive, Article: article, Provenance: provenance(article, library)}
}

// resolvableNames is clio's KnowledgeResolver.GetNames: item and topic IDs of eligible guidance.
func resolvableNames(libraries []*Library, eligible func(*Article) bool) []string {
	names := map[string]bool{}
	for _, library := range libraries {
		if library.Participation == ParticipationIsolated {
			continue
		}
		for i := range library.Articles {
			article := &library.Articles[i]
			if article.Role != defaultRole || !eligible(article) {
				continue
			}
			for _, id := range []string{article.ItemID, article.TopicID} {
				if !blank(id) {
					names[id] = true
				}
			}
		}
	}
	return sortedStrings(names)
}
