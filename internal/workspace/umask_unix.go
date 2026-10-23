//go:build !windows

package workspace

import "syscall"

// umask reads the process umask, which `chmod +x` honours.
func umask() uint32 {
	mask := syscall.Umask(0)
	syscall.Umask(mask)
	return uint32(mask)
}
