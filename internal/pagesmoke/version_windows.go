//go:build windows

package pagesmoke

import (
	"encoding/binary"
	"fmt"
	"syscall"
	"unicode/utf16"
	"unsafe"
)

var (
	versionDLL                 = syscall.NewLazyDLL("version.dll")
	procGetFileVersionInfoSize = versionDLL.NewProc("GetFileVersionInfoSizeW")
	procGetFileVersionInfo     = versionDLL.NewProc("GetFileVersionInfoW")
	procVerQueryValue          = versionDLL.NewProc("VerQueryValueW")
)

// productVersion is FileVersionInfo.ProductVersion: the ProductVersion string of the executable's first
// translation, then of the usual US English code pages; "" when the file has none.
func productVersion(path string) string {
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return ""
	}
	size, _, _ := procGetFileVersionInfoSize.Call(uintptr(unsafe.Pointer(name)), 0)
	if size == 0 {
		return ""
	}
	data := make([]byte, size)
	if ok, _, _ := procGetFileVersionInfo.Call(uintptr(unsafe.Pointer(name)), 0, size, uintptr(unsafe.Pointer(&data[0]))); ok == 0 {
		return ""
	}
	var codePages []string
	if value := queryValue(data, `\VarFileInfo\Translation`, 4); len(value) >= 4 {
		codePages = append(codePages, fmt.Sprintf("%04x%04x", binary.LittleEndian.Uint16(value), binary.LittleEndian.Uint16(value[2:])))
	}
	codePages = append(codePages, "040904b0", "040904e4", "04090000")
	for _, codePage := range codePages {
		value := queryValue(data, `\StringFileInfo\`+codePage+`\ProductVersion`, 2)
		if len(value) < 2 {
			continue
		}
		units := make([]uint16, 0, len(value)/2)
		for index := 0; index+1 < len(value); index += 2 {
			unit := binary.LittleEndian.Uint16(value[index:])
			if unit == 0 {
				break
			}
			units = append(units, unit)
		}
		return string(utf16.Decode(units))
	}
	return ""
}

// queryValue is VerQueryValue over the version block: the bytes of the value, which lie inside data (a string's
// length is in characters, unitBytes each); nil when the block has none.
func queryValue(data []byte, block string, unitBytes uintptr) []byte {
	name, err := syscall.UTF16PtrFromString(block)
	if err != nil {
		return nil
	}
	var pointer uintptr
	var length uint32
	ok, _, _ := procVerQueryValue.Call(uintptr(unsafe.Pointer(&data[0])), uintptr(unsafe.Pointer(name)),
		uintptr(unsafe.Pointer(&pointer)), uintptr(unsafe.Pointer(&length)))
	if ok == 0 || pointer == 0 {
		return nil
	}
	base := uintptr(unsafe.Pointer(&data[0]))
	if pointer < base || pointer >= base+uintptr(len(data)) {
		return nil
	}
	start := pointer - base
	end := start + uintptr(length)*unitBytes
	if unitBytes == 4 {
		end = start + uintptr(length)
	}
	if end > uintptr(len(data)) {
		end = uintptr(len(data))
	}
	return data[start:end]
}
