package knowledge

import (
	"errors"
	"strings"
)

// FeedbackUpdate is clio's KnowledgeFeedbackPolicyUpdate; nil members were not supplied.
type FeedbackUpdate struct {
	Mode           *string
	Destination    *string
	ReportingScope *string
}

// FeedbackConsent is clio's KnowledgeFeedbackConsent: the snapshot the user approved.
type FeedbackConsent struct {
	PolicyHash     string
	Destination    string
	ReportingScope string
}

// ConfigureFeedback is clio's KnowledgeFeedbackPolicyService.Configure: it runs inside the settings update,
// against the latest knowledge-feedback section. policyHash is the current reporting-policy hash, or nil.
func ConfigureFeedback(settingsPath string, update FeedbackUpdate, requireConsent bool, consent *FeedbackConsent, policyHash *string) error {
	_, err := UpdateFeedbackSettings(settingsPath, func(current *FeedbackSettings) error {
		modeSupplied := update.Mode != nil && strings.TrimSpace(*update.Mode) != ""
		var mode string
		var err error
		if modeSupplied {
			mode, err = normalizeMode(*update.Mode)
		} else {
			mode, err = normalizeMode(current.Mode)
		}
		if err != nil {
			return err
		}
		disables := mode == FeedbackOff
		destination := current.Destination
		switch {
		case update.Destination != nil:
			if destination, err = NormalizeDestination(*update.Destination); err != nil {
				return err
			}
		case !disables:
			if destination, err = NormalizeDestination(current.Destination); err != nil {
				return err
			}
		}
		scope := current.ReportingScope
		switch {
		case update.ReportingScope != nil:
			if scope, err = normalizeScope(*update.ReportingScope); err != nil {
				return err
			}
		case !disables:
			if scope, err = normalizeScope(current.ReportingScope); err != nil {
				return err
			}
		}
		selectsAuto := modeSupplied && mode == FeedbackAuto
		retargetsAuto := strings.EqualFold(strings.TrimSpace(current.Mode), FeedbackAuto) && (!modeSupplied || selectsAuto) &&
			(update.Destination != nil || update.ReportingScope != nil)
		if requireConsent && (selectsAuto || retargetsAuto) {
			if consent == nil {
				return errors.New("Selecting auto or changing its destination/reporting scope requires confirmed=true and the exact policy snapshot shown to the user.")
			}
			approvedDestination, err := NormalizeDestination(consent.Destination)
			if err != nil {
				return err
			}
			approvedScope, err := normalizeScope(consent.ReportingScope)
			if err != nil {
				return err
			}
			if policyHash == nil || consent.PolicyHash != *policyHash || approvedDestination != destination || approvedScope != scope {
				return errors.New("The knowledge-feedback policy changed after it was shown. Read the current policy and ask for approval again.")
			}
		}
		current.Mode, current.Destination, current.ReportingScope = mode, destination, scope
		if selectsAuto {
			if policyHash == nil {
				return errors.New("Cannot approve automatic feedback because '" + FeedbackGuidanceName + "' guidance is unavailable.")
			}
			current.HasStandingApproval, current.StandingApprovalHash = true, *policyHash
		} else if modeSupplied {
			current.HasStandingApproval, current.StandingApprovalHash = false, ""
		}
		return nil
	})
	return err
}
