package creatio

// odata-delete as clio 8.1.0.142 serves it (ODataDeleteTool): one `id` or up to 50 `ids`, every refusal
// explained, `dry-run` listing what would be deleted and what blocks it, and `cascade` deleting the OWNED
// children first once the whole tree was checked and the caller passed the `cascade-plan-hash` of a dry run.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/redact"
)

const (
	odataDeleteMaxIDs          = 50
	odataDeleteMaxCascadeRows  = 100
	odataDeleteMaxCascadeDepth = 3
	odataDeleteMaxCascadeIDs   = 10
	odataDeleteRequestTimeout  = 30 * time.Second
	odataDeleteMinimumDelete   = time.Second
	odataDeleteMaxBatchPasses  = 3
	odataDeleteDiagnosisCall   = 10 * time.Second
	odataDeleteDiagnosisBudget = 25 * time.Second
	// The whole call ends by the call budget, and an id is started only while the reserve is left for its
	// diagnosis and its DELETE: clio's parent kills the worker at 120 s and the answer would then be lost.
	odataDeleteCallBudget     = 100 * time.Second
	odataDeleteIDStartReserve = 30 * time.Second

	// ODataDeleteHiddenRowsNote is ODataDeleteTool.HiddenRowsNote.
	ODataDeleteHiddenRowsNote = "Rows this user cannot read because of record-level rights are not returned to " +
		"the check, so they are not listed here; such a row can still refuse a delete, and with cascade it can stop " +
		"the record after its owned rows were deleted."
	odataDeleteNotCheckedInTime = "not checked: this call's time budget was used up by the ids before it. " +
		"Call again for it."
	odataDeleteNotAttemptedInTime = "not attempted: this call's time budget was used up by the ids before " +
		"it; nothing was deleted for this id. Call again for it."
)

// odataCascadeRefusedEntities is shared master data: a required lookup to it is a reference, not ownership.
var odataCascadeRefusedEntities = map[string]bool{"contact": true, "sysadminunit": true, "employee": true, "sysuserinrole": true}

func odataCascadeRefused(entity string) bool {
	return odataCascadeRefusedEntities[strings.ToLower(entity)]
}

// RecordKey is clio's RecordKey: an entity and a record id.
type RecordKey struct {
	Entity string `json:"entity"`
	ID     string `json:"id"`
}

// ODataDeleteItemResult is clio's ODataDeleteItemResult: the outcome for one id.
type ODataDeleteItemResult struct {
	ID                 string            `json:"id"`
	Success            bool              `json:"success"`
	Error              *string           `json:"error,omitempty"`
	Explanation        *string           `json:"explanation,omitempty"`
	Referencing        []ReferencingRows `json:"referencing,omitzero"`
	CascadeDeleted     []RecordKey       `json:"cascade-deleted,omitzero"`
	CascadeUnconfirmed []RecordKey       `json:"cascade-unconfirmed,omitzero"`
}

// ODataDeletePlanItem is clio's ODataDeletePlanItem: what a dry run found for one id.
type ODataDeletePlanItem struct {
	ID                 string            `json:"id"`
	Visible            *bool             `json:"visible,omitempty"`
	Referencing        []ReferencingRows `json:"referencing"`
	CascadeWouldDelete []RecordKey       `json:"cascade-would-delete"`
	CascadeRefusal     *string           `json:"cascade-refusal,omitempty"`
	BlockedBecause     *string           `json:"blocked-because,omitempty"`
	Complete           bool              `json:"complete"`
	PlatformCascade    []RecordKey       `json:"platform-cascade,omitzero"`
}

// ODataDeleteRequest is odata-delete's arguments. IDs nil and empty both mean "not passed".
type ODataDeleteRequest struct {
	Entity          string
	ID              string
	IDs             []string
	Cascade         bool
	CascadePlanHash string
	DryRun          bool
	Confirm         bool
}

func odataText(text string) *string {
	if text == "" {
		return nil
	}
	return &text
}

// ODataDelete is clio's ODataDeleteTool.Delete. client is the resolved environment, or nil with
// resolveFailure set: clio resolves the environment only after its own argument checks.
func ODataDelete(ctx context.Context, client *Client, resolveFailure error, request ODataDeleteRequest) ODataWriteResponse {
	correlationID := dataWriteCorrelationID()
	run := &odataDeleteRun{}
	result := run.core(ctx, client, resolveFailure, request)
	result.CorrelationID = correlationID
	if result.Plan == nil {
		result.Diagnostic = NewDataWriteDiagnostic("delete", request.Entity, nil, run.attempted, run.received, result.Success, result.Error)
	}
	return result
}

type odataDeleteRun struct {
	attempted, received bool
}

func (run *odataDeleteRun) core(ctx context.Context, client *Client, resolveFailure error, request ODataDeleteRequest) ODataWriteResponse {
	ids, invalid := odataDeleteValidateTargets(request)
	if invalid != nil {
		return *invalid
	}
	entity := dataWriteTrim(request.Entity)
	if !request.DryRun {
		if notConfirmed := odataRequireConfirmation(request.Confirm, entity, strings.Join(ids, ", "), "delete", "deletion"); notConfirmed != nil {
			return *notConfirmed
		}
	}
	if request.Cascade && odataCascadeRefused(entity) {
		return odataWriteFailure(fmt.Sprintf("cascade is refused for %s: rows that require a %s only refer to it, they are not "+
			"owned by it. Delete without cascade, and read explanation for what blocks it.", entity, entity))
	}
	if request.Cascade && len(ids) > odataDeleteMaxCascadeIDs {
		return odataWriteFailure(fmt.Sprintf("cascade takes at most %d ids per call.", odataDeleteMaxCascadeIDs))
	}
	if resolveFailure != nil {
		return odataWriteFailure(redact.Text(resolveFailure.Error()))
	}
	service := newRecordDeletionService(ctx, client)
	callDeadline := time.Now().Add(odataDeleteCallBudget)
	if request.Cascade && service.isLookup(entity, odataDeleteDiagnosisCall) {
		return odataWriteFailure(fmt.Sprintf("cascade is refused for %s: it is a lookup (dictionary), or could not be shown not "+
			"to be one, so the rows that require one of its values only refer to it - they are not owned by it. Delete "+
			"without cascade, and read explanation for what blocks it.", entity))
	}
	if request.DryRun {
		return odataDeleteDryRun(service, entity, ids, request.Cascade, callDeadline)
	}
	if request.Cascade {
		return run.deleteWithCascade(request, service, entity, ids, callDeadline)
	}
	return run.deleteWithoutCascade(request, service, entity, ids, callDeadline)
}

// odataDeleteValidateTargets is ODataDeleteTool.ValidateTargets: the ids trimmed and de-duplicated, first
// spelling kept.
func odataDeleteValidateTargets(request ODataDeleteRequest) ([]string, *ODataWriteResponse) {
	hasID := !dataWriteBlank(request.ID)
	hasIDs := len(request.IDs) > 0
	if hasID == hasIDs {
		if invalid := odataValidateTarget(request.Entity, emptyGUID, "delete"); invalid != nil {
			return nil, invalid
		}
		message := "id is required and must be a record GUID (or pass ids, up to 50); keyless mass delete is not allowed."
		if hasID {
			message = "Pass id or ids, not both."
		}
		failure := odataWriteFailure(message)
		return nil, &failure
	}
	requested := request.IDs
	if hasID {
		requested = []string{request.ID}
	}
	if len(requested) > odataDeleteMaxIDs {
		failure := odataWriteFailure(fmt.Sprintf("ids takes at most %d record GUIDs per call.", odataDeleteMaxIDs))
		return nil, &failure
	}
	for _, candidate := range requested {
		if invalid := odataValidateTarget(request.Entity, candidate, "delete"); invalid != nil {
			return nil, invalid
		}
	}
	seen := map[string]bool{}
	ids := []string{}
	for _, id := range requested {
		trimmed := dataWriteTrim(id)
		if key := strings.ToLower(trimmed); !seen[key] {
			seen[key] = true
			ids = append(ids, trimmed)
		}
	}
	return ids, nil
}

// odataDiagnosisStop gives a diagnosis its budget, but never so much that the DELETE after it would run past
// the call's end.
func odataDiagnosisStop(callDeadline time.Time) time.Time {
	stop := time.Now().Add(odataDeleteDiagnosisBudget)
	if latest := callDeadline.Add(-odataDeleteIDStartReserve); latest.Before(stop) {
		return latest
	}
	return stop
}

// odataDeleteBudget is the DELETE timeout, never past the call's end; false when too little is left.
func odataDeleteBudget(callDeadline time.Time) (time.Duration, bool) {
	left := time.Until(callDeadline)
	if left < odataDeleteMinimumDelete {
		return 0, false
	}
	return min(odataDeleteRequestTimeout, left), true
}

func odataOutOfStartTime(callDeadline time.Time) bool {
	return !time.Now().Before(callDeadline.Add(-odataDeleteIDStartReserve))
}

// odataDeleteKeyOf is ODataDeleteTool.KeyOf.
func odataDeleteKeyOf(entity, id string) string {
	if parsed, ok := parseGUID(id); ok {
		id = parsed
	}
	return entity + "(" + id + ")"
}

// ---- dry run ----

type odataIDPlan struct {
	id   string
	plan *odataCascadePlan
}

// odataDeleteDryRun is BuildDryRun. No top-level id: a dry run deleted nothing.
func odataDeleteDryRun(service *recordDeletionService, entity string, ids []string, cascade bool, callDeadline time.Time) ODataWriteResponse {
	plans := []odataIDPlan{}
	items := []ODataDeletePlanItem{}
	for _, id := range ids {
		if odataOutOfStartTime(callDeadline) {
			plans = append(plans, odataIDPlan{id, nil})
			item := ODataDeletePlanItem{ID: id, Referencing: []ReferencingRows{}, CascadeWouldDelete: []RecordKey{}}
			if cascade {
				item.CascadeRefusal = odataText(odataDeleteNotCheckedInTime)
			} else {
				item.BlockedBecause = odataText(odataDeleteNotCheckedInTime)
			}
			items = append(items, item)
			continue
		}
		item, plan := odataBuildPlan(service, entity, id, cascade, odataDiagnosisStop(callDeadline))
		plans = append(plans, odataIDPlan{id, plan})
		items = append(items, item)
	}
	response := ODataWriteResponse{Success: true, Plan: items, Note: odataText(ODataDeleteHiddenRowsNote)}
	if cascade {
		response.CascadePlanHash = odataText(odataHashPlans(plans))
	}
	return response
}

func odataBuildPlan(service *recordDeletionService, entity, id string, cascade bool, stopAt time.Time) (ODataDeletePlanItem, *odataCascadePlan) {
	diagnosis := service.diagnose(entity, id, odataDeleteDiagnosisCall, stopAt)
	var plan *odataCascadePlan
	if cascade {
		plan = odataCollectOwned(service, entity, id, stopAt, diagnosis)
	}
	blocked := (diagnosis.visible != nil && !*diagnosis.visible) || diagnosis.processElementID != "" || !diagnosis.complete
	for _, rows := range diagnosis.referencing {
		if rows.Kind == referencingLinked || (!cascade && rows.Kind == referencingOwned) {
			blocked = true
		}
	}
	item := ODataDeletePlanItem{ID: id, Visible: diagnosis.visible, Referencing: diagnosis.referencingList(), CascadeWouldDelete: []RecordKey{},
		Complete: diagnosis.complete}
	if plan != nil {
		if plan.refusal == "" {
			item.CascadeWouldDelete = plan.order
			if len(plan.platformCascade) > 0 {
				item.PlatformCascade = plan.platformCascade
			}
		} else {
			item.CascadeRefusal = odataText(plan.refusal)
		}
	}
	if blocked {
		item.BlockedBecause = odataText(explainDeleteRefusal(entity, id, recordDeleteOutcome{}, diagnosis))
	}
	return item, plan
}

// odataHashPlans is HashPlans: a short, stable digest of what the cascade would delete per id, in the ids'
// order, each id's rows sorted.
func odataHashPlans(plans []odataIDPlan) string {
	var text strings.Builder
	keys := func(rows []RecordKey) string {
		values := make([]string, len(rows))
		for i, key := range rows {
			values[i] = odataDeleteKeyOf(key.Entity, key.ID)
		}
		sort.Strings(values)
		return strings.Join(values, ",")
	}
	for _, entry := range plans {
		text.WriteString(odataDeleteKeyOf("", entry.id))
		text.WriteByte('=')
		switch {
		case entry.plan == nil:
			text.WriteString("unchecked")
		case entry.plan.refusal != "":
			text.WriteString("refused")
		default:
			text.WriteString(keys(entry.plan.order))
			text.WriteByte('|')
			text.WriteString(keys(entry.plan.platformCascade))
		}
		text.WriteByte(';')
	}
	digest := sha256.Sum256([]byte(strings.ToLower(text.String())))
	return hex.EncodeToString(digest[:8])
}

// ---- deletes ----

func (run *odataDeleteRun) note(outcome recordDeleteOutcome) {
	run.attempted = run.attempted || outcome.attempted
	run.received = run.received || outcome.received
}

// deleteWithCascade binds the plan the caller approved to the call: it is recomputed, and only the plan the
// dry run showed is deleted.
func (run *odataDeleteRun) deleteWithCascade(request ODataDeleteRequest, service *recordDeletionService, entity string, ids []string,
	callDeadline time.Time) ODataWriteResponse {
	if dataWriteBlank(request.CascadePlanHash) {
		return odataWriteFailure("cascade needs cascade-plan-hash: run the same call with dry-run: true, show the user " +
			"cascade-would-delete, and pass the cascade-plan-hash it returns together with confirm. Nothing was deleted.")
	}
	plans := []odataIDPlan{}
	for _, id := range ids {
		if odataOutOfStartTime(callDeadline) {
			plans = append(plans, odataIDPlan{id, nil})
			continue
		}
		plans = append(plans, odataIDPlan{id, odataCollectOwned(service, entity, id, odataDiagnosisStop(callDeadline), nil)})
	}
	if !strings.EqualFold(dataWriteTrim(request.CascadePlanHash), odataHashPlans(plans)) {
		owned := 0
		for _, entry := range plans {
			if entry.plan != nil {
				owned += len(entry.plan.order)
			}
		}
		return odataWriteFailure(fmt.Sprintf("The cascade plan is not the one cascade-plan-hash was made from: it now deletes %d "+
			"owned row(s), so rows were added, removed or became unreadable since the dry run. Nothing was deleted. Run dry-run "+
			"again, show the user the new plan, and pass its hash.", owned))
	}
	removed := map[string]bool{}
	results := []ODataDeleteItemResult{}
	for _, entry := range plans {
		if removed[strings.ToLower(odataDeleteKeyOf(entity, entry.id))] {
			results = append(results, ODataDeleteItemResult{ID: entry.id, Success: true,
				Explanation: odataText("Deleted as an owned row of an earlier id's cascade in this call.")})
			continue
		}
		var result ODataDeleteItemResult
		if entry.plan == nil {
			result = ODataDeleteItemResult{ID: entry.id, Error: odataText(odataDeleteNotAttemptedInTime)}
		} else {
			result = run.deleteOne(service, entity, entry.id, entry.plan, callDeadline)
		}
		for _, key := range result.CascadeDeleted {
			removed[strings.ToLower(odataDeleteKeyOf(key.Entity, key.ID))] = true
		}
		if result.Success {
			removed[strings.ToLower(odataDeleteKeyOf(entity, entry.id))] = true
		}
		results = append(results, result)
	}
	return odataDeleteResponse(request, results)
}

// deleteWithoutCascade sends one DELETE per id; an id refused while others in the batch were deleted is tried
// again after them, while a pass still deletes something. Only what is still refused is diagnosed.
func (run *odataDeleteRun) deleteWithoutCascade(request ODataDeleteRequest, service *recordDeletionService, entity string, ids []string,
	callDeadline time.Time) ODataWriteResponse {
	results := map[string]ODataDeleteItemResult{}
	refusals := map[string]recordDeleteOutcome{}
	// A DELETE of a missing record is answered like a deletion, so each is looked up first.
	pending := []string{}
	for _, id := range ids {
		if odataOutOfStartTime(callDeadline) {
			results[id] = ODataDeleteItemResult{ID: id, Error: odataText(odataDeleteNotAttemptedInTime)}
			continue
		}
		if budget, ok := odataDeleteBudget(callDeadline); ok {
			if exists := service.exists(entity, id, min(budget, odataDeleteDiagnosisCall)); exists != nil && !*exists {
				results[id] = ODataDeleteItemResult{ID: id, Error: odataText(fmt.Sprintf("%s(%s) was not found: it does not exist, "+
					"was already deleted, or this user cannot read it. Nothing was deleted.", entity, id))}
				continue
			}
		}
		pending = append(pending, id)
	}
	for pass := 0; pass < odataDeleteMaxBatchPasses && len(pending) > 0; pass++ {
		again := []string{}
		for _, id := range pending {
			budget, ok := odataDeleteBudget(callDeadline)
			if !ok {
				again = append(again, id)
				continue
			}
			outcome := service.delete(entity, id, budget)
			run.note(outcome)
			if outcome.success || !outcome.received {
				// A transport failure has no refusal to explain, and its write may have landed.
				result := ODataDeleteItemResult{ID: id, Success: outcome.success}
				if !outcome.success {
					result.Error = odataText(outcome.err)
				}
				results[id] = result
				delete(refusals, id)
			} else {
				refusals[id] = outcome
				again = append(again, id)
			}
		}
		progressed := len(again) < len(pending)
		pending = again
		if !progressed {
			break
		}
	}
	for _, id := range pending {
		if refusal, ok := refusals[id]; ok {
			results[id] = odataExplain(service, entity, id, refusal, callDeadline, nil)
		} else {
			results[id] = ODataDeleteItemResult{ID: id, Error: odataText(fmt.Sprintf("%s(%s) was not deleted: this call's time "+
				"budget ran out before its DELETE could be sent.", entity, id))}
		}
	}
	ordered := make([]ODataDeleteItemResult, 0, len(ids))
	for _, id := range ids {
		ordered = append(ordered, results[id])
	}
	return odataDeleteResponse(request, ordered)
}

// odataExplain diagnoses a refusal with a budget of its own: what ran before may have spent the first one.
func odataExplain(service *recordDeletionService, entity, id string, outcome recordDeleteOutcome, callDeadline time.Time,
	removed []RecordKey) ODataDeleteItemResult {
	diagnosis := service.diagnose(entity, id, odataDeleteDiagnosisCall, odataDiagnosisStop(callDeadline))
	result := ODataDeleteItemResult{ID: id, Error: odataText(outcome.err),
		Explanation: odataText(explainDeleteRefusal(entity, id, outcome, diagnosis)), CascadeDeleted: removed}
	if len(diagnosis.referencing) > 0 {
		result.Referencing = diagnosis.referencing
	}
	return result
}

// odataDeleteResponse is BuildDeleteResponse: the id the caller passed decides the shape.
func odataDeleteResponse(request ODataDeleteRequest, results []ODataDeleteItemResult) ODataWriteResponse {
	if !dataWriteBlank(request.ID) {
		only := results[0]
		response := ODataWriteResponse{Success: only.Success, Error: only.Error, Explanation: only.Explanation,
			Referencing: only.Referencing, CascadeDeleted: only.CascadeDeleted, CascadeUnconfirmed: only.CascadeUnconfirmed}
		if only.Success {
			id := only.ID
			response.ID = &id
		}
		return response
	}
	failed := 0
	for _, result := range results {
		if !result.Success {
			failed++
		}
	}
	response := ODataWriteResponse{Success: failed == 0, Results: results}
	if failed > 0 {
		response.Error = odataText(fmt.Sprintf("%d of %d records were not deleted; results explains each.", failed, len(results)))
	}
	return response
}

// deleteOne deletes the plan's owned rows deepest first, then the record.
func (run *odataDeleteRun) deleteOne(service *recordDeletionService, entity, id string, plan *odataCascadePlan,
	callDeadline time.Time) ODataDeleteItemResult {
	cascadeDeleted := []RecordKey{}
	if refused := run.deleteOwnedChildren(service, RecordKey{entity, id}, plan, callDeadline, &cascadeDeleted); refused != nil {
		return *refused
	}
	var deletedChildren []RecordKey
	if len(cascadeDeleted) > 0 {
		deletedChildren = cascadeDeleted
	}
	budget, ok := odataDeleteBudget(callDeadline)
	if !ok {
		tail := "."
		if deletedChildren != nil {
			tail = "; cascade-deleted lists the owned rows already deleted."
		}
		return ODataDeleteItemResult{ID: id, Error: odataText(fmt.Sprintf("%s(%s) was not deleted: this call's time budget ran out "+
			"before its DELETE could be sent%s", entity, id, tail)), CascadeDeleted: deletedChildren}
	}
	outcome := service.delete(entity, id, budget)
	run.note(outcome)
	if outcome.success {
		return ODataDeleteItemResult{ID: id, Success: true, CascadeDeleted: deletedChildren}
	}
	if !outcome.received {
		// A transport failure has no refusal to explain, and its write may have landed.
		return ODataDeleteItemResult{ID: id, Error: odataText(outcome.err), CascadeDeleted: deletedChildren}
	}
	return odataExplain(service, entity, id, outcome, callDeadline, deletedChildren)
}

func nonEmptyKeys(keys []RecordKey) []RecordKey {
	if len(keys) == 0 {
		return nil
	}
	return append([]RecordKey(nil), keys...)
}

// deleteOwnedChildren deletes the planned owned rows deepest first; a refusal or the first failed child is
// returned.
func (run *odataDeleteRun) deleteOwnedChildren(service *recordDeletionService, target RecordKey, plan *odataCascadePlan,
	callDeadline time.Time, cascadeDeleted *[]RecordKey) *ODataDeleteItemResult {
	if plan.refusal != "" {
		return &ODataDeleteItemResult{ID: target.ID, Error: odataText(plan.refusal), Explanation: odataText(plan.explanation),
			Referencing: plan.referencing}
	}
	for _, child := range plan.order {
		budget, ok := odataDeleteBudget(callDeadline)
		if !ok {
			tail := "."
			if len(*cascadeDeleted) > 0 {
				tail = "; cascade-deleted lists the owned rows already deleted."
			}
			return &ODataDeleteItemResult{ID: target.ID, Error: odataText(fmt.Sprintf("cascade stopped: this call's time budget ran "+
				"out before owned row %s(%s) could be deleted, so %s(%s) was left in place%s", child.Entity, child.ID, target.Entity,
				target.ID, tail)), Referencing: plan.referencing, CascadeDeleted: nonEmptyKeys(*cascadeDeleted)}
		}
		outcome := service.delete(child.Entity, child.ID, budget)
		run.note(outcome)
		if !outcome.success {
			return odataChildFailure(target, child, outcome, plan, *cascadeDeleted)
		}
		*cascadeDeleted = append(*cascadeDeleted, child)
	}
	return nil
}

func odataChildFailure(target, child RecordKey, outcome recordDeleteOutcome, plan *odataCascadePlan, cascadeDeleted []RecordKey) *ODataDeleteItemResult {
	if outcome.attempted && !outcome.received {
		// No answer: the DELETE may have landed, so the row is neither listed as deleted nor as still there.
		return &ODataDeleteItemResult{ID: target.ID, Error: odataText(fmt.Sprintf("cascade stopped: the DELETE of owned row %s(%s) got "+
			"no answer, so it may have been deleted - check with odata-read; %s(%s) was left in place: %s", child.Entity, child.ID,
			target.Entity, target.ID, outcome.err)), Referencing: plan.referencing, CascadeDeleted: nonEmptyKeys(cascadeDeleted),
			CascadeUnconfirmed: []RecordKey{child}}
	}
	reason := outcome.err
	if outcome.serverDetail != "" {
		reason = fmt.Sprintf("%s The server's own reason: %s.", outcome.err, outcome.serverDetail)
	}
	return &ODataDeleteItemResult{ID: target.ID, Error: odataText(fmt.Sprintf("cascade stopped: owned row %s(%s) was not deleted, so "+
		"%s(%s) was left in place: %s", child.Entity, child.ID, target.Entity, target.ID, reason)), Referencing: plan.referencing,
		CascadeDeleted: nonEmptyKeys(cascadeDeleted)}
}

// ---- the cascade walk ----

// odataCascadePlan is CascadePlan: owned rows deepest first, the record's referencing rows, and a refusal.
type odataCascadePlan struct {
	order           []RecordKey
	referencing     []ReferencingRows
	refusal         string
	explanation     string
	platformCascade []RecordKey
}

type odataCascadeWalk struct {
	service         *recordDeletionService
	order           []RecordKey
	seen            map[RecordKey]bool
	stopAt          time.Time
	checkedCascade  map[RecordKey]bool
	platformCascade []RecordKey
}

// odataCollectOwned is CollectOwned: the whole tree is checked BEFORE anything is deleted.
func odataCollectOwned(service *recordDeletionService, entity, id string, stopAt time.Time, root *recordDeleteDiagnosis) *odataCascadePlan {
	walk := &odataCascadeWalk{service: service, order: []RecordKey{}, seen: map[RecordKey]bool{{entity, id}: true}, stopAt: stopAt,
		checkedCascade: map[RecordKey]bool{}, platformCascade: []RecordKey{}}
	if root == nil {
		root = service.diagnose(entity, id, odataDeleteDiagnosisCall, stopAt)
	}
	refusal, explanation := walk.visit(RecordKey{entity, id}, root, 1)
	return &odataCascadePlan{order: walk.order, referencing: root.referencingList(), refusal: refusal, explanation: explanation,
		platformCascade: walk.platformCascade}
}

func noOutcome() recordDeleteOutcome { return recordDeleteOutcome{} }

func (walk *odataCascadeWalk) visit(node RecordKey, diagnosis *recordDeleteDiagnosis, depth int) (string, string) {
	if blocker := odataCascadeBlocker(diagnosis); blocker != "" {
		return fmt.Sprintf("cascade refused: %s(%s) %s. Nothing was deleted.", node.Entity, node.ID, blocker),
			explainDeleteRefusal(node.Entity, node.ID, noOutcome(), diagnosis)
	}
	for _, rows := range diagnosis.referencing {
		if rows.Kind != referencingCascade {
			continue
		}
		if refusal, explanation := walk.checkPlatformCascade(node, rows); refusal != "" {
			return refusal, explanation
		}
	}
	owned := []ReferencingRows{}
	for _, rows := range diagnosis.referencing {
		if rows.Kind == referencingOwned {
			owned = append(owned, rows)
		}
	}
	// A row that requires a lookup value only refers to it: a dictionary value never owns business records.
	if len(owned) > 0 && walk.service.isLookup(node.Entity, odataDeleteDiagnosisCall) {
		return fmt.Sprintf("cascade refused: %s(%s) is a lookup (dictionary) value, or could not be shown not to be one, so the %s "+
			"rows that require it only refer to it. Nothing was deleted.", node.Entity, node.ID, owned[0].Entity), ""
	}
	for _, rows := range owned {
		if rows.More {
			return fmt.Sprintf("cascade refused: more than %d %s rows reference %s(%s) through %s. Nothing was deleted.",
				recordDeletionMaxIDsPerPair, rows.Entity, node.Entity, node.ID, rows.Column), ""
		}
		for _, childID := range rows.IDs {
			if refusal, explanation := walk.visitChild(RecordKey{rows.Entity, childID}, depth); refusal != "" {
				return refusal, explanation
			}
		}
	}
	return "", ""
}

// checkPlatformCascade: a row the platform deletes with the node must itself be free.
func (walk *odataCascadeWalk) checkPlatformCascade(node RecordKey, rows ReferencingRows) (string, string) {
	if rows.More {
		return fmt.Sprintf("cascade refused: more than %d %s rows that the platform deletes with %s(%s) reference it through %s, so "+
			"they cannot all be checked. Nothing was deleted.", recordDeletionMaxIDsPerPair, rows.Entity, node.Entity, node.ID, rows.Column), ""
	}
	for _, id := range rows.IDs {
		dependent := RecordKey{rows.Entity, id}
		if walk.checkedCascade[dependent] {
			continue
		}
		walk.checkedCascade[dependent] = true
		walk.platformCascade = append(walk.platformCascade, dependent)
		diagnosis := walk.service.diagnose(dependent.Entity, dependent.ID, odataDeleteDiagnosisCall, walk.stopAt)
		blocker := odataCascadeBlocker(diagnosis)
		if blocker == "" {
			for _, nested := range diagnosis.referencing {
				if nested.Kind == referencingOwned {
					blocker = "is referenced by owned rows that the platform's cascade does not delete"
					break
				}
			}
		}
		if blocker != "" {
			return fmt.Sprintf("cascade refused: %s(%s), which the platform deletes together with %s(%s), %s. Nothing was deleted.",
					dependent.Entity, dependent.ID, node.Entity, node.ID, blocker),
				explainDeleteRefusal(dependent.Entity, dependent.ID, noOutcome(), diagnosis)
		}
	}
	return "", ""
}

func (walk *odataCascadeWalk) visitChild(child RecordKey, depth int) (string, string) {
	if walk.seen[child] {
		return "", ""
	}
	walk.seen[child] = true
	// Shared master data is never deleted as somebody's owned row, whatever lookup points from it.
	if odataCascadeRefused(child.Entity) {
		return fmt.Sprintf("cascade refused: %s(%s) would be deleted as an owned row, and cascade never deletes %s rows. "+
			"Nothing was deleted.", child.Entity, child.ID, child.Entity), ""
	}
	diagnosis := walk.service.diagnose(child.Entity, child.ID, odataDeleteDiagnosisCall, walk.stopAt)
	if depth < odataDeleteMaxCascadeDepth {
		if refusal, explanation := walk.visit(child, diagnosis, depth+1); refusal != "" {
			return refusal, explanation
		}
	} else if refusal, explanation := walk.checkLeaf(child, diagnosis); refusal != "" {
		return refusal, explanation
	}
	walk.order = append(walk.order, child)
	if len(walk.order) > odataDeleteMaxCascadeRows {
		return fmt.Sprintf("cascade refused: more than %d owned rows. Nothing was deleted.", odataDeleteMaxCascadeRows), ""
	}
	return "", ""
}

// checkLeaf: a row at the depth limit must have nothing below it but rows the platform deletes with it.
func (walk *odataCascadeWalk) checkLeaf(child RecordKey, diagnosis *recordDeleteDiagnosis) (string, string) {
	if blocker := odataCascadeBlocker(diagnosis); blocker != "" {
		return fmt.Sprintf("cascade refused: %s(%s) %s. Nothing was deleted.", child.Entity, child.ID, blocker),
			explainDeleteRefusal(child.Entity, child.ID, noOutcome(), diagnosis)
	}
	for _, nested := range diagnosis.referencing {
		if nested.Kind != referencingCascade {
			return fmt.Sprintf("cascade refused: owned rows go deeper than %d levels below the record (%s(%s) is itself referenced). "+
				"Nothing was deleted.", odataDeleteMaxCascadeDepth, child.Entity, child.ID), ""
		}
	}
	for _, rows := range diagnosis.referencing {
		if refusal, explanation := walk.checkPlatformCascade(child, rows); refusal != "" {
			return refusal, explanation
		}
	}
	return "", ""
}

func odataCascadeBlocker(diagnosis *recordDeleteDiagnosis) string {
	if diagnosis.visible == nil || !*diagnosis.visible {
		return "is not visible to this user or could not be read"
	}
	if diagnosis.processElementID != "" {
		return "is held by live process element " + diagnosis.processElementID
	}
	if !diagnosis.complete {
		return "has referencing tables that could not be checked in time, so its owned rows are not known for certain"
	}
	for _, rows := range diagnosis.referencing {
		if rows.Kind == referencingLinked {
			return fmt.Sprintf("is still referenced by linked rows (%s.%s) that cascade never deletes", rows.Entity, rows.Column)
		}
	}
	return ""
}

// odataDeleteRecord sends DELETE odata/{entity}({id}) once (RecordDeletionService.Delete).
func (s *recordDeletionService) delete(entity, id string, timeout time.Duration) recordDeleteOutcome {
	response, err := s.client.dataWriteSendOnce(s.ctx, http.MethodDelete, dataWriteKeyPath(entity, id), []byte{}, max(time.Millisecond, timeout))
	if err != nil {
		return recordDeleteOutcome{err: redact.Text(err.Error()), attempted: true}
	}
	if message := odataValidateWriteResponse(response); message != "" {
		return recordDeleteOutcome{err: message, serverDetail: recordDeletionInnerMessage(response), attempted: true, received: true}
	}
	return recordDeleteOutcome{success: true, attempted: true, received: true}
}
