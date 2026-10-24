//go:build !windows

package pagesmoke

// productVersion reads a Windows executable's version resource; outside Windows there is none to read.
func productVersion(string) string { return "" }
