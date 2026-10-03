package main

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

// docs://help/command/{name} serves clio's own CLI help (help/en/<verb>.txt), which is not knowledge-bundle
// content. This server does not copy those texts; it reads them from the newest clio installed as a .NET
// tool on this machine, so they always match the clio the user runs. Without one the answer is clio's
// own fallback, "<name> command does not provide documentation.".

// knowledgeMcpToCliCommand is clio's GetHelpResources.McpToCliCommandMap (case-insensitive keys).
var knowledgeMcpToCliCommand = map[string]string{
	"show-web-app-list": "show-web-app-list", "show-webapp-list": "show-web-app-list",
	"restart-by-environment": "restart-web-app", "restart-by-environment-name": "restart-web-app",
	"restart-by-environmentname": "restart-web-app", "restart-by-credentials": "restart-web-app",
	"clear-redis-by-environment": "clear-redis-db", "clear-redis-db-by-environment": "clear-redis-db",
	"clear-redis-by-credentials": "clear-redis-db", "clear-redis-db-by-credentials": "clear-redis-db",
	"start-creatio": "start", "stop-creatio": "stop", "stop-all-creatio": "stop", "stopallcreatio": "stop",
	"unlock-for-hotfix": "pkg-hotfix", "finish-hotfix": "pkg-hotfix",
}

func knowledgeNoDocumentation(name string) string {
	return name + " command does not provide documentation."
}

// knowledgeVerbFor returns the verb whose name or alias equals name (case-sensitive, like clio).
func knowledgeVerbFor(name string) (string, bool) {
	if _, ok := knowledgeClioVerbs[name]; ok {
		return name, true
	}
	verbs := make([]string, 0, len(knowledgeClioVerbs))
	for verb := range knowledgeClioVerbs {
		verbs = append(verbs, verb)
	}
	sort.Strings(verbs)
	for _, verb := range verbs {
		for _, alias := range knowledgeClioVerbs[verb] {
			if alias == name {
				return verb, true
			}
		}
	}
	return "", false
}

func knowledgeCommandHelp(commandName string) string {
	commandName = strings.TrimSpace(commandName)
	resolved := commandName
	if mapped, ok := knowledgeMcpToCliCommand[strings.ToLower(commandName)]; ok {
		resolved = mapped
	}
	verb, ok := knowledgeVerbFor(resolved)
	if !ok {
		return knowledgeNoDocumentation(commandName)
	}
	text, ok := knowledgeReadHelpFile(verb)
	if !ok {
		return knowledgeNoDocumentation(commandName)
	}
	if !strings.EqualFold(commandName, resolved) {
		newline := "\n"
		if runtime.GOOS == "windows" {
			newline = "\r\n"
		}
		text = "MCP help mapping: `" + commandName + "` resolves to CLI command `" + resolved + "`." + newline + newline + text
	}
	return text
}

// knowledgeStaticHelp answers docs://help/restart and docs://help/flushdb (clio's BaseResource).
func knowledgeStaticHelp(name string) string {
	verb, ok := knowledgeVerbFor(name)
	if !ok {
		return knowledgeNoDocumentation(name)
	}
	if text, ok := knowledgeReadHelpFile(verb); ok {
		return text
	}
	return "Could not find file '" + filepath.Join(knowledgeHelpDirectory(), verb+".txt") + "'."
}

func knowledgeReadHelpFile(verb string) (string, bool) {
	directory := knowledgeHelpDirectory()
	if directory == "" {
		return "", false
	}
	data, err := os.ReadFile(filepath.Join(directory, verb+".txt"))
	if err != nil {
		return "", false
	}
	return strings.TrimPrefix(string(data), "\xEF\xBB\xBF"), true
}

// knowledgeHelpDirectory finds help/en of the newest clio .NET tool:
// ~/.dotnet/tools/.store/clio/<version>/clio/<version>/tools/<tfm>/any/help/en.
func knowledgeHelpDirectory() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	matches, _ := filepath.Glob(filepath.Join(home, ".dotnet", "tools", ".store", "clio", "*", "clio", "*", "tools", "*", "any", "help", "en"))
	best, bestVersion := "", []int(nil)
	for _, match := range matches {
		parts := strings.Split(filepath.ToSlash(match), "/")
		version := knowledgeParseToolVersion(parts[len(parts)-6])
		if version != nil && (bestVersion == nil || knowledgeVersionLess(bestVersion, version)) {
			best, bestVersion = match, version
		}
	}
	return best
}

func knowledgeParseToolVersion(text string) []int {
	var version []int
	for _, part := range strings.Split(text, ".") {
		number, err := strconv.Atoi(part)
		if err != nil {
			return nil
		}
		version = append(version, number)
	}
	return version
}

func knowledgeVersionLess(left, right []int) bool {
	for i := 0; i < len(left) && i < len(right); i++ {
		if left[i] != right[i] {
			return left[i] < right[i]
		}
	}
	return len(left) < len(right)
}
