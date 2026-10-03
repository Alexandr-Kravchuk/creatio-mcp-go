package creatio

// update-entity-schema, modify-entity-schema-column and set-entity-schema-properties: clio's
// UpdateEntitySchemaTool / ModifyEntitySchemaColumnTool / SetEntitySchemaPropertiesTool, the commands they
// run, and RemoteEntitySchemaColumnManager (load the package's design item, mutate it, save, publish,
// reload and verify) with the dependency diagnosis it adds to a failed load (EntitySchemaDependencyResolver).

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// UpdateEntitySchemaMissingOperations is UpdateEntitySchemaTool.MissingOperationsError.
const UpdateEntitySchemaMissingOperations = "update-entity-schema requires a non-empty 'operations' array - it applies COLUMN operations " +
	"(add/modify/remove) only. Pass at least one operation, for example " +
	"[{\"action\":\"add\",\"column-name\":\"UsrCode\",\"data-value-type\":\"Text\"," +
	"\"title-localizations\":{\"en-US\":\"Code\"}}]. " +
	"To change SCHEMA-level properties such as the schema caption or the primary display column, " +
	"use set-entity-schema-properties (title-localizations / primary-display-column) instead - " +
	"'title-localizations' on this tool is a per-COLUMN property and is ignored at schema level."

// schemaWriteEntNoPropertyToSet is SetEntitySchemaPropertiesOptions.NoPropertyToSetError.
const schemaWriteEntNoPropertyToSet = "At least one schema property to set is required " +
	"(for example --primary-display-column, --is-db-view, --title or --title-localizations)."

// schemaWriteEntColumnOptions is clio's ModifyEntitySchemaColumnOptions.
type schemaWriteEntColumnOptions struct {
	pkg, schemaName, action, columnName, newName, typeName, title, description, captionCulture string
	referenceSchema, defaultValueSource, usageType                                             string
	titleLocalizations, descriptionLocalizations                                               schemaWriteEntLocMap
	defaultValue                                                                               *string
	defaultConfig                                                                              *schemaWriteEntDefaultConfig
	required, indexed, cloneable, trackChanges, multilineText, localizableText                 *bool
	accentInsensitive, masked, formatValidated, useSeconds, simpleLookup, cascade, noIntegrity *bool
}

func (o schemaWriteEntColumnOptions) hasMutableOptions() bool {
	return strings.TrimSpace(o.newName) != "" || strings.TrimSpace(o.typeName) != "" || strings.TrimSpace(o.title) != "" ||
		len(o.titleLocalizations) > 0 || strings.TrimSpace(o.description) != "" || len(o.descriptionLocalizations) > 0 ||
		strings.TrimSpace(o.referenceSchema) != "" || o.required != nil || o.indexed != nil || o.cloneable != nil ||
		o.trackChanges != nil || strings.TrimSpace(o.defaultValueSource) != "" || o.defaultValue != nil || o.defaultConfig != nil ||
		o.multilineText != nil || o.localizableText != nil || o.accentInsensitive != nil || o.masked != nil ||
		o.formatValidated != nil || o.useSeconds != nil || o.simpleLookup != nil || o.cascade != nil || o.noIntegrity != nil ||
		strings.TrimSpace(o.usageType) != ""
}

// hasNonCaptionMutation is HasNonCaptionInheritedMutation.
func (o schemaWriteEntColumnOptions) hasNonCaptionMutation() bool {
	return strings.TrimSpace(o.newName) != "" || strings.TrimSpace(o.typeName) != "" || strings.TrimSpace(o.referenceSchema) != "" ||
		o.required != nil || o.indexed != nil || o.cloneable != nil || o.trackChanges != nil ||
		strings.TrimSpace(o.defaultValueSource) != "" || o.defaultValue != nil || o.defaultConfig != nil ||
		o.multilineText != nil || o.localizableText != nil || o.accentInsensitive != nil || o.masked != nil ||
		o.formatValidated != nil || o.useSeconds != nil || o.simpleLookup != nil || o.cascade != nil || o.noIntegrity != nil ||
		strings.TrimSpace(o.usageType) != ""
}

func (o schemaWriteEntColumnOptions) hasLookupOptions() bool {
	return strings.TrimSpace(o.referenceSchema) != "" || o.simpleLookup != nil || o.cascade != nil || o.noIntegrity != nil
}

func (o schemaWriteEntColumnOptions) hasTextOptions() bool {
	return o.multilineText != nil || o.localizableText != nil || o.accentInsensitive != nil || o.formatValidated != nil
}

// schemaWriteEntActionOf is Enum.TryParse(action, ignoreCase) over add/modify/remove.
func schemaWriteEntActionOf(action string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "add", "modify", "remove":
		return strings.ToLower(strings.TrimSpace(action)), true
	}
	return "", false
}

// schemaWriteEntValidateColumnOptions is ModifyEntitySchemaColumnCommand.ValidateOptions.
func schemaWriteEntValidateColumnOptions(options schemaWriteEntColumnOptions) error {
	if strings.TrimSpace(options.pkg) == "" {
		return schemaWriteEntError("package-name is required.")
	}
	if strings.TrimSpace(options.schemaName) == "" {
		return schemaWriteEntError("schema-name is required.")
	}
	if strings.TrimSpace(options.action) == "" {
		return schemaWriteEntError("action is required.")
	}
	action, ok := schemaWriteEntActionOf(options.action)
	if !ok {
		return schemaWriteEntError("action must be one of: add, modify, remove.")
	}
	if strings.TrimSpace(options.columnName) == "" {
		return schemaWriteEntError("column-name is required.")
	}
	if action == "add" && strings.TrimSpace(options.typeName) == "" {
		return schemaWriteEntError("type is required for the add action.")
	}
	if action == "remove" && options.hasMutableOptions() {
		return schemaWriteEntError("Remove action does not accept column property options.")
	}
	if action == "modify" && !options.hasMutableOptions() {
		return schemaWriteEntError("Modify action requires at least one property option to change.")
	}
	return nil
}

func schemaWriteEntDerefBool(value *bool) bool { return value != nil && *value }

// ---------------------------------------------------------------------------------------------- tools

// UpdateEntitySchemaArgs is clio's UpdateEntitySchemaArgs.
type UpdateEntitySchemaArgs struct {
	PackageName, SchemaName string
	Operations              []SchemaWriteEntOperationArgs
}

// ParseUpdateEntitySchemaArgs binds update-entity-schema's arguments.
func ParseUpdateEntitySchemaArgs(args map[string]any) (UpdateEntitySchemaArgs, error) {
	b := schemaWriteEntBinder{tool: "update-entity-schema"}
	var parsed UpdateEntitySchemaArgs
	var err error
	if parsed.PackageName, err = b.text(args, "package-name"); err != nil {
		return parsed, err
	}
	if parsed.SchemaName, err = b.text(args, "schema-name"); err != nil {
		return parsed, err
	}
	items, _, err := b.list(args, "operations")
	if err != nil {
		return parsed, err
	}
	for _, item := range items {
		operation, err := b.operation(item)
		if err != nil {
			return parsed, err
		}
		parsed.Operations = append(parsed.Operations, operation)
	}
	return parsed, nil
}

// UpdateEntitySchemaTerms returns the candidate terms and lookup hints update-entity-schema enriches with.
func UpdateEntitySchemaTerms(args UpdateEntitySchemaArgs) ([]string, []string) {
	terms := []string{args.SchemaName}
	var hints []string
	for _, operation := range args.Operations {
		if !strings.EqualFold(operation.action(), "add") {
			continue
		}
		if name := strings.TrimSpace(operation.resolveColumnName()); name != "" {
			terms = append(terms, name)
		}
		if reference := strings.TrimSpace(operation.resolveReference()); reference != "" {
			hints = append(hints, reference)
		}
	}
	return schemaWriteEntDistinctTerms(terms...), schemaWriteEntDistinctTerms(hints...)
}

// schemaWriteEntOperationPayload is UpdateEntitySchemaTool.BuildOperationPayload followed by
// UpdateEntitySchemaCommand's reading of it: the column options one operation turns into.
func schemaWriteEntOperationPayload(operation SchemaWriteEntOperationArgs, context string) (schemaWriteEntColumnOptions, error) {
	if _, err := schemaWriteEntColumnIdentity(operation.resolveColumnName(), context, "operation"); err != nil {
		return schemaWriteEntColumnOptions{}, err
	}
	titles, err := schemaWriteEntMutationTitles(operation.action(), operation.TitleLocalizations, schemaWriteEntDeref(operation.Title),
		schemaWriteEntDeref(operation.Caption), operation.resolveColumnName(), context)
	if err != nil {
		return schemaWriteEntColumnOptions{}, err
	}
	descriptions, err := schemaWriteEntMutationDescriptions(operation.action(), operation.DescriptionLocalizations, schemaWriteEntDeref(operation.Description), context)
	if err != nil {
		return schemaWriteEntColumnOptions{}, err
	}
	options := schemaWriteEntColumnOptionsFrom(operation)
	options.titleLocalizations, options.descriptionLocalizations = titles, descriptions
	options.title, options.description = "", ""
	return options, nil
}

func schemaWriteEntColumnOptionsFrom(operation SchemaWriteEntOperationArgs) schemaWriteEntColumnOptions {
	return schemaWriteEntColumnOptions{action: operation.action(), columnName: operation.resolveColumnName(),
		newName: schemaWriteEntDeref(operation.NewName), typeName: operation.resolveType(), referenceSchema: operation.resolveReference(),
		defaultValueSource: schemaWriteEntDeref(operation.DefaultValueSource), defaultValue: operation.DefaultValue,
		defaultConfig: operation.DefaultValueConfig, usageType: schemaWriteEntDeref(operation.UsageType),
		required: operation.resolveRequired(), indexed: operation.Indexed, cloneable: operation.Cloneable,
		trackChanges: operation.TrackChanges, multilineText: operation.MultilineText, localizableText: operation.LocalizableText,
		accentInsensitive: operation.AccentInsensitive, masked: operation.Masked, formatValidated: operation.FormatValidated,
		useSeconds: operation.UseSeconds, simpleLookup: operation.SimpleLookup, cascade: operation.Cascade,
		noIntegrity: operation.DoNotControlInteg}
}

// schemaWriteEntBuildMutations is SerializeOperations + UpdateEntitySchemaCommand.BuildColumnMutations: each
// operation's payload, its title normalized (the effective title becomes the scalar title).
func schemaWriteEntBuildMutations(pkg, schemaName string, operations []SchemaWriteEntOperationArgs) ([]schemaWriteEntColumnOptions, error) {
	var payloads []schemaWriteEntColumnOptions
	for index, operation := range operations {
		options, err := schemaWriteEntOperationPayload(operation, fmt.Sprintf("Schema '%s' operation #%d", schemaName, index+1))
		if err != nil {
			return nil, err
		}
		payloads = append(payloads, options)
	}
	return schemaWriteEntCommandMutations(pkg, schemaName, payloads)
}

// schemaWriteEntCommandMutations is UpdateEntitySchemaCommand.Execute up to the manager call: its own
// validation, then the per-operation title normalization and ValidateOptions.
func schemaWriteEntCommandMutations(pkg, schemaName string, payloads []schemaWriteEntColumnOptions) ([]schemaWriteEntColumnOptions, error) {
	if strings.TrimSpace(pkg) == "" {
		return nil, schemaWriteEntError("Package is required.")
	}
	if strings.TrimSpace(schemaName) == "" {
		return nil, schemaWriteEntError("Schema name is required.")
	}
	if len(payloads) == 0 {
		return nil, schemaWriteEntError("At least one operation is required (use --operation or --operations).")
	}
	mutations := make([]schemaWriteEntColumnOptions, 0, len(payloads))
	for _, options := range payloads {
		normalization, err := schemaWriteEntNormalizeTitles(options.titleLocalizations, options.title, schemaWriteEntTitleField, "")
		if err != nil {
			return nil, err
		}
		options.pkg, options.schemaName = pkg, schemaName
		options.title, options.titleLocalizations = normalization.effectiveTitle, normalization.localizations
		mutations = append(mutations, options)
	}
	for _, options := range mutations {
		if err := schemaWriteEntValidateColumnOptions(options); err != nil {
			return nil, err
		}
	}
	return mutations, nil
}

// UpdateEntitySchema runs update-entity-schema after enrichment.
func (c *Client) UpdateEntitySchema(ctx context.Context, args UpdateEntitySchemaArgs, dataForge *SchemaWriteEntDataForge) SchemaWriteEntResult {
	var payloads []schemaWriteEntColumnOptions
	for index, operation := range args.Operations {
		options, err := schemaWriteEntOperationPayload(operation, fmt.Sprintf("Schema '%s' operation #%d", args.SchemaName, index+1))
		if err != nil {
			return SchemaWriteEntFailure(schemaWriteEntRedact(err.Error()), dataForge)
		}
		payloads = append(payloads, options)
	}
	log := &schemaWriteEntLog{}
	run := c.schemaWriteEntNewRun(ctx, log)
	exitCode := run.executeUpdate(args.PackageName, args.SchemaName, payloads)
	result := SchemaWriteEntResult{ExitCode: exitCode, Messages: log.snapshot(), DataForge: dataForge}
	if exitCode == 0 {
		result.Note = schemaWriteEntCompileNote
	}
	return result
}

// executeUpdate is UpdateEntitySchemaCommand.Execute.
func (r *schemaWriteEntRun) executeUpdate(pkg, schemaName string, payloads []schemaWriteEntColumnOptions) int {
	mutations, err := schemaWriteEntCommandMutations(pkg, schemaName, payloads)
	if err == nil {
		err = r.modifyColumns(mutations)
	}
	if err != nil {
		r.log.err(err.Error())
		return 1
	}
	r.log.info("Done")
	return 0
}

// ModifyEntitySchemaColumnArgs is clio's ModifyEntitySchemaColumnArgs.
type ModifyEntitySchemaColumnArgs struct {
	PackageName, SchemaName string
	Operation               SchemaWriteEntOperationArgs
}

// ParseModifyEntitySchemaColumnArgs binds modify-entity-schema-column's arguments.
func ParseModifyEntitySchemaColumnArgs(args map[string]any) (ModifyEntitySchemaColumnArgs, error) {
	b := schemaWriteEntBinder{tool: "modify-entity-schema-column"}
	var parsed ModifyEntitySchemaColumnArgs
	var err error
	if parsed.PackageName, err = b.text(args, "package-name"); err != nil {
		return parsed, err
	}
	if parsed.SchemaName, err = b.text(args, "schema-name"); err != nil {
		return parsed, err
	}
	parsed.Operation, err = b.operationFields(args)
	return parsed, err
}

// ModifyEntitySchemaColumn is ModifyEntitySchemaColumnTool.ModifyEntitySchemaColumn.
func (c *Client) ModifyEntitySchemaColumn(ctx context.Context, args ModifyEntitySchemaColumnArgs) SchemaWriteEntResult {
	operation := args.Operation
	columnName, err := schemaWriteEntColumnIdentity(operation.resolveColumnName(), "modify-entity-schema-column", "args")
	if err != nil {
		return SchemaWriteEntFailure(schemaWriteEntRedact(err.Error()), nil)
	}
	context := fmt.Sprintf("Column '%s' action '%s'", columnName, operation.action())
	titles, err := schemaWriteEntMutationTitles(operation.action(), operation.TitleLocalizations, schemaWriteEntDeref(operation.Title),
		schemaWriteEntDeref(operation.Caption), columnName, context)
	if err == nil {
		var normalization schemaWriteEntTitleNormalization
		normalization, err = schemaWriteEntNormalizeTitles(titles, "", schemaWriteEntTitleField, "")
		if err == nil {
			options := schemaWriteEntColumnOptionsFrom(operation)
			options.pkg, options.schemaName, options.columnName = args.PackageName, args.SchemaName, columnName
			options.title, options.titleLocalizations = normalization.effectiveTitle, normalization.localizations
			options.captionCulture = schemaWriteEntDeref(operation.CaptionCulture)
			if options.descriptionLocalizations, err = schemaWriteEntMutationDescriptions(operation.action(), operation.DescriptionLocalizations,
				schemaWriteEntDeref(operation.Description), context); err == nil {
				log := &schemaWriteEntLog{}
				run := c.schemaWriteEntNewRun(ctx, log)
				exitCode := 0
				if err := schemaWriteEntValidateColumnOptions(options); err != nil {
					log.err(err.Error())
					exitCode = 1
				} else if err := run.modifyColumns([]schemaWriteEntColumnOptions{options}); err != nil {
					log.err(err.Error())
					exitCode = 1
				} else {
					log.info("Done")
				}
				return SchemaWriteEntResult{ExitCode: exitCode, Messages: log.snapshot()}
			}
		}
	}
	return SchemaWriteEntFailure(schemaWriteEntRedact(err.Error()), nil)
}

// SetEntitySchemaPropertiesArgs is clio's SetEntitySchemaPropertiesArgs.
type SetEntitySchemaPropertiesArgs struct {
	PackageName, SchemaName, PrimaryDisplayColumn string
	TitleLocalizations                            schemaWriteEntLocMap
	IsDBView                                      *bool
}

// ParseSetEntitySchemaPropertiesArgs binds set-entity-schema-properties' arguments.
func ParseSetEntitySchemaPropertiesArgs(args map[string]any) (SetEntitySchemaPropertiesArgs, error) {
	b := schemaWriteEntBinder{tool: "set-entity-schema-properties"}
	var parsed SetEntitySchemaPropertiesArgs
	var err error
	for key, target := range map[string]*string{"package-name": &parsed.PackageName, "schema-name": &parsed.SchemaName,
		"primary-display-column": &parsed.PrimaryDisplayColumn} {
		if *target, err = b.text(args, key); err != nil {
			return parsed, err
		}
	}
	if parsed.TitleLocalizations, err = b.locMap(args, "title-localizations"); err != nil {
		return parsed, err
	}
	parsed.IsDBView, err = b.boolean(args, "is-db-view")
	return parsed, err
}

// SetEntitySchemaProperties is SetEntitySchemaPropertiesTool + SetEntitySchemaPropertiesCommand.
func (c *Client) SetEntitySchemaProperties(ctx context.Context, args SetEntitySchemaPropertiesArgs) SchemaWriteEntResult {
	log := &schemaWriteEntLog{}
	run := c.schemaWriteEntNewRun(ctx, log)
	if err := run.setSchemaProperties(args); err != nil {
		log.err(err.Error())
		return SchemaWriteEntResult{ExitCode: 1, Messages: log.snapshot()}
	}
	log.info("Done")
	return SchemaWriteEntResult{ExitCode: 0, Messages: log.snapshot()}
}

func (r *schemaWriteEntRun) setSchemaProperties(args SetEntitySchemaPropertiesArgs) error {
	if strings.TrimSpace(args.PackageName) == "" {
		return schemaWriteEntError("package-name is required.")
	}
	if strings.TrimSpace(args.SchemaName) == "" {
		return schemaWriteEntError("schema-name is required.")
	}
	var titles schemaWriteEntLocMap
	if len(args.TitleLocalizations) > 0 {
		var err error
		if titles, err = schemaWriteEntNormalizeSchemaCaptions(args.TitleLocalizations, schemaWriteEntTitleField); err != nil {
			return err
		}
	}
	if args.IsDBView == nil && strings.TrimSpace(args.PrimaryDisplayColumn) == "" && len(titles) == 0 {
		return schemaWriteEntError(schemaWriteEntNoPropertyToSet + " (Parameter 'options')")
	}
	pkg, err := r.resolvePackage(args.PackageName)
	if err != nil {
		return err
	}
	schema, err := r.loadSchema(args.SchemaName, pkg, true)
	if err != nil {
		return err
	}
	if args.IsDBView != nil {
		schema.IsDBView = *args.IsDBView
	}
	requested := strings.TrimSpace(args.PrimaryDisplayColumn)
	if requested != "" {
		column, _, found := schemaWriteEntFindColumn(schema, requested)
		if !found {
			return fmt.Errorf("Column '%s' was not found in schema '%s'.", requested, schema.name())
		}
		schema.PrimaryDisplayColumn = column
	}
	for _, pair := range titles {
		schema.Caption = schemaWriteEntSetLocalizable(schema.Caption, pair.Value, pair.Key)
	}
	if err := r.ensureCulturesAvailable(titles.keys()); err != nil {
		return err
	}
	reloaded, err := r.saveAndReload(schema, pkg, "schema properties were saved", false)
	if err != nil {
		return err
	}
	if requested != "" && (reloaded.PrimaryDisplayColumn == nil || !strings.EqualFold(reloaded.PrimaryDisplayColumn.name(), requested)) {
		return fmt.Errorf("Primary-display column '%s' was not persisted for schema '%s'. The target environment may not support setting the primary-display column through this API.", requested, schema.name())
	}
	for _, pair := range titles {
		var persisted *string
		for _, value := range reloaded.Caption {
			if strings.EqualFold(value.culture(), pair.Key) {
				persisted = value.Value
				break
			}
		}
		if persisted == nil || *persisted != pair.Value {
			shown := "<none>"
			if persisted != nil {
				shown = *persisted
			}
			return fmt.Errorf("Schema caption '%s' (%s) was not persisted for schema '%s'. The server returned '%s'.", pair.Value, pair.Key, args.SchemaName, shown)
		}
	}
	if args.IsDBView != nil {
		if reloaded.IsDBView != *args.IsDBView {
			return fmt.Errorf("Database-view flag was not persisted for schema '%s'.", schema.name())
		}
		r.log.info(fmt.Sprintf("Database-view flag set to '%s' for schema '%s'.", schemaWriteEntDotnetBool(*args.IsDBView), schema.name()))
	}
	if requested != "" {
		r.log.info(fmt.Sprintf("Primary-display column set to '%s' for schema '%s'.", requested, args.SchemaName))
	}
	for _, pair := range titles {
		r.log.info(fmt.Sprintf("Schema caption set to '%s' (%s) for schema '%s'.", pair.Value, pair.Key, args.SchemaName))
	}
	return nil
}

func schemaWriteEntDotnetBool(value bool) string {
	if value {
		return "True"
	}
	return "False"
}

// ---------------------------------------------------------------------------------------------- manager

// schemaWriteEntFindColumn is FindColumnForRead / FindColumnForMutation: own columns first, then inherited.
func schemaWriteEntFindColumn(schema *schemaWriteEntSchema, name string) (*schemaWriteEntColumn, bool, bool) {
	for _, column := range schema.Columns {
		if strings.EqualFold(column.name(), name) {
			return column, false, true
		}
	}
	for _, column := range schema.InheritedColumns {
		if strings.EqualFold(column.name(), name) {
			return column, true, true
		}
	}
	return nil, false, false
}

// modifyColumns is RemoteEntitySchemaColumnManager.ModifyColumns.
func (r *schemaWriteEntRun) modifyColumns(operations []schemaWriteEntColumnOptions) error {
	if len(operations) == 0 {
		return schemaWriteEntError("At least one column mutation is required.")
	}
	root := operations[0]
	pkg, err := r.resolvePackage(root.pkg)
	if err != nil {
		return err
	}
	schema, err := r.loadSchema(root.schemaName, pkg, true)
	if err != nil {
		return err
	}
	for _, operation := range operations {
		if !strings.EqualFold(operation.pkg, root.pkg) || !strings.EqualFold(operation.schemaName, root.schemaName) {
			return schemaWriteEntError("All batch column mutations must target the same package, schema, and environment.")
		}
	}
	culture, err := r.captionCulture(root.captionCulture)
	if err != nil {
		return err
	}
	cross := &schemaWriteEntCrossNames{pristine: map[string]bool{}}
	for _, column := range schema.allColumns() {
		if strings.TrimSpace(column.name()) != "" {
			cross.pristine[strings.ToLower(column.name())] = true
		}
	}
	for _, operation := range operations {
		if err := r.applyMutation(schema, pkg, operation, culture, cross); err != nil {
			return err
		}
	}
	if err := r.ensureCulturesAvailable(schemaWriteEntWrittenCultures(operations, culture)); err != nil {
		return err
	}
	reloaded, err := r.saveAndReload(schema, pkg, "columns were saved", schemaWriteEntContractChanged(operations))
	if err != nil {
		return err
	}
	if err := schemaWriteEntVerifyMutations(reloaded, operations, culture); err != nil {
		return err
	}
	for _, operation := range operations {
		r.log.info(fmt.Sprintf("Column '%s' action '%s' completed for schema '%s'.", operation.columnName, operation.action, operation.schemaName))
	}
	return nil
}

func schemaWriteEntContractChanged(operations []schemaWriteEntColumnOptions) bool {
	for _, operation := range operations {
		action, _ := schemaWriteEntActionOf(operation.action)
		if action == "add" || action == "remove" || strings.TrimSpace(operation.newName) != "" ||
			strings.TrimSpace(operation.typeName) != "" || strings.TrimSpace(operation.referenceSchema) != "" {
			return true
		}
	}
	return false
}

// schemaWriteEntWrittenCultures is GetWrittenCaptionCultures.
func schemaWriteEntWrittenCultures(operations []schemaWriteEntColumnOptions, culture string) []string {
	var cultures []string
	for _, operation := range operations {
		action, _ := schemaWriteEntActionOf(operation.action)
		if action == "remove" {
			continue
		}
		cultures = append(cultures, operation.titleLocalizations.keys()...)
		cultures = append(cultures, operation.descriptionLocalizations.keys()...)
		scalarTitle := operation.titleLocalizations == nil && (action == "add" || strings.TrimSpace(operation.title) != "")
		scalarDescription := operation.descriptionLocalizations == nil && strings.TrimSpace(operation.description) != ""
		if scalarTitle || scalarDescription {
			cultures = append(cultures, culture)
		}
	}
	return cultures
}

// saveAndReload is SaveAndReloadSchema.
func (r *schemaWriteEntRun) saveAndReload(schema *schemaWriteEntSchema, pkg pkgWriteInstalledPackage, reason string, contractChanged bool) (*schemaWriteEntSchema, error) {
	saved, err := r.saveSchema(schema)
	if err != nil {
		return nil, err
	}
	schemaUID := saved.SchemaUID
	if schemaUID.isEmpty() {
		schemaUID = schema.UID
	}
	if schemaUID.isEmpty() {
		return nil, fmt.Errorf("Schema '%s' was saved but schema UId is unavailable.", schema.name())
	}
	if err := r.saveDBStructure(schemaUID); err != nil {
		return nil, err
	}
	if err := r.publishSavedChanges(schema.name(), reason, contractChanged); err != nil {
		return nil, err
	}
	return r.loadSchema(schema.name(), pkg, false)
}

// loadSchema is RemoteEntitySchemaColumnManager.LoadSchema; diagnose selects DependencyDiagnosis.Report.
func (r *schemaWriteEntRun) loadSchema(schemaName string, pkg pkgWriteInstalledPackage, diagnose bool) (*schemaWriteEntSchema, error) {
	request := schemaWriteEntDesignRequest{Name: schemaName, PackageUID: newSchemaWriteEntGUID(pkg.UID), Cultures: []string{}}
	response, err := r.tryDesignItem(request)
	if err != nil {
		return nil, err
	}
	var resolution *schemaWriteEntDependencyResolution
	if response == nil || response.Schema == nil {
		if diagnose {
			resolution = r.resolveDependencies(schemaName, pkg)
		}
		response, err = r.designItem(request)
		if err != nil {
			if !strings.Contains(err.Error(), "was answered with the Creatio sign-in response") && strings.Contains(err.Error(), "answered with an HTML/XML page instead of JSON") {
				return nil, schemaWriteEntError(schemaWriteEntUnavailableMessage(schemaName, pkg.Name, resolution, schemaWriteEntSummariseFailure(err.Error()), diagnose))
			}
			return nil, err
		}
	}
	if response.Schema == nil {
		return nil, schemaWriteEntError(schemaWriteEntUnavailableMessage(schemaName, pkg.Name, resolution,
			fmt.Sprintf("GetSchemaDesignItem returned no schema for '%s'.", schemaName), diagnose))
	}
	response.Schema.normalizeLists()
	return response.Schema, nil
}

func schemaWriteEntSummariseFailure(message string) string {
	summary := message
	if index := strings.Index(message, ". "); index > 0 {
		summary = message[:index+1]
	}
	if runes := []rune(summary); len(runes) > 300 {
		summary = string(runes[:300]) + "…"
	}
	return summary
}

// schemaWriteEntDependencyResolution is clio's EntitySchemaDependencyResolution.
type schemaWriteEntDependencyResolution struct {
	candidates        []string
	applicationCount  int
	lookupSucceeded   bool
	dependenciesKnown bool
	failureReason     string
}

// resolveDependencies is EntitySchemaDependencyResolver.Resolve: the packages that contribute the schema
// and that the target package does not depend on yet, installed applications first. It only reads.
func (r *schemaWriteEntRun) resolveDependencies(schemaName string, pkg pkgWriteInstalledPackage) *schemaWriteEntDependencyResolution {
	describe := func(err error) string {
		text := schemaWriteEntRedact(err.Error())
		if runes := []rune(text); len(runes) > 300 {
			text = string(runes[:300]) + "…"
		}
		return text
	}
	found, err := r.client.FindEntitySchemas(r.ctx, EntitySchemaSearchRequest{SchemaName: schemaName})
	if err != nil {
		reason := describe(err)
		r.log.warning(fmt.Sprintf("Dependency candidate lookup failed for schema '%s': %s", schemaName, reason))
		return &schemaWriteEntDependencyResolution{lookupSucceeded: false, dependenciesKnown: true, failureReason: reason}
	}
	var contributors []string
	seen := map[string]bool{}
	for _, item := range found {
		name := item.PackageName
		if strings.TrimSpace(name) == "" || strings.EqualFold(name, pkg.Name) || seen[strings.ToLower(name)] {
			continue
		}
		seen[strings.ToLower(name)] = true
		contributors = append(contributors, name)
	}
	existing, dependencyErr := r.packageDependencies(pkg)
	none := &schemaWriteEntDependencyResolution{lookupSucceeded: true, dependenciesKnown: true}
	if len(contributors) == 0 {
		return none
	}
	var candidates []string
	for _, name := range contributors {
		if !existing[strings.ToLower(name)] {
			candidates = append(candidates, name)
		}
	}
	if dependencyErr == nil && len(candidates) == 0 {
		return none
	}
	applications := map[string]bool{}
	if rows, err := r.client.selectRows(r.ctx, buildSelectQuery("SysInstalledApp", map[string]string{"Code": "Code"}, nil, -1)); err != nil {
		r.log.warning("Could not read the installed applications used to rank dependency candidates: " + describe(err) +
			". The candidates are reported in no particular order.")
	} else {
		for _, row := range rows {
			if code := rowString(row, "Code"); strings.TrimSpace(code) != "" {
				applications[strings.ToLower(code)] = true
			}
		}
	}
	var apps, others []string
	for _, name := range candidates {
		if applications[strings.ToLower(name)] {
			apps = append(apps, name)
		} else {
			others = append(others, name)
		}
	}
	byName := func(list []string) {
		sort.SliceStable(list, func(i, j int) bool { return compareOrdinalIgnoreCase(list[i], list[j]) < 0 })
	}
	byName(apps)
	byName(others)
	if dependencyErr != nil {
		r.log.warning(fmt.Sprintf("Could not read the current dependencies of package '%s': %s. The candidate list may include packages that are already dependencies.",
			pkg.Name, describe(dependencyErr)))
	}
	return &schemaWriteEntDependencyResolution{candidates: append(apps, others...), applicationCount: len(apps),
		lookupSucceeded: true, dependenciesKnown: dependencyErr == nil}
}

// packageDependencies is PackageDependencyManager.GetDependencies: the package's declared dependency names.
func (r *schemaWriteEntRun) packageDependencies(pkg pkgWriteInstalledPackage) (map[string]bool, error) {
	if pkg.UID == emptyGUID {
		return map[string]bool{}, schemaWriteEntError("Package UId must not be empty. (Parameter 'packageUId')")
	}
	body, _ := json.Marshal(pkg.UID)
	payload, err := r.client.pkgWritePostPackageService(r.ctx, pkgWritePackagePropertiesRoute, body)
	if err != nil {
		return map[string]bool{}, err
	}
	var response struct {
		pkgWriteBaseResponse
		Package *struct {
			DependsOnPackages []struct {
				Name *string `json:"name"`
				UID  string  `json:"uId"`
			} `json:"dependsOnPackages"`
		} `json:"package"`
	}
	if err := json.Unmarshal(payload, &response); err != nil {
		return map[string]bool{}, err
	}
	if !response.Success || response.Package == nil {
		if message, ok := response.errorMessage(); ok {
			return map[string]bool{}, schemaWriteEntError(message)
		}
		return map[string]bool{}, fmt.Errorf("Could not read properties of package \"%s\".", pkg.Name)
	}
	names := map[string]bool{}
	for _, dependency := range response.Package.DependsOnPackages {
		if dependency.Name != nil {
			names[strings.ToLower(*dependency.Name)] = true
		} else {
			names[strings.ToLower(dependency.UID)] = true
		}
	}
	return names, nil
}

// schemaWriteEntUnavailableMessage is BuildSchemaUnavailableMessage.
func schemaWriteEntUnavailableMessage(schemaName, packageName string, resolution *schemaWriteEntDependencyResolution, transport string, diagnose bool) string {
	if !diagnose {
		return fmt.Sprintf("Schema '%s' was saved and published successfully in package '%s', ", schemaName, packageName) +
			"but the verification reload could not read it back yet. Publishing refreshes the schema " +
			"manager in two steps and the schema being changed is briefly missing while that runs - " +
			"measured at about nine seconds on a stand. Nothing needs to be added or repaired: do NOT " +
			"repeat the write and do NOT add a package dependency. Re-read the schema (for example " +
			"get-entity-schema-properties) to confirm the change, and if it is still missing after a " +
			"minute check the Creatio server log for this endpoint. Underlying failure: " + transport
	}
	if resolution == nil {
		resolution = &schemaWriteEntDependencyResolution{lookupSucceeded: true, dependenciesKnown: true}
	}
	var message strings.Builder
	message.WriteString(fmt.Sprintf("Schema '%s' could not be opened in package '%s'. ", schemaName, packageName))
	switch {
	case !resolution.lookupSucceeded:
		message.WriteString("clio could not complete the lookup of the packages that contribute " +
			fmt.Sprintf("'%s', so it has NO evidence about the cause and states none. The lookup failed ", schemaName) +
			fmt.Sprintf("with: %s. Retry once the environment answers again, and ", resolution.failureReason) +
			"check the Creatio server log for the failed request. ")
	case len(resolution.candidates) == 0:
		message.WriteString(fmt.Sprintf("clio found no other package that contributes '%s' and that '%s' does ", schemaName, packageName) +
			"not already depend on, so it has NO evidence about the cause and states none. Confirm the " +
			"schema name and the target package with find-entity-schema and list-packages, and check the " +
			"Creatio server log for the failed request. ")
	default:
		reported := resolution.candidates
		overflow := ""
		if len(reported) > 8 {
			overflow = fmt.Sprintf(" (+%d more, see find-entity-schema --schema-name %s)", len(reported)-8, schemaName)
			reported = reported[:8]
		}
		message.WriteString(fmt.Sprintf("The usual cause is that '%s' has no dependency on the package that owns the ", packageName) +
			fmt.Sprintf("layer of '%s' it is trying to extend. These packages contribute '%s'", schemaName, schemaName))
		if resolution.dependenciesKnown {
			message.WriteString(fmt.Sprintf(" and are not already dependencies of '%s'", packageName))
		}
		if resolution.applicationCount > 0 {
			message.WriteString(fmt.Sprintf(", installed applications first: %s%s. ", strings.Join(reported, ", "), overflow))
		} else {
			message.WriteString(fmt.Sprintf(": %s%s. ", strings.Join(reported, ", "), overflow))
		}
		if !resolution.dependenciesKnown {
			message.WriteString(fmt.Sprintf("clio could not read the dependencies '%s' already declares, so this list is ", packageName) +
				"NOT filtered - some of these may already be dependencies. Adding one that is already " +
				"declared is a no-op, so this costs nothing, but it also fixes nothing: if the package you " +
				"add was already there, the cause lies elsewhere. ")
		}
		message.WriteString(fmt.Sprintf("Add the owning one with: clio add-package-dependency --package-name %s ", packageName) +
			"--dependencies <PACKAGE>. ")
		if len(resolution.candidates) > 1 {
			message.WriteString("clio does not choose for you - more than one of these can be a valid dependency and adding " +
				"the wrong one changes the package for real; the order above is a ranking hint, not an " +
				"answer. ")
		} else {
			message.WriteString("clio does not add it for you - adding a dependency changes the package for real, and this " +
				"failure does not prove a missing dependency is the cause. ")
		}
	}
	message.WriteString("Do NOT write into the owning (managed) package and do NOT fall back to raw SQL/OData/DataService. " +
		"Underlying failure: " + transport)
	return message.String()
}

// schemaWriteEntCrossNames is CrossPackageNameContext: the package layer's names before the batch and the
// merged runtime names, read once and only when needed.
type schemaWriteEntCrossNames struct {
	pristine map[string]bool
	loaded   bool
	runtime  map[string]bool
}

// applyMutation is ApplyColumnMutation.
func (r *schemaWriteEntRun) applyMutation(schema *schemaWriteEntSchema, pkg pkgWriteInstalledPackage, options schemaWriteEntColumnOptions, culture string, cross *schemaWriteEntCrossNames) error {
	action, ok := schemaWriteEntActionOf(options.action)
	if !ok {
		return fmt.Errorf("Unsupported action '%s'. Supported actions: add, modify, remove.", options.action)
	}
	switch action {
	case "add":
		return r.addColumn(schema, pkg, options, culture, cross)
	case "modify":
		return r.modifyColumn(schema, pkg, options, culture)
	default:
		return schemaWriteEntRemoveColumn(schema, options.columnName)
	}
}

func schemaWriteEntEnsureUnique(schema *schemaWriteEntSchema, name string, exclude schemaWriteEntGUID) error {
	for _, column := range schema.allColumns() {
		if (exclude == "" || column.UID != exclude) && strings.EqualFold(column.name(), name) {
			return fmt.Errorf("Column '%s' already exists in schema '%s'.", name, schema.name())
		}
	}
	return nil
}

func schemaWriteEntParseSupportedType(typeName, action string) (int, error) {
	if strings.TrimSpace(typeName) == "" {
		return 0, fmt.Errorf("--type is required for '%s' action.", action)
	}
	dataValueType, ok := schemaWriteEntResolveType(typeName)
	if !ok {
		return 0, fmt.Errorf("Unsupported type '%s'. Supported types: %s.", typeName, schemaWriteEntSupportedTypesList())
	}
	return dataValueType, nil
}

// validateForType is ValidateOptionsForType.
func (r *schemaWriteEntRun) validateForType(options schemaWriteEntColumnOptions, dataValueType int, isAdd bool) error {
	isLookup := dataValueType == schemaWriteEntTypeLookup
	isImageLookup := dataValueType == schemaWriteEntTypeImageLookup
	if isLookup {
		if strings.TrimSpace(options.referenceSchema) == "" && isAdd {
			return schemaWriteEntError("Lookup columns require --reference-schema.")
		}
	} else if options.hasLookupOptions() {
		if isImageLookup {
			return schemaWriteEntError("ImageLookup ('Image link') columns reference the SysImage schema automatically; " +
				"do not pass --reference-schema or other lookup-specific options.")
		}
		return schemaWriteEntError("Lookup-specific options can be used only when the effective column type is Lookup.")
	}
	if !schemaWriteEntTextTypes[dataValueType] && options.hasTextOptions() {
		return schemaWriteEntError("Text-specific options can be used only when the effective column type is Text.")
	}
	if options.masked != nil && !schemaWriteEntTextTypes[dataValueType] && dataValueType != schemaWriteEntTypeSecureText {
		return schemaWriteEntError("Masked option can be used only when the effective column type is Text or SecureText.")
	}
	if dataValueType != schemaWriteEntTypeDateTime && options.useSeconds != nil {
		return schemaWriteEntError("--use-seconds can be used only when the effective column type is DateTime.")
	}
	legacy, err := schemaWriteEntLegacyBinaryDefault(options.defaultConfig, options.defaultValueSource, options.defaultValue, dataValueType)
	if err != nil {
		return err
	}
	if legacy {
		return fmt.Errorf("Type '%s' does not support --default-value or --default-value-source Const.", friendlyDataValueType(dataValueType))
	}
	context := fmt.Sprintf("Column '%s'", options.columnName)
	config, err := schemaWriteEntResolveDefaultConfig(options.defaultConfig, options.defaultValueSource, options.defaultValue, context)
	if err != nil {
		return err
	}
	if config != nil {
		if config, err = r.resolveDefault(config, dataValueType, context, ""); err != nil {
			return err
		}
	}
	return schemaWriteEntValidateDefaultConfig(config, dataValueType, context)
}

// ensureFreeAcrossPackages is EnsureNameIsFreeAcrossPackages.
func (r *schemaWriteEntRun) ensureFreeAcrossPackages(schemaName string, pkg pkgWriteInstalledPackage, name string, cross *schemaWriteEntCrossNames) error {
	if strings.TrimSpace(name) == "" || cross.pristine[strings.ToLower(name)] {
		return nil
	}
	if !cross.loaded {
		cross.loaded = true
		cross.runtime = r.runtimeColumnNames(schemaName)
	}
	if cross.runtime == nil || !cross.runtime[strings.ToLower(name)] {
		return nil
	}
	return schemaWriteEntError(fmt.Sprintf("Column '%s' already exists on the compiled schema but not in package ", name) +
		fmt.Sprintf("'%s', so another package contributes it. Adding a second column of that name ", pkg.Name) +
		"would break code generation for this schema across the WHOLE environment (a duplicate member raises a " +
		"ValidateException), not just for this package. Choose a different name — the package prefix is " +
		"required, so prefix a distinguishing word rather than reusing this one.")
}

func (r *schemaWriteEntRun) runtimeColumnNames(schemaName string) map[string]bool {
	body, _ := json.Marshal(map[string]string{"Name": schemaName})
	payload, err := r.client.postDataServiceJSON(r.ctx, "RuntimeEntitySchemaRequest", body, schemaWriteEntTimeout, maxResponseBytes)
	var response runtimeSchemaResponse
	if err == nil {
		err = json.Unmarshal(payload, &response)
	}
	if err != nil {
		r.log.warning(fmt.Sprintf("Could not read the merged runtime schema for '%s', so the cross-package column-name check was skipped: %s", schemaName, err.Error()))
		return nil
	}
	if !response.Success || response.Schema == nil {
		return nil
	}
	names := map[string]bool{}
	for _, column := range response.Schema.Columns.Items {
		if strings.TrimSpace(column.Name) != "" {
			names[strings.ToLower(column.Name)] = true
		}
	}
	return names
}

// addColumn is RemoteEntitySchemaColumnManager.AddColumn.
func (r *schemaWriteEntRun) addColumn(schema *schemaWriteEntSchema, pkg pkgWriteInstalledPackage, options schemaWriteEntColumnOptions, culture string, cross *schemaWriteEntCrossNames) error {
	if err := schemaWriteEntEnsureUnique(schema, options.columnName, ""); err != nil {
		return err
	}
	dataValueType, err := schemaWriteEntParseSupportedType(options.typeName, "add")
	if err != nil {
		return err
	}
	if err := r.validateForType(options, dataValueType, true); err != nil {
		return err
	}
	if err := r.ensureFreeAcrossPackages(schema.name(), pkg, options.columnName, cross); err != nil {
		return err
	}
	fallback := options.columnName
	if strings.TrimSpace(options.title) != "" {
		fallback = strings.TrimSpace(options.title)
	}
	titles, err := schemaWriteEntNormalizeTitles(options.titleLocalizations, fallback, schemaWriteEntTitleField, culture)
	if err != nil {
		return err
	}
	var descriptions schemaWriteEntLocMap
	if options.descriptionLocalizations != nil {
		if descriptions, err = schemaWriteEntNormalizeMap(options.descriptionLocalizations, schemaWriteEntDescriptionField, true); err != nil {
			return err
		}
	}
	caption, err := schemaWriteEntLocalizableStrings(titles.localizations, titles.effectiveTitle, culture)
	if err != nil {
		return err
	}
	description, err := schemaWriteEntLocalizableStrings(descriptions, options.description, culture)
	if err != nil {
		return err
	}
	name := options.columnName
	column := &schemaWriteEntColumn{UID: schemaWriteEntNewUID(), Name: &name, DataValueType: schemaWriteEntIntPointer(dataValueType),
		Caption: caption, Description: description, RequirementType: schemaWriteEntRequirementNone,
		Indexed: schemaWriteEntDerefBool(options.indexed), IsValueCloneable: schemaWriteEntDerefBool(options.cloneable),
		IsTrackChangesInDB: schemaWriteEntDerefBool(options.trackChanges), MultiLineText: schemaWriteEntDerefBool(options.multilineText),
		LocalizableText: schemaWriteEntDerefBool(options.localizableText), AccentInsensitive: schemaWriteEntDerefBool(options.accentInsensitive),
		Masked: schemaWriteEntDerefBool(options.masked), ValueMasked: schemaWriteEntDerefBool(options.masked),
		FormatValidated: schemaWriteEntDerefBool(options.formatValidated), UseSeconds: schemaWriteEntDerefBool(options.useSeconds),
		List: schemaWriteEntDerefBool(options.simpleLookup), CascadeConnection: schemaWriteEntDerefBool(options.cascade),
		DoNotControlIntegrity: schemaWriteEntDerefBool(options.noIntegrity)}
	if schemaWriteEntDerefBool(options.required) {
		column.RequirementType = schemaWriteEntRequirementApp
	}
	if dataValueType == schemaWriteEntTypeLookup {
		reference, err := r.resolveReference(pkg, options.referenceSchema)
		if err != nil {
			return err
		}
		column.ReferenceSchema = reference
	} else if dataValueType == schemaWriteEntTypeImageLookup {
		column.ReferenceSchema = schemaWriteEntSysImageReference()
		column.Indexed = true
	}
	if err := r.applyColumnDefault(column, options, false); err != nil {
		return err
	}
	if err := schemaWriteEntApplyUsageType(column, options); err != nil {
		return err
	}
	schema.Columns = append(schema.Columns, column)
	if schema.PrimaryDisplayColumn == nil && column.DataValueType != nil && schemaWriteEntTextTypes[*column.DataValueType] {
		schema.PrimaryDisplayColumn = column
	}
	return nil
}

func (r *schemaWriteEntRun) resolveReference(pkg pkgWriteInstalledPackage, name string) (*schemaWriteEntSchema, error) {
	response := &schemaWriteEntAvailableResponse{}
	if err := r.designer("GetAvailableReferenceSchemas", schemaWriteEntAvailableRequest{PackageUID: newSchemaWriteEntGUID(pkg.UID)}, response); err != nil {
		return nil, err
	}
	for _, item := range response.Items {
		if strings.EqualFold(item.Name, name) {
			reference := newSchemaWriteEntSchema()
			itemName := item.Name
			reference.UID, reference.Name = item.UID, &itemName
			reference.Caption = []schemaWriteEntLocalizable{{CultureName: stringPointer(schemaWriteDefaultCulture), Value: item.Caption}}
			return reference, nil
		}
	}
	return nil, fmt.Errorf("Reference schema '%s' was not found.", name)
}

// applyColumnDefault is the manager's ApplyDefaultValue.
func (r *schemaWriteEntRun) applyColumnDefault(column *schemaWriteEntColumn, options schemaWriteEntColumnOptions, preserve bool) error {
	context := fmt.Sprintf("Column '%s'", options.columnName)
	config, err := schemaWriteEntResolveDefaultConfig(options.defaultConfig, options.defaultValueSource, options.defaultValue, context)
	if err != nil {
		return err
	}
	if config == nil {
		if !preserve {
			column.DefValue = nil
		}
		return nil
	}
	source, err := schemaWriteEntDefaultSource(config.source())
	if err != nil {
		return err
	}
	if source < 0 {
		return fmt.Errorf("Column '%s' requires default-value-config.source.", options.columnName)
	}
	if source == defaultSourceNone {
		column.DefValue = &schemaWriteEntDefValue{ValueSourceType: defaultSourceNone}
		return nil
	}
	dataValueType := 0
	if column.DataValueType != nil {
		dataValueType = *column.DataValueType
	}
	reference := ""
	if column.ReferenceSchema != nil {
		reference = column.ReferenceSchema.name()
	}
	resolved, err := r.resolveDefault(config, dataValueType, context, reference)
	if err != nil {
		return err
	}
	column.DefValue, err = schemaWriteEntDefaultDTO(resolved, context)
	return err
}

func schemaWriteEntApplyUsageType(column *schemaWriteEntColumn, options schemaWriteEntColumnOptions) error {
	if strings.TrimSpace(options.usageType) == "" {
		return nil
	}
	usage, ok := schemaWriteEntUsageType(options.usageType)
	if !ok {
		return schemaWriteEntError("usage-type must be one of: General, Advanced, None.")
	}
	column.UsageType = usage
	return nil
}

// modifyColumn is RemoteEntitySchemaColumnManager.ModifyColumn.
func (r *schemaWriteEntRun) modifyColumn(schema *schemaWriteEntSchema, pkg pkgWriteInstalledPackage, options schemaWriteEntColumnOptions, culture string) error {
	column, inherited, found := schemaWriteEntFindColumn(schema, options.columnName)
	if !found {
		return fmt.Errorf("Column '%s' was not found in schema '%s'.", options.columnName, schema.name())
	}
	if inherited {
		if options.hasNonCaptionMutation() {
			return fmt.Errorf("Column '%s' is inherited; only its caption and description can be overridden. Its name, type, and flags are read-only.", options.columnName)
		}
		if strings.TrimSpace(options.title) == "" && len(options.titleLocalizations) == 0 && strings.TrimSpace(options.description) == "" && len(options.descriptionLocalizations) == 0 {
			return fmt.Errorf("Column '%s' is inherited; provide title-localizations (or a description) to override its caption.", options.columnName)
		}
		return schemaWriteEntApplyCaption(column, options, culture)
	}
	dataValueType := 0
	if column.DataValueType != nil {
		dataValueType = *column.DataValueType
	}
	if options.typeName != "" {
		var err error
		if dataValueType, err = schemaWriteEntParseSupportedType(options.typeName, "modify"); err != nil {
			return err
		}
	}
	if err := r.validateForType(options, dataValueType, false); err != nil {
		return err
	}
	if strings.TrimSpace(options.newName) != "" {
		if err := schemaWriteEntEnsureUnique(schema, options.newName, column.UID); err != nil {
			return err
		}
		renamed := strings.TrimSpace(options.newName)
		column.Name = &renamed
	}
	if options.typeName != "" {
		column.DataValueType = schemaWriteEntIntPointer(dataValueType)
	}
	if err := schemaWriteEntApplyCaption(column, options, culture); err != nil {
		return err
	}
	if options.required != nil {
		column.RequirementType = schemaWriteEntRequirementNone
		if *options.required {
			column.RequirementType = schemaWriteEntRequirementApp
		}
	}
	setFlag := func(target *bool, value *bool) {
		if value != nil {
			*target = *value
		}
	}
	setFlag(&column.Indexed, options.indexed)
	setFlag(&column.IsValueCloneable, options.cloneable)
	setFlag(&column.IsTrackChangesInDB, options.trackChanges)
	if err := r.applyColumnDefault(column, options, true); err != nil {
		return err
	}
	setFlag(&column.MultiLineText, options.multilineText)
	setFlag(&column.LocalizableText, options.localizableText)
	setFlag(&column.AccentInsensitive, options.accentInsensitive)
	if options.masked != nil {
		column.Masked, column.ValueMasked = *options.masked, *options.masked
	}
	setFlag(&column.FormatValidated, options.formatValidated)
	setFlag(&column.UseSeconds, options.useSeconds)
	if err := schemaWriteEntApplyUsageType(column, options); err != nil {
		return err
	}
	switch dataValueType {
	case schemaWriteEntTypeLookup:
		if strings.TrimSpace(options.referenceSchema) != "" {
			reference, err := r.resolveReference(pkg, options.referenceSchema)
			if err != nil {
				return err
			}
			column.ReferenceSchema = reference
		} else if column.ReferenceSchema == nil {
			return fmt.Errorf("Lookup column '%s' must specify --reference-schema.", options.columnName)
		}
		setFlag(&column.List, options.simpleLookup)
		setFlag(&column.CascadeConnection, options.cascade)
		setFlag(&column.DoNotControlIntegrity, options.noIntegrity)
	case schemaWriteEntTypeImageLookup:
		column.ReferenceSchema = schemaWriteEntSysImageReference()
		column.Indexed, column.List, column.CascadeConnection, column.DoNotControlIntegrity = true, false, false, false
	default:
		column.ReferenceSchema = nil
		column.List, column.CascadeConnection, column.DoNotControlIntegrity = false, false, false
	}
	return nil
}

// schemaWriteEntApplyCaption is ApplyColumnCaptionAndDescription.
func schemaWriteEntApplyCaption(column *schemaWriteEntColumn, options schemaWriteEntColumnOptions, culture string) error {
	caption := append([]schemaWriteEntLocalizable{}, column.Caption...)
	description := append([]schemaWriteEntLocalizable{}, column.Description...)
	if options.titleLocalizations != nil {
		normalization, err := schemaWriteEntNormalizeTitles(options.titleLocalizations, options.title, schemaWriteEntTitleField, culture)
		if err != nil {
			return err
		}
		if caption, err = schemaWriteEntLocalizableStrings(normalization.localizations, "", ""); err != nil {
			return err
		}
	} else if strings.TrimSpace(options.title) != "" {
		caption = schemaWriteEntSetLocalizable(caption, strings.TrimSpace(options.title), culture)
	}
	if options.descriptionLocalizations != nil {
		normalized, err := schemaWriteEntNormalizeMap(options.descriptionLocalizations, schemaWriteEntDescriptionField, true)
		if err != nil {
			return err
		}
		if description, err = schemaWriteEntLocalizableStrings(normalized, "", ""); err != nil {
			return err
		}
	} else if strings.TrimSpace(options.description) != "" {
		description = schemaWriteEntSetLocalizable(description, options.description, culture)
	}
	column.Caption, column.Description = caption, description
	return nil
}

// schemaWriteEntRemoveColumn is RemoveColumn: the column goes, and every schema reference to it falls back.
func schemaWriteEntRemoveColumn(schema *schemaWriteEntSchema, columnName string) error {
	column, inherited, found := schemaWriteEntFindColumn(schema, columnName)
	if !found {
		return fmt.Errorf("Column '%s' was not found in schema '%s'.", columnName, schema.name())
	}
	if inherited {
		return fmt.Errorf("Column '%s' is inherited and cannot be removed.", columnName)
	}
	var own []*schemaWriteEntColumn
	for _, item := range schema.Columns {
		if item.UID != column.UID {
			own = append(own, item)
		}
	}
	if own == nil {
		own = []*schemaWriteEntColumn{}
	}
	schema.Columns = own
	remaining := schema.allColumns()
	var firstGUID, firstText *schemaWriteEntColumn
	for _, item := range remaining {
		if firstGUID == nil && item.typeIs(schemaWriteEntTypeGUID) {
			firstGUID = item
		}
		if firstText == nil && item.DataValueType != nil && schemaWriteEntTextTypes[*item.DataValueType] {
			firstText = item
		}
	}
	replace := func(current *schemaWriteEntColumn, fallback *schemaWriteEntColumn, required bool) (*schemaWriteEntColumn, error) {
		if current == nil || current.UID != column.UID {
			return current, nil
		}
		if required && fallback == nil {
			return nil, fmt.Errorf("Cannot remove column '%s' because it is the primary column and no valid fallback exists.", column.name())
		}
		return fallback, nil
	}
	var err error
	if schema.PrimaryColumn, err = replace(schema.PrimaryColumn, firstGUID, true); err != nil {
		return err
	}
	schema.PrimaryDisplayColumn, _ = replace(schema.PrimaryDisplayColumn, firstText, false)
	for _, target := range []**schemaWriteEntColumn{&schema.PrimaryImageColumn, &schema.PrimaryColorColumn, &schema.PrimaryOrderColumn,
		&schema.HierarchyColumn, &schema.OwnerColumn, &schema.MasterRecordColumn, &schema.CreatedByColumn, &schema.CreatedOnColumn,
		&schema.ModifiedByColumn, &schema.ModifiedOnColumn} {
		*target, _ = replace(*target, nil, false)
	}
	return nil
}

// schemaWriteEntVerifyMutations is VerifyColumnMutations.
func schemaWriteEntVerifyMutations(reloaded *schemaWriteEntSchema, operations []schemaWriteEntColumnOptions, culture string) error {
	own := map[string]bool{}
	for _, column := range reloaded.Columns {
		own[strings.ToLower(column.name())] = true
	}
	type expectation struct {
		name  string
		exist bool
	}
	var expected []expectation
	setExpectation := func(name string, exist bool) {
		for index := range expected {
			if strings.EqualFold(expected[index].name, name) {
				expected[index].exist = exist
				return
			}
		}
		expected = append(expected, expectation{name, exist})
	}
	for _, operation := range operations {
		action, _ := schemaWriteEntActionOf(operation.action)
		name := strings.TrimSpace(operation.columnName)
		if action == "modify" && strings.TrimSpace(operation.newName) == "" && !own[strings.ToLower(name)] {
			var inheritedMatch *schemaWriteEntColumn
			for _, column := range reloaded.InheritedColumns {
				if strings.EqualFold(column.name(), name) {
					inheritedMatch = column
					break
				}
			}
			if inheritedMatch != nil {
				expectedCaption := schemaWriteEntExpectedCaption(operation, culture)
				if expectedCaption == nil {
					continue
				}
				actual := schemaWriteEntLocalizableValue(inheritedMatch.Caption, culture)
				if actual == nil || *actual != *expectedCaption {
					shown := "<none>"
					if actual != nil {
						shown = *actual
					}
					return fmt.Errorf("Caption override for inherited column '%s' was not persisted (expected '%s', got '%s').", operation.columnName, *expectedCaption, shown)
				}
				continue
			}
		}
		if action == "modify" && strings.TrimSpace(operation.newName) != "" {
			setExpectation(name, false)
			setExpectation(strings.TrimSpace(operation.newName), true)
			continue
		}
		setExpectation(name, action != "remove")
	}
	for _, item := range expected {
		exists := own[strings.ToLower(item.name)]
		if item.exist && !exists {
			return fmt.Errorf("Column '%s' could not be reloaded after save.", item.name)
		}
		if !item.exist && exists {
			return fmt.Errorf("Column '%s' is still present after save.", item.name)
		}
	}
	return nil
}

func schemaWriteEntExpectedCaption(operation schemaWriteEntColumnOptions, culture string) *string {
	if len(operation.titleLocalizations) > 0 {
		find := func(key string) *string {
			for _, pair := range operation.titleLocalizations {
				if strings.EqualFold(pair.Key, key) && strings.TrimSpace(pair.Value) != "" {
					value := strings.TrimSpace(pair.Value)
					return &value
				}
			}
			return nil
		}
		if value := find(culture); value != nil {
			return value
		}
		if value := find(schemaWriteDefaultCulture); value != nil {
			return value
		}
		for _, pair := range operation.titleLocalizations {
			if strings.TrimSpace(pair.Value) != "" {
				value := strings.TrimSpace(pair.Value)
				return &value
			}
		}
		return nil
	}
	if strings.TrimSpace(operation.title) == "" {
		return nil
	}
	value := strings.TrimSpace(operation.title)
	return &value
}
