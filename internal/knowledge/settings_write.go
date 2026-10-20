package knowledge

// clio's SettingsRepository.UpdateSettingsIfChanged, ported: every change of appsettings.json happens
// under <appsettings.json>.lock (the FileShare.None lock every clio process takes, 60 s timeout), reads
// the latest file, applies the mutation, writes a uniquely named temporary file beside it with the file's
// own permissions, checks that nobody (an editor outside the lock) changed the file since it was read and
// publishes the temporary file over it in one atomic replace. A changed file is re-read and the mutation
// re-applied, at most three times, as clio does.

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

const (
	settingsLockTimeout   = 60 * time.Second
	settingsLockPoll      = 50 * time.Millisecond
	settingsUpdateAttempt = 3
	// settingsPublishWindow is clio's AtomicPublishRetry.DefaultWindow: how long a contended publish
	// (Windows readers holding the file) keeps retrying.
	settingsPublishWindow = 5 * time.Second
)

var errSettingsChanged = errors.New("appsettings.json changed while it was being updated")

// SettingsMutation edits the parsed settings in place and reports whether anything changed.
type SettingsMutation func(root *JSONObject) (changed bool, err error)

// UpdateSettings applies mutation to the clio settings file at path under clio's settings lock.
func UpdateSettings(path string, mutation SettingsMutation) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o777); err != nil {
		return err
	}
	lock, err := acquireFileLock(path+".lock", settingsLockTimeout, settingsLockPoll)
	if errors.Is(err, errLockBusy) {
		return fmt.Errorf("Timed out waiting to update %s. Another clio process may still be updating settings.", path)
	} else if err != nil {
		return err
	}
	defer lock.Unlock()
	for attempt := 0; attempt < settingsUpdateAttempt; attempt++ {
		expected, root, err := loadSettingsForUpdate(path)
		if err != nil {
			if attempt < settingsUpdateAttempt-1 {
				time.Sleep(10 * time.Millisecond)
				continue
			}
			return fmt.Errorf("Cannot update settings because appsettings.json changed to unreadable content. Fix or delete %s and retry.", path)
		}
		changed, err := mutation(root)
		if err != nil || !changed {
			return err
		}
		err = saveSettings(path, root, expected)
		if errors.Is(err, errSettingsChanged) {
			if attempt == settingsUpdateAttempt-1 {
				return errors.New("appsettings.json kept changing while clio was updating it. Try the command again.")
			}
			continue
		}
		return err
	}
	return errors.New("Settings update retry loop ended unexpectedly.")
}

// loadSettingsForUpdate returns the exact bytes read (nil when the file does not exist) and their parse.
func loadSettingsForUpdate(path string) ([]byte, *JSONObject, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, NewJSONObject(), nil
	}
	if err != nil {
		return nil, nil, err
	}
	if len(data) > maximumSettingsBytes {
		return nil, nil, errors.New("Clio appsettings is outside the supported size bounds.")
	}
	root, err := ParseJSONObject(data)
	if err != nil {
		return nil, nil, err
	}
	return data, root, nil
}

func saveSettings(path string, root *JSONObject, expected []byte) error {
	data, err := root.MarshalIndented()
	if err != nil {
		return err
	}
	mode := fs.FileMode(0o600)
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("Refusing to replace symbolic-link settings file '%s'.", path)
		}
		mode = info.Mode().Perm()
	}
	temporary := filepath.Join(filepath.Dir(path), ".appsettings.json."+newGUID()+".tmp")
	defer func() { _ = os.Remove(temporary) }()
	file, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return err
	}
	if expected == nil {
		if _, err := os.Lstat(path); err == nil {
			return errSettingsChanged
		}
	} else {
		current, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(current, expected) {
			return errSettingsChanged
		}
	}
	return publishWithRetry(temporary, path)
}

// publishWithRetry is clio's PublishSettingsFile: contention (a Windows reader on the target) is retried
// for a bounded window with a growing backoff; any other failure surfaces at once.
func publishWithRetry(temporary, path string) error {
	started := time.Now()
	for attempt := 1; ; attempt++ {
		err := replaceFile(temporary, path)
		if err == nil || !isPublishContention(err) || time.Since(started) >= settingsPublishWindow {
			return err
		}
		delay := min(15*attempt, 100)
		if delay == 100 {
			delay += int(time.Now().UnixNano() % 25)
		}
		time.Sleep(time.Duration(delay) * time.Millisecond)
	}
}
