//go:build windows

package workspace

func umask() uint32 { return 0 }
