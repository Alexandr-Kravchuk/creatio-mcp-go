package clioconfig

// The SettingsRepository operations reg-web-app uses: find an environment, register or update one, set the
// active one. Every write goes through knowledge.UpdateSettings (clio's lock file, re-read under the lock,
// atomic replace) after the same bootstrap clio's LoadLatestSettings runs, and is refused where clio refuses
// it: while the file is broken or a member fails to bind.

import (
	"errors"
	"strings"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/knowledge"
)

// Find is SettingsRepository.FindEnvironment for a name: the first registered environment whose name
// matches ignoring case, as the file holds it now. ok is false when there is none or the file cannot be read.
func Find(path, name string) (key string, environment Environment, ok bool) {
	if strings.TrimSpace(name) == "" {
		return "", Environment{}, false
	}
	root, usable := readUsable(path)
	if !usable {
		return "", Environment{}, false
	}
	environments := environmentsOf(root)
	if environments == nil {
		return "", Environment{}, false
	}
	for _, candidate := range environments.Keys() {
		if strings.EqualFold(candidate, name) {
			value, _ := environments.Get(candidate)
			return candidate, ParseEnvironment(value), true
		}
	}
	return "", Environment{}, false
}

// Exists is SettingsRepository.IsEnvironmentExists.
func Exists(path, name string) bool {
	_, _, ok := Find(path, name)
	return ok
}

// readUsable reads the file as the repository's model holds it: nothing when it is missing, broken, or an
// environment fails to bind.
func readUsable(path string) (*knowledge.JSONObject, bool) {
	report, err := Check(path, false)
	if err != nil || report.Status == statusBroken || report.Status == statusFileMissing {
		return nil, false
	}
	data, err := readFile(path)
	if err != nil {
		return nil, false
	}
	root, failure := parseSettings(data)
	return root, failure == ""
}

// Configure is SettingsRepository.ConfigureEnvironment for a named environment: a name that is not
// registered (compared exactly, as the dictionary does) is added, and becomes the active environment when
// none is set; a registered one is merged with the update.
func Configure(path, name string, update Environment) error {
	if err := prepareWrite(path); err != nil {
		return err
	}
	return knowledge.UpdateSettings(path, func(root *knowledge.JSONObject) (bool, error) {
		if err := refuseUnbindable(path, root); err != nil {
			return false, err
		}
		environments := ensureEnvironments(root)
		normalizeEnvironments(root)
		if stored, exists := environments.Get(name); exists {
			merged := ParseEnvironment(stored)
			merged.Merge(update)
			environments.Set(name, merged.Object())
			return true, nil
		}
		environments.Set(name, update.Object())
		if active := activeEnvironmentKey(root); active == nil || strings.TrimSpace(*active) == "" {
			setRootMember(root, "ActiveEnvironmentKey", name)
		}
		return true, nil
	})
}

// SetActive is SettingsRepository.SetActiveEnvironment: the name is written as given.
func SetActive(path, name string) error {
	if err := prepareWrite(path); err != nil {
		return err
	}
	return knowledge.UpdateSettings(path, func(root *knowledge.JSONObject) (bool, error) {
		if err := refuseUnbindable(path, root); err != nil {
			return false, err
		}
		normalizeEnvironments(root)
		setRootMember(root, "ActiveEnvironmentKey", name)
		return true, nil
	})
}

// normalizeEnvironments rewrites every registered environment the way clio's save does, since clio writes
// the whole file from its model: declared members in order with their defaults, values in their bound types.
// A null entry stays null.
func normalizeEnvironments(root *knowledge.JSONObject) {
	environments := environmentsOf(root)
	if environments == nil {
		return
	}
	for _, key := range environments.Keys() {
		if value, _ := environments.Get(key); value != nil {
			environments.Set(key, ParseEnvironment(value).Object())
		}
	}
}

// prepareWrite is LoadLatestSettings: the repairing bootstrap first (a missing file is created, a pending
// migration written), then clio's refusal for a file it must not rewrite.
func prepareWrite(path string) error {
	report, err := Check(path, true)
	if err != nil {
		return err
	}
	return writeRefusal(path, report)
}

func writeRefusal(path string, report Report) error {
	if mismatch := report.ShapeMismatch(); mismatch != nil {
		return errors.New("Cannot update settings (" + codeShapeMismatch + "): " + mismatch.Message +
			" Until then clio writes nothing at all - including its own automatic update schedule - so " +
			"'clio update-cli' is the way out when no resident process is holding the old build.")
	}
	if report.Status == statusBroken {
		issue := "appsettings.json is unreadable."
		if len(report.Issues) > 0 {
			issue = report.Issues[0].Message
		}
		return errors.New("Cannot update settings because " + issue + " Fix or delete " + path + " and retry.")
	}
	return nil
}

// refuseUnbindable repeats the refusal on the content the lock holder actually reads.
func refuseUnbindable(path string, root *knowledge.JSONObject) error {
	bound := bindSettings(root)
	if len(bound.failures) == 0 {
		return nil
	}
	if environmentPath := firstEnvironmentFailure(bound.failures); environmentPath != "" {
		return writeRefusal(path, broken(path, codeShapeMismatch, environmentBindFailureMessage(environmentPath)))
	}
	return writeRefusal(path, Report{Issues: []Issue{{codeShapeMismatch, shapeMismatchMessage(bound, settingsVersion(root))}}})
}

func ensureEnvironments(root *knowledge.JSONObject) *knowledge.JSONObject {
	key, value, found := memberFold(root, "Environments")
	if environments, ok := value.(*knowledge.JSONObject); ok {
		return environments
	}
	if !found {
		key = "Environments"
	}
	environments := knowledge.NewJSONObject()
	root.Set(key, environments)
	return environments
}

// setRootMember sets a declared top-level member under the name the file already uses for it.
func setRootMember(root *knowledge.JSONObject, name string, value any) {
	key, _, found := memberFold(root, name)
	if !found {
		key = name
	}
	root.Set(key, value)
}
