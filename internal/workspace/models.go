package workspace

// add-item-model: clio 8.1.0.142's AddItemModelTool (folder checks, output compaction) over AddItemCommand with
// CreateAll and ModelBuilder.GetModels, which writes one ATF.Repository model class per entity schema and
// BaseModelExtensions.cs. The schema reads are internal/creatio's EntityModelSchemas.

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
)

// ModelSource reads the entity schemas a model generation needs.
type ModelSource interface {
	EntityModelSchemas(ctx context.Context, culture string) ([]*creatio.ModelSchema, []creatio.LogMessage, error)
}

const (
	modelDefaultNamespace = "Models"
	// modelCulture is AddItemOptions.Culture's default; the MCP tool does not set it.
	modelCulture                  = "en-US"
	modelExtensionsFileName       = "BaseModelExtensions.cs"
	modelRequiredErrorMessage     = "The {0} field is required."
	modelMinLengthErrorMessage    = "The {0} field must be a minimum of {1} character long."
	modelMaxLengthErrorMessage    = "The {0} field must be a maximum of {1} character long."
	modelInvalidEmailErrorMessage = "The {0} field is not a valid e-mail address."
)

// ValidateModelFolder is AddItemModelToolPathValidator.ValidateFolder: the refusal text, or "".
func ValidateModelFolder(folder string) string {
	if strings.TrimSpace(folder) == "" {
		return "Folder is required."
	}
	if strings.HasPrefix(folder, `\\`) || strings.HasPrefix(folder, "//") {
		return "Folder path must be a local absolute path: " + folder
	}
	if !IsFullyQualified(folder) {
		return "Folder path must be absolute: " + folder
	}
	return ""
}

// PrepareModelFolder is the tool's GetFullPath and CreateDirectory: the full path, or the refusal text.
func PrepareModelFolder(folder string) (string, string) {
	full := fullPath(folder)
	if fileExists(full) {
		// Directory.CreateDirectory over a file.
		return full, "Failed to create output folder '" + full + "': The file '" + full + "' already exists."
	}
	if err := os.MkdirAll(full, 0o777); err != nil {
		return full, "Failed to create output folder '" + full + "': " + directoryFailureText(full, err)
	}
	return full, ""
}

// directoryFailureText is the IOException Directory.CreateDirectory raises on Unix.
func directoryFailureText(path string, err error) string {
	switch {
	case errors.Is(err, syscall.ENOTDIR):
		return "Could not find a part of the path '" + path + "'."
	case errors.Is(err, fs.ErrExist), errors.Is(err, syscall.EEXIST):
		return "The file '" + path + "' already exists."
	case errors.Is(err, fs.ErrPermission):
		return "Access to the path '" + path + "' is denied."
	}
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Err.Error()
	}
	return err.Error()
}

// GenerateModels is AddItemCommand.Execute (item type model, all models) followed by the tool's
// AddItemModelToolOutputCompactor: the namespace check, the schema reads, one file per schema, the extension
// class, and the summary line.
func GenerateModels(ctx context.Context, source ModelSource, namespace, destination string) creatio.CommandResult {
	log := &Log{}
	if strings.TrimSpace(namespace) == "" {
		log.Error("Namespace is required for model generation.")
		return log.Result(1)
	}
	log.Info("Generating models...")
	schemas, lines, err := source.EntityModelSchemas(ctx, modelCulture)
	log.Append(lines)
	if err != nil {
		log.Error(err.Error())
		return log.Result(1)
	}
	log.Line("Models will be generated in directory: " + destination)
	if err := os.MkdirAll(destination, 0o777); err != nil {
		log.Error(directoryFailureText(destination, err))
		return log.Result(1)
	}
	writer := newModelWriter(namespace, schemas)
	for _, schema := range schemas {
		if err := writeAllText(combinePath(destination, schema.Name+".cs"), writer.classText(schema)); err != nil {
			log.Error(fileFailureText(err))
			return log.Result(1)
		}
	}
	if err := writeAllText(combinePath(destination, modelExtensionsFileName), modelExtensionsText(namespace)); err != nil {
		log.Error(fileFailureText(err))
		return log.Result(1)
	}
	count := len(schemas)
	if count == 0 {
		// No progress line was written: the compactor counts the model files in the folder.
		count = countModelFiles(destination)
	}
	log.Info("Generated " + strconv.Itoa(count) + " models; requested filter: none.")
	return log.Result(0)
}

func fileFailureText(err error) string {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		switch {
		case errors.Is(err, fs.ErrPermission):
			return "Access to the path '" + pathErr.Path + "' is denied."
		case errors.Is(err, syscall.ENOENT), errors.Is(err, syscall.ENOTDIR):
			return "Could not find a part of the path '" + pathErr.Path + "'."
		}
	}
	return err.Error()
}

var modelFilePattern = regexp.MustCompile(`(?i)\.cs$`)

// countModelFiles is CountGeneratedModels: the *.cs files directly in the folder, BaseModelExtensions.cs aside.
func countModelFiles(folder string) int {
	entries, err := os.ReadDir(folder)
	if err != nil {
		return 0
	}
	count := 0
	for _, entry := range entries {
		if entry.IsDir() || !modelFilePattern.MatchString(entry.Name()) || strings.EqualFold(entry.Name(), modelExtensionsFileName) {
			continue
		}
		count++
	}
	return count
}

// ------------------------------------------------------------------------------------------- the writer

// modelDetail is DetailConnection.
type modelDetail struct {
	schema, property, name string
}

type modelWriter struct {
	namespace string
	details   map[string][]modelDetail
}

// newModelWriter is BuildDetailConnections: every lookup of every schema to a schema in the list becomes a
// detail collection of the master, once per (detail schema, column).
func newModelWriter(namespace string, schemas []*creatio.ModelSchema) *modelWriter {
	writer := &modelWriter{namespace: namespace, details: map[string][]modelDetail{}}
	known := map[string]bool{}
	for _, schema := range schemas {
		known[schema.Name] = true
	}
	for _, detail := range schemas {
		for _, column := range detail.Columns {
			if column.ReferenceSchemaName == nil || strings.TrimSpace(*column.ReferenceSchemaName) == "" || column.Name == "Id" {
				continue
			}
			master := *column.ReferenceSchemaName
			if !known[master] {
				continue
			}
			connection := modelDetail{schema: detail.Name, property: column.Name + "Id", name: "CollectionOf" + detail.Name + "By" + column.Name}
			duplicate := false
			for _, existing := range writer.details[master] {
				if existing.schema == connection.schema && existing.property == connection.property {
					duplicate = true
					break
				}
			}
			if !duplicate {
				writer.details[master] = append(writer.details[master], connection)
			}
		}
	}
	return writer
}

// sortedDetails is GetDetailConnections: ordered by the collection's name with the current culture's comparer
// (OrderBy, a stable sort).
func (w *modelWriter) sortedDetails(schema string) []modelDetail {
	details := append([]modelDetail(nil), w.details[schema]...)
	sort.SliceStable(details, func(i, j int) bool { return cultureLess(details[i].name, details[j].name) })
	return details
}

// modelType is GetTypeFromDataValueType.
func modelType(dataValueType int) string {
	switch dataValueType {
	case 0, 10, 11:
		return "Guid"
	case 4:
		return "int"
	case 5:
		return "Single"
	case 6, 31, 32, 33, 34, 40:
		return "decimal"
	case 7, 8, 9:
		return "DateTime"
	case 12:
		return "bool"
	case 13:
		return "byte[]"
	case 23, 24:
		return "String"
	}
	return "string"
}

// classText is CreateClassFileText.
func (w *modelWriter) classText(schema *creatio.ModelSchema) string {
	nl := newLine()
	var b strings.Builder
	line := func(parts ...string) {
		for _, part := range parts {
			b.WriteString(part)
		}
		b.WriteString(nl)
	}
	namespace := w.namespace
	if strings.TrimSpace(namespace) == "" {
		namespace = modelDefaultNamespace
	}
	line("#pragma warning disable CS8618, // Non-nullable field is uninitialized.")
	line()
	line("using ATF.Repository;")
	line("using ATF.Repository.Attributes;")
	line("using System;")
	line("using System.Collections.Generic;")
	line("using System.Diagnostics.CodeAnalysis;")
	line("using System.ComponentModel.DataAnnotations;")
	line()
	line("namespace ", namespace, " {")
	if schema.Description != nil && *schema.Description != "" {
		line("\t/// <summary>")
		for _, text := range strings.Split(*schema.Description, "\n") {
			line("\t/// ", text)
		}
		line("\t/// </summary>")
	}
	line("\t[ExcludeFromCodeCoverage]")
	line("\t[Schema(\"", schema.Name, "\")]")
	line("\tpublic class ", schema.Name, ": BaseModel {")
	line()
	line("\t\t#region Schema Columns")
	line()
	for _, column := range schema.Columns {
		if column.Name == "Id" {
			continue
		}
		described := column.Description != nil && *column.Description != ""
		if described {
			line("\t\t/// <summary>")
			line("\t\t/// ", *column.Description)
			line("\t\t/// </summary>")
		}
		if column.DataValueType == 13 {
			// byte[] properties are not supported by ATF.Repository.
			continue
		}
		if column.IsRequired {
			line("\t\t[Required(ErrorMessage = \"", modelRequiredErrorMessage, "\")]")
			if column.DataValueType == 27 || column.DataValueType == 28 || column.DataValueType == 29 {
				line("\t\t[MinLength(1, ErrorMessage = \"", modelMinLengthErrorMessage, "\")]")
			}
		}
		switch column.DataValueType {
		case 27:
			line("\t\t[MaxLength(50, ErrorMessage = \"", modelMaxLengthErrorMessage, "\")]")
		case 28:
			line("\t\t[MaxLength(250, ErrorMessage = \"", modelMaxLengthErrorMessage, "\")]")
		case 29:
			line("\t\t[MaxLength(500, ErrorMessage = \"", modelMaxLengthErrorMessage, "\")]")
		case 42:
			line("\t\t[MaxLength(250, ErrorMessage = \"", modelMaxLengthErrorMessage, "\")]")
			line("\t\t[Phone(ErrorMessage = \"", modelInvalidEmailErrorMessage, "\")]")
		case 44:
			line("\t\t[MaxLength(250, ErrorMessage = \"", modelMaxLengthErrorMessage, "\")]")
			line("\t\t[Url(ErrorMessage = \"", modelInvalidEmailErrorMessage, "\")]")
		case 45:
			line("\t\t[MaxLength(250, ErrorMessage = \"", modelMaxLengthErrorMessage, "\")]")
			line("\t\t[EmailAddress(ErrorMessage = \"", modelInvalidEmailErrorMessage, "\")]")
		}
		line("\t\t[SchemaProperty(\"", column.Name, "\")]")
		typeName := modelType(column.DataValueType)
		if column.ReferenceSchemaName == nil || *column.ReferenceSchemaName == "" {
			if column.Name == "IsNew" {
				// BaseModel already defines IsNew.
				line("\t\tpublic new ", typeName, " ", column.Name, " { get; set; }")
			} else {
				line("\t\tpublic ", typeName, " ", column.Name, " { get; set; }")
			}
		} else {
			line("\t\tpublic ", typeName, " ", column.Name, "Id { get; set; }")
			line()
			if described {
				line("\t\t/// <inheritdoc cref=\"", column.Name, "Id\"/>")
			}
			line("\t\t[LookupProperty(\"", column.Name, "\")]")
			line("\t\tpublic virtual ", *column.ReferenceSchemaName, " ", column.Name, " { get; set; }")
		}
		line()
	}
	line("\t\t#endregion")
	line()
	if details := w.sortedDetails(schema.Name); len(details) > 0 {
		line("\t\t#region Details")
		line()
		for _, detail := range details {
			line("\t\t/// <summary>")
			line("\t\t/// Collection of ", detail.schema, " by column ", detail.property)
			line("\t\t/// <remarks>")
			line("\t\t/// <see cref=\"global::", w.namespace, ".", detail.schema, "\">See more about the ", detail.schema, " model</see>")
			line("\t\t/// </remarks>")
			line("\t\t/// </summary>")
			line("\t\t[DetailProperty(nameof(global::", w.namespace, ".", detail.schema, ".", detail.property, "))]")
			line("\t\tpublic virtual List<", detail.schema, "> ", detail.name, " { get; set; }")
			line()
		}
		line("\t\t#endregion")
	}
	line("\t}")
	line("}")
	line("#pragma warning restore CS8618 // Non-nullable field is uninitialized.")
	return b.String()
}

// modelExtensionsText is CreateExtensionClass: a raw string literal of clio's source, whose line breaks are
// CRLF on every platform.
func modelExtensionsText(namespace string) string {
	lines := []string{
		"using System.Collections.Generic;",
		"using System.ComponentModel.DataAnnotations;",
		"using ATF.Repository;",
		"",
		"namespace " + namespace + " {",
		"",
		"\tpublic static class BaseModelExtensions{",
		"\t\t#region Methods: Public",
		"\t\t",
		"\t\tpublic static List<ValidationResult> ValidateModel(this BaseModel model) {",
		"\t\t\tList<ValidationResult> validationResult = new List<ValidationResult>();",
		"\t\t\tbool isValid = Validator.TryValidateObject(model, new ValidationContext(model), validationResult, true);",
		"\t\t\treturn isValid ? new List<ValidationResult>() : validationResult;",
		"\t\t}",
		"\t\t",
		"\t\t#endregion",
		"\t}",
		"",
		"}",
		"",
	}
	return strings.Join(lines, "\r\n")
}
