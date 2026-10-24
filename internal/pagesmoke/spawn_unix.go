//go:build !windows

package pagesmoke

import (
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// spawnPipeBrowser starts the browser with its file descriptors 3 and 4 the two ends of a private DevTools pipe
// and 0-2 the null device (under the MCP server this process's stdout is the JSON-RPC stream). The browser gets
// its own process group, as Puppeteer and Playwright start one, so the whole group is ended together. Every other
// descriptor of this process is close-on-exec, so the browser inherits nothing else.
func spawnPipeBrowser(executable string, arguments []string) (*pipeProcess, error) {
	browserRead, toBrowser, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	fromBrowser, browserWrite, err := os.Pipe()
	if err != nil {
		browserRead.Close()
		toBrowser.Close()
		return nil, err
	}
	command := exec.Command(executable, arguments...)
	command.ExtraFiles = []*os.File{browserRead, browserWrite}
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	err = command.Start()
	browserRead.Close()
	browserWrite.Close()
	if err != nil {
		toBrowser.Close()
		fromBrowser.Close()
		return nil, err
	}
	lifetime := &childLifetime{command: command, done: make(chan struct{})}
	return &pipeProcess{pid: command.Process.Pid, fromBrowser: fromBrowser, toBrowser: toBrowser, lifetime: lifetime}, nil
}

// childLifetime keeps the browser unreaped until it is ended, so its id cannot name another process before the
// kill: the kill goes to the browser's group and to the browser itself only while it has not been reaped.
type childLifetime struct {
	command *exec.Cmd
	mutex   sync.Mutex
	waiting bool
	done    chan struct{}
}

func (l *childLifetime) startWait() {
	l.mutex.Lock()
	defer l.mutex.Unlock()
	if l.waiting {
		return
	}
	l.waiting = true
	go func() {
		_ = l.command.Wait()
		close(l.done)
	}()
}

func (l *childLifetime) reaped() bool {
	select {
	case <-l.done:
		return true
	default:
		return false
	}
}

func (l *childLifetime) waitForExit(timeout time.Duration) bool {
	l.startWait()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-l.done:
		return true
	case <-timer.C:
		return false
	}
}

func (l *childLifetime) terminate() {
	l.mutex.Lock()
	waiting := l.waiting
	l.mutex.Unlock()
	if waiting && l.reaped() {
		return
	}
	pid := l.command.Process.Pid
	if pid > 1 {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}

func (l *childLifetime) release() { l.startWait() }
