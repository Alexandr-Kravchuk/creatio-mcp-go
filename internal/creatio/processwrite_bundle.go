package creatio

// The CrtProcessBuilder archive clio ships (clio/CrtProcessBuilder/CrtProcessBuilder.gz). This server does not
// carry it; it reads the copy of the installed clio tool, as the knowledge and help readers do, so the
// version the convergence check compares against and the bytes install-process-builder installs are the
// ones clio would use.

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf16"
)

// processWriteArchiveGlob finds the archive under the dotnet global tool store of clio.
var processWriteArchiveGlob = func() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	matches, _ := filepath.Glob(filepath.Join(home, ".dotnet", "tools", ".store", "clio", "*", "clio", "*", "tools", "*", "any",
		processBuilderPackageName, processBuilderPackageName+".gz"))
	return matches
}

// ProcessWriteArchivePath returns the CrtProcessBuilder archive of the newest installed clio, or "".
func ProcessWriteArchivePath() string {
	matches := processWriteArchiveGlob()
	if len(matches) == 0 {
		return ""
	}
	sort.Slice(matches, func(i, j int) bool {
		left, leftOK := processWriteParseVersion(processWriteStoreVersion(matches[i]))
		right, rightOK := processWriteParseVersion(processWriteStoreVersion(matches[j]))
		if leftOK && rightOK {
			return left.compare(right) < 0
		}
		return matches[i] < matches[j]
	})
	return matches[len(matches)-1]
}

// processWriteStoreVersion is the clio version directory of a tool-store path (.store/clio/<version>/...).
func processWriteStoreVersion(path string) string {
	parts := strings.Split(filepath.ToSlash(path), "/")
	for index := 0; index+1 < len(parts); index++ {
		if parts[index] == ".store" && index+2 < len(parts) && parts[index+1] == "clio" {
			return parts[index+2]
		}
	}
	return ""
}

// processWriteBundledVersion is BundledPackageCatalog.TryGetVersion for CrtProcessBuilder: the archive's
// descriptor.json Descriptor.PackageVersion. Without a readable archive there is nothing to converge to.
func processWriteBundledVersion() (processWriteVersion, bool) {
	path := ProcessWriteArchivePath()
	if path == "" {
		return processWriteVersion{}, false
	}
	content, found, err := processWriteReadArchiveEntry(path, "descriptor.json")
	if err != nil || !found {
		return processWriteVersion{}, false
	}
	content = bytes.TrimPrefix(content, []byte{0xEF, 0xBB, 0xBF})
	var descriptor struct {
		Descriptor *struct {
			PackageVersion *string `json:"PackageVersion"`
		} `json:"Descriptor"`
	}
	if json.Unmarshal(content, &descriptor) != nil || descriptor.Descriptor == nil || descriptor.Descriptor.PackageVersion == nil {
		return processWriteVersion{}, false
	}
	return processWriteParseVersion(*descriptor.Descriptor.PackageVersion)
}

// processWriteReadArchiveEntry is CompressionUtilities.TryReadFileFromGZip: the gzip holds, per file, the
// relative path as an int32 count of UTF-16 units and the units, then an int32 length and the content.
func processWriteReadArchiveEntry(path, entry string) ([]byte, bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer file.Close()
	reader, err := gzip.NewReader(file)
	if err != nil {
		return nil, false, err
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, false, err
	}
	wanted := processWriteNormalizeEntry(entry)
	offset := 0
	readInt := func() (int, bool) {
		if offset+4 > len(data) {
			return 0, false
		}
		value := int(int32(binary.LittleEndian.Uint32(data[offset:])))
		offset += 4
		return value, true
	}
	for offset < len(data) {
		units, ok := readInt()
		if !ok || units <= 0 || offset+units*2 > len(data) {
			return nil, false, errors.New("not a readable package archive")
		}
		name := make([]uint16, units)
		for index := range name {
			name[index] = binary.LittleEndian.Uint16(data[offset+index*2:])
		}
		offset += units * 2
		length, ok := readInt()
		if !ok || length < 0 || offset+length > len(data) {
			return nil, false, errors.New("not a readable package archive")
		}
		if strings.EqualFold(processWriteNormalizeEntry(string(utf16.Decode(name))), wanted) {
			return data[offset : offset+length], true, nil
		}
		offset += length
	}
	return nil, false, nil
}

func processWriteNormalizeEntry(path string) string {
	return strings.Trim(strings.ReplaceAll(path, "\\", "/"), "/")
}
