package creatio

// place-section-in-workplace, ported from clio 8.1.0.142's WorkplacePlacementService, WorkplaceRepository and
// PackageTargetResolver: every input is resolved before the first write, the workplace (SysWorkplace), the role
// grants (SysAdminUnitInWorkplace) and the section placement (SysModuleInWorkplace) are written through the
// DB-first data-binding service so each row ships with the package, the result is read back, and the
// navigation cache is reset. A failure after the first write reports every write already made.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
)

const (
	wpMyApplicationsBinding = "SysWorkplace_MyApps"
	wpMyApplicationsName    = "My applications"
	wpWorkplaceSchema       = "SysWorkplace"
	wpPlacementSchema       = "SysModuleInWorkplace"
	wpGrantSchema           = "SysAdminUnitInWorkplace"
	wpIDColumnUID           = "ae0e45ca-c495-4fe7-a39d-3ab7278e1617"
	wpReloginNote           = "Workplace and section lists are cached per session: tell the user to log out and back in to see the change " +
		"(a browser refresh is not enough; no restart is needed)."
	wpAskForPackage = "Ask the user which package should receive the change, then call again with that package named."
	// wpSelectTimeout stands in for the client's default request timeout; clio's SelectQueryHelper sends an
	// unbounded read up to three times while the server reports a transient failure.
	wpSelectTimeout = 100 * time.Second
)

var wpClientTypeNames = map[string]string{"web": "Web", "mobile": "Mobile"}

// wpWellKnownClientTypes are the platform-seeded SysApplicationClientType ids.
var wpWellKnownClientTypes = map[string]string{"web": "195785b4-f55a-4e72-ace3-6480b54c8fa5", "mobile": "be8ab9ee-344e-4a56-8c97-41c84f22bd88"}

// WorkplacePlacementRequest is clio's WorkplacePlacementRequest as the MCP tool fills it.
type WorkplacePlacementRequest struct {
	EnvironmentName          string
	PackageName              string
	SectionCode              string
	WorkplaceName            string
	ClientType               string
	Roles                    []*string
	RemoveFromMyApplications bool
	Position                 *int
	Mode                     string
}

// WorkplaceLocation is clio's WorkplaceLocation.
type WorkplaceLocation struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// WorkplacePlacementWorkplace is clio's WorkplacePlacementWorkplace.
type WorkplacePlacementWorkplace struct {
	ID          string  `json:"id"`
	Name        *string `json:"name"`
	ClientType  string  `json:"client-type"`
	Position    int     `json:"position"`
	Created     bool    `json:"created"`
	BindingName *string `json:"binding-name,omitempty"`
}

// WorkplacePlacementSection is clio's WorkplacePlacementSection.
type WorkplacePlacementSection struct {
	SectionID   string  `json:"section-id"`
	Code        *string `json:"code"`
	PlacementID string  `json:"placement-id"`
	Action      string  `json:"action"`
	BindingName *string `json:"binding-name,omitempty"`
}

// WorkplacePlacementRole is clio's WorkplacePlacementRole.
type WorkplacePlacementRole struct {
	RoleID      string  `json:"role-id"`
	Name        *string `json:"name"`
	GrantID     string  `json:"grant-id"`
	Action      string  `json:"action"`
	BindingName *string `json:"binding-name,omitempty"`
}

// WorkplacePlacementWrite is clio's WorkplacePlacementWrite.
type WorkplacePlacementWrite struct {
	Schema      string  `json:"schema"`
	ID          *string `json:"id,omitempty"`
	BindingName string  `json:"binding-name"`
}

// PlaceSectionInWorkplaceResponse is clio's PlaceSectionInWorkplaceResponse.
type PlaceSectionInWorkplaceResponse struct {
	Success           bool                         `json:"success"`
	PackageName       *string                      `json:"package-name,omitempty"`
	Workplace         *WorkplacePlacementWorkplace `json:"workplace,omitempty"`
	Section           *WorkplacePlacementSection   `json:"section,omitempty"`
	Roles             *[]WorkplacePlacementRole    `json:"roles,omitempty"`
	Warnings          []string                     `json:"warnings,omitempty"`
	SectionWorkplaces *[]WorkplaceLocation         `json:"section-workplaces,omitempty"`
	NextStep          *string                      `json:"next-step,omitempty"`
	Error             *string                      `json:"error,omitempty"`
	WritesStarted     *bool                        `json:"writes-started,omitempty"`
	Written           *[]WorkplacePlacementWrite   `json:"written,omitempty"`
}

// PlaceSectionFailure is PlaceSectionInWorkplaceTool.Failure.
func PlaceSectionFailure(message string) PlaceSectionInWorkplaceResponse {
	if strings.TrimSpace(message) == "" {
		message = "unknown"
	}
	return PlaceSectionInWorkplaceResponse{Error: &message}
}

// wpResult is clio's WorkplacePlacementResult.
type wpResult struct {
	packageName       *string
	workplace         *WorkplacePlacementWorkplace
	section           *WorkplacePlacementSection
	roles             []WorkplacePlacementRole
	warnings          []string
	sectionWorkplaces []WorkplaceLocation
	nextStep          *string
	err               *string
	written           []WorkplacePlacementWrite
}

type wpWorkplaceRecord struct {
	id, clientTypeID, typeID, loaderID string
	name                               *string
	position                           int
}

type wpSectionRecord struct {
	id   string
	code *string
}

type wpJunctionRecord struct {
	id, workplaceID, targetID string
	workplaceName             *string
}

type wpAdminUnit struct {
	id   string
	name *string
}

type wpBindingRecord struct {
	packageName, bindingName string
	unverified               bool
}

type wpPackage struct{ name, uid string }

// wpBindings is one schema's bindings in the target package: name -> bound row ids, in the order they were read.
type wpBindings struct {
	names []string
	ids   map[string]map[string]bool // lower-case binding name -> lower-case ids
	real  map[string]string          // lower-case binding name -> name as read
}

func (b *wpBindings) has(name string) bool { _, ok := b.ids[strings.ToLower(name)]; return ok }

func (b *wpBindings) add(name string, ids map[string]bool) {
	if !b.has(name) {
		b.names = append(b.names, name)
		b.real[strings.ToLower(name)] = name
	}
	b.ids[strings.ToLower(name)] = ids
}

// wpRun is one Place call: WorkplacePlacementService with its per-call caches.
type wpRun struct {
	ctx     context.Context
	c       *Client
	request WorkplacePlacementRequest
	pkg     wpPackage
	bound   map[string]*wpBindings // lower-case schema name
	order   []string               // schema names in the order their bindings were loaded
	written []WorkplacePlacementWrite
	// binding is the call's one DataBindingDbService: it reads the file system development mode once per call.
	binding *dataWriteBindingRun
}

func (r *wpRun) bindingRun() *dataWriteBindingRun {
	if r.binding == nil {
		r.binding = &dataWriteBindingRun{ctx: r.ctx, client: r.c}
	}
	return r.binding
}

func wpFail(format string, args ...any) error { return errors.New(fmt.Sprintf(format, args...)) }

// PlaceSectionInWorkplace is PlaceSectionInWorkplaceTool's call of IWorkplacePlacementService.Place: a
// precondition failure (nothing written) is the tool's Failure, a failure after the first write its PartialFailure.
func (c *Client) PlaceSectionInWorkplace(ctx context.Context, request WorkplacePlacementRequest) (PlaceSectionInWorkplaceResponse, error) {
	run := &wpRun{ctx: ctx, c: c, request: request, bound: map[string]*wpBindings{}}
	result, err := run.place()
	if err != nil {
		return PlaceSectionInWorkplaceResponse{}, err
	}
	if result.err != nil {
		started := true
		return PlaceSectionInWorkplaceResponse{Error: result.err, WritesStarted: &started, Written: &result.written,
			PackageName: result.packageName, Warnings: wpNilIfEmpty(result.warnings), NextStep: result.nextStep}, nil
	}
	return PlaceSectionInWorkplaceResponse{Success: true, PackageName: result.packageName, Workplace: result.workplace,
		Section: result.section, Roles: &result.roles, Warnings: wpNilIfEmpty(result.warnings),
		SectionWorkplaces: &result.sectionWorkplaces, NextStep: result.nextStep}, nil
}

func wpNilIfEmpty(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	return values
}

func wpNormalizeMode(mode string) string {
	if strings.TrimSpace(mode) == "" {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(mode))
}

// wpValidate is WorkplacePlacementService.Validate.
func wpValidate(request WorkplacePlacementRequest) error {
	if strings.TrimSpace(request.SectionCode) == "" {
		return errors.New("section-code is required.")
	}
	mode := wpNormalizeMode(request.Mode)
	if mode != "" && mode != "existing" && mode != "new" && mode != "none" {
		return errors.New("mode must be 'existing', 'new' or 'none'.")
	}
	if mode == "none" {
		if len(request.Roles) > 0 {
			return errors.New("mode=none writes nothing, so roles cannot be granted; omit roles.")
		}
		return nil
	}
	if strings.TrimSpace(request.WorkplaceName) == "" {
		return errors.New("workplace-name is required unless mode is 'none'.")
	}
	if _, ok := wpClientTypeNames[strings.ToLower(strings.TrimSpace(request.ClientType))]; !ok || strings.TrimSpace(request.ClientType) == "" {
		return errors.New("client-type must be 'web' or 'mobile'.")
	}
	for _, role := range request.Roles {
		if role == nil || strings.TrimSpace(*role) == "" {
			return errors.New("roles must not contain blank entries.")
		}
	}
	if strings.EqualFold(strings.TrimSpace(request.WorkplaceName), wpMyApplicationsName) && request.RemoveFromMyApplications {
		return fmt.Errorf("The target is '%s' itself; pass remove-from-my-applications=false.", wpMyApplicationsName)
	}
	return nil
}

func (r *wpRun) place() (wpResult, error) {
	if err := wpValidate(r.request); err != nil {
		return wpResult{}, err
	}
	warnings := []string{}
	mode := wpNormalizeMode(r.request.Mode)
	if mode == "none" {
		section, err := r.resolveSection(strings.TrimSpace(r.request.SectionCode))
		if err != nil {
			return wpResult{}, err
		}
		placements, err := r.findSectionPlacements(section.id)
		if err != nil {
			return wpResult{}, err
		}
		return wpResult{roles: []WorkplacePlacementRole{}, warnings: warnings, sectionWorkplaces: wpLocations(placements)}, nil
	}
	pkg, err := r.resolvePackageTarget(r.request.PackageName)
	if err != nil {
		return wpResult{}, err
	}
	r.pkg = pkg
	clientTypeName := wpClientTypeNames[strings.ToLower(strings.TrimSpace(r.request.ClientType))]
	clientTypeID, err := r.findClientTypeID(clientTypeName)
	if err != nil {
		return wpResult{}, err
	}
	if clientTypeID == "" {
		return wpResult{}, wpFail("SysApplicationClientType '%s' was not found in the environment.", clientTypeName)
	}
	section, err := r.resolveSection(strings.TrimSpace(r.request.SectionCode))
	if err != nil {
		return wpResult{}, err
	}
	roles, err := r.resolveRoles(r.request.Roles)
	if err != nil {
		return wpResult{}, err
	}
	workplaceName := strings.TrimSpace(r.request.WorkplaceName)
	code := ""
	if section.code != nil {
		code = *section.code
	}
	bindingSuffix := wpBindingSuffix(workplaceName, code)
	myApplications, err := r.findMyApplicationsIDs(clientTypeID)
	if err != nil {
		return wpResult{}, err
	}
	sectionPlacements, err := r.findSectionPlacements(section.id)
	if err != nil {
		return wpResult{}, err
	}
	holding := map[string]bool{}
	for _, row := range sectionPlacements {
		holding[row.workplaceID] = true
	}
	existing, err := r.findWorkplaces(workplaceName, clientTypeID)
	if err != nil {
		return wpResult{}, err
	}
	var reused *wpWorkplaceRecord
	for i := range existing {
		if holding[existing[i].id] {
			reused = &existing[i]
			break
		}
	}
	if reused == nil && len(existing) > 0 {
		reused = &existing[0]
	}
	if err := r.checkMode(mode, workplaceName, existing); err != nil {
		return wpResult{}, err
	}
	workplaceOwner, err := r.findForeignWorkplaceOwner(reused)
	if err != nil {
		return wpResult{}, err
	}
	if err := r.checkMove(roles, sectionPlacements, myApplications, existing, reused); err != nil {
		return wpResult{}, err
	}
	if len(existing) > 1 && reused != nil {
		ids := make([]string, 0, len(existing))
		for _, item := range existing {
			ids = append(ids, item.id)
		}
		warnings = append(warnings, fmt.Sprintf("%d workplaces are named '%s' for this client type (%s); %s was used.",
			len(existing), workplaceName, strings.Join(ids, ", "), reused.id))
	}
	placed, failure := r.write(clientTypeID, clientTypeName, bindingSuffix, reused, workplaceOwner, section, roles,
		sectionPlacements, myApplications, &warnings)
	if failure != nil {
		if len(r.written) == 0 || errors.Is(failure, context.Canceled) {
			return wpResult{}, failure
		}
		nextStep := r.resetNavigation(&warnings)
		message := failure.Error() + " The call failed after its first write: the rows in 'written' are already on the " +
			"environment. Re-run the same call to converge."
		name := pkg.name
		return wpResult{packageName: &name, roles: []WorkplacePlacementRole{}, warnings: warnings, nextStep: &nextStep,
			err: &message, written: append([]WorkplacePlacementWrite{}, r.written...)}, nil
	}
	nextStep := r.resetNavigation(&warnings)
	placed.nextStep = &nextStep
	placed.warnings = warnings
	return placed, nil
}

func (r *wpRun) write(clientTypeID, clientTypeName, bindingSuffix string, reused *wpWorkplaceRecord, owner *wpBindingRecord,
	section wpSectionRecord, roles []wpAdminUnit, placements []wpJunctionRecord, myApplications map[string]bool,
	warnings *[]string) (wpResult, error) {
	workplace, created, bindingName, err := r.ensureWorkplace(clientTypeID, bindingSuffix, reused, owner, warnings)
	if err != nil {
		return wpResult{}, err
	}
	grants := []WorkplacePlacementRole{}
	for _, role := range roles {
		grant, err := r.ensureRoleGrant(workplace.id, role, bindingSuffix)
		if err != nil {
			return wpResult{}, err
		}
		grants = append(grants, grant)
	}
	placement, err := r.ensureSectionPlacement(section, workplace, placements, myApplications, warnings)
	if err != nil {
		return wpResult{}, err
	}
	readPlacements, grantCount, err := r.verifyReadBack(section, workplace, placement, grants)
	if err != nil {
		return wpResult{}, err
	}
	if grantCount == 0 {
		*warnings = append(*warnings, "The workplace has no role grant, so nobody sees it. Pass roles to grant access.")
	}
	name := r.pkg.name
	return wpResult{packageName: &name,
		workplace: &WorkplacePlacementWorkplace{ID: workplace.id, Name: workplace.name, ClientType: clientTypeName,
			Position: workplace.position, Created: created, BindingName: bindingName},
		section: &placement, roles: grants, sectionWorkplaces: wpLocations(readPlacements)}, nil
}

func (r *wpRun) resetNavigation(warnings *[]string) string {
	if warning := r.c.appWriteResetNavigation(r.ctx); warning != "" {
		*warnings = append(*warnings, warning)
	}
	return wpReloginNote + " " + r.c.appWriteBrowserSessionNote()
}

// findMyApplicationsIDs is FindMyApplicationsIds: by the en-US name, else through create-app's binding.
func (r *wpRun) findMyApplicationsIDs(clientTypeID string) (map[string]bool, error) {
	ids := map[string]bool{}
	workplaces, err := r.findWorkplaces(wpMyApplicationsName, clientTypeID)
	if err != nil {
		return nil, err
	}
	for _, item := range workplaces {
		ids[item.id] = true
	}
	if len(ids) > 0 {
		return ids, nil
	}
	packages, err := r.findPackagesShippingBinding(wpMyApplicationsBinding)
	if err != nil {
		return nil, err
	}
	for _, packageName := range packages {
		rows, err := r.readBindingIDs(packageName, wpMyApplicationsBinding)
		if err != nil {
			return nil, err
		}
		for _, value := range rows {
			id, ok := parseGUID(value)
			if !ok {
				continue
			}
			workplace, err := r.getWorkplace(id)
			if err != nil {
				return nil, err
			}
			if workplace != nil && workplace.clientTypeID == clientTypeID {
				ids[id] = true
			}
		}
		if len(ids) > 0 {
			break
		}
	}
	return ids, nil
}

func (r *wpRun) checkMode(mode, workplaceName string, existing []wpWorkplaceRecord) error {
	switch {
	case mode == "":
		return nil
	case mode == "existing" && len(existing) == 0:
		return wpFail("No workplace named '%s' exists for this client type; pass mode=new to create it.", workplaceName)
	case mode == "new" && len(existing) > 0:
		for _, workplace := range existing {
			name, err := r.findBindingContaining(wpWorkplaceSchema, workplace.id)
			if err != nil {
				return err
			}
			if name != nil {
				return nil
			}
		}
		ids := make([]string, 0, len(existing))
		for _, item := range existing {
			ids = append(ids, item.id)
		}
		return wpFail("A workplace named '%s' already exists (%s); a second one with the same name would compete for every binding. "+
			"Pass mode=existing to use it, or choose another name.", workplaceName, strings.Join(ids, ", "))
	}
	return nil
}

// checkMove is CheckMoveBeforeWriting.
func (r *wpRun) checkMove(roles []wpAdminUnit, placements []wpJunctionRecord, myApplications map[string]bool,
	existing []wpWorkplaceRecord, reused *wpWorkplaceRecord) error {
	if !r.request.RemoveFromMyApplications {
		return nil
	}
	holding := map[string]bool{}
	for _, row := range placements {
		holding[row.workplaceID] = true
	}
	for _, workplace := range existing {
		if holding[workplace.id] || myApplications[workplace.id] {
			return nil
		}
	}
	var toMove *wpJunctionRecord
	for i := range placements {
		if myApplications[placements[i].workplaceID] {
			toMove = &placements[i]
			break
		}
	}
	if toMove == nil {
		return nil
	}
	targetHasGrant := false
	if reused != nil {
		grants, err := r.findRoleGrants(reused.id)
		if err != nil {
			return err
		}
		targetHasGrant = len(grants) > 0
	}
	if len(roles) == 0 && !targetHasGrant {
		return wpFail("Moving the section out of '%s' into '%s', which no role sees, would hide the app from everyone. "+
			"Pass roles, or remove-from-my-applications=false to add a placement.", wpMyApplicationsName, strings.TrimSpace(r.request.WorkplaceName))
	}
	owner, err := r.findBindingInOtherPackage(wpPlacementSchema, toMove.id)
	if err != nil {
		return err
	}
	if owner != nil {
		return wpFail("The section's '%s' placement %s is shipped by package '%s' (binding %s), so installing that package would "+
			"undo the move. Pass package-name=%s, or remove-from-my-applications=false.", wpMyApplicationsName, toMove.id,
			owner.packageName, owner.bindingName, owner.packageName)
	}
	return nil
}

// findForeignWorkplaceOwner is FindForeignWorkplaceOwner.
func (r *wpRun) findForeignWorkplaceOwner(reused *wpWorkplaceRecord) (*wpBindingRecord, error) {
	if reused == nil {
		return nil, nil
	}
	own, err := r.findBindingContaining(wpWorkplaceSchema, reused.id)
	if err != nil || own != nil {
		return nil, err
	}
	owner, err := r.findBindingInOtherPackage(wpWorkplaceSchema, reused.id)
	if err != nil {
		return nil, err
	}
	if owner != nil && r.request.Position != nil && *r.request.Position != reused.position {
		return nil, wpFail("Workplace '%s' (%s) is shipped by package '%s', so its position is not this package's to change. "+
			"Omit position, or use a workplace of your own.", wpText(reused.name), reused.id, owner.packageName)
	}
	return owner, nil
}

func wpText(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func (r *wpRun) findBindingInOtherPackage(schema, rowID string) (*wpBindingRecord, error) {
	bindings, err := r.findBindingsShippingRow(schema, rowID)
	if err != nil {
		return nil, err
	}
	for _, binding := range bindings {
		if strings.EqualFold(binding.packageName, r.pkg.name) {
			continue
		}
		if !binding.unverified {
			found := binding
			return &found, nil
		}
		ids, err := r.readBindingIDs(binding.packageName, binding.bindingName)
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			if strings.EqualFold(id, rowID) {
				found := binding
				return &found, nil
			}
		}
	}
	return nil, nil
}

func wpLocations(placements []wpJunctionRecord) []WorkplaceLocation {
	locations := []WorkplaceLocation{}
	seen := map[string]bool{}
	for _, row := range placements {
		if seen[row.workplaceID] {
			continue
		}
		seen[row.workplaceID] = true
		locations = append(locations, WorkplaceLocation{ID: row.workplaceID, Name: wpText(row.workplaceName)})
	}
	return locations
}

func (r *wpRun) resolveSection(code string) (wpSectionRecord, error) {
	candidates, err := r.findSectionsByCode(code)
	if err != nil {
		return wpSectionRecord{}, err
	}
	switch len(candidates) {
	case 0:
		return wpSectionRecord{}, wpFail("No section with SysModule.Code '%s' was found. Pass the section code, not its caption.", code)
	case 1:
		return candidates[0], nil
	}
	var placed []wpSectionRecord
	for _, candidate := range candidates {
		rows, err := r.findSectionPlacements(candidate.id)
		if err != nil {
			return wpSectionRecord{}, err
		}
		if len(rows) > 0 {
			placed = append(placed, candidate)
		}
	}
	if len(placed) == 1 {
		return placed[0], nil
	}
	ids := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		ids = append(ids, candidate.id)
	}
	return wpSectionRecord{}, wpFail("SysModule.Code '%s' matches %d sections (%s) and they cannot be told apart.", code, len(candidates),
		strings.Join(ids, ", "))
}

func (r *wpRun) resolveRoles(roles []*string) ([]wpAdminUnit, error) {
	resolved := []wpAdminUnit{}
	for _, raw := range roles {
		role := dotnetTrim(*raw)
		var record wpAdminUnit
		if id, ok := parseGUID(role); ok {
			unit, err := r.getAdminUnit(id)
			if err != nil {
				return nil, err
			}
			if unit == nil {
				return nil, wpFail("No role with id '%s' was found in SysAdminUnit.", id)
			}
			record = *unit
		} else {
			matches, err := r.findAdminUnitsByName(role)
			if err != nil {
				return nil, err
			}
			switch len(matches) {
			case 0:
				return nil, wpFail("No role named '%s' was found in SysAdminUnit.", role)
			case 1:
				record = matches[0]
			default:
				ids := make([]string, 0, len(matches))
				for _, match := range matches {
					ids = append(ids, match.id)
				}
				return nil, wpFail("Role name '%s' matches %d SysAdminUnit rows (%s); pass the id instead.", role, len(matches), strings.Join(ids, ", "))
			}
		}
		duplicate := false
		for _, existing := range resolved {
			if existing.id == record.id {
				duplicate = true
			}
		}
		if !duplicate {
			resolved = append(resolved, record)
		}
	}
	return resolved, nil
}

// ensureWorkplace is EnsureWorkplace: returns the read-back workplace, whether it was created and its binding.
func (r *wpRun) ensureWorkplace(clientTypeID, bindingSuffix string, reused *wpWorkplaceRecord, owner *wpBindingRecord,
	warnings *[]string) (wpWorkplaceRecord, bool, *string, error) {
	workplaceName := strings.TrimSpace(r.request.WorkplaceName)
	if owner != nil {
		*warnings = append(*warnings, fmt.Sprintf("Workplace '%s' is shipped by package '%s', so it was used but not bound into '%s'; "+
			"an environment that installs '%s' needs '%s' for the workplace.", wpText(reused.name), owner.packageName, r.pkg.name,
			r.pkg.name, owner.packageName))
		return *reused, false, nil, nil
	}
	created := reused == nil
	var workplaceID, bindingName string
	if created {
		workplaceID = newGUID()
		bindingName = wpWorkplaceBindingName(bindingSuffix)
		if err := r.ensureBinding(wpWorkplaceSchema, bindingName); err != nil {
			return wpWorkplaceRecord{}, false, nil, err
		}
		if err := r.upsert(wpWorkplaceSchema, bindingName, orderedFields{{"Id", workplaceID}, {"Name", workplaceName},
			{"SysApplicationClientType", clientTypeID}}); err != nil {
			return wpWorkplaceRecord{}, false, nil, err
		}
	} else {
		workplaceID = reused.id
		found, err := r.findBindingContaining(wpWorkplaceSchema, workplaceID)
		if err != nil {
			return wpWorkplaceRecord{}, false, nil, err
		}
		if found != nil {
			bindingName = *found
		} else {
			bindingName = wpWorkplaceBindingName(bindingSuffix)
			if err := r.ensureBinding(wpWorkplaceSchema, bindingName); err != nil {
				return wpWorkplaceRecord{}, false, nil, err
			}
			*warnings = append(*warnings, fmt.Sprintf("Workplace '%s' already existed and was not shipped by package '%s'; it is now "+
				"bound there, so installing the package ships it.", workplaceName, r.pkg.name))
		}
	}
	record, err := r.getWorkplace(workplaceID)
	if err != nil {
		return wpWorkplaceRecord{}, false, nil, err
	}
	if record == nil {
		return wpWorkplaceRecord{}, false, nil, wpFail("Workplace %s could not be read back.", workplaceID)
	}
	position := record.position
	if r.request.Position != nil {
		position = *r.request.Position
	}
	name := workplaceName
	if record.name != nil {
		name = *record.name
	}
	values := orderedFields{{"Id", record.id}, {"Name", name}, {"Position", position}, {"SysApplicationClientType", clientTypeID}}
	if record.typeID != emptyGUID {
		values = append(values, field{"Type", record.typeID})
	}
	if record.loaderID != emptyGUID {
		values = append(values, field{"LoaderId", record.loaderID})
	}
	if record.typeID == emptyGUID || record.loaderID == emptyGUID {
		*warnings = append(*warnings, fmt.Sprintf("Workplace %s has an empty Type or LoaderId, so its binding cannot carry them and "+
			"installing the package elsewhere creates an incomplete workplace.", record.id))
	}
	if err := r.upsert(wpWorkplaceSchema, bindingName, values); err != nil {
		return wpWorkplaceRecord{}, false, nil, err
	}
	readBack, err := r.getWorkplace(workplaceID)
	if err != nil {
		return wpWorkplaceRecord{}, false, nil, err
	}
	if readBack == nil {
		readBack = record
	}
	return *readBack, created, &bindingName, nil
}

// ensureSectionPlacement is EnsureSectionPlacement.
func (r *wpRun) ensureSectionPlacement(section wpSectionRecord, target wpWorkplaceRecord, placements []wpJunctionRecord,
	myApplications map[string]bool, warnings *[]string) (WorkplacePlacementSection, error) {
	unknown := len(myApplications) == 0
	if myApplications[target.id] {
		myApplications = map[string]bool{}
	}
	var inMyApplications []wpJunctionRecord
	var inTarget *wpJunctionRecord
	for i := range placements {
		if myApplications[placements[i].workplaceID] {
			inMyApplications = append(inMyApplications, placements[i])
		}
		if inTarget == nil && placements[i].workplaceID == target.id {
			inTarget = &placements[i]
		}
	}
	var placementID, action string
	switch {
	case inTarget != nil:
		placementID, action = inTarget.id, "already-present"
		if r.request.RemoveFromMyApplications && len(inMyApplications) > 0 {
			ids := make([]string, 0, len(inMyApplications))
			for _, row := range inMyApplications {
				ids = append(ids, row.id)
			}
			*warnings = append(*warnings, fmt.Sprintf("The section is also in '%s' (row %s); it was left there because this tool "+
				"never deletes a placement.", wpMyApplicationsName, strings.Join(ids, ", ")))
		}
	case r.request.RemoveFromMyApplications && len(inMyApplications) > 0:
		placementID, action = inMyApplications[0].id, "moved"
		if len(inMyApplications) > 1 {
			*warnings = append(*warnings, fmt.Sprintf("The section has %d placements in '%s'; only %s was moved.",
				len(inMyApplications), wpMyApplicationsName, placementID))
		}
	default:
		placementID, action = newGUID(), "created"
		elsewhere := false
		for _, row := range placements {
			if row.workplaceID != target.id {
				elsewhere = true
			}
		}
		if r.request.RemoveFromMyApplications && unknown && elsewhere {
			*warnings = append(*warnings, fmt.Sprintf("'%s' could not be identified (no en-US name and no %s binding), so the "+
				"section was added to '%s', not moved; it also stays in its other workplaces (see section-workplaces).",
				wpMyApplicationsName, wpMyApplicationsBinding, wpText(target.name)))
		}
	}
	return r.writePlacement(section, target, placementID, action)
}

func (r *wpRun) writePlacement(section wpSectionRecord, target wpWorkplaceRecord, placementID, action string) (WorkplacePlacementSection, error) {
	var bindingName *string
	if action != "created" {
		found, err := r.findBindingContaining(wpPlacementSchema, placementID)
		if err != nil {
			return WorkplacePlacementSection{}, err
		}
		bindingName = found
	}
	if action == "already-present" && bindingName == nil {
		owner, err := r.findBindingInOtherPackage(wpPlacementSchema, placementID)
		if err != nil {
			return WorkplacePlacementSection{}, err
		}
		if owner != nil {
			return WorkplacePlacementSection{SectionID: section.id, Code: section.code, PlacementID: placementID, Action: action}, nil
		}
	}
	if bindingName == nil {
		code := wpText(section.code)
		name := wpPlacementSchema + "_" + wpBindingSuffix(code, code)
		if err := r.ensureBinding(wpPlacementSchema, name); err != nil {
			return WorkplacePlacementSection{}, err
		}
		bindingName = &name
	}
	if err := r.upsert(wpPlacementSchema, *bindingName, orderedFields{{"Id", placementID}, {"SysWorkplace", target.id},
		{"SysModule", section.id}}); err != nil {
		return WorkplacePlacementSection{}, err
	}
	return WorkplacePlacementSection{SectionID: section.id, Code: section.code, PlacementID: placementID, Action: action,
		BindingName: bindingName}, nil
}

func (r *wpRun) ensureRoleGrant(workplaceID string, role wpAdminUnit, bindingSuffix string) (WorkplacePlacementRole, error) {
	grants, err := r.findRoleGrants(workplaceID)
	if err != nil {
		return WorkplacePlacementRole{}, err
	}
	var existing *wpJunctionRecord
	for i := range grants {
		if grants[i].targetID == role.id {
			existing = &grants[i]
			break
		}
	}
	grantID := newGUID()
	var bindingName *string
	if existing != nil {
		grantID = existing.id
		if bindingName, err = r.findBindingContaining(wpGrantSchema, grantID); err != nil {
			return WorkplacePlacementRole{}, err
		}
		if bindingName == nil {
			owner, err := r.findBindingInOtherPackage(wpGrantSchema, grantID)
			if err != nil {
				return WorkplacePlacementRole{}, err
			}
			if owner != nil {
				return WorkplacePlacementRole{RoleID: role.id, Name: role.name, GrantID: grantID, Action: "already-granted"}, nil
			}
		}
	}
	if bindingName == nil {
		name := wpGrantSchema + "_" + bindingSuffix
		if err := r.ensureBinding(wpGrantSchema, name); err != nil {
			return WorkplacePlacementRole{}, err
		}
		bindingName = &name
	}
	if err := r.upsert(wpGrantSchema, *bindingName, orderedFields{{"Id", grantID}, {"SysWorkplace", workplaceID},
		{"SysAdminUnit", role.id}}); err != nil {
		return WorkplacePlacementRole{}, err
	}
	action := "granted"
	if existing != nil {
		action = "already-granted"
	}
	return WorkplacePlacementRole{RoleID: role.id, Name: role.name, GrantID: grantID, Action: action, BindingName: bindingName}, nil
}

func (r *wpRun) verifyReadBack(section wpSectionRecord, target wpWorkplaceRecord, placement WorkplacePlacementSection,
	grants []WorkplacePlacementRole) ([]wpJunctionRecord, int, error) {
	placements, err := r.findSectionPlacements(section.id)
	if err != nil {
		return nil, 0, err
	}
	found := false
	for _, row := range placements {
		if row.id == placement.PlacementID && row.workplaceID == target.id {
			found = true
		}
	}
	if !found {
		return nil, 0, wpFail("Read-back failed: placement %s is not in workplace %s.", placement.PlacementID, target.id)
	}
	rows, err := r.findRoleGrants(target.id)
	if err != nil {
		return nil, 0, err
	}
	granted := map[string]bool{}
	for _, row := range rows {
		granted[row.targetID] = true
	}
	for _, grant := range grants {
		if !granted[grant.RoleID] {
			return nil, 0, wpFail("Read-back failed: role '%s' has no grant on workplace %s.", wpText(grant.Name), target.id)
		}
	}
	return placements, len(granted), nil
}

// ---- bindings of the target package ----

func (r *wpRun) findBindingContaining(schema, rowID string) (*string, error) {
	bindings, err := r.loadBindings(schema)
	if err != nil {
		return nil, err
	}
	for _, name := range bindings.names {
		if bindings.ids[strings.ToLower(name)][strings.ToLower(rowID)] {
			found := name
			return &found, nil
		}
	}
	return nil, nil
}

func (r *wpRun) loadBindings(schema string) (*wpBindings, error) {
	if cached, ok := r.bound[strings.ToLower(schema)]; ok {
		return cached, nil
	}
	names, err := r.findBindingNames(r.pkg.uid, schema)
	if err != nil {
		return nil, err
	}
	bindings := &wpBindings{ids: map[string]map[string]bool{}, real: map[string]string{}}
	for _, name := range names {
		ids, err := r.readBindingIDs(r.pkg.name, name)
		if err != nil {
			return nil, err
		}
		set := map[string]bool{}
		for _, id := range ids {
			if strings.TrimSpace(id) != "" {
				set[strings.ToLower(id)] = true
			}
		}
		bindings.add(name, set)
	}
	r.bound[strings.ToLower(schema)] = bindings
	r.order = append(r.order, schema)
	return bindings, nil
}

func (r *wpRun) ensureBinding(schema, bindingName string) error {
	bindings, err := r.loadBindings(schema)
	if err != nil {
		return err
	}
	if bindings.has(bindingName) {
		return nil
	}
	r.written = append(r.written, WorkplacePlacementWrite{Schema: schema, BindingName: bindingName})
	run := r.bindingRun()
	if _, _, err := run.createBinding(DataWriteBindingDbRequest{PackageName: r.pkg.name, SchemaName: schema, BindingName: bindingName}); err != nil {
		return err
	}
	bindings.add(bindingName, map[string]bool{})
	return nil
}

func (r *wpRun) upsert(schema, bindingName string, values orderedFields) error {
	id := fmt.Sprint(values[0].value)
	r.written = append(r.written, WorkplacePlacementWrite{Schema: schema, ID: &id, BindingName: bindingName})
	run := r.bindingRun()
	if err := run.upsertRow(DataWriteBindingDbRequest{PackageName: r.pkg.name, BindingName: bindingName,
		ValuesJSON: string(toJNode(values).stjJSON())}); err != nil {
		return err
	}
	for _, schemaName := range r.order {
		bindings := r.bound[strings.ToLower(schemaName)]
		if ids, ok := bindings.ids[strings.ToLower(bindingName)]; ok {
			ids[strings.ToLower(id)] = true
		}
	}
	return nil
}

// readBindingIDs is DataBindingDbService.ReadBinding reduced to each row's Id value.
func (r *wpRun) readBindingIDs(packageName, bindingName string) ([]string, error) {
	packageUID, err := r.c.dataBindingPackageUID(r.ctx, packageName)
	if err != nil {
		return nil, err
	}
	bindingUID, _, err := r.c.dataBindingFind(r.ctx, packageUID, bindingName)
	if err != nil {
		return nil, err
	}
	rows, err := r.c.dataBindingRows(r.ctx, bindingUID)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, row := range rows {
		if value, ok := row.props["Id"]; ok {
			ids = append(ids, dataBindingValue(value))
		}
	}
	return ids, nil
}

// wpWorkplaceBindingName is WorkplaceBindingName: never the My applications binding's name.
func wpWorkplaceBindingName(suffix string) string {
	name := wpWorkplaceSchema + "_" + suffix
	if strings.EqualFold(name, wpMyApplicationsBinding) {
		return name + "Workplace"
	}
	return name
}

// wpBindingSuffix is BuildBindingSuffix: the ASCII letters and digits of name, or of fallback when it has none.
func wpBindingSuffix(name, fallback string) string {
	clean := func(value string) string {
		var builder strings.Builder
		for _, r := range value {
			if r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
				builder.WriteRune(r)
			}
		}
		return builder.String()
	}
	if suffix := clean(name); suffix != "" {
		return suffix
	}
	return clean(fallback)
}

// ---- target package (PackageTargetResolver, requireEditable) ----

func (r *wpRun) resolvePackageTarget(packageName string) (wpPackage, error) {
	unavailable := func(err error) error {
		return errors.New("The environment could not be asked which package to deliver the data into: " + err.Error())
	}
	if strings.TrimSpace(packageName) != "" {
		name := strings.TrimSpace(packageName)
		rows, err := r.selectPackages(nil)
		if err != nil {
			return wpPackage{}, unavailable(err)
		}
		for _, row := range rows {
			if !strings.EqualFold(wpRowText(row, "Name"), name) {
				continue
			}
			if wpLocked(row) {
				return wpPackage{}, wpFail("Package '%s' is locked, so it cannot receive design-time writes. Unlock it with "+
					"unlock-package, or name another package.", wpRowText(row, "Name"))
			}
			return wpMaterialize(row, fmt.Sprintf("Package '%s'", wpRowText(row, "Name")))
		}
		return wpPackage{}, wpFail("Package '%s' was not found in the environment. Check the name against list-packages.", name)
	}
	current, err := r.c.readSysSettingValue(r.ctx, currentPackageSettingCode)
	if err != nil {
		return wpPackage{}, unavailable(r.c.rightsTransportFault(err, 0))
	}
	packageID, ok := parseGUID(current)
	if !ok || packageID == emptyGUID {
		return wpPackage{}, wpFail("No package was named, and the environment's %s system setting does not point at one, so there "+
			"is nowhere to deliver the package data. %s", currentPackageSettingCode, wpAskForPackage)
	}
	rows, err := r.selectPackages([]rightsFilter{{column: "UId", value: packageID, dataValueType: rightsGUIDType}})
	if err != nil {
		return wpPackage{}, unavailable(err)
	}
	if len(rows) > 1 {
		return wpPackage{}, wpFail("The environment's %s system setting points at package '%s', which matched %d packages, so the "+
			"delivery target cannot be told apart. %s", currentPackageSettingCode, current, len(rows), wpAskForPackage)
	}
	if len(rows) == 0 || strings.TrimSpace(wpRowText(rows[0], "Name")) == "" {
		return wpPackage{}, wpFail("The environment's %s system setting points at package '%s', which could not be resolved to a "+
			"usable package. %s", currentPackageSettingCode, current, wpAskForPackage)
	}
	if wpLocked(rows[0]) {
		return wpPackage{}, wpFail("The environment's %s system setting points at package '%s', which is locked, so it cannot "+
			"receive design-time writes. Unlock it with unlock-package, or name another package.", currentPackageSettingCode,
			wpRowText(rows[0], "Name"))
	}
	return wpMaterialize(rows[0], fmt.Sprintf("The %s package '%s'", currentPackageSettingCode, wpRowText(rows[0], "Name")))
}

func wpLocked(row *jnode) bool {
	value := row.get("InstallType")
	return value != nil && value.kind != jkNull && value.text != "0"
}

func wpMaterialize(row *jnode, subject string) (wpPackage, error) {
	uid, ok := parseGUID(wpRowText(row, "UId"))
	if !ok || uid == emptyGUID {
		return wpPackage{}, wpFail("%s has no usable UId in the environment, so package data cannot be addressed to it. %s", subject, wpAskForPackage)
	}
	return wpPackage{name: wpRowText(row, "Name"), uid: uid}, nil
}

func (r *wpRun) selectPackages(filters []rightsFilter) ([]*jnode, error) {
	return r.selectRows("SysPackage", []rightsColumn{{path: "Name", alias: "Name"}, {path: "UId", alias: "UId"},
		{path: "InstallType", alias: "InstallType"}}, filters)
}

// ---- WorkplaceRepository ----

// selectRows is SelectQueryHelper.ExecuteSelectQuery without a timeout: up to three sends while the server
// reports a transient failure, 500 ms apart.
func (r *wpRun) selectRows(root string, columns []rightsColumn, filters []rightsFilter) ([]*jnode, error) {
	return r.c.clioSelectRows(r.ctx, root, columns, filters)
}

// clioSelectRows is SelectQueryHelper.ExecuteSelectQuery(BuildSelectQuery(...)) without a request timeout.
func (c *Client) clioSelectRows(ctx context.Context, root string, columns []rightsColumn, filters []rightsFilter) ([]*jnode, error) {
	query := rightsSelectQuery(root, columns, filters, 10000, false)
	var rows []*jnode
	var err error
	for attempt := 1; ; attempt++ {
		rows, err = c.rightsSelect(ctx, query, rightsRequest{timeout: wpSelectTimeout})
		if err == nil || attempt >= 3 || !wpTransient(err) {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return rows, err
}

func wpTransient(err error) bool {
	var fault *rightsFault
	if !errors.As(err, &fault) || !strings.HasPrefix(fault.message, "SelectQuery failed: ") {
		return false
	}
	message := strings.ToLower(strings.TrimPrefix(fault.message, "SelectQuery failed: "))
	for _, marker := range []string{"collection was modified", "deadlock", "timeout", "timed out"} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

// wpRowText is WorkplaceRepository.ReadString with null read as "".
func wpRowText(row *jnode, key string) string {
	value := wpRowString(row, key)
	if value == nil {
		return ""
	}
	return *value
}

// wpRowString is WorkplaceRepository.ReadString: null when absent or null, a lookup's value, else the JSON text.
func wpRowString(row *jnode, key string) *string {
	value, ok := row.props[key]
	if !ok || value == nil || value.kind == jkNull {
		return nil
	}
	if value.kind == jkObject {
		if inner, ok := value.props["value"]; ok {
			if inner == nil || inner.kind == jkNull {
				return nil
			}
			return wpElementText(inner)
		}
	}
	return wpElementText(value)
}

// wpElementText is JsonElement.ToString(): a string's text, true/false as True/False, anything else its raw JSON.
func wpElementText(value *jnode) *string {
	var text string
	switch value.kind {
	case jkString:
		text = value.text
	case jkBool:
		text = "False"
		if value.flag {
			text = "True"
		}
	case jkInteger, jkFloat:
		text = value.text
	default:
		text = string(value.stjJSON())
	}
	return &text
}

func wpRowGUID(row *jnode, key string) string {
	if id, ok := parseGUID(wpRowText(row, key)); ok {
		return id
	}
	return emptyGUID
}

func wpRowInt(row *jnode, key string) int {
	var value int
	if _, err := fmt.Sscanf(strings.TrimSpace(wpRowText(row, key)), "%d", &value); err != nil {
		return 0
	}
	return value
}

func wpText1(path string) rightsColumn { return rightsColumn{path: path, alias: path} }

func (r *wpRun) findClientTypeID(name string) (string, error) {
	rows, err := r.selectRows("SysApplicationClientType", []rightsColumn{wpText1("Id"), wpText1("Name")}, nil)
	if err != nil {
		return "", err
	}
	for _, row := range rows {
		if strings.EqualFold(wpRowText(row, "Name"), name) && wpRowString(row, "Name") != nil {
			return wpRowGUID(row, "Id"), nil
		}
	}
	wellKnown, ok := wpWellKnownClientTypes[strings.ToLower(name)]
	if !ok {
		return "", nil
	}
	for _, row := range rows {
		if wpRowGUID(row, "Id") == wellKnown {
			return wellKnown, nil
		}
	}
	return "", nil
}

func (r *wpRun) findSectionsByCode(code string) ([]wpSectionRecord, error) {
	rows, err := r.selectRows("SysModule", []rightsColumn{wpText1("Id"), wpText1("Code")},
		[]rightsFilter{{column: "Code", value: code, dataValueType: rightsTextType}})
	if err != nil {
		return nil, err
	}
	records := make([]wpSectionRecord, 0, len(rows))
	for _, row := range rows {
		records = append(records, wpSectionRecord{id: wpRowGUID(row, "Id"), code: wpRowString(row, "Code")})
	}
	return records, nil
}

func wpWorkplaceColumns() []rightsColumn {
	return []rightsColumn{wpText1("Id"), wpText1("Name"), wpText1("Position"),
		{path: "SysApplicationClientType.Id", alias: "ClientTypeId"}, {path: "Type.Id", alias: "TypeId"}, wpText1("LoaderId"),
		{path: "CreatedOn", alias: "CreatedOn", orderDirection: 1, orderPosition: 0}}
}

func wpWorkplace(row *jnode) wpWorkplaceRecord {
	return wpWorkplaceRecord{id: wpRowGUID(row, "Id"), name: wpRowString(row, "Name"), position: wpRowInt(row, "Position"),
		clientTypeID: wpRowGUID(row, "ClientTypeId"), typeID: wpRowGUID(row, "TypeId"), loaderID: wpRowGUID(row, "LoaderId")}
}

func (r *wpRun) findWorkplaces(name, clientTypeID string) ([]wpWorkplaceRecord, error) {
	rows, err := r.selectRows("SysWorkplace", wpWorkplaceColumns(), []rightsFilter{
		{column: "Name", value: name, dataValueType: rightsTextType},
		{column: "SysApplicationClientType.Id", value: clientTypeID, dataValueType: rightsGUIDType}})
	if err != nil {
		return nil, err
	}
	records := make([]wpWorkplaceRecord, 0, len(rows))
	for _, row := range rows {
		records = append(records, wpWorkplace(row))
	}
	return records, nil
}

func (r *wpRun) getWorkplace(id string) (*wpWorkplaceRecord, error) {
	rows, err := r.selectRows("SysWorkplace", wpWorkplaceColumns(), []rightsFilter{{column: "Id", value: id, dataValueType: rightsGUIDType}})
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	record := wpWorkplace(rows[0])
	return &record, nil
}

func (r *wpRun) selectJunction(schema, target string, filter rightsFilter) ([]wpJunctionRecord, error) {
	rows, err := r.selectRows(schema, []rightsColumn{wpText1("Id"), {path: "SysWorkplace.Id", alias: "WorkplaceId"},
		{path: "SysWorkplace.Name", alias: "WorkplaceName"}, {path: target + ".Id", alias: "TargetId"}}, []rightsFilter{filter})
	if err != nil {
		return nil, err
	}
	records := make([]wpJunctionRecord, 0, len(rows))
	for _, row := range rows {
		records = append(records, wpJunctionRecord{id: wpRowGUID(row, "Id"), workplaceID: wpRowGUID(row, "WorkplaceId"),
			targetID: wpRowGUID(row, "TargetId"), workplaceName: wpRowString(row, "WorkplaceName")})
	}
	return records, nil
}

func (r *wpRun) findSectionPlacements(sectionID string) ([]wpJunctionRecord, error) {
	return r.selectJunction(wpPlacementSchema, "SysModule", rightsFilter{column: "SysModule.Id", value: sectionID, dataValueType: rightsGUIDType})
}

func (r *wpRun) findRoleGrants(workplaceID string) ([]wpJunctionRecord, error) {
	return r.selectJunction(wpGrantSchema, "SysAdminUnit", rightsFilter{column: "SysWorkplace.Id", value: workplaceID, dataValueType: rightsGUIDType})
}

func (r *wpRun) findAdminUnitsByName(name string) ([]wpAdminUnit, error) {
	rows, err := r.selectRows("SysAdminUnit", []rightsColumn{wpText1("Id"), wpText1("Name")},
		[]rightsFilter{{column: "Name", value: name, dataValueType: rightsTextType}})
	if err != nil {
		return nil, err
	}
	units := make([]wpAdminUnit, 0, len(rows))
	for _, row := range rows {
		units = append(units, wpAdminUnit{id: wpRowGUID(row, "Id"), name: wpRowString(row, "Name")})
	}
	return units, nil
}

func (r *wpRun) getAdminUnit(id string) (*wpAdminUnit, error) {
	rows, err := r.selectRows("SysAdminUnit", []rightsColumn{wpText1("Id"), wpText1("Name")},
		[]rightsFilter{{column: "Id", value: id, dataValueType: rightsGUIDType}})
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return &wpAdminUnit{id: wpRowGUID(rows[0], "Id"), name: wpRowString(rows[0], "Name")}, nil
}

func (r *wpRun) findBindingNames(packageUID, schema string) ([]string, error) {
	rows, err := r.selectRows("SysPackageSchemaData", []rightsColumn{wpText1("Name")}, []rightsFilter{
		{column: "SysPackage.UId", value: packageUID, dataValueType: rightsGUIDType},
		{column: "SysSchema.Name", value: schema, dataValueType: rightsTextType}})
	if err != nil {
		return nil, err
	}
	return wpDistinctNames(rows, "Name"), nil
}

func wpDistinctNames(rows []*jnode, key string) []string {
	var names []string
	seen := map[string]bool{}
	for _, row := range rows {
		name := wpRowText(row, key)
		if strings.TrimSpace(name) == "" || seen[strings.ToLower(name)] {
			continue
		}
		seen[strings.ToLower(name)] = true
		names = append(names, name)
	}
	return names
}

func (r *wpRun) findPackagesShippingBinding(bindingName string) ([]string, error) {
	rows, err := r.selectRows("SysPackageSchemaData", []rightsColumn{{path: "SysPackage.Name", alias: "PackageName"}},
		[]rightsFilter{{column: "Name", value: bindingName, dataValueType: rightsTextType}})
	if err != nil {
		return nil, err
	}
	return wpDistinctNames(rows, "PackageName"), nil
}

func (r *wpRun) findBindingsShippingRow(schema, rowID string) ([]wpBindingRecord, error) {
	rows, err := r.selectRows("SysPackageSchemaData", []rightsColumn{{path: "SysPackage.Name", alias: "PackageName"}, wpText1("Name"),
		wpText1("Data")}, []rightsFilter{{column: "SysSchema.Name", value: schema, dataValueType: rightsTextType}})
	if err != nil {
		return nil, err
	}
	var records []wpBindingRecord
	seen := map[wpBindingRecord]bool{}
	for _, row := range rows {
		ships := wpBindingDataShipsRow(wpRowText(row, "Data"), rowID)
		if ships != nil && !*ships {
			continue
		}
		record := wpBindingRecord{packageName: wpRowText(row, "PackageName"), bindingName: wpRowText(row, "Name"), unverified: ships == nil}
		if strings.TrimSpace(record.packageName) == "" || strings.TrimSpace(record.bindingName) == "" || seen[record] {
			continue
		}
		seen[record] = true
		records = append(records, record)
	}
	return records, nil
}

// wpBindingDataShipsRow is BindingDataShipsRow: whether {"PackageData":[{"Row":[...]}]} has the row's Id cell;
// nil when the data is missing or cannot be parsed. Base64 text is decoded first.
func wpBindingDataShipsRow(data, rowID string) *bool {
	if strings.TrimSpace(data) == "" {
		return nil
	}
	trimmed := strings.TrimLeft(strings.TrimLeft(strings.TrimSpace(data), "\uFEFF"), " \t\r\n")
	if !strings.HasPrefix(trimmed, "{") {
		decoded, err := base64.StdEncoding.DecodeString(trimmed)
		if err != nil {
			return nil
		}
		trimmed = strings.TrimLeft(string(decoded), "\uFEFF")
	}
	var document map[string]json.RawMessage
	if json.Unmarshal([]byte(trimmed), &document) != nil {
		return nil
	}
	var items []json.RawMessage
	if json.Unmarshal(document["PackageData"], &items) != nil || document["PackageData"] == nil {
		return nil
	}
	found := false
	for _, raw := range items {
		var item map[string]json.RawMessage
		if json.Unmarshal(raw, &item) != nil {
			continue
		}
		var cells []any
		if json.Unmarshal(item["Row"], &cells) != nil {
			continue
		}
		for _, element := range cells {
			cell, ok := element.(map[string]any)
			if !ok {
				continue
			}
			column := fmt.Sprint(cell["SchemaColumnUId"])
			value := fmt.Sprint(cell["Value"])
			if id, ok := parseGUID(value); strings.EqualFold(column, wpIDColumnUID) && ok && id == strings.ToLower(rowID) {
				found = true
			}
		}
	}
	return &found
}
