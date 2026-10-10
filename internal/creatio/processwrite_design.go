package creatio

// create-business-process, modify-business-process and modify-business-process-as-new-version: clio's
// CreateBusinessProcessCommand, ModifyBusinessProcessCommand and ModifyProcessAsNewVersionCommand with their
// services, behind the CrtProcessBuilder gate (1.6.6.77).

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ProcessWriteBuildRequest is create-business-process after its argument checks: the descriptor JSON text
// and an optional package override.
type ProcessWriteBuildRequest struct {
	Environment string
	Descriptor  string
	PackageName string
}

// ProcessWriteModifyRequest is modify-business-process and modify-business-process-as-new-version after
// their argument checks. Operations is the array's JSON text ("" for none on a new version).
type ProcessWriteModifyRequest struct {
	Environment         string
	ProcessName         string
	ProcessUID          string
	Operations          string
	ConfirmLayoutChange bool
	PackageName         string
}

const (
	processWriteBuildUnknown = "BuildProcess returned a response clio could not read, so whether the process was created is " +
		"UNKNOWN — re-read it with describe-business-process before deciding what to do. The parser " +
		"detail is on the inner exception; it names a .NET type and a byte offset, which helps a " +
		"developer reading a stack trace and is noise to the caller reading this."
	processWriteModifyUnknown = "ModifyProcess returned a response clio could not read, so whether the edit was applied is " +
		"UNKNOWN — re-read the process with describe-business-process before deciding what to do. " +
		"The parser detail is on the inner exception; it names a .NET type and a byte offset, which " +
		"helps a developer reading a stack trace and is noise to the caller reading this."
	processWriteVersionUnknown = "ModifyProcessAsNewVersion returned a response clio could not read, so whether a version was " +
		"created is UNKNOWN - and a version that DID persist cannot be deleted. Re-read the process " +
		"with describe-business-process and check its versions[] before retrying, because a retry " +
		"would allocate a second version rather than replace the first. The parser detail is on the " +
		"inner exception; it names a .NET type and a byte offset, which helps a developer reading a " +
		"stack trace and is noise to the caller reading this."
)

// processWriteGated runs a command body behind the 1.6.6.77 gate; a body error is the command's catch: the
// message as an Error line and exit code 1.
func (c *Client) processWriteGated(ctx context.Context, body func(log *processWriteLog) error) ProcessWriteResult {
	refusal, err := c.processWriteGate(ctx, processWriteDesignMinVersion)
	if err != nil {
		return processWriteGateFailure(err)
	}
	if refusal != "" {
		return ProcessWriteFailure(refusal)
	}
	log := &processWriteLog{}
	if err := body(log); err != nil {
		log.error(err.Error())
		return log.result(1)
	}
	return log.result(0)
}

// CreateBusinessProcess is create-business-process: parse the descriptor, apply the package override, check
// its pre-configured pages, BuildProcess, then report the created schema and the server's warnings.
func (c *Client) CreateBusinessProcess(ctx context.Context, request ProcessWriteBuildRequest) ProcessWriteResult {
	return c.processWriteGated(ctx, func(log *processWriteLog) error {
		if strings.TrimSpace(request.Descriptor) == "" {
			return errors.New("A process descriptor is required.")
		}
		descriptor, err := parseJNode([]byte(request.Descriptor))
		if err != nil {
			return fmt.Errorf("Process descriptor is not valid JSON: %v", err)
		}
		if !descriptor.isObject() {
			return errors.New("Process descriptor must be a JSON object.")
		}
		if strings.TrimSpace(request.PackageName) != "" {
			descriptor.set("packageName", newString(request.PackageName))
		}
		if err := c.processWritePageGate(ctx, descriptor, log); err != nil {
			return err
		}
		log.info(fmt.Sprintf("Building process '%s' on '%s'...", processWriteNodeText(descriptor.get("name")), request.Environment))
		result, err := c.processWriteDesign(ctx, "BuildProcess", descriptor, processWriteBuildUnknown)
		if err != nil {
			return err
		}
		fields := processWriteFields{node: result}
		success := fields.boolean("success")
		schemaUID := fields.text("schemaUId")
		schemaName := fields.text("schemaName")
		message := fields.text("errorMessage")
		warnings := fields.texts("warnings")
		if fields.bad {
			return errors.New(processWriteBuildUnknown)
		}
		if !success {
			if message != nil {
				return errors.New(*message)
			}
			return errors.New("BuildProcess failed.")
		}
		log.info(fmt.Sprintf("Process '%s' created (UId: %s).", processWriteDeref(schemaName), processWriteDeref(schemaUID)))
		for _, warning := range warnings {
			log.warning(warning)
		}
		processWriteGuardVerification(log, "The process was created, but verifying its configuration failed: %s. Re-read it with describe-business-process before reporting a grant or revoke as applied.",
			func() { c.processWriteVerifyCreate(ctx, request.Descriptor, processWriteDeref(schemaName), log) })
		return nil
	})
}

// ModifyBusinessProcess is modify-business-process: the operations go to ModifyProcess with the identity
// and confirmLayoutChange; a refusal names the failed operation and relays a diagram re-draw.
func (c *Client) ModifyBusinessProcess(ctx context.Context, request ProcessWriteModifyRequest) ProcessWriteResult {
	return c.processWriteGated(ctx, func(log *processWriteLog) error {
		if err := processWriteCommandIdentity(request); err != nil {
			return err
		}
		if strings.TrimSpace(request.Operations) == "" {
			return errors.New("An operations array is required.")
		}
		target := processWriteIdentity(request)
		operations, err := processWriteParseOperations(request.Operations)
		if err != nil {
			return err
		}
		target.set("operations", operations)
		target.set("confirmLayoutChange", newBool(request.ConfirmLayoutChange))
		if err := c.processWritePageGate(ctx, operations, log); err != nil {
			return err
		}
		log.info(fmt.Sprintf("Editing process '%s' on '%s'...", processWriteIdentityText(request), request.Environment))
		result, err := c.processWriteDesign(ctx, "ModifyProcess", target, processWriteModifyUnknown)
		if err != nil {
			return err
		}
		fields := processWriteFields{node: result}
		success := fields.boolean("success")
		schemaUID := fields.text("schemaUId")
		schemaName := fields.text("schemaName")
		applied := fields.integer("appliedOperations")
		failedIndex := fields.optionalInt("failedOperationIndex")
		message := fields.text("errorMessage")
		warnings := fields.texts("warnings")
		layout := fields.layoutChange()
		if fields.bad {
			return errors.New(processWriteModifyUnknown)
		}
		if !success {
			text := "ModifyProcess failed."
			if message != nil {
				text = *message
			}
			if failedIndex != nil {
				text += fmt.Sprintf(" The operation at index %d is the one that refused.", *failedIndex)
			}
			if layout.present {
				text += layout.relaySentence()
			}
			return errors.New(text)
		}
		log.info(fmt.Sprintf("Process '%s' edited (%d operation(s) applied; UId: %s).", processWriteDeref(schemaName), applied, processWriteDeref(schemaUID)))
		for _, warning := range warnings {
			log.warning(warning)
		}
		processWriteGuardVerification(log, "The edit was applied, but verifying its configuration failed: %s. Re-read the process with describe-business-process before reporting a grant or revoke as applied.",
			func() { c.processWriteVerifyModify(ctx, request, processWriteDeref(schemaName), log) })
		return nil
	})
}

// ModifyBusinessProcessAsNewVersion is modify-business-process-as-new-version: the operations (none is a
// snapshot) go to ModifyProcessAsNewVersion; the answer names the version and whether it is active.
func (c *Client) ModifyBusinessProcessAsNewVersion(ctx context.Context, request ProcessWriteModifyRequest) ProcessWriteResult {
	return c.processWriteGated(ctx, func(log *processWriteLog) error {
		if err := processWriteCommandIdentity(request); err != nil {
			return err
		}
		target := processWriteIdentity(request)
		if strings.TrimSpace(request.PackageName) != "" {
			target.set("packageName", newString(request.PackageName))
		}
		operations := newArray()
		if strings.TrimSpace(request.Operations) != "" {
			parsed, err := processWriteParseOperations(request.Operations)
			if err != nil {
				return err
			}
			operations = parsed
		}
		target.set("operations", operations)
		if err := c.processWritePageGate(ctx, operations, log); err != nil {
			return err
		}
		log.info(fmt.Sprintf("Saving a new version of process '%s' on '%s'...", processWriteIdentityText(request), request.Environment))
		result, err := c.processWriteDesign(ctx, "ModifyProcessAsNewVersion", target, processWriteVersionUnknown)
		if err != nil {
			return err
		}
		fields := processWriteFields{node: result}
		success := fields.boolean("success")
		message := fields.text("errorMessage")
		versionUID := fields.text("versionSchemaUId")
		versionName := fields.text("versionName")
		version := fields.optionalInt("version")
		active := fields.optionalBool("isActiveVersion")
		rootUID := fields.text("versionRootSchemaUId")
		applied := fields.integer("appliedOperations")
		failedIndex := fields.optionalInt("failedOperationIndex")
		warnings := fields.texts("warnings")
		layout := fields.layoutChange()
		if fields.bad {
			return errors.New(processWriteVersionUnknown)
		}
		if !success {
			text := "ModifyProcessAsNewVersion failed."
			if message != nil {
				text = *message
			}
			if failedIndex != nil {
				text += fmt.Sprintf(" The operation at index %d is the one that refused.", *failedIndex)
			}
			if applied > 0 {
				text += fmt.Sprintf(" %d operation(s) had been applied to the version draft.", applied)
			}
			if layout.present {
				text += layout.elementsClause()
			}
			if strings.TrimSpace(processWriteDeref(versionName)) != "" || strings.TrimSpace(processWriteDeref(versionUID)) != "" {
				text += fmt.Sprintf(" The version '%s' (UId: %s) WAS created and still exists — a version cannot be deleted.",
					processWriteDeref(versionName), processWriteDeref(versionUID))
			}
			if len(warnings) > 0 {
				text += " " + strings.Join(warnings, " ")
			}
			return errors.New(text)
		}
		label := "A new version"
		if version != nil {
			label = fmt.Sprintf("Version %d", *version)
		}
		log.info(fmt.Sprintf("%s '%s' created (%d operation(s) applied; UId: %s; family root: %s).", label,
			processWriteDeref(versionName), applied, processWriteDeref(versionUID), processWriteDeref(rootUID)))
		switch {
		case active == nil:
			log.info("The environment did not report which version is actual — verify with describe-business-process before reporting what runs.")
		case *active:
			log.info("This version is reported ACTIVE — unexpected for a create; verify with describe-business-process.")
		default:
			log.info("The source version is still the actual one and keeps running. ASK THE USER to open " +
				"this version and say whether to make it actual; call " +
				"set-active-business-process-version only once they have. Do not chain the two - a " +
				"version is created inactive precisely so they get to look first.")
		}
		if clause := layout.elementsClause(); layout.present && strings.TrimSpace(clause) != "" {
			log.warning("The diagram of this version is laid out afresh." + clause)
		}
		for _, warning := range warnings {
			log.warning(warning)
		}
		return nil
	})
}

// processWriteGuardVerification runs the post-write check the way the commands do: a failure inside it is
// a warning, never a failed write.
func processWriteGuardVerification(log *processWriteLog, format string, verify func()) {
	defer func() {
		if recovered := recover(); recovered != nil {
			log.warning(fmt.Sprintf(format, fmt.Sprint(recovered)))
		}
	}()
	verify()
}

// processWritePageGate runs the pre-configured page check: its refusal fails the command, its warnings are
// logged before the request.
func (c *Client) processWritePageGate(ctx context.Context, payload *jnode, log *processWriteLog) error {
	refusal, warnings := c.processWriteCheckPages(ctx, payload)
	if refusal != "" {
		return errors.New(refusal)
	}
	for _, warning := range warnings {
		log.warning(warning)
	}
	return nil
}

// processWriteCommandIdentity is the command's own one-identity check (the tool has already refused the
// same mistakes in its own words).
func processWriteCommandIdentity(request ProcessWriteModifyRequest) error {
	hasName, hasUID := strings.TrimSpace(request.ProcessName) != "", strings.TrimSpace(request.ProcessUID) != ""
	if hasName == hasUID {
		if hasName {
			return errors.New("Provide only one of --name or --uid, not both.")
		}
		return errors.New("One of --name or --uid is required.")
	}
	return nil
}

func processWriteIdentity(request ProcessWriteModifyRequest) *jnode {
	target := newObject()
	if strings.TrimSpace(request.ProcessName) != "" {
		target.set("name", newString(request.ProcessName))
	}
	if strings.TrimSpace(request.ProcessUID) != "" {
		target.set("uid", newString(request.ProcessUID))
	}
	return target
}

func processWriteIdentityText(request ProcessWriteModifyRequest) string {
	if strings.TrimSpace(request.ProcessName) == "" {
		return request.ProcessUID
	}
	return request.ProcessName
}

// processWriteParseOperations is the services' ParseOperations: valid JSON that is an array.
func processWriteParseOperations(text string) (*jnode, error) {
	operations, err := parseJNode([]byte(text))
	if err != nil {
		return nil, fmt.Errorf("Operations content is not valid JSON: %v", err)
	}
	if !operations.isArray() {
		return nil, errors.New("Operations content must be a JSON array of operations.")
	}
	return operations, nil
}
