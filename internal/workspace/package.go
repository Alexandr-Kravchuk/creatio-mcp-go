package workspace

// add-package: clio's AddPackageCommand over PackageCreator (package template, descriptor.json, project
// macros, the as-app localization schema of SchemaBuilder and app-descriptor.json), SchemaNamePrefixResolver
// and the DconfChainItem follow-up that downloads the configuration into a fresh workspace.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
)

const (
	invalidPackageNameMessage = "Package name must start with a letter or underscore and contain only letters, digits, and underscores. " +
		"Its length must be 1 to 70 characters, and a lone underscore is not valid."
	invalidPrefixMessage = "Schema name prefix must start with a letter or underscore and contain only letters, digits, " +
		"and underscores, because it becomes part of a generated C# class name."
	prefixConsequence = "Creatio refuses a schema whose code does not start with the target environment's SchemaNamePrefix: " +
		"the source-code schema designer rejects it (measured), and that is also what blocks the " +
		"file-system package-load path issue #1309 reported."
	supplyPrefixMessage = "Supply the prefix directly instead: --schema-name-prefix <prefix> on the command line, " +
		"schema-name-prefix in MCP. That path makes no Creatio request."
	maxPackageNameLength = 70
	maxSchemaNameLength  = 250
)

var identifierPattern = regexp.MustCompile(`\A[A-Za-z_][A-Za-z0-9_]*\z`)

// isIdentifierFragment is ClioIdentifier.IsIdentifierFragment.
func isIdentifierFragment(value string) bool { return value != "" && identifierPattern.MatchString(value) }

// validPackageName is PackageCreator.IsValidPackageName.
func validPackageName(name string) bool {
	return strings.TrimSpace(name) != "" && len([]rune(name)) <= maxPackageNameLength && name != "_" && isIdentifierFragment(name)
}

// validRequestedPrefix is SchemaNamePrefixResolver.IsValidRequestedPrefix.
func validRequestedPrefix(prefix *string) bool {
	if prefix == nil {
		return true
	}
	if *prefix != "" && strings.TrimSpace(*prefix) == "" {
		return false
	}
	trimmed := strings.TrimSpace(*prefix)
	return trimmed == "" || isIdentifierFragment(trimmed)
}

// PrefixReadFailure is SysSettingCodes.ClassifyReadFailure's category of a failed SchemaNamePrefix read.
type PrefixReadFailure int

const (
	PrefixReadOther PrefixReadFailure = iota
	PrefixReadNetwork
	PrefixReadAuthentication
	PrefixReadTimeout
)

// AddPackageRequest is AddPackageArgs plus what the command takes from the resolved environment.
type AddPackageRequest struct {
	Name             string
	WorkspacePath    string
	AsApp            bool
	SchemaNamePrefix *string
	BuildZipPath     *string
	EnvironmentName  *string
	// Maintainer is EnvironmentSettings.Maintainer (nil: "Customer").
	Maintainer *string
	// EnvironmentURI is EnvironmentSettings.Uri; blank means no environment was resolved.
	EnvironmentURI string
	// ReadPrefix reads the environment's SchemaNamePrefix (already trimmed) within clio's 30 s budget.
	ReadPrefix func(ctx context.Context) (string, PrefixReadFailure, error)
	// DownloadConfiguration is DownloadConfigurationCommand for one environment name or build path.
	DownloadConfiguration func(ctx context.Context, root, value string, isEnvironment bool) creatio.CommandResult
	// WorkingDirectory is the process directory, used when WorkspacePath is blank.
	WorkingDirectory string
}

// commandException is an exception clio's command lets escape: the tool answers exit code -1 with
// "[<type>] <message>" after the messages already logged.
type commandException struct {
	kind    string
	message string
}

func (e commandException) Error() string { return e.message }

func invalidOperation(format string, args ...any) error {
	return commandException{kind: "InvalidOperationException", message: fmt.Sprintf(format, args...)}
}

func argumentException(message, parameter string) error {
	return commandException{kind: "ArgumentException", message: message + " (Parameter '" + parameter + "')"}
}

// exceptionResult is CommandExecutionResult.FromException over the log so far.
func exceptionResult(log *Log, err error) creatio.CommandResult {
	kind := "Exception"
	if typed, ok := err.(commandException); ok {
		kind = typed.kind
	} else if _, ok := err.(*os.PathError); ok {
		kind = "IOException"
	}
	log.Error("[" + kind + "] " + err.Error())
	return log.Result(-1)
}

// AddPackage runs add-package.
func AddPackage(ctx context.Context, request AddPackageRequest) creatio.CommandResult {
	log := &Log{}
	if !validPackageName(request.Name) {
		log.Error(invalidPackageNameMessage)
		return log.Result(1)
	}
	if !validRequestedPrefix(request.SchemaNamePrefix) {
		log.Error(invalidPrefixMessage)
		return log.Result(1)
	}
	if request.SchemaNamePrefix != nil && !request.AsApp {
		log.Warning("--schema-name-prefix has no effect without --as-app: no schema is generated for a " +
			"package that is not an application package.")
	}
	current := request.WorkingDirectory
	if strings.TrimSpace(request.WorkspacePath) != "" {
		current = request.WorkspacePath
	}
	layout := Layout{Root: FindRoot(current)}
	packagesPath := current
	if layout.IsWorkspace() {
		packagesPath = layout.PackagesFolder()
	}
	creator := packageCreator{ctx: ctx, log: log, layout: layout, request: request}
	if err := creator.create(packagesPath); err != nil {
		return exceptionResult(log, err)
	}
	log.Info("Done")
	return addPackageFollowUp(ctx, log, layout, request)
}

// addPackageFollowUp is the FollowUpChain with DconfChainItem: with an environment name or a build path,
// and a workspace whose .application/net-core or net-framework folder is missing or empty, the
// configuration is downloaded once per comma-separated value; the exit codes add up.
func addPackageFollowUp(ctx context.Context, log *Log, layout Layout, request AddPackageRequest) creatio.CommandResult {
	if request.EnvironmentName == nil && request.BuildZipPath == nil {
		return log.Result(0)
	}
	empty := func(folder string) bool { return !directoryExists(folder) || isEmptyDirectory(folder) }
	if !empty(filepath.Join(layout.Root, ".application", "net-core")) && !empty(filepath.Join(layout.Root, ".application", "net-framework")) {
		return log.Result(0)
	}
	value, isEnvironment := "", true
	if request.EnvironmentName != nil {
		value = *request.EnvironmentName
	} else {
		value, isEnvironment = *request.BuildZipPath, false
	}
	if strings.TrimSpace(value) == "" {
		return log.Result(0)
	}
	total := 0
	for _, item := range strings.Split(value, ",") {
		result := request.DownloadConfiguration(ctx, layout.Root, item, isEnvironment)
		log.Append(result.Messages)
		total += result.ExitCode
	}
	return log.Result(total)
}

type packageCreator struct {
	ctx     context.Context
	log     *Log
	layout  Layout
	request AddPackageRequest
}

func (p packageCreator) maintainer() string {
	if p.request.Maintainer != nil {
		return *p.request.Maintainer
	}
	return "Customer"
}

// create is PackageCreator.Create(packagesPath, name, asApp, prefix) / CreateCore.
func (p packageCreator) create(packagesPath string) error {
	name := p.request.Name
	if !validPackageName(name) {
		return argumentException(invalidPackageNameMessage, "packageName")
	}
	packagePath := combinePath(packagesPath, name)
	if directoryExists(packagePath) {
		return invalidOperation("Directory '%s' already exists", packagePath)
	}
	schemaName := ""
	if p.request.AsApp {
		prefix, err := p.resolvePrefix()
		if err != nil {
			return err
		}
		schemaName = name + "LocalizableStrings"
		if prefix != "" && !strings.HasPrefix(schemaName, prefix) {
			schemaName = prefix + schemaName
		}
		if length := len([]rune(schemaName)); length > maxSchemaNameLength {
			return commandException{kind: "ArgumentException", message: fmt.Sprintf("Generated schema name '%s' is %d characters long; Creatio "+
				"stores a schema code in SysSchema.Name and accepts at most %d. "+
				"Shorten the schema-name prefix or the package name.", schemaName, length, maxSchemaNameLength)}
		}
	}
	if err := p.writeSkeleton(packagesPath, packagePath); err != nil {
		return err
	}
	if p.request.AsApp {
		if err := p.addLocalizationSchema(packagePath, schemaName); err != nil {
			return err
		}
	}
	if err := p.addToWorkspace(); err != nil {
		return err
	}
	if p.request.AsApp {
		if err := p.addAppDescriptor(packagesPath, packagePath); err != nil {
			return err
		}
	} else if err := p.updateAppDescriptor(packagesPath, packagePath); err != nil {
		return err
	}
	if p.layout.IsWorkspace() {
		return p.layout.createWorkspaceSolution(p.log)
	}
	return nil
}

// resolvePrefix is SchemaNamePrefixResolver.Resolve.
func (p packageCreator) resolvePrefix() (string, error) {
	if explicit := p.request.SchemaNamePrefix; explicit != nil {
		if !validRequestedPrefix(explicit) {
			return "", argumentException(invalidPrefixMessage, "explicitPrefix")
		}
		requested := strings.TrimSpace(*explicit)
		if requested == "" {
			p.log.Warning("An explicitly empty schema-name prefix was requested, so the generated schema gets no " +
				"prefix and no environment is contacted. " + prefixConsequence + " Omit the argument " +
				"instead to read the prefix from the target environment.")
		}
		return requested, nil
	}
	uri := p.request.EnvironmentURI
	if strings.TrimSpace(uri) == "" || p.request.ReadPrefix == nil {
		p.log.Warning("No Creatio environment was resolved, so the generated schema gets no schema-name prefix. " +
			"An environment name that is not registered, and a comma-separated list of environments, " +
			"both resolve none. " + prefixConsequence + " " + supplyPrefixMessage)
		return "", nil
	}
	p.log.Info("Reading the SchemaNamePrefix system setting from " + uri + "...")
	prefix, failure, err := p.request.ReadPrefix(p.ctx)
	if err != nil {
		if p.ctx.Err() != nil && failure != PrefixReadTimeout {
			return "", p.ctx.Err()
		}
		if failure == PrefixReadTimeout {
			p.log.Warning("Timed out reading the SchemaNamePrefix system setting from " + uri + " after 30 seconds. The " +
				"generated schema gets no prefix. " + prefixConsequence + " " + supplyPrefixMessage)
			return "", nil
		}
		description := "Failed to read the SchemaNamePrefix system setting"
		switch failure {
		case PrefixReadNetwork:
			description = "Network error reading the SchemaNamePrefix system setting"
		case PrefixReadAuthentication:
			description = "Authentication error reading the SchemaNamePrefix system setting"
		}
		p.log.Warning(description + " from " + uri + ". The generated schema gets no prefix. " + prefixConsequence + " " + supplyPrefixMessage)
		return "", nil
	}
	if prefix == "" {
		p.log.Info(uri + " configures an empty SchemaNamePrefix, so the generated schema gets no prefix.")
		return "", nil
	}
	if !isIdentifierFragment(prefix) {
		p.log.Warning("The SchemaNamePrefix value '" + prefix + "' read from " + uri + " is not usable " +
			"in a generated C# class name. " + invalidPrefixMessage + " The generated schema gets no prefix. " + supplyPrefixMessage)
		return "", nil
	}
	p.log.Info("Using schema-name prefix '" + prefix + "', read from the SchemaNamePrefix system setting of " + uri + ".")
	return prefix, nil
}

// writeSkeleton is WritePackageSkeleton: the package template (and package-localization for as-app),
// descriptor.json, then the project macros and renames of CreatePackageProj.
func (p packageCreator) writeSkeleton(packagesPath, packagePath string) error {
	name := p.request.Name
	if err := copyTemplateFolder("package", packagePath, true); err != nil {
		return err
	}
	if p.request.AsApp {
		if err := copyTemplateFolder("package-localization", packagePath, false); err != nil {
			return err
		}
	}
	descriptor := indentedJSON(object(member("Descriptor", object(
		member("UId", newGUID()),
		member("PackageVersion", "0.1.0"),
		member("Name", name),
		member("Type", 1),
		member("ProjectPath", "Files/"+name+".csproj"),
		member("ModifiedOnUtc", modifiedOnUtc(now())),
		member("Maintainer", p.maintainer()),
		member("InstallBehavior", 1),
		member("DependsOn", jsonArray{object(
			member("UId", "3a71e376-9ac3-3049-c62b-9c43b9abe054"),
			member("PackageVersion", "7.8.0"),
			member("Name", "CrtCoreBase"),
			member("Type", 1),
		)}),
	))), newtonsoftStyle)
	if err := writeAllText(filepath.Join(packagePath, "descriptor.json"), descriptor); err != nil {
		return err
	}
	files := filepath.Join(packagesPath, name, "Files")
	rootNamespace := name + "App"
	macros := func(path string, extra [][2]string) error {
		content, err := readAllText(path)
		if err != nil {
			return err
		}
		return writeAllText(path, replaceMacros(content, append([][2]string{{"#PackageName#", name}, {"#RootNameSpace#", rootNamespace}}, extra...)))
	}
	if err := macros(filepath.Join(files, "Directory.Build.targets"), nil); err != nil {
		return err
	}
	project := filepath.Join(files, name+".csproj")
	if err := moveFile(filepath.Join(files, "PackageName.csproj"), project); err != nil {
		return err
	}
	services := ""
	if p.request.AsApp {
		services = "serviceCollection.AddTransient<LocalizableStrings.ILocalizableStringResolver, " +
			"LocalizableStrings.LocalizableStringResolver>();"
	}
	sources, err := filesWithExtension(files, ".cs")
	if err != nil {
		return err
	}
	for _, source := range sources {
		if err := macros(source, [][2]string{{"#LocalizationServices#", services}}); err != nil {
			return err
		}
	}
	if err := macros(project, nil); err != nil {
		return err
	}
	return moveFile(filepath.Join(files, "src", "cs", "MainApp.cs"), filepath.Join(files, "src", "cs", rootNamespace+".cs"))
}

// moveFile is File.Move, which refuses an existing target.
func moveFile(from, to string) error {
	if fileExists(to) {
		return commandException{kind: "IOException", message: fmt.Sprintf("The file '%s' already exists.", to)}
	}
	return os.Rename(from, to)
}

// filesWithExtension is Directory.GetFiles(path, "*<ext>", AllDirectories).
func filesWithExtension(folder, extension string) ([]string, error) {
	var found []string
	err := filepath.WalkDir(folder, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.HasSuffix(strings.ToLower(entry.Name()), extension) {
			found = append(found, path)
		}
		return nil
	})
	return found, err
}

// packageDescriptor is what PackageInfoProvider reads from descriptor.json.
type packageDescriptor struct {
	Descriptor struct {
		UId        string `json:"UId"`
		Name       string `json:"Name"`
		Maintainer string `json:"Maintainer"`
	} `json:"Descriptor"`
}

func readPackageDescriptor(packagePath string) (packageDescriptor, error) {
	var descriptor packageDescriptor
	text, err := readAllText(filepath.Join(packagePath, "descriptor.json"))
	if err != nil {
		return descriptor, err
	}
	return descriptor, json.Unmarshal([]byte(text), &descriptor)
}

// addLocalizationSchema is SchemaBuilder.AddSchema("source-code", ...) with the localization options.
func (p packageCreator) addLocalizationSchema(packagePath, schemaName string) error {
	resources := filepath.Join(packagePath, "Resources", schemaName+".SourceCode")
	schema := filepath.Join(packagePath, "Schemas", schemaName)
	for _, folder := range []string{resources, schema} {
		if directoryExists(folder) {
			return commandException{kind: "ArgumentException", message: "Directory " + folder + " already exists"}
		}
		if err := os.MkdirAll(folder, 0o777); err != nil {
			return err
		}
	}
	descriptor, err := readPackageDescriptor(packagePath)
	if err != nil {
		return err
	}
	maintainer := descriptor.Descriptor.Maintainer
	if maintainer == "" {
		maintainer = "Customer"
	}
	documentation := "Owns package-level backend localizable values that have no more natural schema owner. " +
		"Page and other schema resources stay with the schema that renders or consumes them."
	nl := newLine()
	classDocumentation := "\t/// <summary>" + nl + "\t/// " + securityEscape(documentation) + nl + "\t/// </summary>" + nl
	resourceItems := "\t\t\t<Item Name=\"" + securityEscape("LocalizableStrings.PackageLevelExample.Value") + "\" " +
		"Value=\"" + securityEscape("Package-level localizable value") + "\" />"
	macros := [][2]string{
		{"[SCHEMA_NAME]", schemaName},
		{"[MAINTAINER]", maintainer},
		{"[PACKAGE_NAME]", descriptor.Descriptor.Name},
		{"[SCHEMA_UID]", newGUID()},
		{"[DATETIME_NOW_TICK]", modifiedOnUtc(now().UTC())},
		{"[PACKAGE_UID]", descriptor.Descriptor.UId},
		{"[NAMESPACE]", p.request.Name + "App"},
		{"[CLASS_DOCUMENTATION]", classDocumentation},
		{"[RESOURCE_ITEMS]", resourceItems},
	}
	if err := copyTemplateFolderWithMacros("schemas-template/source-code/Resources", resources, macros); err != nil {
		return err
	}
	return copyTemplateFolderWithMacros("schemas-template/source-code/Schema", schema, macros)
}

// securityEscape is System.Security.SecurityElement.Escape.
func securityEscape(text string) string {
	return strings.NewReplacer("<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;", "&", "&amp;").Replace(text)
}

// addToWorkspace is AddPackageToWorkspaceIfNeeded.
func (p packageCreator) addToWorkspace() error {
	if !p.layout.IsWorkspace() {
		return nil
	}
	settings, err := p.layout.ReadSettings()
	if err != nil {
		return err
	}
	for _, existing := range settings.Packages {
		if existing == p.request.Name {
			return nil
		}
	}
	settings.Packages = append(settings.Packages, p.request.Name)
	if err := p.layout.WriteSettings(settings); err != nil {
		return err
	}
	return p.layout.createWorkspaceSolution(p.log)
}

type appPackage struct {
	UId  string `json:"UId"`
	Name string `json:"Name"`
}

// appDescriptor is AppDescriptorJson, written by System.Text.Json in this member order.
type appDescriptor struct {
	Name, Description, Maintainer, Icon, IconName, Color, Version, RequiredPlatformVersion string
	MarketplaceLink, HelpLink, OrderLink, SupportEmail, Code                             string
	Packages                                                                             []appPackage
}

func (a appDescriptor) json() string {
	packages := jsonArray{}
	for _, item := range a.Packages {
		packages = append(packages, object(member("UId", item.UId), member("Name", item.Name)))
	}
	return indentedJSON(object(
		member("Name", a.Name), member("Description", a.Description), member("Maintainer", a.Maintainer),
		member("Icon", a.Icon), member("IconName", a.IconName), member("Color", a.Color), member("Version", a.Version),
		member("RequiredPlatformVersion", a.RequiredPlatformVersion), member("MarketplaceLink", a.MarketplaceLink),
		member("HelpLink", a.HelpLink), member("OrderLink", a.OrderLink), member("SupportEmail", a.SupportEmail),
		member("Code", a.Code), member("Packages", packages),
	), systemTextJSONStyle)
}

func (p packageCreator) packageEntry(packagePath string) (appPackage, error) {
	descriptor, err := readPackageDescriptor(packagePath)
	if err != nil {
		return appPackage{}, err
	}
	return appPackage{UId: strings.ToLower(descriptor.Descriptor.UId), Name: descriptor.Descriptor.Name}, nil
}

// addAppDescriptor is AddAppDescriptor: Files/app-descriptor.json naming the new package.
func (p packageCreator) addAppDescriptor(packagesPath, packagePath string) error {
	entry, err := p.packageEntry(packagePath)
	if err != nil {
		return err
	}
	descriptor := appDescriptor{Name: p.request.Name, Maintainer: p.maintainer(), Color: "#FFAC07", Version: "0.1.0",
		RequiredPlatformVersion: "8.3.3", Code: p.request.Name, Packages: []appPackage{entry}}
	return writeAllText(filepath.Join(packagesPath, p.request.Name, "Files", "app-descriptor.json"), descriptor.json())
}

// updateAppDescriptor is UpdateAppDescriptorIfExists: when exactly one app-descriptor.json lies below the
// packages folder, the package replaces any entry of the same name at the end of its list.
func (p packageCreator) updateAppDescriptor(packagesPath, packagePath string) error {
	var found []string
	err := filepath.WalkDir(packagesPath, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.EqualFold(entry.Name(), "app-descriptor.json") {
			found = append(found, path)
		}
		return nil
	})
	if err != nil || len(found) != 1 {
		return err
	}
	text, err := readAllText(found[0])
	if err != nil {
		return err
	}
	descriptor := appDescriptor{RequiredPlatformVersion: "8.3.3"}
	if err := json.Unmarshal([]byte(text), &descriptor); err != nil {
		return err
	}
	kept := descriptor.Packages[:0]
	for _, item := range descriptor.Packages {
		if item.Name != p.request.Name {
			kept = append(kept, item)
		}
	}
	entry, err := p.packageEntry(packagePath)
	if err != nil {
		return err
	}
	descriptor.Packages = append(kept, entry)
	return writeAllText(found[0], descriptor.json())
}
