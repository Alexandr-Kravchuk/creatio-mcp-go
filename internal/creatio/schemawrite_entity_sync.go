package creatio

// sync-schemas: clio's SchemaSyncTool with its SchemaConvergenceService. A batch of create-lookup,
// create-entity, update-entity and seed-data operations runs in order and stops at the first failure; the
// schema operations converge (create if absent, reconcile only the missing delta, already-satisfied on a
// replay), transient network failures are retried, and an aborted batch answers a resume plan.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	schemaWriteSyncTool       = "sync-schemas"
	schemaWriteSyncLookup     = "create-lookup"
	schemaWriteSyncEntity     = "create-entity"
	schemaWriteSyncUpdate     = "update-entity"
	schemaWriteSyncSeed       = "seed-data"
	schemaWriteSyncCreated    = "created"
	schemaWriteSyncReconciled = "reconciled"
	schemaWriteSyncSatisfied  = "already-satisfied"
	schemaWriteSyncCollision  = "collision"
	schemaWriteSyncFailed     = "failed"
	schemaWriteSyncAttempts   = 3
	// schemaWriteSyncCollisionCode is clio's message-less CollisionExitCode sentinel.
	schemaWriteSyncCollisionCode = -2147483648
	schemaWriteSyncArgsHint      = "Valid fields: environment-name, package-name, operations."
	schemaWriteSyncOpHint        = "Valid operation fields: type, schema-name, title-localizations, parent-schema-name, extend-parent, " +
		"columns, update-operations, seed-rows, is-virtual, is-db-view (legacy: title)."
)

var schemaWriteSyncArgAliases = map[string]string{
	"packageName": "package-name", "package_name": "package-name", "package": "package-name", "ops": "operations",
	"operation-list": "operations", "Operations": "operations", "environmentName": "environment-name",
	"environment_name": "environment-name", "environment": "environment-name",
}

var schemaWriteSyncOpAliases = map[string]string{
	"seed-data": "seed-rows", "seedData": "seed-rows", "seed_data": "seed-rows", "seedRows": "seed-rows",
	"seed_rows": "seed-rows", "rows": "seed-rows", "values": "seed-rows", "name": "schema-name",
	"schemaName": "schema-name", "schema_name": "schema-name", "titleLocalizations": "title-localizations",
	"title_localizations": "title-localizations", "parentSchemaName": "parent-schema-name",
	"parent_schema_name": "parent-schema-name", "extendParent": "extend-parent", "extend_parent": "extend-parent",
	"updateOperations": "update-operations", "update_operations": "update-operations", "isVirtual": "is-virtual",
	"is_virtual": "is-virtual", "isDBView": "is-db-view", "is_db_view": "is-db-view",
}

var schemaWriteSyncTransientMarkers = []string{
	"no such host is known", "name or service not known", "nodename nor servname provided",
	"temporary failure in name resolution", "the remote name could not be resolved", "connection reset",
	"connection refused", "actively refused", "forcibly closed", "a connection attempt failed",
	"an existing connection was forcibly closed", "broken pipe", "timed out", "the operation has timed out",
	"a task was canceled", "an error occurred while sending the request", "the ssl connection could not be established",
	"unable to read data from the transport connection", "bad gateway", "service unavailable", "gateway timeout",
	// Go's transport wording for the same conditions.
	"no such host", "i/o timeout", "context deadline exceeded", "eof",
}

// SchemaSyncOperation is clio's SchemaSyncOperation.
type SchemaSyncOperation struct {
	Type, SchemaName, ParentSchemaName, LegacyTitle string
	TitleLocalizations                              schemaWriteEntLocMap
	ExtendParent, IsVirtual                         bool
	IsDBView                                        *bool
	Columns                                         []SchemaWriteEntColumnArgs
	HasColumns                                      bool
	UpdateOperations                                []SchemaWriteEntOperationArgs
	SeedRows                                        []any
	HasSeedRows                                     bool
	Extension                                       map[string]any
	Raw                                             map[string]any
}

// SchemaSyncArgs is clio's SchemaSyncArgs.
type SchemaSyncArgs struct {
	PackageName   string
	Operations    []SchemaSyncOperation
	HasOperations bool
	Extension     map[string]any
}

// ParseSchemaSyncArgs binds sync-schemas' arguments (environment-name already removed).
func ParseSchemaSyncArgs(args map[string]any) (SchemaSyncArgs, error) {
	b := schemaWriteEntBinder{tool: schemaWriteSyncTool}
	parsed := SchemaSyncArgs{Extension: map[string]any{}}
	var err error
	if parsed.PackageName, err = b.text(args, "package-name"); err != nil {
		return parsed, err
	}
	for key, value := range args {
		if key != "package-name" && key != "operations" && key != "environment-name" {
			parsed.Extension[key] = value
		}
	}
	items, present, err := b.list(args, "operations")
	if err != nil {
		return parsed, err
	}
	parsed.HasOperations = present
	for _, item := range items {
		operation, err := schemaWriteSyncParseOperation(b, item)
		if err != nil {
			return parsed, err
		}
		parsed.Operations = append(parsed.Operations, operation)
	}
	return parsed, nil
}

func schemaWriteSyncParseOperation(b schemaWriteEntBinder, item any) (SchemaSyncOperation, error) {
	object, err := b.object(item)
	if err != nil {
		return SchemaSyncOperation{}, err
	}
	operation := SchemaSyncOperation{Extension: map[string]any{}, Raw: object}
	for key, target := range map[string]*string{"type": &operation.Type, "schema-name": &operation.SchemaName,
		"parent-schema-name": &operation.ParentSchemaName, "title": &operation.LegacyTitle} {
		if *target, err = b.text(object, key); err != nil {
			return operation, err
		}
	}
	if operation.TitleLocalizations, err = b.locMap(object, "title-localizations"); err != nil {
		return operation, err
	}
	for key, target := range map[string]*bool{"extend-parent": &operation.ExtendParent, "is-virtual": &operation.IsVirtual} {
		flag, err := b.boolean(object, key)
		if err != nil {
			return operation, err
		}
		*target = flag != nil && *flag
	}
	if operation.IsDBView, err = b.boolean(object, "is-db-view"); err != nil {
		return operation, err
	}
	if operation.Columns, operation.HasColumns, err = b.columns(object, "columns"); err != nil {
		return operation, err
	}
	updates, _, err := b.list(object, "update-operations")
	if err != nil {
		return operation, err
	}
	for _, update := range updates {
		parsed, err := b.operation(update)
		if err != nil {
			return operation, err
		}
		operation.UpdateOperations = append(operation.UpdateOperations, parsed)
	}
	if operation.SeedRows, operation.HasSeedRows, err = b.list(object, "seed-rows"); err != nil {
		return operation, err
	}
	for _, row := range operation.SeedRows {
		if row == nil {
			continue
		}
		rowObject, ok := row.(map[string]any)
		if !ok {
			return operation, b.fail()
		}
		if values, present := rowObject["values"]; present && values != nil {
			if _, ok := values.(map[string]any); !ok {
				return operation, b.fail()
			}
		}
	}
	known := map[string]bool{"type": true, "schema-name": true, "title-localizations": true, "parent-schema-name": true,
		"extend-parent": true, "columns": true, "update-operations": true, "seed-rows": true, "is-virtual": true,
		"is-db-view": true, "title": true}
	for key, value := range object {
		if !known[key] {
			operation.Extension[key] = value
		}
	}
	return operation, nil
}

// SchemaSyncTerms returns the candidate terms and lookup hints sync-schemas enriches with.
func SchemaSyncTerms(args SchemaSyncArgs) ([]string, []string) {
	var terms, hints []string
	for _, operation := range args.Operations {
		terms = append(terms, operation.SchemaName)
	}
	for _, operation := range args.Operations {
		terms = append(terms, operation.TitleLocalizations.values()...)
	}
	for _, operation := range args.Operations {
		if operation.Type == schemaWriteSyncLookup {
			hints = append(hints, operation.SchemaName)
		}
	}
	for _, operation := range args.Operations {
		if operation.Type == schemaWriteSyncLookup {
			hints = append(hints, operation.TitleLocalizations.values()...)
		}
	}
	return schemaWriteEntDistinctTerms(terms...), schemaWriteEntDistinctTerms(hints...)
}

// SchemaSyncResponse is clio's SchemaSyncResponse.
type SchemaSyncResponse struct {
	Success    bool                        `json:"success"`
	Results    []SchemaSyncOperationResult `json:"results"`
	ResumePlan *SchemaSyncResumePlan       `json:"resume-plan,omitempty"`
	DataForge  *SchemaWriteEntDataForge    `json:"dataforge,omitempty"`
}

// SchemaSyncOperationResult is clio's SchemaSyncOperationResult.
type SchemaSyncOperationResult struct {
	Type           string                   `json:"type"`
	SchemaName     *string                  `json:"schema-name,omitempty"`
	Success        bool                     `json:"success"`
	Outcome        string                   `json:"outcome,omitempty"`
	Status         string                   `json:"status"`
	OperationIndex int                      `json:"operation-index"`
	Attempts       *int                     `json:"attempts,omitempty"`
	Error          *string                  `json:"error,omitempty"`
	Messages       *[]LogMessage            `json:"messages,omitempty"`
	CollisionInfo  *SchemaSyncCollisionInfo `json:"collision-info,omitempty"`
}

// schemaWriteSyncMessages is a present (possibly empty) messages list; a nil pointer is clio's null.
func schemaWriteSyncMessages(messages []LogMessage) *[]LogMessage {
	if messages == nil {
		messages = []LogMessage{}
	}
	return &messages
}

// SchemaSyncCollisionInfo is clio's SchemaSyncCollisionInfo.
type SchemaSyncCollisionInfo struct {
	ExistingPackageName string `json:"existing-package-name"`
	Hint                string `json:"hint"`
}

// SchemaSyncResumePlan is clio's SchemaSyncResumePlan.
type SchemaSyncResumePlan struct {
	Instruction            string                `json:"instruction"`
	FailedOperation        *SchemaSyncResumeFail `json:"failed-operation,omitempty"`
	NotRunOperationIndexes []int                 `json:"not-run-operation-indexes"`
	Operations             []*orderedObject      `json:"operations"`
}

// SchemaSyncResumeFail is clio's SchemaSyncResumeFailure.
type SchemaSyncResumeFail struct {
	OperationIndex int     `json:"operation-index"`
	Type           string  `json:"type"`
	SchemaName     *string `json:"schema-name,omitempty"`
	Error          *string `json:"error,omitempty"`
}

func schemaWriteSyncName(name string) *string {
	if name == "" {
		return nil
	}
	return &name
}

func schemaWriteSyncText(text string) *string { return &text }

// SchemaSyncRejection is BuildTopLevelRejection.
func SchemaSyncRejection(message string) SchemaSyncResponse {
	return SchemaSyncResponse{Results: []SchemaSyncOperationResult{{Type: schemaWriteSyncTool, Status: schemaWriteSyncFailed,
		OperationIndex: -1, Error: &message}}}
}

// schemaWriteSyncAliasError is BuildLegacyAliasError over the keys clio could not bind.
func schemaWriteSyncAliasError(extension map[string]any, aliases map[string]string, hint string) string {
	if len(extension) == 0 {
		return ""
	}
	keys := make([]string, 0, len(extension))
	for key := range extension {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var mapped, unknown []string
	for _, key := range keys {
		shown := strings.NewReplacer("'", "", "\"", "").Replace(key)
		if canonical, ok := aliases[key]; ok {
			mapped = append(mapped, fmt.Sprintf("'%s' -> '%s'", shown, canonical))
		} else {
			unknown = append(unknown, fmt.Sprintf("'%s'", shown))
		}
	}
	join := func(items []string) string {
		if len(items) <= 10 {
			return strings.Join(items, ", ")
		}
		return fmt.Sprintf("%s and %d more", strings.Join(items[:10], ", "), len(items)-10)
	}
	var parts []string
	if len(mapped) > 0 {
		parts = append(parts, "Rename: "+join(mapped)+".")
	}
	if len(unknown) > 0 {
		parts = append(parts, "Unknown args: "+join(unknown)+". "+hint)
	}
	return strings.Join(parts, " ")
}

// SchemaSyncValidateTopLevel returns the top-level rejection clio answers before materializing the
// operations, or nil.
func SchemaSyncValidateTopLevel(args SchemaSyncArgs) *SchemaSyncResponse {
	if message := schemaWriteSyncAliasError(args.Extension, schemaWriteSyncArgAliases, schemaWriteSyncArgsHint); message != "" {
		rejection := SchemaSyncRejection("sync-schemas arguments are invalid: " + message + " Nothing was applied.")
		return &rejection
	}
	if !args.HasOperations {
		rejection := SchemaSyncRejection("sync-schemas arguments are invalid: 'operations' is required and must be a non-empty array of schema operations. " +
			schemaWriteSyncArgsHint + " Nothing was applied.")
		return &rejection
	}
	if len(args.Operations) == 0 {
		rejection := SchemaSyncRejection("sync-schemas arguments are invalid: 'operations' is empty, so there is nothing to apply. Send at least one operation. " +
			schemaWriteSyncArgsHint + " Nothing was applied.")
		return &rejection
	}
	return nil
}

// ---------------------------------------------------------------------------------------------- batch

type schemaWriteSyncState struct {
	results         []SchemaSyncOperationResult
	abortedAt       int
	failedResume    *orderedObject
	failedType      string
	failedSchema    string
	resubmittable   bool
	deferredIndexes []int
	deferredSeedOps []*orderedObject
	budgetRemaining time.Duration
}

func (s *schemaWriteSyncState) abort(index int, operation SchemaSyncOperation, resume *orderedObject, resubmittable bool) {
	s.abortedAt, s.failedResume, s.resubmittable = index, resume, resubmittable
	s.failedType, s.failedSchema = operation.Type, operation.SchemaName
}

// schemaWriteSyncSleep is the retry backoff wait; tests replace it.
var schemaWriteSyncSleep = func(ctx context.Context, interval time.Duration) {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

// SchemaSync is SchemaSyncTool.ExecuteBatch after the top-level checks and the enrichment; stage reports a
// per-operation progress line.
func (c *Client) SchemaSync(ctx context.Context, args SchemaSyncArgs, dataForge *SchemaWriteEntDataForge, stage func(string)) SchemaSyncResponse {
	if stage == nil {
		stage = func(string) {}
	}
	state := &schemaWriteSyncState{abortedAt: -1, resubmittable: true, budgetRemaining: 30 * time.Second}
	total := len(args.Operations)
	for index, operation := range args.Operations {
		if ctx.Err() != nil {
			break
		}
		if !c.schemaWriteSyncRunOperation(ctx, args, operation, index, total, state, stage) {
			break
		}
	}
	success := len(state.results) > 0
	for _, result := range state.results {
		success = success && result.Success
	}
	return SchemaSyncResponse{Success: success, Results: state.results, ResumePlan: schemaWriteSyncResumePlan(args, state), DataForge: dataForge}
}

func schemaWriteSyncReportedType(operation SchemaSyncOperation) string {
	if strings.TrimSpace(operation.Type) != "" {
		return operation.Type
	}
	if legacy, ok := operation.Extension["operation"].(string); ok {
		return legacy
	}
	return ""
}

func schemaWriteSyncClassify(result SchemaSyncOperationResult, index int) SchemaSyncOperationResult {
	result.OperationIndex = index
	if result.Status == "" {
		result.Status = schemaWriteSyncFailed
		if result.Success {
			result.Status = "completed"
		}
	}
	return result
}

func (c *Client) schemaWriteSyncRunOperation(ctx context.Context, args SchemaSyncArgs, operation SchemaSyncOperation, index, total int,
	state *schemaWriteSyncState, stage func(string)) bool {
	failure, shapeRejected := schemaWriteSyncShapeFailure(operation, index)
	if failure == nil {
		failure = schemaWriteSyncSeedFailure(operation, index)
	}
	if failure != nil {
		state.results = append(state.results, schemaWriteSyncClassify(*failure, index))
		state.abort(index, operation, schemaWriteSyncEcho(operation), !shapeRejected)
		return false
	}
	stage(fmt.Sprintf("%d/%d: %s %s", index+1, total, schemaWriteSyncReportedType(operation), operation.SchemaName))
	result := schemaWriteSyncClassify(c.schemaWriteSyncExecute(ctx, args, operation, index, state), index)
	state.results = append(state.results, result)
	if !result.Success {
		switch operation.Type {
		case schemaWriteSyncLookup, schemaWriteSyncEntity, schemaWriteSyncUpdate, schemaWriteSyncSeed:
			state.abort(index, operation, schemaWriteSyncEcho(operation), true)
		default:
			state.abort(index, operation, schemaWriteSyncEcho(operation), false)
		}
		return false
	}
	if !operation.HasSeedRows || len(operation.SeedRows) == 0 || operation.Type == schemaWriteSyncSeed {
		return true
	}
	seedOperation := SchemaSyncOperation{Type: schemaWriteSyncSeed, SchemaName: operation.SchemaName, SeedRows: operation.SeedRows, HasSeedRows: true}
	if result.Outcome == schemaWriteSyncSatisfied {
		skipped := SchemaSyncOperationResult{Type: schemaWriteSyncSeed, SchemaName: schemaWriteSyncName(operation.SchemaName),
			Success: true, Outcome: schemaWriteSyncSatisfied, Messages: schemaWriteSyncMessages([]LogMessage{{MessageType: "Info", Value: "sync-schemas: schema already existed (already-satisfied); inline seed-rows were SKIPPED to stay " +
				"replay-safe (no-Name/no-Id rows are not idempotent). To seed an existing schema, submit a " +
				"standalone seed-data operation, which reconciles rows by key — the response's resume-plan " +
				"already carries it, ready to resubmit if the rows are not yet on the server."}})}
		state.results = append(state.results, schemaWriteSyncClassify(skipped, index))
		state.deferredIndexes = append(state.deferredIndexes, index)
		state.deferredSeedOps = append(state.deferredSeedOps, schemaWriteSyncEcho(seedOperation))
		return true
	}
	stage(fmt.Sprintf("%d/%d: seed-data %s", index+1, total, operation.SchemaName))
	seed := schemaWriteSyncClassify(c.schemaWriteSyncSeed(seedOperation), index)
	state.results = append(state.results, seed)
	if seed.Success {
		return true
	}
	state.abort(index, seedOperation, schemaWriteSyncEcho(seedOperation), true)
	return false
}

// schemaWriteSyncShapeFailure runs TryValidateOperationFields, TryValidateSchemaName and
// TryValidateStorageKind; the bool says the failure is a field-shape rejection.
func schemaWriteSyncShapeFailure(operation SchemaSyncOperation, index int) (*SchemaSyncOperationResult, bool) {
	unbound := map[string]any{}
	for key, value := range operation.Extension {
		if key != "operation" {
			unbound[key] = value
		}
	}
	if message := schemaWriteSyncAliasError(unbound, schemaWriteSyncOpAliases, schemaWriteSyncOpHint); message != "" {
		note := ""
		if _, ok := unbound["seed-data"]; ok {
			note = " Note: 'seed-data' is an operation TYPE (\"type\": \"seed-data\"), not a field; rows always go in " +
				"'seed-rows', which is an array of row objects: \"seed-rows\": [{\"values\": {\"Name\": \"Positive\"}}]."
		} else if _, values := unbound["values"]; values {
			note = " Note: 'seed-rows' is an ARRAY of row objects, each wrapping its columns in a 'values' map: " +
				"\"seed-rows\": [{\"values\": {\"Name\": \"Positive\"}}]."
		} else if _, rows := unbound["rows"]; rows {
			note = " Note: 'seed-rows' is an ARRAY of row objects, each wrapping its columns in a 'values' map: " +
				"\"seed-rows\": [{\"values\": {\"Name\": \"Positive\"}}]."
		}
		text := fmt.Sprintf("sync-schemas operations[%d] is invalid: %s%s Nothing was applied for this operation.", index, message, note)
		return &SchemaSyncOperationResult{Type: schemaWriteSyncReportedType(operation), SchemaName: schemaWriteSyncName(operation.SchemaName), Error: &text}, true
	}
	if strings.TrimSpace(operation.SchemaName) == "" {
		text := fmt.Sprintf("sync-schemas operations[%d] is invalid: 'schema-name' is required and cannot be empty. Nothing was applied for this operation.", index)
		return &SchemaSyncOperationResult{Type: schemaWriteSyncReportedType(operation), SchemaName: schemaWriteSyncName(operation.SchemaName), Error: &text}, true
	}
	if operation.IsDBView != nil && operation.Type != schemaWriteSyncEntity {
		text := "is-db-view is supported only for create-entity. Nothing was applied for this operation."
		return &SchemaSyncOperationResult{Type: operation.Type, SchemaName: schemaWriteSyncName(operation.SchemaName), Error: &text}, true
	}
	if operation.IsDBView != nil && *operation.IsDBView && len(operation.SeedRows) > 0 {
		text := "DB-view create-entity operations cannot include seed-rows. Provision the SQL view separately. Nothing was applied for this operation."
		return &SchemaSyncOperationResult{Type: operation.Type, SchemaName: schemaWriteSyncName(operation.SchemaName), Error: &text}, true
	}
	if len(operation.SeedRows) > 0 && operation.Type == schemaWriteSyncEntity && operation.IsVirtual {
		text := fmt.Sprintf("sync-schemas operations[%d] is invalid: virtual create-entity operations cannot "+
			"include seed-rows because virtual entities have no physical database table. Drop 'seed-rows', or "+
			"set 'is-virtual' to false. Nothing was applied for this operation.", index)
		return &SchemaSyncOperationResult{Type: schemaWriteSyncEntity, SchemaName: schemaWriteSyncName(operation.SchemaName), Error: &text}, true
	}
	return nil, false
}

// schemaWriteSyncSeedFailure is TryValidateSeedRows.
func schemaWriteSyncSeedFailure(operation SchemaSyncOperation, index int) *SchemaSyncOperationResult {
	if operation.Type == schemaWriteSyncSeed && len(operation.SeedRows) == 0 {
		text := fmt.Sprintf("sync-schemas operations[%d] is invalid: a seed-data operation requires a non-empty 'seed-rows' array.", index)
		return &SchemaSyncOperationResult{Type: schemaWriteSyncSeed, SchemaName: schemaWriteSyncName(operation.SchemaName), Error: &text}
	}
	for _, row := range operation.SeedRows {
		object, _ := row.(map[string]any)
		if object == nil || object["values"] == nil {
			text := fmt.Sprintf("sync-schemas operations[%d] seed-rows validation failed: each row must contain a non-null 'values' object.", index)
			return &SchemaSyncOperationResult{Type: schemaWriteSyncSeed, SchemaName: schemaWriteSyncName(operation.SchemaName), Error: &text}
		}
	}
	return nil
}

func (c *Client) schemaWriteSyncExecute(ctx context.Context, args SchemaSyncArgs, operation SchemaSyncOperation, index int, state *schemaWriteSyncState) SchemaSyncOperationResult {
	switch operation.Type {
	case schemaWriteSyncLookup:
		return c.schemaWriteSyncCreate(ctx, args, operation, schemaWriteEntBaseLookup, false, schemaWriteSyncLookup, state)
	case schemaWriteSyncEntity:
		return c.schemaWriteSyncCreate(ctx, args, operation, operation.ParentSchemaName, operation.ExtendParent, schemaWriteSyncEntity, state)
	case schemaWriteSyncUpdate:
		return c.schemaWriteSyncUpdateEntity(ctx, args, operation, state)
	case schemaWriteSyncSeed:
		return c.schemaWriteSyncSeed(operation)
	}
	var text string
	if strings.TrimSpace(operation.Type) == "" {
		if legacy, ok := operation.Extension["operation"].(string); ok {
			text = fmt.Sprintf("sync-schemas operations[%d] uses unsupported request field 'operation'. Send 'type': '%s' instead.", index, legacy)
		} else {
			text = fmt.Sprintf("sync-schemas operations[%d] is missing required field 'type'.", index)
		}
	} else {
		text = fmt.Sprintf("sync-schemas operations[%d].type '%s' is invalid. Supported values: create-lookup, create-entity, update-entity, seed-data.", index, operation.Type)
	}
	return SchemaSyncOperationResult{Type: schemaWriteSyncReportedType(operation), SchemaName: schemaWriteSyncName(operation.SchemaName), Error: &text}
}

// schemaWriteSyncSeed reports the seed-data step this server does not run yet: clio applies it with
// create-data-binding-db, which is ported with the data tools (T11).
func (c *Client) schemaWriteSyncSeed(operation SchemaSyncOperation) SchemaSyncOperationResult {
	text := "seed-data failed with exit code 1: seed-data is not supported by this server yet; it needs create-data-binding-db, which is not ported."
	return SchemaSyncOperationResult{Type: schemaWriteSyncSeed, SchemaName: schemaWriteSyncName(operation.SchemaName), Status: schemaWriteSyncFailed,
		Error: &text, Messages: schemaWriteSyncMessages([]LogMessage{{MessageType: "Error", Value: "seed-data is not supported by this server yet; it needs create-data-binding-db, which is not ported."}})}
}

// schemaWriteSyncExecution is clio's OperationExecution.
type schemaWriteSyncExecution struct {
	exitCode int
	err      error
	messages []LogMessage
	attempts int
}

// schemaWriteSyncTransient is TransientNetworkFailureClassifier.IsTransientErrorMessage.
func schemaWriteSyncTransient(message string) bool {
	if strings.TrimSpace(message) == "" {
		return false
	}
	lower := strings.ToLower(message)
	for _, marker := range schemaWriteSyncTransientMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func schemaWriteSyncLastError(messages []LogMessage) string {
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].MessageType == "Error" {
			return strings.TrimSpace(messages[index].Value)
		}
	}
	return ""
}

// runAttempts is RunAttempts: up to three attempts when a failure is a transient network fault, within the
// batch's 30-second backoff budget; earlier attempts leave an Info retry note instead of their output.
func (c *Client) schemaWriteSyncRunAttempts(ctx context.Context, state *schemaWriteSyncState, attempt func(log *schemaWriteEntLog) (int, error)) schemaWriteSyncExecution {
	var notes []LogMessage
	backoffs := []time.Duration{time.Second, 2 * time.Second}
	for attempts := 1; ; attempts++ {
		log := &schemaWriteEntLog{}
		exitCode, err := attempt(log)
		raw := log.snapshot()
		failed := err != nil || exitCode != 0
		transient := false
		if failed {
			if err != nil {
				transient = schemaWriteSyncTransient(err.Error())
			} else {
				transient = schemaWriteSyncTransient(schemaWriteSyncLastError(raw))
			}
		}
		if transient && attempts < schemaWriteSyncAttempts {
			backoff := backoffs[min(attempts-1, len(backoffs)-1)]
			if backoff > state.budgetRemaining {
				notes = append(notes, LogMessage{MessageType: "Info", Value: fmt.Sprintf("sync-schemas: transient network failure on attempt %d/%d; batch retry budget exhausted, failing fast.", attempts, schemaWriteSyncAttempts)})
				return schemaWriteSyncExecution{exitCode, err, append(notes, raw...), attempts}
			}
			state.budgetRemaining -= backoff
			notes = append(notes, LogMessage{MessageType: "Info", Value: fmt.Sprintf("sync-schemas: transient network failure on attempt %d/%d; retrying in %gs.", attempts, schemaWriteSyncAttempts, backoff.Seconds())})
			schemaWriteSyncSleep(ctx, backoff)
			continue
		}
		return schemaWriteSyncExecution{exitCode, err, append(notes, raw...), attempts}
	}
}

func (e schemaWriteSyncExecution) append(next schemaWriteSyncExecution) schemaWriteSyncExecution {
	return schemaWriteSyncExecution{next.exitCode, next.err, append(append([]LogMessage{}, e.messages...), next.messages...), max(e.attempts, next.attempts)}
}

// finalize is FinalizeResult.
func schemaWriteSyncFinalize(operationName, schemaName string, execution schemaWriteSyncExecution, outcome string) SchemaSyncOperationResult {
	messages := schemaWriteSyncMessages(execution.messages)
	var attempts *int
	if execution.attempts > 1 {
		value := execution.attempts
		attempts = &value
	}
	result := SchemaSyncOperationResult{Type: operationName, SchemaName: schemaWriteSyncName(schemaName), Messages: messages, Attempts: attempts}
	if execution.err != nil {
		text := schemaWriteEntRedact(execution.err.Error())
		result.Status, result.Error = schemaWriteSyncFailed, &text
		return result
	}
	result.Success = execution.exitCode == 0
	if result.Success {
		result.Status, result.Outcome = "completed", outcome
		return result
	}
	result.Status = schemaWriteSyncFailed
	text := fmt.Sprintf("%s failed with exit code %d", operationName, execution.exitCode)
	if detail := schemaWriteSyncLastError(*messages); detail != "" {
		text += ": " + detail
	}
	result.Error = &text
	return result
}

func schemaWriteSyncDeterministic(operationName, schemaName string, err error) SchemaSyncOperationResult {
	text := schemaWriteEntRedact(err.Error())
	return SchemaSyncOperationResult{Type: operationName, SchemaName: schemaWriteSyncName(schemaName), Error: &text, Messages: schemaWriteSyncMessages(nil)}
}

// ---------------------------------------------------------------------------------------------- convergence

type schemaWriteSyncPlan struct {
	outcome          string
	columnsToAdd     []SchemaWriteEntColumnArgs
	columnsToModify  []SchemaWriteEntOperationArgs
	collisionPackage string
	err              string
}

// classify is SchemaConvergenceService.Classify.
func (c *Client) schemaWriteSyncConverge(ctx context.Context, args SchemaSyncArgs, operation SchemaSyncOperation, parent string, isLookup, extend bool, isDBView *bool) (schemaWriteSyncPlan, error) {
	found, err := c.FindEntitySchemas(ctx, EntitySchemaSearchRequest{SchemaName: operation.SchemaName})
	if err != nil {
		return schemaWriteSyncPlan{}, err
	}
	targetPackage := strings.TrimSpace(args.PackageName)
	var existing *EntitySchemaSearchResult
	for index := range found {
		if strings.EqualFold(found[index].PackageName, targetPackage) {
			existing = &found[index]
			break
		}
	}
	if existing == nil && len(found) > 0 {
		sorted := append([]EntitySchemaSearchResult{}, found...)
		sort.SliceStable(sorted, func(i, j int) bool { return compareOrdinalIgnoreCase(sorted[i].PackageName, sorted[j].PackageName) < 0 })
		existing = &sorted[0]
	}
	if existing == nil {
		return schemaWriteSyncPlan{outcome: schemaWriteSyncCreated}, nil
	}
	if !strings.EqualFold(existing.PackageName, targetPackage) {
		if extend {
			return schemaWriteSyncPlan{outcome: schemaWriteSyncCreated}, nil
		}
		return schemaWriteSyncPlan{outcome: schemaWriteSyncCollision, collisionPackage: existing.PackageName,
			err: fmt.Sprintf("Error: schema '%s' already exists in package '%s'. ", operation.SchemaName, existing.PackageName) +
				"Reuse the existing schema by referencing it without creation, or delete the stale version before recreating."}, nil
	}
	if isDBView != nil {
		run := c.schemaWriteEntNewRun(ctx, nil)
		pkg, err := run.resolvePackage(args.PackageName)
		if err != nil {
			return schemaWriteSyncPlan{}, err
		}
		schema, err := run.loadSchema(operation.SchemaName, pkg, true)
		if err != nil {
			return schemaWriteSyncPlan{}, err
		}
		if schema.IsDBView != *isDBView {
			return schemaWriteSyncPlan{outcome: schemaWriteSyncCollision, collisionPackage: existing.PackageName,
				err: fmt.Sprintf("Error: schema '%s' has a different is-db-view value. ", operation.SchemaName) +
					"Use set-entity-schema-properties to explicitly change its storage kind."}, nil
		}
	}
	existingParent := ""
	if existing.ParentSchemaName != nil {
		existingParent = *existing.ParentSchemaName
	}
	if extend && !strings.EqualFold(existingParent, operation.SchemaName) {
		return schemaWriteSyncPlan{outcome: schemaWriteSyncCollision, collisionPackage: existing.PackageName,
			err: fmt.Sprintf("Error: schema '%s' already exists in package '%s' but is not a same-name replacement. ", operation.SchemaName, existing.PackageName) +
				"Choose a different target package to create the replacement."}, nil
	}
	if !extend && strings.TrimSpace(parent) != "" && strings.TrimSpace(existingParent) != "" && !strings.EqualFold(strings.TrimSpace(parent), existingParent) {
		return schemaWriteSyncPlan{outcome: schemaWriteSyncCollision, collisionPackage: existing.PackageName,
			err: fmt.Sprintf("Error: schema '%s' already exists in package '%s' with parent ", operation.SchemaName, existing.PackageName) +
				fmt.Sprintf("'%s', which is incompatible with the requested parent '%s'. ", existingParent, parent) +
				"Delete the stale schema before recreating, or target a different schema name."}, nil
	}
	plan := schemaWriteSyncPlan{outcome: schemaWriteSyncSatisfied}
	if len(operation.Columns) == 0 {
		return plan, nil
	}
	existingColumns, err := c.schemaWriteSyncReadColumns(ctx, operation.SchemaName)
	if err != nil {
		return schemaWriteSyncPlan{}, err
	}
	for _, column := range operation.Columns {
		name := column.resolveName()
		if strings.TrimSpace(name) == "" {
			continue
		}
		existingType, present := existingColumns[strings.ToLower(name)]
		if !present {
			plan.columnsToAdd = append(plan.columnsToAdd, column)
			continue
		}
		if !schemaWriteEntTypesEquivalent(column.resolveType(), existingType) {
			action, typeName, reference := "modify", column.resolveType(), column.resolveReference()
			plan.columnsToModify = append(plan.columnsToModify, SchemaWriteEntOperationArgs{Action: &action, ColumnName: &name,
				Type: schemaWriteSyncOptional(typeName), ReferenceSchemaName: schemaWriteSyncOptional(reference), Required: column.resolveRequired()})
		}
	}
	if len(plan.columnsToAdd) > 0 || len(plan.columnsToModify) > 0 {
		plan.outcome = schemaWriteSyncReconciled
	}
	return plan, nil
}

func schemaWriteSyncOptional(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// schemaWriteSyncReadColumns is ReadColumns: the merged schema's columns by name with their friendly type.
func (c *Client) schemaWriteSyncReadColumns(ctx context.Context, schemaName string) (map[string]string, error) {
	properties, err := c.GetEntitySchemaProperties(ctx, EntitySchemaPropertiesRequest{SchemaName: schemaName})
	if err != nil {
		return nil, err
	}
	columns := map[string]string{}
	for _, column := range properties.Columns {
		if strings.TrimSpace(column.Name) != "" {
			columns[strings.ToLower(column.Name)] = column.Type
		}
	}
	return columns, nil
}

// schemaWriteSyncCoerce is CoerceColumnToAddOperation.
func schemaWriteSyncCoerce(column SchemaWriteEntColumnArgs) SchemaWriteEntOperationArgs {
	action, name := "add", column.resolveName()
	titles := column.TitleLocalizations
	if len(titles) == 0 && column.Caption != nil && strings.TrimSpace(*column.Caption) != "" {
		titles = schemaWriteEntLocMap{{Key: schemaWriteDefaultCulture, Value: strings.TrimSpace(*column.Caption)}}
	}
	return SchemaWriteEntOperationArgs{Action: &action, ColumnName: &name, Type: schemaWriteSyncOptional(column.resolveType()),
		TitleLocalizations: titles, ReferenceSchemaName: schemaWriteSyncOptional(column.resolveReference()), Required: column.resolveRequired(),
		DefaultValue: column.DefaultValue, DefaultValueSource: column.DefaultValueSource, Masked: column.Masked,
		Title: column.Title, Caption: column.Caption, DefaultValueConfig: column.DefaultValueConfig}
}

// schemaWriteSyncCreate is ExecuteCreateSchema.
func (c *Client) schemaWriteSyncCreate(ctx context.Context, args SchemaSyncArgs, operation SchemaSyncOperation, parent string, extend bool, operationName string, state *schemaWriteSyncState) SchemaSyncOperationResult {
	context := fmt.Sprintf("%s operation for schema '%s'", operationName, operation.SchemaName)
	if extend && strings.TrimSpace(parent) != "" && !strings.EqualFold(operation.SchemaName, parent) {
		return schemaWriteSyncDeterministic(operationName, operation.SchemaName, schemaWriteEntError(schemaWriteEntReplacementName))
	}
	titles, err := schemaWriteEntRequireTitles(operation.TitleLocalizations, operation.LegacyTitle, "", "", context)
	if err != nil {
		return schemaWriteSyncDeterministic(operationName, operation.SchemaName, err)
	}
	isLookup := operationName == schemaWriteSyncLookup
	if isLookup {
		if message := SchemaWriteEntLookupShadowError(operation.Columns); message != "" {
			return schemaWriteSyncDeterministic(operationName, operation.SchemaName, schemaWriteEntError(message))
		}
	}
	targetParent := parent
	var isDBView *bool
	if isLookup {
		targetParent = schemaWriteEntBaseLookup
	} else {
		isDBView = operation.IsDBView
	}
	var plan schemaWriteSyncPlan
	execution := c.schemaWriteSyncRunAttempts(ctx, state, func(log *schemaWriteEntLog) (int, error) {
		var err error
		if plan, err = c.schemaWriteSyncConverge(ctx, args, operation, targetParent, isLookup, extend, isDBView); err != nil {
			return 1, err
		}
		switch plan.outcome {
		case schemaWriteSyncCollision:
			return schemaWriteSyncCollisionCode, nil
		case schemaWriteSyncCreated:
			createArgs := SchemaWriteEntCreateArgs{PackageName: args.PackageName, SchemaName: operation.SchemaName, TitleLocalizations: titles, Columns: operation.Columns}
			isVirtual := operationName == schemaWriteSyncEntity && operation.IsVirtual
			var dbView *bool
			if operationName == schemaWriteSyncEntity {
				dbView = operation.IsDBView
			}
			options, err := schemaWriteEntBuildCreateOptions(createArgs, parent, extend, isVirtual, dbView)
			if err != nil {
				return 1, err
			}
			return c.schemaWriteEntNewRun(ctx, log).executeCreate(options), nil
		case schemaWriteSyncReconciled:
			var operations []SchemaWriteEntOperationArgs
			for _, column := range plan.columnsToAdd {
				operations = append(operations, schemaWriteSyncCoerce(column))
			}
			operations = append(operations, plan.columnsToModify...)
			if len(operations) == 0 {
				return 0, nil
			}
			return c.schemaWriteSyncRunUpdate(ctx, log, args.PackageName, operation.SchemaName, operations)
		}
		return 0, nil
	})
	if execution.exitCode == schemaWriteSyncCollisionCode && execution.err == nil {
		text := schemaWriteEntRedact(plan.err)
		result := SchemaSyncOperationResult{Type: operationName, SchemaName: schemaWriteSyncName(operation.SchemaName), Outcome: schemaWriteSyncCollision,
			Error: &text, Messages: schemaWriteSyncMessages(nil)}
		if plan.collisionPackage != "" {
			result.CollisionInfo = &SchemaSyncCollisionInfo{ExistingPackageName: plan.collisionPackage, Hint: text}
		}
		return result
	}
	if execution.exitCode == 0 && execution.err == nil && isLookup {
		title, err := schemaWriteEntDefaultTitle(titles, context)
		registration := c.schemaWriteSyncRunAttempts(ctx, state, func(log *schemaWriteEntLog) (int, error) {
			if err != nil {
				return 1, err
			}
			return 0, c.schemaWriteEntNewRun(ctx, log).ensureLookupRegistration(args.PackageName, operation.SchemaName, title)
		})
		execution = execution.append(registration)
	}
	outcome := ""
	if execution.exitCode == 0 && execution.err == nil {
		outcome = plan.outcome
	}
	return schemaWriteSyncFinalize(operationName, operation.SchemaName, execution, outcome)
}

// schemaWriteSyncRunUpdate serializes the operations as UpdateEntitySchemaTool.SerializeOperations does and
// runs UpdateEntitySchemaCommand; a payload failure is raised, as SerializeOperations throws.
func (c *Client) schemaWriteSyncRunUpdate(ctx context.Context, log *schemaWriteEntLog, pkg, schemaName string, operations []SchemaWriteEntOperationArgs) (int, error) {
	var payloads []schemaWriteEntColumnOptions
	for index, operation := range operations {
		options, err := schemaWriteEntOperationPayload(operation, fmt.Sprintf("Schema '%s' operation #%d", schemaName, index+1))
		if err != nil {
			return 1, err
		}
		payloads = append(payloads, options)
	}
	return c.schemaWriteEntNewRun(ctx, log).executeUpdate(pkg, schemaName, payloads), nil
}

// schemaWriteSyncUpdateEntity is ExecuteUpdateEntity.
func (c *Client) schemaWriteSyncUpdateEntity(ctx context.Context, args SchemaSyncArgs, operation SchemaSyncOperation, state *schemaWriteSyncState) SchemaSyncOperationResult {
	requested := operation.UpdateOperations
	if len(requested) == 0 {
		for _, column := range operation.Columns {
			requested = append(requested, schemaWriteSyncCoerce(column))
		}
	}
	if len(requested) == 0 {
		text := "sync-schemas update-entity requires either an 'update-operations' array " +
			"(each item: 'action' = add|modify|remove, 'column-name' [alias 'name'], 'type' [alias 'data-value-type'], " +
			"'reference-schema-name' [alias 'reference-schema'], 'required' [alias 'is-required'], plus optional flags) " +
			"or a 'columns' array (read/create shape: 'name', 'type' [alias 'data-value-type'], " +
			"'title-localizations' [the read-shape scalar 'caption' is also accepted], " +
			"'required' [alias 'is-required'], 'reference-schema-name' [alias 'reference-schema']) " +
			"which is treated as an implicit add-batch. " +
			"A column read from get-app-info ('name', 'type'/'data-value-type', " +
			"'reference-schema-name'/'reference-schema', 'caption', 'required') can be sent back directly — " +
			"add an 'action' for modify/remove."
		return SchemaSyncOperationResult{Type: schemaWriteSyncUpdate, SchemaName: schemaWriteSyncName(operation.SchemaName), Error: &text}
	}
	outcome := ""
	var collisions []string
	execution := c.schemaWriteSyncRunAttempts(ctx, state, func(log *schemaWriteEntLog) (int, error) {
		existing, err := c.schemaWriteSyncReadColumns(ctx, operation.SchemaName)
		if err != nil {
			return 1, err
		}
		collisions = nil
		var delta []SchemaWriteEntOperationArgs
		for _, item := range requested {
			reconciled, collision := schemaWriteSyncReconcile(item, existing)
			if collision != "" {
				collisions = append(collisions, collision)
				continue
			}
			if reconciled != nil {
				delta = append(delta, *reconciled)
			}
		}
		if len(collisions) > 0 {
			return schemaWriteSyncCollisionCode, nil
		}
		if len(delta) == 0 {
			outcome = schemaWriteSyncSatisfied
			return 0, nil
		}
		outcome = schemaWriteSyncReconciled
		return c.schemaWriteSyncRunUpdate(ctx, log, args.PackageName, operation.SchemaName, delta)
	})
	if execution.exitCode == schemaWriteSyncCollisionCode && execution.err == nil {
		text := schemaWriteEntRedact(fmt.Sprintf("Column collision on schema '%s': %s. ", operation.SchemaName, strings.Join(collisions, "; ")) +
			"An 'add' never changes an existing column's type — send an explicit 'modify' action to converge " +
			"the type, or use a different column name.")
		return SchemaSyncOperationResult{Type: schemaWriteSyncUpdate, SchemaName: schemaWriteSyncName(operation.SchemaName), Outcome: schemaWriteSyncCollision,
			Error: &text, Messages: schemaWriteSyncMessages(nil)}
	}
	if execution.exitCode != 0 || execution.err != nil {
		outcome = ""
	}
	return schemaWriteSyncFinalize(schemaWriteSyncUpdate, operation.SchemaName, execution, outcome)
}

// schemaWriteSyncReconcile is ReconcileUpdateOperation over a per-batch column view.
func schemaWriteSyncReconcile(operation SchemaWriteEntOperationArgs, columns map[string]string) (*SchemaWriteEntOperationArgs, string) {
	name := operation.resolveColumnName()
	if strings.TrimSpace(name) == "" {
		return &operation, ""
	}
	action := strings.ToLower(operation.action())
	isModify := action == "modify"
	if action != "add" && !isModify && action != "remove" {
		return &operation, ""
	}
	key := strings.ToLower(name)
	existingType, present := columns[key]
	if action == "remove" {
		if !present {
			return nil, ""
		}
		delete(columns, key)
		return &operation, ""
	}
	if !present {
		columns[key] = operation.resolveType()
		return &operation, ""
	}
	if !schemaWriteEntTypesEquivalent(operation.resolveType(), existingType) {
		if !isModify {
			return nil, fmt.Sprintf("'%s' exists as '%s' but was requested as '%s'", name, existingType, operation.resolveType())
		}
		columns[key] = operation.resolveType()
		return &operation, ""
	}
	if isModify {
		return &operation, ""
	}
	return nil, ""
}

// ---------------------------------------------------------------------------------------------- resume plan

// schemaWriteSyncEcho is the operation as clio echoes it in resume-plan.operations: the arguments it
// bound, nulls left out, unbound keys after them.
func schemaWriteSyncEcho(operation SchemaSyncOperation) *orderedObject {
	echo := &orderedObject{values: map[string]any{}}
	add := func(key string, value any) {
		echo.keys = append(echo.keys, key)
		echo.values[key] = value
	}
	if raw, ok := operation.Raw["type"]; ok && raw != nil || operation.Raw == nil && operation.Type != "" {
		add("type", operation.Type)
	}
	if raw, ok := operation.Raw["schema-name"]; ok && raw != nil || operation.Raw == nil && operation.SchemaName != "" {
		add("schema-name", operation.SchemaName)
	}
	if operation.TitleLocalizations != nil {
		add("title-localizations", schemaWriteSyncLocObject(operation.TitleLocalizations))
	}
	if operation.ParentSchemaName != "" {
		add("parent-schema-name", operation.ParentSchemaName)
	}
	add("extend-parent", operation.ExtendParent)
	if operation.HasColumns {
		columns := []any{}
		for _, column := range operation.Columns {
			columns = append(columns, schemaWriteSyncEchoFields(column.Raw, schemaWriteSyncColumnOrder))
		}
		add("columns", columns)
	}
	if operation.UpdateOperations != nil {
		updates := []any{}
		for _, update := range operation.UpdateOperations {
			updates = append(updates, schemaWriteSyncEchoFields(update.Raw, schemaWriteSyncOperationOrder))
		}
		add("update-operations", updates)
	}
	if operation.HasSeedRows {
		add("seed-rows", operation.SeedRows)
	}
	add("is-virtual", operation.IsVirtual)
	if operation.IsDBView != nil {
		add("is-db-view", *operation.IsDBView)
	}
	if operation.LegacyTitle != "" {
		add("title", operation.LegacyTitle)
	}
	extensionKeys := make([]string, 0, len(operation.Extension))
	for key := range operation.Extension {
		extensionKeys = append(extensionKeys, key)
	}
	sort.Strings(extensionKeys)
	for _, key := range extensionKeys {
		add(key, operation.Extension[key])
	}
	return echo
}

var schemaWriteSyncColumnOrder = []string{"name", "type", "title-localizations", "reference-schema-name", "title", "caption",
	"required", "default-value-source", "default-value", "default-value-config", "masked", "data-value-type",
	"reference-schema", "is-required", "column-name"}

var schemaWriteSyncOperationOrder = []string{"action", "column-name", "new-name", "type", "title-localizations",
	"description-localizations", "reference-schema-name", "required", "indexed", "cloneable", "track-changes",
	"default-value", "default-value-source", "multiline-text", "localizable-text", "accent-insensitive", "masked",
	"format-validated", "use-seconds", "simple-lookup", "cascade", "do-not-control-integrity", "title", "caption",
	"description", "default-value-config", "name", "data-value-type", "reference-schema", "is-required",
	"caption-culture", "usage-type"}

var schemaWriteSyncConfigOrder = []string{"source", "value", "value-source", "resolved-value-source", "sequence-prefix",
	"sequence-number-of-chars", "display-value", "record-resolution", "source-resolution"}

func schemaWriteSyncEchoFields(raw map[string]any, order []string) *orderedObject {
	echo := &orderedObject{values: map[string]any{}}
	for _, key := range order {
		value, ok := raw[key]
		if !ok || value == nil {
			continue
		}
		if key == "default-value-config" {
			if object, isObject := value.(map[string]any); isObject {
				value = schemaWriteSyncEchoFields(object, schemaWriteSyncConfigOrder)
			}
		}
		if key == "title-localizations" || key == "description-localizations" {
			if object, isObject := value.(map[string]any); isObject {
				value = schemaWriteSyncRawLocObject(object)
			}
		}
		echo.keys = append(echo.keys, key)
		echo.values[key] = value
	}
	return echo
}

func schemaWriteSyncLocObject(values schemaWriteEntLocMap) *orderedObject {
	object := &orderedObject{values: map[string]any{}}
	for _, pair := range values {
		object.keys = append(object.keys, pair.Key)
		object.values[pair.Key] = pair.Value
	}
	return object
}

func schemaWriteSyncRawLocObject(values map[string]any) *orderedObject {
	b := schemaWriteEntBinder{}
	parsed, err := b.locMap(map[string]any{"value": values}, "value")
	if err != nil {
		object := &orderedObject{values: map[string]any{}}
		for key, value := range values {
			object.keys = append(object.keys, key)
			object.values[key] = value
		}
		sort.Strings(object.keys)
		return object
	}
	return schemaWriteSyncLocObject(parsed)
}

// schemaWriteSyncResumePlan is BuildResumePlan.
func schemaWriteSyncResumePlan(args SchemaSyncArgs, state *schemaWriteSyncState) *SchemaSyncResumePlan {
	if state.abortedAt < 0 || state.failedResume == nil {
		if len(state.deferredSeedOps) == 0 {
			return nil
		}
		return &SchemaSyncResumePlan{
			Instruction: "Batch completed, but the inline seed-rows of the operations listed in " +
				"resume-plan.not-run-operation-indexes were SKIPPED to stay replay-safe (their create converged to " +
				"'already-satisfied'). If those rows are not yet present on the server — e.g. the create landed on an " +
				"earlier retry attempt of this same call and lost its response — resubmit resume-plan.operations " +
				"(standalone seed-data operations, which reconcile rows by key) as a new sync-schemas call. " +
				"Do NOT resubmit the create operations; they are already satisfied.",
			NotRunOperationIndexes: append([]int{}, state.deferredIndexes...),
			Operations:             append([]*orderedObject{}, state.deferredSeedOps...),
		}
	}
	var failed *SchemaSyncOperationResult
	for index := len(state.results) - 1; index >= 0; index-- {
		if !state.results[index].Success {
			failed = &state.results[index]
			break
		}
	}
	plan := &SchemaSyncResumePlan{NotRunOperationIndexes: []int{}, Operations: []*orderedObject{}}
	if state.resubmittable {
		plan.Operations = append(plan.Operations, state.failedResume)
	}
	for index := state.abortedAt + 1; index < len(args.Operations); index++ {
		plan.NotRunOperationIndexes = append(plan.NotRunOperationIndexes, index)
		plan.Operations = append(plan.Operations, schemaWriteSyncEcho(args.Operations[index]))
	}
	plan.Operations = append(plan.Operations, state.deferredSeedOps...)
	if state.resubmittable {
		plan.Instruction = "Batch aborted before completing. Resubmit ONLY the operations in resume-plan.operations " +
			"(the failed operation, the not-run operations, and any deferred seed-data operations) as a new " +
			"sync-schemas call; do NOT resubmit the operations already marked completed."
	} else {
		plan.Instruction = "Batch aborted before completing. The failed operation was rejected for its FIELD SHAPE and is " +
			"deliberately NOT included in resume-plan.operations — correct the field names reported in its " +
			"error, then resubmit it together with the operations listed here (the not-run operations and any " +
			"deferred seed-data operations); do NOT resubmit the operations already marked completed."
	}
	failure := &SchemaSyncResumeFail{OperationIndex: state.abortedAt, Type: state.failedType, SchemaName: schemaWriteSyncName(state.failedSchema)}
	if failed != nil {
		failure.Type = failed.Type
		failure.Error = failed.Error
	}
	plan.FailedOperation = failure
	return plan
}
