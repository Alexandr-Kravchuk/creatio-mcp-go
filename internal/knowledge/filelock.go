package knowledge

import (
	"errors"
	"sync"
	"time"
)

// clio serializes every mutation of its knowledge store and of appsettings.json on lock files opened with
// FileShare.None: an exclusive flock(2) on Unix and a no-sharing CreateFile on Windows, both taken by .NET
// itself. This server takes exactly the same OS lock on exactly the same file, so a clio process and this
// one exclude each other. Inside one process clio first takes a Monitor per lock path (ProcessLocks); the
// semaphores below are that layer, and they also keep goroutines from spinning on the OS lock.

// errLockBusy reports that a lock could not be taken: the timeout elapsed, or a try-lock found it held.
var errLockBusy = errors.New("knowledge lock is held by another operation")

var processLocks sync.Map // lock path (case-folded on Windows) -> chan struct{} of capacity 1

func processLock(path string) chan struct{} {
	key := processLockKey(path)
	if existing, ok := processLocks.Load(key); ok {
		return existing.(chan struct{})
	}
	created, _ := processLocks.LoadOrStore(key, make(chan struct{}, 1))
	return created.(chan struct{})
}

// fileLockPoll is how often a held OS lock is retried (clio: 25 ms for the store, 50 ms for settings).
type fileLock struct {
	release func()
	gate    chan struct{}
}

func (l *fileLock) Unlock() {
	if l == nil {
		return
	}
	l.release()
	<-l.gate
}

// acquireFileLock waits up to timeout for the process gate and then for the OS lock, like clio's
// Monitor.TryEnter(timeout) followed by its FileShare.None retry loop with a fresh deadline.
func acquireFileLock(path string, timeout, poll time.Duration) (*fileLock, error) {
	gate := processLock(path)
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case gate <- struct{}{}:
	case <-timer.C:
		return nil, errLockBusy
	}
	deadline := time.Now().Add(timeout)
	for {
		release, busy, err := tryOSLock(path)
		if err == nil {
			return &fileLock{release: release, gate: gate}, nil
		}
		if !busy || time.Now().After(deadline) {
			<-gate
			if busy {
				return nil, errLockBusy
			}
			return nil, err
		}
		time.Sleep(poll)
	}
}

// tryFileLock takes the lock only if nobody holds it (clio's Monitor.TryEnter() plus one open attempt).
func tryFileLock(path string) (*fileLock, error) {
	gate := processLock(path)
	select {
	case gate <- struct{}{}:
	default:
		return nil, errLockBusy
	}
	release, busy, err := tryOSLock(path)
	if err != nil {
		<-gate
		if busy {
			return nil, errLockBusy
		}
		return nil, err
	}
	return &fileLock{release: release, gate: gate}, nil
}
