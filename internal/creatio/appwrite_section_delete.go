package creatio

// delete-app-section: clio's ApplicationSectionDeleteService. The section's declared pages (by UId, never by
// name prefix) are deleted unless another section or a registered edit page uses them; the form page and the
// entity stay unless delete-entity-schema is set. Order: SysModuleInWorkplace (must succeed), SysModuleLcz
// (best-effort), workspace schemas, ApplicationSection (best-effort), SysModule (must), SysModuleEntity (must,
// when no other section shares it).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/redact"
)

// AppSectionDeleteResponse is clio's ApplicationSectionDeleteContextResponse.
type AppSectionDeleteResponse struct {
	Success            bool        `json:"success"`
	PackageUID         *string     `json:"package-u-id,omitempty"`
	PackageName        *string     `json:"package-name,omitempty"`
	ApplicationID      string      `json:"application-id,omitempty"`
	ApplicationName    string      `json:"application-name,omitempty"`
	ApplicationCode    string      `json:"application-code,omitempty"`
	ApplicationVersion *string     `json:"application-version,omitempty"`
	DeletedSection     *AppSection `json:"deleted-section,omitempty"`
	Error              string      `json:"error,omitempty"`
}

// AppSectionDeleteFailure is clio's CreateSectionDeleteContextErrorResponse.
func AppSectionDeleteFailure(message string) AppSectionDeleteResponse {
	return AppSectionDeleteResponse{Error: message}
}

// ValidateAppSectionDelete is ApplicationSectionDeleteTool.ValidateSectionDeleteArgs.
func ValidateAppSectionDelete(applicationCode, sectionCode string) error {
	if strings.TrimSpace(applicationCode) == "" {
		return errors.New("application-code is required.")
	}
	if strings.TrimSpace(sectionCode) == "" {
		return errors.New("section-code is required.")
	}
	return nil
}

const (
	appWriteEntitySchemaType = 3
	appWriteClientUnitType   = 4
	appWriteEmptyGUID        = "00000000-0000-0000-0000-000000000000"
)

// appWriteWorkspaceItem is the delete service's WorkspaceSchemaItemDto, sent back to WorkspaceExplorerService.
type appWriteWorkspaceItem struct {
	ID          string  `json:"id"`
	UID         string  `json:"uId"`
	Name        *string `json:"name,omitempty"`
	Title       *string `json:"title,omitempty"`
	PackageUID  string  `json:"packageUId"`
	PackageName *string `json:"packageName,omitempty"`
	Type        int     `json:"type"`
	ModifiedOn  *string `json:"modifiedOn,omitempty"`
	IsChanged   bool    `json:"isChanged"`
	IsLocked    bool    `json:"isLocked"`
	IsReadOnly  bool    `json:"isReadOnly"`
}

func (i appWriteWorkspaceItem) name() string {
	if i.Name == nil {
		return ""
	}
	return *i.Name
}

// appWriteGUID is .NET's Guid round trip: the canonical lower-case form, the empty GUID for anything else.
func appWriteGUID(value string) string {
	if parsed, ok := parseGUID(value); ok {
		return parsed
	}
	return appWriteEmptyGUID
}

// appWriteSectionReference is SectionReferenceDto.
type appWriteSectionReference struct {
	ID                 string  `json:"Id"`
	SectionSchemaUID   *string `json:"SectionSchemaUId"`
	CardSchemaUID      *string `json:"CardSchemaUId"`
	SysModuleEntityID  *string `json:"SysModuleEntityId"`
	EntitySchemaUID    *string `json:"EntitySchemaUId"`
	SearchRowSchemaUID *string `json:"SearchRowSchemaUId"`
}

func appWriteMatchesUID(value *string, uid string) bool {
	if value == nil {
		return false
	}
	parsed, ok := parseGUID(*value)
	return ok && parsed == uid
}

func appWriteText(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// DeleteAppSection is ApplicationSectionDeleteService.DeleteSection.
func (c *Client) DeleteAppSection(ctx context.Context, applicationCode, sectionCode string, deleteEntitySchema bool) AppSectionDeleteResponse {
	if err := ValidateAppSectionDelete(applicationCode, sectionCode); err != nil {
		return AppSectionDeleteFailure(err.Error())
	}
	app, err := c.appWriteFindApp(ctx, applicationCode)
	if err != nil {
		return AppSectionDeleteFailure(redact.Text(err.Error()))
	}
	record, err := c.appWriteSectionByCode(ctx, app.ID, sectionCode, "CardSchemaUId", "SysModuleEntityId")
	if err != nil {
		return AppSectionDeleteFailure(redact.Text(err.Error()))
	}
	if err := c.appWriteDeleteSection(ctx, record, deleteEntitySchema); err != nil {
		return AppSectionDeleteFailure(redact.Text(err.Error()))
	}
	section := record.section()
	var version *string
	if strings.TrimSpace(app.Version) != "" {
		version = &app.Version
	}
	return AppSectionDeleteResponse{Success: true, ApplicationID: app.ID, ApplicationName: app.Name, ApplicationCode: app.Code,
		ApplicationVersion: version, DeletedSection: &section}
}

// appWriteFindApp is ApplicationInfoService.FindApplicationId: the SysInstalledApp row with the code.
func (c *Client) appWriteFindApp(ctx context.Context, code string) (App, error) {
	rows, err := c.selectRows(ctx, buildSelectQuery("SysInstalledApp", map[string]string{"Id": "Id", "Code": "Code", "Name": "Name", "Version": "Version"},
		map[string]any{"filter0": comparisonFilter("Code", strings.TrimSpace(code), appWriteTextDataValueType, 3)}, 10000))
	if err != nil {
		return App{}, err
	}
	if len(rows) == 0 {
		return App{}, fmt.Errorf("Application '%s' not found.", strings.TrimSpace(code))
	}
	row := rows[0]
	return App{ID: rowString(row, "Id"), Code: rowString(row, "Code"), Name: rowString(row, "Name"), Version: rowString(row, "Version")}, nil
}

func (c *Client) appWriteDeleteSection(ctx context.Context, section appWriteSectionRecord, deleteEntity bool) error {
	references, err := c.appWriteReferences(ctx, "SysModule", map[string]string{"Id": "Id", "SectionSchemaUId": "SectionSchemaUId",
		"CardSchemaUId": "CardSchemaUId", "SysModuleEntityId": "SysModuleEntity.Id", "EntitySchemaUId": "SysModuleEntity.SysEntitySchemaUId"},
		"Cannot check shared section artifacts.")
	if err != nil {
		return err
	}
	var target *appWriteSectionReference
	var others []appWriteSectionReference
	for i := range references {
		if strings.EqualFold(references[i].ID, section.ID) {
			target = &references[i]
		} else {
			others = append(others, references[i])
		}
	}
	if target == nil {
		return errors.New("Cannot resolve the persisted section. No artifacts were deleted.")
	}
	section.SectionSchemaUID, section.CardSchemaUID, section.SysModuleEntityID = target.SectionSchemaUID, target.CardSchemaUID, target.SysModuleEntityID
	schemas, err := c.appWriteSectionSchemas(ctx, section, deleteEntity)
	if err != nil {
		return err
	}
	editPages, err := c.appWriteReferences(ctx, "SysModuleEdit", map[string]string{"CardSchemaUId": "CardSchemaUId",
		"SectionSchemaUId": "MiniPageSchemaUId", "SearchRowSchemaUId": "SearchRowSchemaUId"}, "Cannot check registered edit pages.")
	if err != nil {
		return err
	}
	registered := map[string]bool{}
	for _, row := range editPages {
		for _, value := range []*string{row.CardSchemaUID, row.SectionSchemaUID, row.SearchRowSchemaUID} {
			if value != nil {
				if parsed, ok := parseGUID(*value); ok && parsed != appWriteEmptyGUID {
					registered[parsed] = true
				}
			}
		}
	}
	moduleEntity := strings.TrimSpace(appWriteText(section.SysModuleEntityID))
	shared := false
	if moduleEntity != "" {
		for _, other := range others {
			if strings.EqualFold(appWriteText(other.SysModuleEntityID), appWriteText(section.SysModuleEntityID)) {
				shared = true
			}
		}
	}
	if deleteEntity {
		entityShared := shared
		for _, schema := range schemas {
			for _, other := range others {
				if schema.Type == appWriteEntitySchemaType && appWriteMatchesUID(other.EntitySchemaUID, schema.UID) {
					entityShared = true
				}
			}
		}
		if entityShared {
			return errors.New("The entity is used by another section. No artifacts were deleted.")
		}
	}
	kept := schemas[:0]
	for _, schema := range schemas {
		drop := false
		if schema.Type == appWriteClientUnitType {
			drop = !deleteEntity && appWriteMatchesUID(section.CardSchemaUID, schema.UID) || registered[schema.UID]
			for _, other := range others {
				if appWriteMatchesUID(other.SectionSchemaUID, schema.UID) || appWriteMatchesUID(other.CardSchemaUID, schema.UID) {
					drop = true
				}
			}
		}
		if !drop {
			kept = append(kept, schema)
		}
	}
	if err := c.appWriteDeleteRows(ctx, "SysModuleInWorkplace", "SysModule", section.ID); err != nil {
		return err
	}
	_ = c.appWriteDeleteRows(ctx, "SysModuleLcz", "RecordId", section.ID)
	for _, schema := range kept {
		if err := c.appWriteDeleteSchema(ctx, schema); err != nil {
			return err
		}
	}
	_ = c.appWriteDeleteRows(ctx, appWriteSectionSchema, "Id", section.ID)
	if err := c.appWriteDeleteRows(ctx, "SysModule", "Id", section.ID); err != nil {
		return err
	}
	if !shared && moduleEntity != "" {
		return c.appWriteDeleteRows(ctx, "SysModuleEntity", "Id", appWriteText(section.SysModuleEntityID))
	}
	return nil
}

// appWriteReferences is LoadSectionReferences / LoadRegisteredEditPages: every row, refused when the answer
// fails or reaches the 10000-row bound.
func (c *Client) appWriteReferences(ctx context.Context, root string, columns map[string]string, failure string) ([]appWriteSectionReference, error) {
	payload, err := c.appWritePost(ctx, "DataService/json/SyncReply/SelectQuery", buildSelectQuery(root, columns, nil, 10000), 0)
	if err != nil {
		return nil, err
	}
	var answer *struct {
		Success bool                        `json:"success"`
		Rows    *[]appWriteSectionReference `json:"rows"`
	}
	if err := json.Unmarshal(payload, &answer); err != nil {
		return nil, err
	}
	if answer == nil {
		return nil, errors.New(failure)
	}
	if !answer.Success || answer.Rows == nil || len(*answer.Rows) >= 10000 {
		return nil, errors.New(failure + " No artifacts were deleted.")
	}
	return *answer.Rows, nil
}

// appWriteSectionSchemas is LoadSectionSchemas: the declared list and form pages found by UId, and with
// delete-entity-schema the one entity schema named like the section's entity.
func (c *Client) appWriteSectionSchemas(ctx context.Context, section appWriteSectionRecord, deleteEntity bool) ([]appWriteWorkspaceItem, error) {
	payload, err := c.callService(ctx, serviceCall{Route: "ServiceModel/WorkspaceExplorerService.svc/GetWorkspaceItems", Body: []byte{}})
	if err != nil {
		return nil, err
	}
	var collection *struct {
		Success *bool                    `json:"success"`
		Items   *[]appWriteWorkspaceItem `json:"items"`
	}
	if err := json.Unmarshal(payload, &collection); err != nil {
		return nil, err
	}
	if collection == nil {
		return nil, errors.New("GetWorkspaceItems returned an empty response.")
	}
	if collection.Items == nil || collection.Success != nil && !*collection.Success {
		return nil, errors.New("Cannot determine section schemas: GetWorkspaceItems failed or omitted its items.")
	}
	items := *collection.Items
	for i := range items {
		items[i].ID, items[i].UID, items[i].PackageUID = appWriteGUID(items[i].ID), appWriteGUID(items[i].UID), appWriteGUID(items[i].PackageUID)
	}
	var selected []appWriteWorkspaceItem
	for _, declared := range []*string{section.SectionSchemaUID, section.CardSchemaUID} {
		if declared == nil || strings.TrimSpace(*declared) == "" {
			continue
		}
		uid, ok := parseGUID(*declared)
		if !ok {
			return nil, fmt.Errorf("Invalid declared page identity '%s'. No artifacts were deleted.", *declared)
		}
		if uid == appWriteEmptyGUID {
			continue
		}
		var matches []appWriteWorkspaceItem
		nonPage := false
		for _, item := range items {
			if item.UID == uid {
				matches = append(matches, item)
				nonPage = nonPage || item.Type != appWriteClientUnitType
			}
		}
		if len(matches) > 1 || nonPage {
			return nil, fmt.Errorf("Ambiguous or non-page schema '%s'. No artifacts were deleted.", uid)
		}
		duplicate := false
		for _, item := range selected {
			duplicate = duplicate || item.UID == uid
		}
		if len(matches) == 1 && !duplicate {
			selected = append(selected, matches[0])
		}
	}
	if deleteEntity {
		var entities []appWriteWorkspaceItem
		name := appWriteText(section.EntitySchemaName)
		for _, item := range items {
			if item.Type == appWriteEntitySchemaType && strings.TrimSpace(name) != "" && strings.EqualFold(item.name(), name) {
				entities = append(entities, item)
			}
		}
		if len(entities) != 1 || entities[0].UID == appWriteEmptyGUID {
			return nil, fmt.Errorf("Cannot uniquely resolve entity '%s'. No artifacts were deleted.", name)
		}
		selected = append(selected, entities[0])
	}
	return selected, nil
}

// appWriteDeleteSchema is DeleteWorkspaceSchema.
func (c *Client) appWriteDeleteSchema(ctx context.Context, schema appWriteWorkspaceItem) error {
	payload, err := c.appWritePost(ctx, "ServiceModel/WorkspaceExplorerService.svc/Delete", []appWriteWorkspaceItem{schema}, 0)
	if err != nil {
		return err
	}
	var answer *appWriteDataServiceAnswer
	if err := json.Unmarshal(payload, &answer); err != nil {
		return err
	}
	if answer == nil {
		return errors.New("Delete returned an empty response.")
	}
	if !answer.Success {
		return fmt.Errorf("Failed to delete schema '%s': %s. Deletion may be partial; inspect the environment before retrying.", schema.name(), answer.message("Unknown error"))
	}
	return nil
}

// appWriteDeleteRows is ExecuteDeleteQuery for clio's literal DeleteQuery bodies: rows of root whose column
// equals the value (sent with dataValueType 0).
func (c *Client) appWriteDeleteRows(ctx context.Context, root, column, value string) error {
	body := map[string]any{
		"__type": "Terrasoft.Nui.ServiceModel.DataContract.DeleteQuery", "rootSchemaName": root,
		"filters": map[string]any{"isEnabled": true, "filterType": 6, "logicalOperation": 0, "trimDateTimeParameterToDate": false,
			"items": map[string]any{"primaryFilter": map[string]any{"filterType": 1, "comparisonType": 3, "isEnabled": true,
				"trimDateTimeParameterToDate": false, "leftExpression": map[string]any{"expressionType": 0, "columnPath": column},
				"rightExpression": appWriteParam(appWriteGUIDDataValueType, value)}}},
	}
	payload, err := c.appWritePost(ctx, "DataService/json/SyncReply/DeleteQuery", body, 0)
	if err != nil {
		return err
	}
	var answer *appWriteDataServiceAnswer
	if err := json.Unmarshal(payload, &answer); err != nil {
		return err
	}
	if answer == nil {
		return errors.New("DeleteQuery returned an empty response.")
	}
	if !answer.Success {
		return errors.New(answer.message("DeleteQuery failed."))
	}
	return nil
}
