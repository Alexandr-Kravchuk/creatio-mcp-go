package creatio

// create-entity-schema and create-lookup: clio's CreateEntitySchemaTool / CreateLookupTool, the
// CreateEntitySchemaCommand they run and its RemoteEntitySchemaCreator, plus the lookup catalog
// registration create-lookup adds (LookupRegistrationService).

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"strings"
)

// SchemaWriteEntCreateArgs is clio's CreateEntitySchemaArgs / CreateLookupArgs.
type SchemaWriteEntCreateArgs struct {
	PackageName, SchemaName, ParentSchemaName, LegacyTitle, CaptionCulture string
	TitleLocalizations                                                     schemaWriteEntLocMap
	Columns                                                                []SchemaWriteEntColumnArgs
	ExtendParent, IsVirtual                                                bool
	IsDBView                                                               *bool
}

// ParseSchemaWriteEntCreateArgs binds create-entity-schema's or create-lookup's arguments.
func ParseSchemaWriteEntCreateArgs(tool string, args map[string]any) (SchemaWriteEntCreateArgs, error) {
	b := schemaWriteEntBinder{tool: tool}
	var parsed SchemaWriteEntCreateArgs
	var err error
	for key, target := range map[string]*string{"package-name": &parsed.PackageName, "schema-name": &parsed.SchemaName,
		"title": &parsed.LegacyTitle, "caption-culture": &parsed.CaptionCulture} {
		if *target, err = b.text(args, key); err != nil {
			return parsed, err
		}
	}
	if parsed.TitleLocalizations, err = b.locMap(args, "title-localizations"); err != nil {
		return parsed, err
	}
	if parsed.Columns, _, err = b.columns(args, "columns"); err != nil {
		return parsed, err
	}
	if tool == "create-entity-schema" {
		if parsed.ParentSchemaName, err = b.text(args, "parent-schema-name"); err != nil {
			return parsed, err
		}
		for key, target := range map[string]*bool{"extend-parent": &parsed.ExtendParent, "is-virtual": &parsed.IsVirtual} {
			flag, err := b.boolean(args, key)
			if err != nil {
				return parsed, err
			}
			*target = flag != nil && *flag
		}
		if parsed.IsDBView, err = b.boolean(args, "is-db-view"); err != nil {
			return parsed, err
		}
	}
	return parsed, nil
}

// SchemaWriteEntCreateTerms is the candidate-term list create-entity-schema and create-lookup enrich with.
func SchemaWriteEntCreateTerms(args SchemaWriteEntCreateArgs) []string {
	return schemaWriteEntDistinctTerms(append([]string{args.SchemaName}, args.TitleLocalizations.values()...)...)
}

// schemaWriteEntCreateOptions is clio's CreateEntitySchemaOptions as the MCP tool fills it.
type schemaWriteEntCreateOptions struct {
	pkg, schemaName, title, parent, captionCulture string
	titleLocalizations                             schemaWriteEntLocMap
	extendParent, isVirtual                        bool
	isDBView                                       *bool
	columns                                        []schemaWriteEntColumnSpec
	hasColumns                                     bool
}

// schemaWriteEntColumnSpec is one serialized column of CreateOptions: the fields clio puts into its JSON
// spec and the spec text itself, which several failure texts quote.
type schemaWriteEntColumnSpec struct {
	name, typeName, referenceSchema string
	titleLocalizations              schemaWriteEntLocMap
	required, masked                *bool
	defaultValueSource, defaultVal  *string
	defaultConfig                   *schemaWriteEntDefaultConfig
	spec                            string
}

// schemaWriteEntBuildCreateOptions is CreateEntitySchemaTool.CreateOptions.
func schemaWriteEntBuildCreateOptions(args SchemaWriteEntCreateArgs, parent string, extendParent, isVirtual bool, isDBView *bool) (schemaWriteEntCreateOptions, error) {
	context := fmt.Sprintf("Schema '%s'", args.SchemaName)
	titles, err := schemaWriteEntRequireTitles(args.TitleLocalizations, args.LegacyTitle, "", "", context)
	if err != nil {
		return schemaWriteEntCreateOptions{}, err
	}
	normalization, err := schemaWriteEntNormalizeTitles(titles, "", schemaWriteEntTitleField, "")
	if err != nil {
		return schemaWriteEntCreateOptions{}, err
	}
	title := normalization.effectiveTitle
	if title == "" {
		if title, err = schemaWriteEntDefaultTitle(titles, context); err != nil {
			return schemaWriteEntCreateOptions{}, err
		}
	}
	options := schemaWriteEntCreateOptions{pkg: args.PackageName, schemaName: args.SchemaName, title: title,
		titleLocalizations: normalization.localizations, parent: parent, extendParent: extendParent,
		isVirtual: isVirtual, isDBView: isDBView, captionCulture: args.CaptionCulture}
	if options.titleLocalizations == nil {
		options.titleLocalizations = titles
	}
	if args.Columns != nil {
		options.hasColumns = true
	}
	for index, column := range args.Columns {
		spec, err := schemaWriteEntSerializeColumn(column, fmt.Sprintf("%s column #%d", context, index+1))
		if err != nil {
			return schemaWriteEntCreateOptions{}, err
		}
		options.columns = append(options.columns, spec)
	}
	return options, nil
}

// schemaWriteEntSerializeColumn is CreateEntitySchemaTool.SerializeColumn.
func schemaWriteEntSerializeColumn(column SchemaWriteEntColumnArgs, context string) (schemaWriteEntColumnSpec, error) {
	name, err := schemaWriteEntColumnIdentity(column.resolveName(), context, "column")
	if err != nil {
		return schemaWriteEntColumnSpec{}, err
	}
	typeName := strings.TrimSpace(column.resolveType())
	if typeName == "" {
		return schemaWriteEntColumnSpec{}, fmt.Errorf("%s is missing the column type. Send it as 'type' (or its alias 'data-value-type'). (Parameter 'column')", context)
	}
	legacyTitle, legacyCaption := "", ""
	if column.Title != nil {
		legacyTitle = *column.Title
	}
	if column.Caption != nil {
		legacyCaption = *column.Caption
	}
	titles, err := schemaWriteEntRequireTitles(column.TitleLocalizations, legacyTitle, legacyCaption, name, context)
	if err != nil {
		return schemaWriteEntColumnSpec{}, err
	}
	reference := strings.TrimSpace(column.resolveReference())
	spec := schemaWriteEntColumnSpec{name: name, typeName: typeName, referenceSchema: reference, titleLocalizations: titles,
		required: column.resolveRequired(), masked: column.Masked, defaultValueSource: column.DefaultValueSource,
		defaultVal: column.DefaultValue, defaultConfig: column.DefaultValueConfig}
	spec.spec = schemaWriteEntColumnSpecText(spec)
	return spec, nil
}

// schemaWriteEntColumnSpecText is the JSON System.Text.Json writes for the serialized column dictionary.
func schemaWriteEntColumnSpecText(spec schemaWriteEntColumnSpec) string {
	titles := orderedFields{}
	for _, pair := range spec.titleLocalizations {
		titles = append(titles, field{pair.Key, pair.Value})
	}
	var reference any
	if spec.referenceSchema != "" {
		reference = spec.referenceSchema
	}
	var config any
	if spec.defaultConfig != nil {
		value := jsonNullNode()
		if raw := schemaWriteEntNullable(spec.defaultConfig.Value); raw != nil {
			if parsed, err := parseJNode(raw); err == nil {
				value = parsed
			}
		}
		config = orderedFields{{"source", spec.defaultConfig.Source}, {"value", value},
			{"value-source", spec.defaultConfig.ValueSource}, {"resolved-value-source", spec.defaultConfig.ResolvedValueSource},
			{"sequence-prefix", spec.defaultConfig.SequencePrefix}, {"sequence-number-of-chars", spec.defaultConfig.SequenceNumberOfChars},
			{"display-value", spec.defaultConfig.DisplayValue}, {"record-resolution", spec.defaultConfig.RecordResolution},
			{"source-resolution", spec.defaultConfig.SourceResolution}}
	}
	return string(toJNode(orderedFields{{"name", spec.name}, {"type", spec.typeName}, {"title-localizations", titles},
		{"reference-schema-name", reference}, {"required", schemaWriteEntOptionalBool(spec.required)}, {"default-value-source", spec.defaultValueSource},
		{"default-value", spec.defaultVal}, {"default-value-config", config}, {"masked", schemaWriteEntOptionalBool(spec.masked)}}).stjJSON())
}

// schemaWriteEntOptionalBool is a nullable bool as toJNode takes it: nil for null.
func schemaWriteEntOptionalBool(value *bool) any {
	if value == nil {
		return nil
	}
	return *value
}

// ---------------------------------------------------------------------------------------------- command

// schemaWriteEntValidateCreate is CreateEntitySchemaCommand.Validate.
func schemaWriteEntValidateCreate(options schemaWriteEntCreateOptions) error {
	if strings.TrimSpace(options.pkg) == "" {
		return schemaWriteEntError("Package is required.")
	}
	if strings.TrimSpace(options.schemaName) == "" {
		return schemaWriteEntError("Schema name is required.")
	}
	if strings.TrimSpace(options.title) == "" {
		return schemaWriteEntError("Schema title is required.")
	}
	if options.extendParent && strings.TrimSpace(options.parent) != "" && !strings.EqualFold(options.schemaName, options.parent) {
		return schemaWriteEntError(schemaWriteEntReplacementName)
	}
	return nil
}

// executeCreate is CreateEntitySchemaCommand.Execute: 0 with "Done", or 1 with the failure logged.
func (r *schemaWriteEntRun) executeCreate(options schemaWriteEntCreateOptions) int {
	if err := r.createEntitySchema(options); err != nil {
		r.log.err(err.Error())
		return 1
	}
	r.log.info("Done")
	return 0
}

func (r *schemaWriteEntRun) createEntitySchema(options schemaWriteEntCreateOptions) error {
	if err := schemaWriteEntValidateCreate(options); err != nil {
		return err
	}
	if strings.TrimSpace(options.parent) == "" {
		options.parent = schemaWriteEntDefaultParent
		if options.extendParent {
			options.parent = options.schemaName
		}
	}
	return r.create(options)
}

type schemaWriteEntParsedColumn struct {
	schemaWriteEntColumnSpec
	title string
}

func (c schemaWriteEntParsedColumn) isLookup() bool {
	return schemaWriteEntIsLookupTypeName(c.typeName)
}

// parseColumns is RemoteEntitySchemaCreator.ParseColumns over the structured specs the MCP tool builds.
func schemaWriteEntParseColumns(specs []schemaWriteEntColumnSpec) ([]schemaWriteEntParsedColumn, error) {
	var parsed []schemaWriteEntParsedColumn
	for _, spec := range specs {
		if strings.TrimSpace(spec.name) == "" {
			return nil, fmt.Errorf("Column '%s' is missing its column code. Provide a non-empty column name.", spec.spec)
		}
		if _, ok := schemaWriteEntResolveType(spec.typeName); !ok {
			return nil, fmt.Errorf("Column '%s' has an unsupported type '%s'. Supported types: %s. Type names are case-insensitive.",
				spec.spec, spec.typeName, schemaWriteEntSupportedTypesList())
		}
		titles, err := schemaWriteEntNormalizeMap(spec.titleLocalizations, schemaWriteEntTitleField, true)
		if err != nil {
			return nil, err
		}
		title := spec.name
		if len(spec.titleLocalizations) > 0 {
			if title, err = schemaWriteEntRequiredLocalization(spec.titleLocalizations, schemaWriteEntTitleField, schemaWriteDefaultCulture); err != nil {
				return nil, err
			}
		}
		isLookup := schemaWriteEntIsLookupTypeName(spec.typeName)
		if isLookup && spec.referenceSchema == "" {
			return nil, fmt.Errorf("Lookup column '%s' must specify a reference schema name.", spec.spec)
		}
		if !isLookup && spec.referenceSchema != "" {
			if dataValueType, ok := schemaWriteEntResolveType(spec.typeName); ok && dataValueType == schemaWriteEntTypeImageLookup {
				return nil, fmt.Errorf("ImageLookup ('Image link') column '%s' references the SysImage schema automatically; do not specify a reference schema name.", spec.spec)
			}
			return nil, fmt.Errorf("Column '%s' can specify a reference schema name only for lookup columns.", spec.spec)
		}
		if spec.defaultValueSource != nil {
			if _, err := schemaWriteEntDefaultSource(*spec.defaultValueSource); err != nil {
				return nil, err
			}
		}
		copy := spec
		copy.titleLocalizations = titles
		parsed = append(parsed, schemaWriteEntParsedColumn{schemaWriteEntColumnSpec: copy, title: title})
	}
	return parsed, nil
}

// create is RemoteEntitySchemaCreator.Create.
func (r *schemaWriteEntRun) create(options schemaWriteEntCreateOptions) error {
	if err := r.ensureNameAvailable(options); err != nil {
		return err
	}
	pkg, err := r.resolvePackage(options.pkg)
	if err != nil {
		return err
	}
	columns, err := schemaWriteEntParseColumns(options.columns)
	if err != nil {
		return err
	}
	packageUID := newSchemaWriteEntGUID(pkg.UID)
	created, err := r.createNewSchema(packageUID, options.extendParent)
	if err != nil {
		return err
	}
	schema := created.Schema
	if schema == nil {
		return schemaWriteEntError("CreateNewSchema returned no schema.")
	}
	schemaWriteEntAssignPackage(schema, pkg)
	if !options.extendParent {
		unique, err := r.checkUniqueName(options.schemaName, schema.UID)
		if err != nil {
			return err
		}
		if !unique {
			return fmt.Errorf("Schema '%s' already exists.", options.schemaName)
		}
	}
	if strings.TrimSpace(options.parent) != "" {
		if schema, err = r.assignParentSchema(schema, options.parent, packageUID, pkg.Name); err != nil {
			return err
		}
	}
	culture, err := r.captionCulture(options.captionCulture)
	if err != nil {
		return err
	}
	if err := r.applySchemaMetadata(schema, options, columns, pkg, culture); err != nil {
		return err
	}
	cultures := append([]string{}, options.titleLocalizations.keys()...)
	for _, column := range columns {
		cultures = append(cultures, column.titleLocalizations.keys()...)
	}
	if err := r.ensureCulturesAvailable(append(cultures, culture)); err != nil {
		return err
	}
	saved, err := r.saveSchema(schema)
	if err != nil {
		return err
	}
	schemaUID := saved.SchemaUID
	if schemaUID.isEmpty() {
		schemaUID = schema.UID
	}
	if schemaUID.isEmpty() {
		return fmt.Errorf("Schema '%s' was saved but schema UId is unavailable.", options.schemaName)
	}
	if err := r.saveDBStructure(schemaUID); err != nil {
		return err
	}
	if err := r.publishSavedChanges(options.schemaName, "was created and saved", true); err != nil {
		return err
	}
	if err := r.verifyCreated(options, packageUID, culture, schemaUID); err != nil {
		return err
	}
	r.log.info(fmt.Sprintf("Entity schema '%s' created in package '%s'.", options.schemaName, options.pkg))
	return nil
}

func schemaWriteEntAssignPackage(schema *schemaWriteEntSchema, pkg pkgWriteInstalledPackage) {
	if schema.Package == nil {
		schema.Package = &schemaWriteEntPackage{}
	}
	name := pkg.Name
	schema.Package.UID = newSchemaWriteEntGUID(pkg.UID)
	schema.Package.Name = &name
}

// ensureNameAvailable is EnsureSchemaNameAvailable: a replacement must not already exist in the package;
// any other schema must have a unique name.
func (r *schemaWriteEntRun) ensureNameAvailable(options schemaWriteEntCreateOptions) error {
	exists := false
	if options.extendParent {
		found, err := r.client.FindEntitySchemas(r.ctx, EntitySchemaSearchRequest{SchemaName: options.schemaName})
		if err != nil {
			return err
		}
		for _, item := range found {
			if strings.EqualFold(item.PackageName, options.pkg) {
				exists = true
			}
		}
	} else {
		unique, err := r.checkUniqueName(options.schemaName, "")
		if err != nil {
			return err
		}
		exists = !unique
	}
	if exists {
		return fmt.Errorf("Schema '%s' already exists.", options.schemaName)
	}
	return nil
}

func (r *schemaWriteEntRun) assignParentSchema(schema *schemaWriteEntSchema, parentName string, packageUID schemaWriteEntGUID, packageName string) (*schemaWriteEntSchema, error) {
	available, err := r.availableSchemas("GetAvailableParentSchemas", packageUID)
	if err != nil {
		return nil, err
	}
	var parent *schemaWriteEntManagerItem
	for index := range available.Items {
		if strings.EqualFold(available.Items[index].Name, parentName) {
			parent = &available.Items[index]
			break
		}
	}
	if parent == nil {
		label := packageName
		if schema.Package == nil || schema.Package.Name == nil {
			label = packageUID.String()
		}
		return nil, fmt.Errorf("Parent schema '%s' is not available for package '%s'.", parentName, label)
	}
	assigned, err := r.assignParent(schema, parent.UID)
	if err != nil {
		return nil, err
	}
	if assigned.Schema == nil {
		return nil, schemaWriteEntError("AssignParentSchema returned no schema.")
	}
	return assigned.Schema, nil
}

// applySchemaMetadata is RemoteEntitySchemaCreator.ApplySchemaMetadata.
func (r *schemaWriteEntRun) applySchemaMetadata(schema *schemaWriteEntSchema, options schemaWriteEntCreateOptions,
	columns []schemaWriteEntParsedColumn, pkg pkgWriteInstalledPackage, culture string) error {
	normalization, err := schemaWriteEntNormalizeTitles(options.titleLocalizations, options.title, schemaWriteEntTitleField, culture)
	if err != nil {
		return err
	}
	name := options.schemaName
	schema.Name = &name
	if schema.Caption, err = schemaWriteEntLocalizableStrings(normalization.localizations, normalization.effectiveTitle, culture); err != nil {
		return err
	}
	schemaWriteEntAssignPackage(schema, pkg)
	schema.normalizeLists()
	if options.isVirtual {
		schema.IsVirtual = true
	}
	if options.isDBView != nil {
		schema.IsDBView = *options.isDBView
	}
	if !schema.ParentSchema.hasValue() {
		empty := ""
		schema.AdministratedByOperations, schema.AdministratedByColumns, schema.AdministratedByRecords = false, false, false
		schema.UseDenyRecordRights = false
		schema.RightSchemaName = &empty
	}
	references := map[string]schemaWriteEntManagerItem{}
	for _, column := range columns {
		if column.isLookup() {
			if references, err = r.referenceSchemas(newSchemaWriteEntGUID(pkg.UID)); err != nil {
				return err
			}
			break
		}
	}
	for _, column := range columns {
		created, err := r.createColumn(column, references, culture, options.schemaName)
		if err != nil {
			return err
		}
		schema.Columns = append(schema.Columns, created)
	}
	hasGUID := false
	for _, column := range schema.Columns {
		if column.typeIs(schemaWriteEntTypeGUID) {
			hasGUID = true
		}
	}
	if !schema.ParentSchema.hasValue() && !hasGUID {
		primary, err := r.createColumn(schemaWriteEntParsedColumn{schemaWriteEntColumnSpec: schemaWriteEntColumnSpec{
			name: r.primaryColumnName(), typeName: "guid"}, title: "Id"}, references, culture, options.schemaName)
		if err != nil {
			return err
		}
		schema.Columns = append([]*schemaWriteEntColumn{primary}, schema.Columns...)
	}
	if !schema.ParentSchema.hasValue() {
		for _, column := range schema.Columns {
			if column.typeIs(schemaWriteEntTypeGUID) {
				schema.PrimaryColumn = column
				break
			}
		}
	}
	if schema.PrimaryDisplayColumn == nil {
		for _, column := range schema.Columns {
			if column.DataValueType != nil && schemaWriteEntTextTypes[*column.DataValueType] {
				schema.PrimaryDisplayColumn = column
				break
			}
		}
	}
	return nil
}

func (r *schemaWriteEntRun) primaryColumnName() string {
	prefix := r.client.GetSchemaNamePrefix(r.ctx)
	if !prefix.Success || strings.TrimSpace(prefix.SchemaNamePrefix) == "" {
		return "Id"
	}
	return prefix.SchemaNamePrefix + "Id"
}

// referenceSchemas is GetReferenceSchemas: the package's available reference schemas by name.
func (r *schemaWriteEntRun) referenceSchemas(packageUID schemaWriteEntGUID) (map[string]schemaWriteEntManagerItem, error) {
	response := &schemaWriteEntAvailableResponse{}
	err := r.designer("GetAvailableReferenceSchemas", schemaWriteEntAvailableRequest{PackageUID: packageUID}, response)
	if err != nil {
		return nil, err
	}
	references := map[string]schemaWriteEntManagerItem{}
	for _, item := range response.Items {
		if _, exists := references[strings.ToLower(item.Name)]; !exists {
			references[strings.ToLower(item.Name)] = item
		}
	}
	return references, nil
}

// schemaWriteEntNewUID is Guid.NewGuid().
func schemaWriteEntNewUID() schemaWriteEntGUID {
	var raw [16]byte
	_, _ = rand.Read(raw[:])
	raw[6] = raw[6]&0x0f | 0x40
	raw[8] = raw[8]&0x3f | 0x80
	return schemaWriteEntGUID(fmt.Sprintf("%x-%x-%x-%x-%x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16]))
}

// createColumn is RemoteEntitySchemaCreator.CreateColumn.
func (r *schemaWriteEntRun) createColumn(column schemaWriteEntParsedColumn, references map[string]schemaWriteEntManagerItem, culture, schemaName string) (*schemaWriteEntColumn, error) {
	dataValueType, ok := schemaWriteEntResolveType(column.typeName)
	if !ok {
		return nil, fmt.Errorf("Column type '%s' is not supported. Supported types: %s.", column.typeName, schemaWriteEntSupportedTypesList())
	}
	context := fmt.Sprintf("Column '%s'", column.name)
	legacy, err := schemaWriteEntLegacyBinaryDefault(column.defaultConfig, schemaWriteEntDeref(column.defaultValueSource), column.defaultVal, dataValueType)
	if err != nil {
		return nil, err
	}
	if legacy {
		return nil, fmt.Errorf("Column '%s' of type '%s' does not support default-value or default-value-source Const.", column.name, friendlyDataValueType(dataValueType))
	}
	config, err := schemaWriteEntResolveDefaultConfig(column.defaultConfig, schemaWriteEntDeref(column.defaultValueSource), column.defaultVal, context)
	if err != nil {
		return nil, err
	}
	if config != nil {
		if config, err = r.resolveDefault(config, dataValueType, context, ""); err != nil {
			return nil, err
		}
	}
	if err := schemaWriteEntValidateDefaultConfig(config, dataValueType, context); err != nil {
		return nil, err
	}
	if column.masked != nil && !schemaWriteEntTextTypes[dataValueType] && dataValueType != schemaWriteEntTypeSecureText {
		return nil, fmt.Errorf("Column '%s' can use masked only for Text or SecureText types.", column.name)
	}
	normalization, err := schemaWriteEntNormalizeTitles(column.titleLocalizations, column.title, schemaWriteEntTitleField, culture)
	if err != nil {
		return nil, err
	}
	caption, err := schemaWriteEntLocalizableStrings(normalization.localizations, normalization.effectiveTitle, culture)
	if err != nil {
		return nil, err
	}
	name := column.name
	masked := column.masked != nil && *column.masked
	created := &schemaWriteEntColumn{UID: schemaWriteEntNewUID(), Name: &name, DataValueType: schemaWriteEntIntPointer(dataValueType),
		Caption: caption, Description: []schemaWriteEntLocalizable{}, RequirementType: schemaWriteEntRequirementNone,
		Masked: masked, ValueMasked: masked}
	if column.required != nil && *column.required {
		created.RequirementType = schemaWriteEntRequirementApp
	}
	if masked {
		pattern, replacement := schemaWriteEntMaskingPattern, schemaWriteEntMaskingReplace
		code := schemaName + "_" + column.name + "_UnmaskedValue"
		created.ValueMaskingSettings = &schemaWriteEntMasking{Pattern: &pattern, Replacement: &replacement, AdminOperationCode: &code}
	}
	// ApplyDefaultValue runs before the reference is set, as in clio: a create-time lookup Const default is
	// therefore not checked against the referenced records.
	if config != nil {
		source, _ := schemaWriteEntDefaultSource(config.source())
		if source == defaultSourceNone {
			created.DefValue = nil
		} else {
			resolved, err := r.resolveDefault(config, dataValueType, context, "")
			if err != nil {
				return nil, err
			}
			if created.DefValue, err = schemaWriteEntDefaultDTO(resolved, context); err != nil {
				return nil, err
			}
		}
	}
	if column.isLookup() {
		reference, ok := references[strings.ToLower(column.referenceSchema)]
		if !ok {
			return nil, fmt.Errorf("Reference schema '%s' was not found for lookup column '%s'.", column.referenceSchema, column.name)
		}
		created.ReferenceSchema = schemaWriteEntReferenceSchema(reference, culture)
	} else if dataValueType == schemaWriteEntTypeImageLookup {
		created.ReferenceSchema = schemaWriteEntSysImageReference()
		created.Indexed = true
	}
	return created, nil
}

func schemaWriteEntDeref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func schemaWriteEntReferenceSchema(item schemaWriteEntManagerItem, culture string) *schemaWriteEntSchema {
	reference := newSchemaWriteEntSchema()
	name := item.Name
	reference.UID = item.UID
	reference.Name = &name
	reference.Caption = []schemaWriteEntLocalizable{{CultureName: &culture, Value: item.Caption}}
	return reference
}

// schemaWriteEntSysImageReference is CreateSysImageReferenceSchema, captioned in the culture clio runs
// under (en-US).
func schemaWriteEntSysImageReference() *schemaWriteEntSchema {
	reference := newSchemaWriteEntSchema()
	name := schemaWriteEntSysImageName
	reference.UID = schemaWriteEntSysImageUID
	reference.Name = &name
	reference.Caption = []schemaWriteEntLocalizable{schemaWriteEntLocalized(schemaWriteDefaultCulture, schemaWriteEntSysImageName)}
	return reference
}

// verifyCreated is VerifyCreatedSchema: the design item reloaded, or the runtime schema when the designer
// answers with markup.
func (r *schemaWriteEntRun) verifyCreated(options schemaWriteEntCreateOptions, packageUID schemaWriteEntGUID, culture string, schemaUID schemaWriteEntGUID) error {
	reloaded, err := r.tryDesignItem(schemaWriteEntDesignRequest{Name: options.schemaName, PackageUID: packageUID, Cultures: []string{culture}})
	if err != nil {
		return err
	}
	if reloaded != nil {
		if reloaded.Schema == nil {
			return fmt.Errorf("Schema '%s' could not be reloaded after save.", options.schemaName)
		}
		if !strings.EqualFold(reloaded.Schema.name(), options.schemaName) {
			return fmt.Errorf("Schema '%s' was reloaded with unexpected name '%s'.", options.schemaName, reloaded.Schema.name())
		}
		if options.isDBView != nil && reloaded.Schema.IsDBView != *options.isDBView {
			return fmt.Errorf("Database-view flag was not persisted for schema '%s'.", options.schemaName)
		}
		return nil
	}
	runtime, err := r.runtimeSchemaByUID(schemaUID)
	if err != nil {
		return err
	}
	if !runtime.Success || runtime.Schema == nil {
		return fmt.Errorf("Schema '%s' was saved but could not be verified: the designer service returned an HTML response and the runtime schema is unavailable.", options.schemaName)
	}
	if options.isDBView != nil {
		return fmt.Errorf("Schema '%s' was saved but the database-view flag could not be verified because the designer returned HTML.", options.schemaName)
	}
	if !strings.EqualFold(runtime.Schema.Name, options.schemaName) {
		return fmt.Errorf("Schema '%s' was created but runtime schema name '%s' does not match.", options.schemaName, runtime.Schema.Name)
	}
	r.log.info(fmt.Sprintf("Schema '%s': designer service returned an HTML response during verification; confirmed accessible at runtime.", options.schemaName))
	return nil
}

// ---------------------------------------------------------------------------------------------- tools

// CreateEntitySchema is CreateEntitySchemaTool.CreateEntitySchema after enrichment: the options are built,
// the command runs, and a failure to build them is an exit-1 envelope.
func (c *Client) CreateEntitySchema(ctx context.Context, args SchemaWriteEntCreateArgs, dataForge *SchemaWriteEntDataForge) SchemaWriteEntResult {
	options, err := schemaWriteEntBuildCreateOptions(args, args.ParentSchemaName, args.ExtendParent, args.IsVirtual, args.IsDBView)
	if err != nil {
		return SchemaWriteEntFailure(schemaWriteEntRedact(err.Error()), dataForge)
	}
	log := &schemaWriteEntLog{}
	run := c.schemaWriteEntNewRun(ctx, log)
	exitCode := run.executeCreate(options)
	return SchemaWriteEntResult{ExitCode: exitCode, Messages: log.snapshot(), DataForge: dataForge}
}

// SchemaWriteEntLookupShadowError is ModelingGuardrails.EnsureLookupColumnsDoNotShadowInheritedBaseLookupColumns.
func SchemaWriteEntLookupShadowError(columns []SchemaWriteEntColumnArgs) string {
	seen := map[string]string{}
	for _, column := range columns {
		name := strings.TrimSpace(column.resolveName())
		if strings.EqualFold(name, "Name") || strings.EqualFold(name, "Description") {
			if _, exists := seen[strings.ToLower(name)]; !exists {
				seen[strings.ToLower(name)] = name
			}
		}
	}
	if len(seen) == 0 {
		return ""
	}
	var names []string
	for _, key := range []string{"description", "name"} {
		if name, ok := seen[key]; ok {
			names = append(names, name)
		}
	}
	return "create-lookup inherits BaseLookup columns. Do not add inherited columns: " + strings.Join(names, ", ") + "."
}

// CreateLookup is CreateLookupTool.CreateLookup after enrichment: the guardrail, the create under BaseLookup
// and, when it succeeded, the lookup catalog registration.
func (c *Client) CreateLookup(ctx context.Context, args SchemaWriteEntCreateArgs, dataForge *SchemaWriteEntDataForge) SchemaWriteEntResult {
	if message := SchemaWriteEntLookupShadowError(args.Columns); message != "" {
		return SchemaWriteEntFailure(message, dataForge)
	}
	options, err := schemaWriteEntBuildCreateOptions(args, schemaWriteEntBaseLookup, false, false, nil)
	if err != nil {
		return SchemaWriteEntFailure(schemaWriteEntRedact(err.Error()), dataForge)
	}
	title, err := schemaWriteEntDefaultTitle(options.titleLocalizations, fmt.Sprintf("Lookup '%s'", args.SchemaName))
	if err != nil {
		return SchemaWriteEntFailure(schemaWriteEntRedact(err.Error()), dataForge)
	}
	log := &schemaWriteEntLog{}
	run := c.schemaWriteEntNewRun(ctx, log)
	exitCode := run.executeCreate(options)
	if exitCode == 0 {
		if err := run.ensureLookupRegistration(args.PackageName, args.SchemaName, title); err != nil {
			messages := append(log.snapshot(), LogMessage{MessageType: "Error", Value: schemaWriteEntRedact(err.Error())})
			return SchemaWriteEntResult{ExitCode: 1, Messages: messages, DataForge: dataForge}
		}
	}
	return SchemaWriteEntResult{ExitCode: exitCode, Messages: log.snapshot(), DataForge: dataForge}
}

// ---------------------------------------------------------------------------------------------- lookup catalog

type schemaWriteEntRuntimeColumn struct {
	UID           schemaWriteEntGUID `json:"uId"`
	Name          string             `json:"name"`
	DataValueType int                `json:"dataValueType"`
}

type schemaWriteEntRuntimeSchema struct {
	UID              schemaWriteEntGUID `json:"uId"`
	Name             string             `json:"name"`
	PrimaryColumnUID schemaWriteEntGUID `json:"primaryColumnUId"`
	Columns          struct {
		Items map[string]schemaWriteEntRuntimeColumn `json:"items"`
	} `json:"columns"`
}

// fetchBindingSchema is DataBindingSchemaClient.Fetch over RuntimeEntitySchemaReader.GetByName.
func (r *schemaWriteEntRun) fetchBindingSchema(schemaName string) (schemaWriteEntRuntimeSchema, []schemaWriteEntRuntimeColumn, error) {
	body, _ := json.Marshal(map[string]string{"Name": schemaName})
	payload, err := r.client.postDataServiceJSON(r.ctx, "RuntimeEntitySchemaRequest", body, schemaWriteEntTimeout, maxResponseBytes)
	if err != nil {
		return schemaWriteEntRuntimeSchema{}, nil, err
	}
	var response struct {
		Success   bool                         `json:"success"`
		Schema    *schemaWriteEntRuntimeSchema `json:"schema"`
		ErrorInfo *schemaWriteEntErrorInfo     `json:"errorInfo"`
	}
	if err := json.Unmarshal(payload, &response); err != nil {
		return schemaWriteEntRuntimeSchema{}, nil, err
	}
	if !response.Success || response.Schema == nil {
		if response.ErrorInfo != nil && response.ErrorInfo.Message != nil {
			return schemaWriteEntRuntimeSchema{}, nil, schemaWriteEntError(*response.ErrorInfo.Message)
		}
		return schemaWriteEntRuntimeSchema{}, nil, fmt.Errorf("Runtime schema '%s' was not returned by Creatio.", schemaName)
	}
	columns := make([]schemaWriteEntRuntimeColumn, 0, len(response.Schema.Columns.Items))
	for _, column := range response.Schema.Columns.Items {
		columns = append(columns, column)
	}
	sortStrings := func(i, j int) bool { return compareOrdinalIgnoreCase(columns[i].Name, columns[j].Name) < 0 }
	schemaWriteEntSortColumns(columns, sortStrings)
	if response.Schema.PrimaryColumnUID.isEmpty() {
		return schemaWriteEntRuntimeSchema{}, nil, fmt.Errorf("Schema '%s' does not expose a primary column.", schemaName)
	}
	return *response.Schema, columns, nil
}

// ensureLookupRegistration is LookupRegistrationService.EnsureLookupRegistration.
func (r *schemaWriteEntRun) ensureLookupRegistration(packageName, lookupSchemaName, lookupTitle string) error {
	if strings.TrimSpace(packageName) == "" {
		return schemaWriteEntError("Package name is required for lookup registration.")
	}
	if strings.TrimSpace(lookupSchemaName) == "" {
		return schemaWriteEntError("Lookup schema name is required for lookup registration.")
	}
	title := strings.TrimSpace(lookupTitle)
	if title == "" {
		title = lookupSchemaName
	}
	packageRef, err := r.bindingPackage(packageName)
	if err != nil {
		return err
	}
	lookupSchema, lookupColumns, err := r.fetchBindingSchema("Lookup")
	if err != nil {
		return err
	}
	var bindingColumns []schemaWriteEntRuntimeColumn
	for _, column := range lookupColumns {
		if !strings.EqualFold(column.Name, "CreatedBy") && !strings.EqualFold(column.Name, "ModifiedBy") {
			bindingColumns = append(bindingColumns, column)
		}
	}
	registered, _, err := r.fetchBindingSchema(lookupSchemaName)
	if err != nil {
		return err
	}
	rowID, err := r.ensureLookupRow(bindingColumns, registered.UID, title)
	if err != nil {
		return err
	}
	bindingName := "Lookup_" + lookupSchemaName
	existingUID, existingSchema, found, err := r.findBinding(packageRef.UID, bindingName)
	if err != nil {
		return err
	}
	if found && !strings.EqualFold(existingSchema, "Lookup") {
		if strings.TrimSpace(existingSchema) == "" {
			return fmt.Errorf("Package schema data '%s' already exists, but the environment did not report which entity schema it delivers, so it cannot be confirmed as the Lookup binding.", bindingName)
		}
		return fmt.Errorf("Package schema data '%s' already exists for schema '%s'.", bindingName, existingSchema)
	}
	if err := r.saveLookupBinding(packageRef, bindingName, lookupSchema, bindingColumns, rowID, existingUID); err != nil {
		return err
	}
	r.log.info(fmt.Sprintf("Lookup '%s' registered in Lookups.", lookupSchemaName))
	return nil
}

// bindingPackage is PackageDataBindingWriter.ResolvePackage over PackageTargetResolver (not requiring an
// editable package).
func (r *schemaWriteEntRun) bindingPackage(packageName string) (pkgWriteInstalledPackage, error) {
	packages, err := r.client.pkgWriteInstalledPackages(r.ctx)
	if err != nil {
		return pkgWriteInstalledPackage{}, fmt.Errorf("The environment could not be asked which package to deliver the data into: %s", err.Error())
	}
	for _, item := range packages {
		if strings.EqualFold(item.Name, strings.TrimSpace(packageName)) {
			if item.UID == emptyGUID {
				return pkgWriteInstalledPackage{}, fmt.Errorf("Package '%s' has no usable UId in the environment, so package data cannot be addressed to it. Name another package (see list-packages for the available names).", item.Name)
			}
			return item, nil
		}
	}
	return pkgWriteInstalledPackage{}, fmt.Errorf("Package '%s' was not found in the environment. Check the name against list-packages.", strings.TrimSpace(packageName))
}

func schemaWriteEntInsertType(columnName string, columns []schemaWriteEntRuntimeColumn) int {
	dataValueType := 1
	for _, column := range columns {
		if strings.EqualFold(column.Name, columnName) {
			dataValueType = column.DataValueType
			break
		}
	}
	if dataValueType >= 26 && dataValueType <= 30 {
		return 1
	}
	return dataValueType
}

func schemaWriteEntParameter(dataValueType int, value string) orderedFields {
	return orderedFields{{"expressionType", 2}, {"parameter", orderedFields{{"dataValueType", dataValueType}, {"value", value}}}}
}

// ensureLookupRow finds the Lookup row for the schema, inserting it or renaming it as needed.
func (r *schemaWriteEntRun) ensureLookupRow(columns []schemaWriteEntRuntimeColumn, schemaUID schemaWriteEntGUID, title string) (string, error) {
	rows, err := r.client.selectRows(r.ctx, buildSelectQuery("Lookup", map[string]string{"Id": "Id", "Name": "Name"},
		map[string]any{"filter0": comparisonFilter("SysEntitySchemaUId", schemaUID.String(), 0, 3)}, -1))
	if err != nil {
		return "", err
	}
	if len(rows) > 1 {
		return "", fmt.Errorf("Lookup '%s' already has multiple registrations in Lookup.", schemaUID.String())
	}
	if len(rows) == 0 {
		rowID := string(schemaWriteEntNewUID())
		body := toJNode(orderedFields{{"rootSchemaName", "Lookup"}, {"columnValues", orderedFields{{"items", orderedFields{
			{"Id", schemaWriteEntParameter(schemaWriteEntInsertType("Id", columns), rowID)},
			{"Name", schemaWriteEntParameter(schemaWriteEntInsertType("Name", columns), title)},
			{"SysEntitySchemaUId", schemaWriteEntParameter(schemaWriteEntInsertType("SysEntitySchemaUId", columns), schemaUID.String())},
		}}}}}).stjJSON()
		if err := r.dataServiceWrite(schemaWriteEntInsertRoute, "InsertQuery", body); err != nil {
			return "", err
		}
		return rowID, nil
	}
	rowID, ok := parseGUID(rowString(rows[0], "Id"))
	if !ok {
		return "", fmt.Errorf("Lookup registration row for schema '%s' returned an invalid Id.", schemaUID.String())
	}
	if rowString(rows[0], "Name") != title {
		filter := orderedFields{{"filterType", 6}, {"isEnabled", true}, {"trimDateTimeParameterToDate", false}, {"logicalOperation", 0},
			{"items", orderedFields{{"primaryFilter", orderedFields{{"filterType", 1}, {"comparisonType", 3}, {"isEnabled", true},
				{"trimDateTimeParameterToDate", false}, {"leftExpression", orderedFields{{"expressionType", 0}, {"columnPath", "Id"}}},
				{"rightExpression", orderedFields{{"expressionType", 2}, {"parameter", orderedFields{{"dataValueType", 0}, {"value", rowID}}}}}}}}}}
		body := toJNode(orderedFields{{"rootSchemaName", "Lookup"}, {"columnValues", orderedFields{{"items", orderedFields{
			{"Name", schemaWriteEntParameter(schemaWriteEntInsertType("Name", columns), title)}}}}}, {"filters", filter}}).stjJSON()
		if err := r.dataServiceWrite(schemaWriteEntUpdateRoute, "UpdateQuery", body); err != nil {
			return "", err
		}
	}
	return rowID, nil
}

// dataServiceWrite posts a DataService write and applies DataServiceResponse.ThrowIfUnsuccessful.
func (r *schemaWriteEntRun) dataServiceWrite(route, operation string, body []byte) error {
	payload, err := r.client.callService(r.ctx, serviceCall{Route: route, Body: body, Timeout: schemaWriteEntTimeout, Label: operation})
	if err != nil {
		return err
	}
	return schemaWriteEntThrowIfUnsuccessful(payload, operation)
}

// schemaWriteEntThrowIfUnsuccessful is DataServiceResponse.ThrowIfUnsuccessful.
func schemaWriteEntThrowIfUnsuccessful(payload []byte, operation string) error {
	if strings.TrimSpace(string(payload)) == "" {
		return nil
	}
	node, err := parseJNode(payload)
	if err != nil {
		return fmt.Errorf("%s failed: the environment answered with a body that is not a service response (an authentication redirect or an error page), so the request never reached the service.", operation)
	}
	if !node.isObject() {
		return fmt.Errorf("%s failed: the environment answered with a bare JSON value rather than a service response envelope, so the request never reached the service.", operation)
	}
	success := node.get("success")
	if success == nil {
		return nil
	}
	if success.kind == jkBool && success.flag {
		return nil
	}
	if success.kind != jkBool {
		return fmt.Errorf("%s failed: the environment answered with a 'success' flag that is not a true/false value, so whether the write reached the service cannot be determined.", operation)
	}
	message := "Unknown error"
	if errorInfo := node.get("errorInfo"); errorInfo.isObject() && errorInfo.get("message") != nil {
		if text := errorInfo.get("message").stringValue(); text != nil {
			message = *text
		}
	} else if status := node.get("responseStatus"); status.isObject() && status.get("Message") != nil {
		if text := status.get("Message").stringValue(); text != nil {
			message = *text
		}
	}
	guidance := ""
	if strings.Contains(strings.ToLower(message), "does not have permissions for the") {
		guidance = " This is an object-permission refusal, not a bad request: DB-first bindings apply rows through " +
			"the DataService, which enforces object permissions, so a protected system object is refused " +
			"regardless of the authenticated user's administrative rights. Bindings for ordinary schemas are " +
			"unaffected. For record-level access rights use the set-record-rights tool (it goes through the " +
			"native RightsService instead). Object-operation rights (SysEntitySchemaOperationRight) have no " +
			"administration-capable path in clio yet — deploy them through Creatio's own Object permissions " +
			"administration or a package installation script."
	}
	return fmt.Errorf("%s failed: %s%s", operation, message, guidance)
}

// findBinding is PackageDataBindingWriter.FindBinding.
func (r *schemaWriteEntRun) findBinding(packageUID, bindingName string) (string, string, bool, error) {
	rows, err := r.client.selectRows(r.ctx, buildSelectQuery("SysPackageSchemaData",
		map[string]string{"UId": "UId", "EntitySchemaName": "SysSchema.Name"},
		map[string]any{"filter0": comparisonFilter("Name", bindingName, 1, 3), "filter1": comparisonFilter("SysPackage.UId", packageUID, 0, 3)}, -1))
	if err != nil {
		return "", "", false, err
	}
	if len(rows) > 1 {
		return "", "", false, fmt.Errorf("Package data binding '%s' has multiple registrations in package '%s'.", bindingName, packageUID)
	}
	if len(rows) == 0 {
		return "", "", false, nil
	}
	uid, ok := parseGUID(rowString(rows[0], "UId"))
	if !ok || uid == emptyGUID {
		return "", "", false, fmt.Errorf("Package data binding '%s' in package '%s' carries an unusable UId '%s', so a re-save could not update it in place.", bindingName, packageUID, rowString(rows[0], "UId"))
	}
	return uid, rowString(rows[0], "EntitySchemaName"), true, nil
}

// saveLookupBinding is PackageDataBindingWriter.SaveBinding for the Lookup row.
func (r *schemaWriteEntRun) saveLookupBinding(pkg pkgWriteInstalledPackage, bindingName string, schema schemaWriteEntRuntimeSchema,
	columns []schemaWriteEntRuntimeColumn, rowID, existingUID string) error {
	uid := existingUID
	if uid == "" {
		uid = string(schemaWriteEntNewUID())
	}
	var columnItems []any
	hasKey := false
	for _, column := range columns {
		typeUID, ok := schemaWriteEntBindingTypes[column.DataValueType]
		if !ok {
			return fmt.Errorf("Column '%s' uses unsupported runtime dataValueType '%d' for DB-first binding generation.", column.Name, column.DataValueType)
		}
		isKey := strings.EqualFold(column.Name, "Id")
		hasKey = hasKey || isKey
		columnItems = append(columnItems, orderedFields{{"id", string(schemaWriteEntNewUID())}, {"uId", column.UID.String()},
			{"isForceUpdate", false}, {"isKey", isKey}, {"name", column.Name}, {"caption", column.Name}, {"dataValueTypeUId", typeUID}})
	}
	if !hasKey {
		return fmt.Errorf("Binding '%s' would deliver schema 'Lookup' with no key column, so the install target would match every row of the entity instead of the delivered one. Include the Id column in the delivered set, or supply a column policy naming the natural key.", bindingName)
	}
	if columnItems == nil {
		columnItems = []any{}
	}
	body := toJNode(orderedFields{{"uId", uid}, {"name", bindingName}, {"package", orderedFields{{"uId", pkg.UID}, {"name", pkg.Name}}},
		{"entitySchemaUId", schema.UID.String()}, {"entitySchemaName", "Lookup"}, {"installType", 0},
		{"columns", columnItems}, {"boundRecordIds", []any{rowID}}}).stjJSON()
	payload, err := r.client.callService(r.ctx, serviceCall{Route: "ServiceModel/SchemaDataDesignerService.svc/SaveSchema", Body: body,
		Timeout: schemaWriteEntTimeout, Label: "SaveSchema"})
	if err != nil {
		return err
	}
	return schemaWriteEntThrowIfUnsuccessful(payload, "SaveSchema")
}

// schemaWriteEntBindingTypes is DataValueTypeMap.FromRuntimeValueType.
var schemaWriteEntBindingTypes = map[int]string{
	0: "23018567-a13c-4320-8687-fd6f9e3699bd", 1: "8b3f29bb-ea14-4ce5-a5c5-293a929b6ba2",
	4: "6b6b74e2-820d-490e-a017-2b73d4ccf2b0", 5: "57ee4c31-5ec4-45fa-b95d-3a2868aa89a8",
	6: "969093e2-2b4e-463b-883a-3d3b8c61f0cd", 7: "d21e9ef4-c064-4012-b286-fa1a8171da44",
	8: "603d4960-a1a2-45e9-b232-206a54421b01", 9: "04cc757b-8f06-482c-8a1a-0c0e171d2410",
	10: "b295071f-7ea9-4e62-8d1a-919bf3732ff2", 12: "90b65bf8-0ffc-4141-8779-2420877af907",
	18: "dafb71f9-ee9f-4e0b-a4d7-37aa15987155", 13: "fa6e6e49-b996-475e-a77e-73904e4c5a88",
	14: "b039feb0-ee7c-4884-8aa6-d6d45d84316f", 26: "5ca35f10-a101-4c67-a96a-383da6afacfc",
	27: "325a73b8-0f47-44a0-8412-7606f78003ac", 28: "ddb3a1ee-07e8-4d62-b7a9-d0e618b00fbd",
	29: "c0f04627-4620-4bc0-84e5-9419dc8516b1", 30: "5ca35f10-a101-4c67-a96a-383da6afacfc",
	43: "79bccffa-8c8b-4863-b376-a69d2244182b", 31: "3509b9dd-2c90-4540-b82e-8f6ae85d8248",
	32: "ecbcce18-2a17-4ead-829a-9d02fa9578a4", 33: "0eaaa70f-2a5a-444e-bdf1-98b37895c820",
	37: "07ba84ce-0bf7-44b4-9f2c-7b15032eb98c", 38: "5cc8060d-6d10-4773-89fc-8c12d6f659a6",
	39: "3f62414e-6c25-4182-bcef-a73c9e396f31", 40: "ff22e049-4d16-46ee-a529-92d8808932dc",
	41: "a4aaf398-3531-4a0d-9d75-a587f5b5b59e", 42: "26cba63c-daf1-4f36-b2ea-73c0d675d90c",
	44: "26cba64c-daf1-4f36-b2ea-73c0d695d90c", 45: "66cba64c-daf1-4f36-b8ea-73c0d695d90c",
	46: "651ec16f-d140-46db-b9e2-825c985a8ac2",
}
