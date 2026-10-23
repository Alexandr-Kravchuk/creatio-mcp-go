package pagesmoke

import (
	"os"
	"strings"
	"sync"
	"time"
)

// pipeSwitch makes Chromium's DevTools endpoint a private pipe pair instead of a TCP port (PipeBrowserSpawner):
// on Unix it reads commands from file descriptor 3 and writes replies to 4; on Windows it adopts the two
// inherited handles --remote-debugging-io-pipes names (Chromium 117 and later). There is no fallback to
// --remote-debugging-port: an open port is what this avoids.
const pipeSwitch = "--remote-debugging-pipe"

// pipeProcess is a browser this server started, and the two ends of its DevTools pipe (PipeBrowserProcess).
type pipeProcess struct {
	pid         int
	fromBrowser *os.File // the browser writes replies and events here
	toBrowser   *os.File // commands are written here
	lifetime    processLifetime
	disposeOnce sync.Once
}

// processLifetime waits for, ends and releases a browser process.
type processLifetime interface {
	waitForExit(timeout time.Duration) bool
	terminate()
	release()
}

func (p *pipeProcess) dispose() {
	p.disposeOnce.Do(p.lifetime.release)
}

// indexBeforeTrailingURL puts a switch before a trailing URL, like any other switch.
func indexBeforeTrailingURL(arguments []string) int {
	if len(arguments) > 0 && !strings.HasPrefix(arguments[len(arguments)-1], "-") {
		return len(arguments) - 1
	}
	return len(arguments)
}

func insertArgument(arguments []string, index int, argument string) []string {
	result := make([]string, 0, len(arguments)+1)
	result = append(result, arguments[:index]...)
	result = append(result, argument)
	return append(result, arguments[index:]...)
}

// startPipeBrowser is PipeBrowserSpawner.Start: the pipe switch goes before a trailing URL.
func startPipeBrowser(executable string, arguments []string) (*pipeProcess, error) {
	withPipe := insertArgument(arguments, indexBeforeTrailingURL(arguments), pipeSwitch)
	return spawnPipeBrowser(executable, withPipe)
}
