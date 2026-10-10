package creatio

import (
	"context"
	"fmt"
	"strings"
)

// ProcessWriteActivateRequest names the family member set-active-business-process-version makes actual:
// exactly one of the two.
type ProcessWriteActivateRequest struct {
	Environment string
	VersionName string
	VersionUID  string
}

const processWriteActivateUnknown = "SetActiveProcessVersion returned a response clio could not read, so WHICH version the " +
	"environment now runs is UNKNOWN - the write may have taken effect. Read the family back with " +
	"describe-business-process and check isActiveVersion before retrying or reporting this. The " +
	"parser detail is on the inner exception; it names a .NET type and a byte offset, which helps " +
	"a developer reading a stack trace and is noise to the caller reading this."

// SetActiveBusinessProcessVersion is clio's SetActiveProcessVersionTool behind its argument checks: the
// CrtProcessBuilder gate (1.6.1.0), then SetActiveProcessVersionCommand with its service.
func (c *Client) SetActiveBusinessProcessVersion(ctx context.Context, request ProcessWriteActivateRequest) ProcessWriteResult {
	refusal, err := c.processWriteGate(ctx, processWriteActivateMinVersion)
	if err != nil {
		return processWriteGateFailure(err)
	}
	if refusal != "" {
		return ProcessWriteFailure(refusal)
	}
	log := &processWriteLog{}
	if err := c.processWriteActivate(ctx, request, log); err != nil {
		log.error(err.Error())
		return log.result(1)
	}
	return log.result(0)
}

func (c *Client) processWriteActivate(ctx context.Context, request ProcessWriteActivateRequest, log *processWriteLog) error {
	hasName, hasUID := strings.TrimSpace(request.VersionName) != "", strings.TrimSpace(request.VersionUID) != ""
	if hasName == hasUID {
		if hasName {
			return fmt.Errorf("Provide only one of --name or --uid, not both.")
		}
		return fmt.Errorf("One of --name or --uid is required.")
	}
	target := newObject()
	identity := request.VersionName
	if hasName {
		target.set("name", newString(request.VersionName))
	} else {
		target.set("uid", newString(request.VersionUID))
		identity = request.VersionUID
	}
	log.info(fmt.Sprintf("Activating version '%s' on '%s'...", identity, request.Environment))
	result, err := c.processWriteDesign(ctx, "SetActiveProcessVersion", target, processWriteActivateUnknown)
	if err != nil {
		return err
	}
	fields := processWriteFields{node: result}
	success := fields.boolean("success")
	message := fields.text("errorMessage")
	activeUID := fields.text("activeVersionSchemaUId")
	activeName := fields.text("activeVersionName")
	failures := fields.integer("deactivationFailureCount")
	warnings := fields.texts("warnings")
	if fields.bad {
		return fmt.Errorf("%s", processWriteActivateUnknown)
	}
	if !success {
		text := "SetActiveProcessVersion failed."
		if message != nil {
			text = *message
		}
		if strings.TrimSpace(processWriteDeref(activeName)) != "" || strings.TrimSpace(processWriteDeref(activeUID)) != "" {
			text += fmt.Sprintf(" The version the environment reports as actual is '%s' (UId: %s).", processWriteDeref(activeName), processWriteDeref(activeUID))
		}
		if failures > 0 {
			text += fmt.Sprintf(" %d sibling(s) are ALSO still flagged active, so which one runs is decided by package order rather than by this call.", failures)
		}
		if len(warnings) > 0 {
			text += " " + strings.Join(warnings, " ")
		}
		return fmt.Errorf("%s", text)
	}
	log.info(fmt.Sprintf("Version '%s' is now the actual one (UId: %s).", processWriteDeref(activeName), processWriteDeref(activeUID)))
	log.info("New process instances will start on this version. Instances already running stay on the version " +
		"they started with — activation never migrates them.")
	if failures > 0 {
		log.warning(fmt.Sprintf("%d sibling(s) are ALSO still flagged active, so which one runs "+
			"is decided by package order rather than by this call. Read the family back with "+
			"describe-business-process before reporting the rollback as done.", failures))
	}
	if !processWriteActivationEstablished(request, activeName, activeUID) {
		log.warning("The activation was accepted, but the environment did not report which version is actual, so " +
			"clio could NOT confirm the change took effect. Read the family back with " +
			"describe-business-process and check isActiveVersion before reporting this as done.")
	} else if !processWriteActivationMatches(request, activeName, activeUID) {
		log.warning(fmt.Sprintf("The environment reports '%s' (UId: %s) as "+
			"the actual version, which is NOT the version this call asked for. The activation was accepted "+
			"but the version that runs is not the one requested.", processWriteDeref(activeName), processWriteDeref(activeUID)))
	}
	for _, warning := range warnings {
		log.warning(warning)
	}
	return nil
}

// processWriteActivationEstablished is ReadBackEstablished: a name request needs a reported name, a uid
// request a reported uid that parses as a GUID.
func processWriteActivationEstablished(request ProcessWriteActivateRequest, name, uid *string) bool {
	if strings.TrimSpace(request.VersionName) != "" {
		return strings.TrimSpace(processWriteDeref(name)) != ""
	}
	return processWriteParseGUID(processWriteDeref(uid)) != ""
}

// processWriteActivationMatches is ReadBackMatchesRequest.
func processWriteActivationMatches(request ProcessWriteActivateRequest, name, uid *string) bool {
	if strings.TrimSpace(request.VersionName) != "" {
		return name != nil && strings.EqualFold(*name, strings.TrimSpace(request.VersionName))
	}
	requested, reported := processWriteParseGUID(request.VersionUID), processWriteParseGUID(processWriteDeref(uid))
	if requested == "" || reported == "" {
		return true
	}
	return requested == reported
}

// processWriteParseGUID is Guid.TryParse: the five forms .NET accepts (D, N, B, P, X is not needed here),
// surrounding white space allowed; it returns the lower-case D form, or "" when the text is no GUID.
func processWriteParseGUID(value string) string {
	text := strings.TrimSpace(value)
	if len(text) >= 2 && ((text[0] == '{' && text[len(text)-1] == '}') || (text[0] == '(' && text[len(text)-1] == ')')) {
		text = text[1 : len(text)-1]
		if len(text) != 36 {
			return ""
		}
	}
	switch len(text) {
	case 32:
		for _, r := range text {
			if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
				return ""
			}
		}
		text = text[:8] + "-" + text[8:12] + "-" + text[12:16] + "-" + text[16:20] + "-" + text[20:]
	case 36:
		for index, r := range text {
			if index == 8 || index == 13 || index == 18 || index == 23 {
				if r != '-' {
					return ""
				}
				continue
			}
			if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
				return ""
			}
		}
	default:
		return ""
	}
	return strings.ToLower(text)
}
