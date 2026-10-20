package toolkit

// Agent config files are edited the way clio's JsonConfigEditor edits them with System.Text.Json.Nodes:
// member order is kept, new members are appended, and the file is rewritten with System.Text.Json's
// indented writer and default (escaping) encoder, followed by a newline.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unicode/utf16"
)

type object struct {
	keys   []string
	values map[string]any
}

func newObject() *object { return &object{values: map[string]any{}} }

func (o *object) get(key string) any { return o.values[key] }

func (o *object) set(key string, value any) {
	if _, ok := o.values[key]; !ok {
		o.keys = append(o.keys, key)
	}
	o.values[key] = value
}

func (o *object) remove(key string) bool {
	if _, ok := o.values[key]; !ok {
		return false
	}
	delete(o.values, key)
	for i, existing := range o.keys {
		if existing == key {
			o.keys = append(o.keys[:i], o.keys[i+1:]...)
			break
		}
	}
	return true
}

func parseObject(data []byte) (*object, error) {
	decoder := json.NewDecoder(bytes.NewReader(bytes.TrimPrefix(data, []byte("\xEF\xBB\xBF"))))
	decoder.UseNumber()
	value, err := parseValue(decoder)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("'" + "' is invalid after a single JSON value. Expected end of data.")
	}
	if typed, ok := value.(*object); ok {
		return typed, nil
	}
	return nil, nil
}

func parseValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return token, nil
	}
	switch delimiter {
	case '{':
		result := newObject()
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			value, err := parseValue(decoder)
			if err != nil {
				return nil, err
			}
			result.set(key.(string), value)
		}
		_, err := decoder.Token()
		return result, err
	case '[':
		var items []any
		for decoder.More() {
			value, err := parseValue(decoder)
			if err != nil {
				return nil, err
			}
			items = append(items, value)
		}
		_, err := decoder.Token()
		if items == nil {
			items = []any{}
		}
		return items, err
	}
	return nil, fmt.Errorf("unexpected %v", delimiter)
}

// readConfigObject is JsonConfigEditor.ReadObject: nil for a missing file, empty for blank or non-object
// content, and clio's refusal for content that is not JSON.
func readConfigObject(path string) (*object, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(string(data)) == "" {
		return newObject(), nil
	}
	parsed, err := parseObject(data)
	if err != nil {
		return nil, fmt.Errorf("Could not parse JSON in %s: %s. Fix or remove the file and retry.", path, err.Error())
	}
	if parsed == nil {
		return newObject(), nil
	}
	return parsed, nil
}

func writeConfigObject(path string, root *object) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o777); err != nil {
		return err
	}
	return os.WriteFile(path, append(root.indented(true), '\n'), 0o666)
}

func newline() string {
	if runtime.GOOS == "windows" {
		return "\r\n"
	}
	return "\n"
}

// indented is System.Text.Json's WriteIndented output (stj escaping) or, with stj=false, the same layout
// with the default serializer escaping JsonSerializer.Serialize uses.
func (o *object) indented(stj bool) []byte {
	var buffer bytes.Buffer
	writeValue(&buffer, o, 0, stj)
	return buffer.Bytes()
}

func writeValue(buffer *bytes.Buffer, value any, depth int, stj bool) {
	indent := func(level int) { buffer.WriteString(newline() + strings.Repeat("  ", level)) }
	switch typed := value.(type) {
	case *object:
		if len(typed.keys) == 0 {
			buffer.WriteString("{}")
			return
		}
		buffer.WriteByte('{')
		for i, key := range typed.keys {
			if i > 0 {
				buffer.WriteByte(',')
			}
			indent(depth + 1)
			writeString(buffer, key)
			buffer.WriteString(": ")
			writeValue(buffer, typed.values[key], depth+1, stj)
		}
		indent(depth)
		buffer.WriteByte('}')
	case []any:
		if len(typed) == 0 {
			buffer.WriteString("[]")
			return
		}
		buffer.WriteByte('[')
		for i, item := range typed {
			if i > 0 {
				buffer.WriteByte(',')
			}
			indent(depth + 1)
			writeValue(buffer, item, depth+1, stj)
		}
		indent(depth)
		buffer.WriteByte(']')
	case string:
		writeString(buffer, typed)
	case json.Number:
		buffer.WriteString(typed.String())
	case bool:
		fmt.Fprint(buffer, typed)
	case nil:
		buffer.WriteString("null")
	}
}

// writeString is System.Text.Json's default JavaScriptEncoder: short escapes for \b \f \n \r \t \\, and
// \uXXXX for other controls, non-ASCII and the HTML-sensitive characters " & ' + < > `.
func writeString(buffer *bytes.Buffer, value string) {
	buffer.WriteByte('"')
	for _, r := range value {
		switch r {
		case '\n':
			buffer.WriteString(`\n`)
			continue
		case '\r':
			buffer.WriteString(`\r`)
			continue
		case '\t':
			buffer.WriteString(`\t`)
			continue
		case '\b':
			buffer.WriteString(`\b`)
			continue
		case '\f':
			buffer.WriteString(`\f`)
			continue
		case '\\':
			buffer.WriteString(`\\`)
			continue
		}
		if r < 0x20 || r > 0x7E || strings.ContainsRune("\"&'+<>`", r) {
			for _, unit := range utf16.Encode([]rune{r}) {
				fmt.Fprintf(buffer, `\u%04X`, unit)
			}
			continue
		}
		buffer.WriteRune(r)
	}
	buffer.WriteByte('"')
}

func asString(value any) string {
	text, _ := value.(string)
	return text
}

// enableMarketplaceAutoUpdate is JsonConfigEditor.EnableMarketplaceAutoUpdate.
func enableMarketplaceAutoUpdate(path, marketplace string) error {
	settings, err := readConfigObject(path)
	if err != nil {
		return err
	}
	if settings == nil {
		settings = newObject()
	}
	extra, ok := settings.get("extraKnownMarketplaces").(*object)
	if !ok {
		extra = newObject()
		settings.set("extraKnownMarketplaces", extra)
	}
	entry, ok := extra.get(marketplace).(*object)
	if !ok {
		entry = newObject()
		extra.set(marketplace, entry)
	}
	if source, ok := entry.get("source").(*object); ok && asString(source.get("source")) == "directory" {
		entry.remove("source")
	}
	entry.set("autoUpdate", true)
	return writeConfigObject(path, settings)
}

// removeMarketplaceFromSettings is JsonConfigEditor.RemoveMarketplaceFromSettings.
func removeMarketplaceFromSettings(path, marketplace string) error {
	settings, err := readConfigObject(path)
	if err != nil || settings == nil {
		return err
	}
	extra, ok := settings.get("extraKnownMarketplaces").(*object)
	if !ok || !extra.remove(marketplace) {
		return nil
	}
	return writeConfigObject(path, settings)
}

// mergeJSONClioServer is JsonConfigEditor.MergeClioMcpServer: an existing "clio" entry is left alone.
func mergeJSONClioServer(path string) error {
	root, err := readConfigObject(path)
	if err != nil {
		return err
	}
	if root == nil {
		root = newObject()
	}
	servers, ok := root.get("mcpServers").(*object)
	if !ok {
		servers = newObject()
		root.set("mcpServers", servers)
	}
	if _, exists := servers.values["clio"]; exists {
		return nil
	}
	clio := newObject()
	clio.set("command", "clio")
	clio.set("args", []any{"mcp-server"})
	servers.set("clio", clio)
	return writeConfigObject(path, root)
}

// removePersonalMarketplacePluginEntry is JsonConfigEditor.RemovePersonalMarketplacePluginEntry.
func removePersonalMarketplacePluginEntry(path, marketplace, plugin string) error {
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	catalog, err := readConfigObject(path)
	if err != nil || catalog == nil {
		return err
	}
	plugins, ok := catalog.get("plugins").([]any)
	if !ok {
		if asString(catalog.get("name")) == marketplace {
			_ = os.Remove(path)
		}
		return nil
	}
	kept := []any{}
	for _, item := range plugins {
		if entry, ok := item.(*object); ok && asString(entry.get("name")) == plugin {
			continue
		}
		kept = append(kept, item)
	}
	if len(kept) == len(plugins) {
		return nil
	}
	if len(kept) == 0 && asString(catalog.get("name")) == marketplace {
		return os.Remove(path)
	}
	catalog.set("plugins", kept)
	return writeConfigObject(path, catalog)
}
