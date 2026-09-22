package creatio

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
)

const (
	maxPackageTextFileBytes     = 10 << 20
	maxPackageFileResponseBytes = 64 << 20 // JSON escaping can expand a 10 MiB UTF-8 file several-fold.
)

type PackageFilesResult struct {
	Success     bool     `json:"success"`
	PackageName string   `json:"package-name,omitempty"`
	Files       []string `json:"files"`
	Count       int      `json:"count"`
	Error       string   `json:"error,omitempty"`
}

type PackageFileResult struct {
	Success              bool   `json:"success"`
	PackageName          string `json:"package-name,omitempty"`
	FilePath             string `json:"file-path,omitempty"`
	Content              string `json:"content"`
	ContentLength        int    `json:"content-length"`
	ProjectFilePath      string `json:"project-file-path"`
	ProjectContent       string `json:"project-content"`
	ProjectContentLength int    `json:"project-content-length"`
	ProjectError         string `json:"project-error,omitempty"`
	Error                string `json:"error,omitempty"`
}

// ListPackageFiles calls the read-only ClioGate file inventory endpoint. ClioGate
// 2.0.0.47 or newer must be installed in the target Creatio environment.
func (c *Client) ListPackageFiles(ctx context.Context, packageName string) PackageFilesResult {
	packageName = strings.TrimSpace(packageName)
	if packageName == "" {
		return PackageFilesResult{Error: "package-name is required and cannot be empty."}
	}
	query := url.Values{"packageName": {packageName}}
	response, err := c.getClioGateJSON(ctx, "rest/CreatioApiGateway/GetPackageFilesDirectoryContent", query, 45*time.Second, maxResponseBytes)
	if err != nil {
		return PackageFilesResult{Error: err.Error()}
	}
	var files []string
	if err := json.Unmarshal(response, &files); err != nil {
		return PackageFilesResult{Error: fmt.Sprintf("ClioGate returned an invalid package file list: %v", err)}
	}
	for i := range files {
		files[i] = strings.TrimLeft(strings.ReplaceAll(files[i], `\`, "/"), "/")
	}
	sort.SliceStable(files, func(i, j int) bool {
		left, right := strings.ToLower(files[i]), strings.ToLower(files[j])
		if left == right {
			return files[i] < files[j]
		}
		return left < right
	})
	return PackageFilesResult{Success: true, PackageName: packageName, Files: files, Count: len(files)}
}

// GetPackageFile reads one package-relative file and the generated csproj when available.
// The server owns the physical path check; this client rejects rooted and traversing input too.
func (c *Client) GetPackageFile(ctx context.Context, packageName, filePath string) PackageFileResult {
	packageName = strings.TrimSpace(packageName)
	if packageName == "" {
		return PackageFileResult{Error: "package-name is required and cannot be empty."}
	}
	filePath, err := normalizePackageFilePath(filePath)
	if err != nil {
		return PackageFileResult{Error: err.Error()}
	}
	content, err := c.readPackageFile(ctx, packageName, filePath)
	if err != nil {
		return PackageFileResult{Error: err.Error()}
	}
	projectPath := packageName + ".csproj"
	projectContent := content
	var projectError string
	if !strings.EqualFold(filePath, projectPath) {
		projectContent, err = c.readPackageFile(ctx, packageName, projectPath)
		if err != nil {
			projectContent = ""
			projectError = fmt.Sprintf("The generated project %q is unavailable: %v", projectPath, err)
		}
	}
	return PackageFileResult{
		Success: true, PackageName: packageName, FilePath: filePath, Content: content,
		ContentLength: utf16Length(content), ProjectFilePath: projectPath,
		ProjectContent: projectContent, ProjectContentLength: utf16Length(projectContent), ProjectError: projectError,
	}
}

func (c *Client) readPackageFile(ctx context.Context, packageName, filePath string) (string, error) {
	query := url.Values{"packageName": {packageName}, "filePath": {filePath}}
	response, err := c.getClioGateJSON(ctx, "rest/CreatioApiGateway/GetPackageFileContent", query, 45*time.Second, maxPackageFileResponseBytes)
	if err != nil {
		return "", err
	}
	var content string
	if err := json.Unmarshal(response, &content); err != nil {
		return "", fmt.Errorf("ClioGate returned an invalid file-content response")
	}
	if len(content) > maxPackageTextFileBytes {
		return "", fmt.Errorf("package file exceeds the %d MiB read limit", maxPackageTextFileBytes>>20)
	}
	return content, nil
}

func normalizePackageFilePath(filePath string) (string, error) {
	trimmed := strings.TrimSpace(filePath)
	if trimmed == "" {
		return "", fmt.Errorf("file-path is required and cannot be empty")
	}
	if strings.HasPrefix(trimmed, "/") || strings.HasPrefix(trimmed, `\`) || hasWindowsDrivePrefix(trimmed) {
		return "", fmt.Errorf("file path must be relative and remain inside the package Files directory")
	}
	normalized := strings.ReplaceAll(trimmed, `\`, "/")
	for _, segment := range strings.Split(normalized, "/") {
		if segment == "." || segment == ".." {
			return "", fmt.Errorf("file path must remain inside the package Files directory")
		}
	}
	return normalized, nil
}

func hasWindowsDrivePrefix(value string) bool {
	return len(value) >= 2 && ((value[0] >= 'a' && value[0] <= 'z') || (value[0] >= 'A' && value[0] <= 'Z')) && value[1] == ':'
}

func utf16Length(value string) int {
	length := 0
	for _, character := range value {
		length++
		if character > 0xFFFF {
			length++
		}
	}
	return length
}
