package toolkit

// clio's CodexTomlConfigEditor: line-based edits of ~/.codex/config.toml that keep every other byte.

import (
	"errors"
	"os"
	"regexp"
	"strings"
)

var tomlTableHeader = regexp.MustCompile(`^(?P<header>\[{1,2}(?:[A-Za-z0-9_\-]+|"(?:[^"\\]|\\.)*"|'[^']*')(?:\.(?:[A-Za-z0-9_\-]+|"(?:[^"\\]|\\.)*"|'[^']*'))*\]{1,2})\s*(?:#.*)?$`)

func tomlQuote(value string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value) + `"`
}

func tomlHeaderMarker(line string) string {
	match := tomlTableHeader.FindStringSubmatch(strings.TrimSpace(line))
	if match == nil {
		return ""
	}
	return match[1]
}

// splitLinesKeepEnds is clio's SplitLinesKeepEnds (\n, \r\n and a lone \r end a line).
func splitLinesKeepEnds(text string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(text); i++ {
		switch text[i] {
		case '\n':
			lines = append(lines, text[start:i+1])
			start = i + 1
		case '\r':
			end := i + 1
			if end < len(text) && text[end] == '\n' {
				end++
			}
			lines = append(lines, text[start:end])
			i = end - 1
			start = end
		}
	}
	if start < len(text) {
		lines = append(lines, text[start:])
	}
	return lines
}

// mergeTomlClioServer is MergeClioMcpServer: the clio server block is appended once.
func mergeTomlClioServer(path string) error {
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	existing := string(data)
	if strings.Contains(existing, "[mcp_servers.clio]") || strings.Contains(existing, "[mcp_servers."+tomlQuote("clio")+"]") {
		return nil
	}
	block := "\n# Added by clio installer.\n[mcp_servers.clio]\ncommand = " + tomlQuote("clio") + "\nargs = [" + tomlQuote("mcp-server") + "]\nenabled = true\n"
	separator := ""
	if existing != "" && !strings.HasSuffix(existing, "\n") {
		separator = "\n"
	}
	return os.WriteFile(path, []byte(existing+separator+block), 0o666)
}

// removeTomlTables is RemoveTableBlock: each named table and its body up to the next header go.
func removeTomlTables(path string, markers ...string) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	lines := splitLinesKeepEnds(string(data))
	var updated []string
	changed := false
	for index := 0; index < len(lines); {
		marker := tomlHeaderMarker(lines[index])
		matched := false
		for _, candidate := range markers {
			matched = matched || (marker != "" && marker == candidate)
		}
		if matched {
			end := index + 1
			for end < len(lines) && tomlHeaderMarker(lines[end]) == "" {
				end++
			}
			changed = true
			index = end
			continue
		}
		updated = append(updated, lines[index])
		index++
	}
	if !changed {
		return nil
	}
	return os.WriteFile(path, []byte(strings.Join(updated, "")), 0o666)
}

// removeTomlSkillOverride is RemoveSkillConfigOverride: [[skills.config]] blocks naming the skill go.
func removeTomlSkillOverride(path, skill string) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	lines := splitLinesKeepEnds(string(data))
	var updated []string
	changed := false
	for index := 0; index < len(lines); {
		if tomlHeaderMarker(lines[index]) != "[[skills.config]]" {
			updated = append(updated, lines[index])
			index++
			continue
		}
		end := index + 1
		for end < len(lines) && tomlHeaderMarker(lines[end]) == "" {
			end++
		}
		if strings.Contains(strings.Join(lines[index:end], ""), `name = "`+skill+`"`) {
			changed = true
		} else {
			updated = append(updated, lines[index:end]...)
		}
		index = end
	}
	if !changed {
		return nil
	}
	return os.WriteFile(path, []byte(strings.Join(updated, "")), 0o666)
}
