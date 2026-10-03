package knowledge

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"strings"
)

// Feedback policy modes and scopes (KnowledgeFeedbackPolicyService).
const (
	FeedbackGuidanceName = "knowledge-feedback"
	FeedbackAsk          = "ask"
	FeedbackAuto         = "auto"
	FeedbackOff          = "off"
	scopeFull            = "full"
	scopeSanitized       = "sanitized"
)

// FeedbackPolicy is clio's KnowledgeFeedbackPolicy.
type FeedbackPolicy struct {
	ConfiguredMode      string  `json:"configuredMode"`
	EffectiveMode       string  `json:"effectiveMode"`
	Destination         string  `json:"destination"`
	ReportingScope      string  `json:"reportingScope"`
	ReportingPolicyHash *string `json:"reportingPolicyHash"`
	StandingApproval    *struct {
		PolicyHash string `json:"policyHash"`
	} `json:"standingApproval,omitempty"`
	ApprovalState string `json:"approvalState"`
}

// FeedbackPolicy resolves the effective knowledge-feedback policy against the active reporting article.
func (r *Runtime) FeedbackPolicy() FeedbackPolicy {
	settings := defaultFeedback()
	if loaded, err := r.LoadSettings(); err == nil {
		settings = loaded.Feedback
	}
	var hash *string
	if lookup := r.FindByName(FeedbackGuidanceName); lookup.Status == LookupActive {
		value := ComputePolicyHash(lookup.Article.Text)
		hash = &value
	}
	return resolveFeedback(settings, hash)
}

// ComputePolicyHash is "sha256:" + the lower-case hex SHA-256 of the article text.
func ComputePolicyHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func resolveFeedback(settings FeedbackSettings, hash *string) FeedbackPolicy {
	approval := approvalView(settings)
	policy := func(configured, effective, destination, scope, state string) FeedbackPolicy {
		p := FeedbackPolicy{ConfiguredMode: configured, EffectiveMode: effective, Destination: destination,
			ReportingScope: scope, ReportingPolicyHash: hash, ApprovalState: state}
		if approval != "" {
			p.StandingApproval = &struct {
				PolicyHash string `json:"policyHash"`
			}{approval}
		}
		return p
	}
	configured, err := normalizeMode(settings.Mode)
	if err != nil {
		return policy(settings.Mode, FeedbackAsk, settings.Destination, settings.ReportingScope, "invalid-configuration: "+err.Error())
	}
	if configured == FeedbackOff {
		return policy(configured, FeedbackOff, settings.Destination, settings.ReportingScope, "disabled")
	}
	destination, err := NormalizeDestination(settings.Destination)
	if err == nil {
		var scope string
		scope, err = normalizeScope(settings.ReportingScope)
		if err == nil {
			if configured != FeedbackAuto {
				return policy(configured, configured, destination, scope, "ask-each-time")
			}
			switch {
			case approval == "":
				return policy(configured, FeedbackAsk, destination, scope, "approval-missing")
			case hash == nil:
				return policy(configured, FeedbackAuto, destination, scope, "approved-policy-unavailable")
			case *hash != approval:
				return policy(configured, FeedbackAsk, destination, scope, "reporting-policy-changed")
			}
			return policy(configured, FeedbackAuto, destination, scope, "approved")
		}
	}
	return policy(settings.Mode, FeedbackAsk, settings.Destination, settings.ReportingScope, "invalid-configuration: "+err.Error())
}

func approvalView(settings FeedbackSettings) string {
	hash := settings.StandingApprovalHash
	if !settings.HasStandingApproval || len(hash) != 71 || !strings.HasPrefix(hash, "sha256:") {
		return ""
	}
	for _, c := range hash[7:] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return ""
		}
	}
	return hash
}

func normalizeMode(mode string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(mode))
	if normalized == FeedbackAsk || normalized == FeedbackAuto || normalized == FeedbackOff {
		return normalized, nil
	}
	return "", errors.New("Knowledge-feedback mode must be ask, auto, or off. (Parameter 'mode')")
}

func normalizeScope(scope string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(scope))
	if normalized == scopeFull || normalized == scopeSanitized {
		return normalized, nil
	}
	return "", errors.New("Knowledge-feedback reporting scope must be full or sanitized. (Parameter 'reportingScope')")
}

// NormalizeDestination accepts a credential-free HTTPS owner/repository URL.
func NormalizeDestination(destination string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(destination))
	if err != nil || !parsed.IsAbs() || !strings.EqualFold(parsed.Scheme, "https") || parsed.Hostname() == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return "", errors.New("Knowledge-feedback destination must be a credential-free HTTPS GitHub repository URL. (Parameter 'destination')")
	}
	path := strings.Trim(parsed.Path, "/")
	if strings.HasSuffix(strings.ToLower(path), ".git") {
		path = path[:len(path)-4]
	}
	segments := 0
	for _, segment := range strings.Split(path, "/") {
		if segment != "" {
			segments++
		}
	}
	if segments != 2 {
		return "", errors.New("Knowledge-feedback destination must identify a repository as owner/repository. (Parameter 'destination')")
	}
	authority := parsed.Hostname()
	if port := parsed.Port(); port != "" && port != "443" {
		authority += ":" + port
	}
	return "https://" + strings.ToLower(authority) + "/" + path, nil
}
