package creatio

// download-sys-setting-file: clio master's DownloadSysSettingFileTool over DownloadSysSettingFileService.
// The value is read the way clio's data provider reads one (QuerySysSettings), decoded from Base64 and
// written to the caller's new file without conversion; the file content is never returned.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// AdminWriteDownloadResult is clio's DownloadSysSettingFileResult.
type AdminWriteDownloadResult struct {
	ExitCode  int          `json:"exit-code"`
	Messages  []LogMessage `json:"execution-log-messages"`
	FileName  *string      `json:"file-name"`
	ByteCount *int64       `json:"byte-count"`
}

// AdminWriteDownloadFailure is the tool's Failure(message): exit code 1 and one Error message.
func AdminWriteDownloadFailure(message string) AdminWriteDownloadResult {
	return AdminWriteDownloadResult{ExitCode: 1, Messages: []LogMessage{{MessageType: "Error", Value: message}}}
}

// adminWriteDownloadError is a failure whose message clio passes through (after redaction by the caller).
type adminWriteDownloadError string

func (e adminWriteDownloadError) Error() string { return string(e) }

// DownloadSysSettingFile runs the requirement gate (cliogate installed) and the download. The returned
// error's text is what clio shows, still to be redacted.
func (c *Client) DownloadSysSettingFile(ctx context.Context, code, fileName string) (AdminWriteDownloadResult, error) {
	refusal, err := c.AdminWriteEnsurePackage(ctx, "cliogate", "", "Run 'clio install-gate -e <environment>' first.")
	if err != nil {
		return AdminWriteDownloadResult{}, err
	}
	if refusal != nil {
		return AdminWriteDownloadResult{}, refusal
	}
	destination, count, err := c.adminWriteDownload(ctx, code, fileName)
	if err != nil {
		return AdminWriteDownloadResult{}, err
	}
	return AdminWriteDownloadResult{ExitCode: 0,
		Messages: []LogMessage{{MessageType: "Info", Value: "Binary system setting saved without content conversion."}},
		FileName: &destination, ByteCount: &count}, nil
}

func (c *Client) adminWriteDownload(ctx context.Context, code, fileName string) (string, int64, error) {
	if strings.HasSuffix(fileName, string(filepath.Separator)) || strings.HasSuffix(fileName, "/") || dataWriteBlank(filepath.Base(fileName)) {
		return "", 0, adminWriteDownloadError("file-name must include a filename, not only a directory.")
	}
	destination, err := filepath.Abs(fileName)
	if err != nil {
		return "", 0, err
	}
	if _, err := os.Stat(destination); err == nil {
		return "", 0, adminWriteDownloadError("Destination already exists; choose a new filename.")
	}
	directory := filepath.Dir(destination)
	if info, err := os.Stat(directory); err != nil || !info.IsDir() {
		return "", 0, adminWriteDownloadError("Destination directory does not exist.")
	}
	found, valueType, _, err := c.adminWriteSettingDefinition(ctx, code)
	if err != nil {
		return "", 0, err
	}
	if !found {
		return "", 0, adminWriteDownloadError("System setting does not exist or is not readable.")
	}
	if valueType != "Binary" {
		return "", 0, adminWriteDownloadError("Only Binary system settings can be downloaded as files.")
	}
	value, err := c.adminWriteQuerySettingString(ctx, code)
	if err != nil {
		return "", 0, err
	}
	if dataWriteBlank(value) {
		return "", 0, adminWriteDownloadError("Binary system setting has no downloadable value.")
	}
	limit := int64(adminWriteMaxBinaryBytes)
	if int64(len(value)) > ((limit+2)/3)*4 {
		return "", 0, adminWriteDownloadError("Binary value exceeds the 10 MB download limit.")
	}
	data, err := dotnetBase64Decode(value)
	if err != nil {
		return "", 0, adminWriteDownloadError("The input is not a valid Base-64 string as it contains a non-base 64 character, more than two padding characters, or an illegal character among the padding characters.")
	}
	if int64(len(data)) > limit {
		return "", 0, adminWriteDownloadError("Binary value exceeds the 10 MB download limit.")
	}
	if err := adminWriteSaveNew(directory, destination, data); err != nil {
		return "", 0, err
	}
	return destination, int64(len(data)), nil
}

// adminWriteSaveNew writes a temporary file next to the destination and links it into place, so an
// existing file is never overwritten (File.Move with overwrite: false).
func adminWriteSaveNew(directory, destination string, data []byte) error {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return err
	}
	temporary := filepath.Join(directory, ".clio-"+hex.EncodeToString(random)+".tmp")
	file, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	defer os.Remove(temporary)
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Link(temporary, destination); err != nil {
		if errors.Is(err, os.ErrExist) {
			return adminWriteDownloadError("Destination already exists; choose a new filename.")
		}
		return err
	}
	return nil
}

// adminWriteQuerySettingString is RemoteDataProvider.GetSysSettingValue<string>: the setting's value from
// QuerySysSettings when it is a string, "" otherwise.
func (c *Client) adminWriteQuerySettingString(ctx context.Context, code string) (string, error) {
	body, err := json.Marshal(map[string]any{"sysSettingsNameCollection": []string{code}})
	if err != nil {
		return "", err
	}
	response, err := c.postDataServiceJSON(ctx, "QuerySysSettings", body, 10*time.Minute, 4*adminWriteMaxBinaryBytes)
	if err != nil {
		return "", err
	}
	var decoded struct {
		Success bool `json:"success"`
		Values  map[string]*struct {
			Value any `json:"value"`
		} `json:"values"`
	}
	if err := json.Unmarshal(response, &decoded); err != nil {
		return "", err
	}
	entry := decoded.Values[code]
	if !decoded.Success || entry == nil {
		return "", nil
	}
	text, _ := entry.Value.(string)
	return text, nil
}
