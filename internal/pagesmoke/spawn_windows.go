//go:build windows

package pagesmoke

import (
	"os"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

const (
	ioPipesSwitch     = "--remote-debugging-io-pipes="
	createNoWindow    = 0x08000000
	handleFlagInherit = 0x00000001
)

var (
	kernel32                 = syscall.NewLazyDLL("kernel32.dll")
	procSetHandleInformation = kernel32.NewProc("SetHandleInformation")
)

// spawnPipeBrowser starts the browser so that it inherits exactly the two ends of a private DevTools pipe, named
// to it by --remote-debugging-io-pipes=<read>,<write> (the contract ChromeDriver uses on Windows). The ends are
// inheritable only for the start and closed here right after it; the process's handle list keeps every other
// inheritable handle of this server out of the browser. A console build gets no window.
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
	closeBrowserEnds := func() {
		browserRead.Close()
		browserWrite.Close()
	}
	readHandle, writeHandle := syscall.Handle(browserRead.Fd()), syscall.Handle(browserWrite.Fd())
	if err := makeInheritable(readHandle); err != nil {
		closeBrowserEnds()
		toBrowser.Close()
		fromBrowser.Close()
		return nil, err
	}
	if err := makeInheritable(writeHandle); err != nil {
		closeBrowserEnds()
		toBrowser.Close()
		fromBrowser.Close()
		return nil, err
	}
	withHandles := insertArgument(arguments, indexBeforeTrailingURL(arguments),
		ioPipesSwitch+strconv.FormatUint(uint64(readHandle), 10)+","+strconv.FormatUint(uint64(writeHandle), 10))
	command := exec.Command(executable, withHandles...)
	command.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags:              createNoWindow,
		HideWindow:                 true,
		AdditionalInheritedHandles: []syscall.Handle{readHandle, writeHandle},
	}
	err = command.Start()
	closeBrowserEnds()
	if err != nil {
		toBrowser.Close()
		fromBrowser.Close()
		return nil, err
	}
	lifetime := &handleLifetime{command: command, done: make(chan struct{})}
	return &pipeProcess{pid: command.Process.Pid, fromBrowser: fromBrowser, toBrowser: toBrowser, lifetime: lifetime}, nil
}

func makeInheritable(handle syscall.Handle) error {
	ok, _, err := procSetHandleInformation.Call(uintptr(handle), handleFlagInherit, handleFlagInherit)
	if ok == 0 {
		return err
	}
	return nil
}

// handleLifetime holds the browser's process handle until release: while it is open, Windows never gives the
// id to another process, so ending the browser's tree cannot reach a stranger.
type handleLifetime struct {
	command *exec.Cmd
	mutex   sync.Mutex
	waiting bool
	done    chan struct{}
}

func (l *handleLifetime) startWait() {
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

func (l *handleLifetime) waitForExit(timeout time.Duration) bool {
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

// terminate ends the browser's descendants (its renderers and helpers), then the browser itself, as
// Process.Kill(entireProcessTree: true) does.
func (l *handleLifetime) terminate() {
	l.mutex.Lock()
	waiting := l.waiting
	l.mutex.Unlock()
	if waiting {
		select {
		case <-l.done:
			return
		default:
		}
	}
	killDescendants(uint32(l.command.Process.Pid))
	_ = l.command.Process.Kill()
}

func (l *handleLifetime) release() { l.startWait() }

// killDescendants terminates every process whose parent chain leads to root, from a toolhelp snapshot.
func killDescendants(root uint32) {
	snapshot, err := syscall.CreateToolhelp32Snapshot(syscall.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return
	}
	defer syscall.CloseHandle(snapshot)
	parents := map[uint32]uint32{}
	var entry syscall.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	for err = syscall.Process32First(snapshot, &entry); err == nil; err = syscall.Process32Next(snapshot, &entry) {
		parents[entry.ProcessID] = entry.ParentProcessID
	}
	for pid := range parents {
		if pid == root || !descendsFrom(pid, root, parents) {
			continue
		}
		handle, err := syscall.OpenProcess(syscall.PROCESS_TERMINATE, false, pid)
		if err != nil {
			continue
		}
		_ = syscall.TerminateProcess(handle, 1)
		_ = syscall.CloseHandle(handle)
	}
}

func descendsFrom(pid, root uint32, parents map[uint32]uint32) bool {
	seen := map[uint32]bool{}
	for current := pid; !seen[current]; {
		seen[current] = true
		parent, ok := parents[current]
		if !ok || parent == 0 {
			return false
		}
		if parent == root {
			return true
		}
		current = parent
	}
	return false
}
