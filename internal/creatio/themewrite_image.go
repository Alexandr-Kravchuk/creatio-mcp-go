package creatio

// upload-image: clio's SysImageUploader (clio master 914dab286, unchanged since 8.1.0.134). The image goes
// to the platform image API the Appearance page uses (ImageAPIService/upload) on a forms session, and the
// upload counts only when the bytes read back through img/entity/hash/SysImage/Data/{id} equal the file:
// an expired session answers HTTP 200 with a login page, so a status check alone would report a false
// success.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	// themeWriteImageMaxBytes is SysImageUploader.MaxImageBytes (SysSettingsManager.MaxBinaryValueBytes).
	themeWriteImageMaxBytes = 10 * 1024 * 1024
	// themeWriteImageUploadRoute is ServiceUrlBuilder.KnownRoute.ImageApiUpload.
	themeWriteImageUploadRoute = "ImageAPIService/upload"
	// themeWriteImageReadRoute is the read endpoint the uploader verifies through; "hash" is literal.
	themeWriteImageReadRoute = "img/entity/hash/SysImage/Data/"
	// themeWriteImageTimeout is Creatio.Client's UploadImageAsync / ExecuteGetRequestAsync default.
	themeWriteImageTimeout = 100 * time.Second
)

// themeWriteImageMimeTypes is SysImageUploader.MimeTypesByExtension (keys compared ignoring case).
var themeWriteImageMimeTypes = map[string]string{
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif",
	".bmp": "image/bmp", ".webp": "image/webp", ".svg": "image/svg+xml",
}

// ImageUploadResult is clio's UploadImageResult: image-id on success, error on failure.
type ImageUploadResult struct {
	Success bool   `json:"success"`
	ImageID string `json:"image-id,omitempty"`
	Error   string `json:"error,omitempty"`
}

// ImageUploadFailure is UploadImageResult.Failure: a blank message reads "unknown".
func ImageUploadFailure(message string) ImageUploadResult {
	if strings.TrimSpace(message) == "" {
		message = "unknown"
	}
	return ImageUploadResult{Error: message}
}

// themeWriteImageUpload is clio's SysImageUploadResult: the created id, or the user-facing failure.
type themeWriteImageUpload struct {
	ImageID string
	Error   string
}

// UploadImage uploads a local image file and returns clio's tool result. The error text is the uploader's,
// unredacted; the MCP side redacts it as clio's tool does.
func (c *Client) UploadImage(ctx context.Context, filePath string) ImageUploadResult {
	upload := c.themeWriteUploadImage(ctx, filePath)
	if upload.Error != "" {
		return ImageUploadResult{Error: upload.Error}
	}
	return ImageUploadResult{Success: true, ImageID: upload.ImageID}
}

// themeWriteUploadImage is SysImageUploader.UploadAsync.
func (c *Client) themeWriteUploadImage(ctx context.Context, filePath string) themeWriteImageUpload {
	payload, mimeType, failure := themeWriteReadImage(filePath)
	if failure != "" {
		return themeWriteImageUpload{Error: failure}
	}
	fileName := themeWriteFileName(filePath)
	if strings.TrimSpace(c.config.Login) == "" || strings.TrimSpace(c.config.Password) == "" {
		return themeWriteImageUpload{Error: "authentication failed for environment while uploading '" + fileName +
			"' — forms username and password are required in env config"}
	}
	imageID := themeWriteNewGUID()
	query := "fileapi" + strconv.FormatInt(time.Now().UnixMilli(), 10) +
		"&totalFileLength=" + strconv.Itoa(len(payload)) + "&fileId=" + imageID +
		"&mimeType=" + themeWriteEscapeDataString(mimeType)
	status, body, err := c.themeWriteImageRequest(ctx, http.MethodPost, themeWriteImageUploadRoute, query, payload,
		func(request *http.Request) {
			request.Header.Set("Content-Type", mimeType)
			request.Header.Set("Content-Range", fmt.Sprintf("bytes 0-%d/%d", len(payload)-1, len(payload)))
			request.Header.Set("Content-Disposition", "attachment; filename="+themeWriteEscapeDataString(fileName))
		})
	if err != nil {
		return themeWriteImageTransportFailure(err, fileName)
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		hint := ""
		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			hint = " Verify the environment credentials and that its proxy preserves the Creatio CSRF cookie."
		}
		return themeWriteImageUpload{Error: fmt.Sprintf("Image upload failed: the image API returned HTTP %d.%s", status, hint)}
	}
	if serverError, rejected := themeWriteImageUploadError(body); rejected {
		return themeWriteImageUpload{Error: "Image upload failed: " + serverError}
	}
	status, stored, err := c.themeWriteImageRequest(ctx, http.MethodGet, themeWriteImageReadRoute+imageID, "", nil, nil)
	if err != nil {
		return themeWriteImageTransportFailure(err, fileName)
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		return themeWriteImageUpload{Error: fmt.Sprintf(
			"Image upload could not be verified: reading the image back returned HTTP %d.", status)}
	}
	if !bytes.Equal(stored, payload) {
		return themeWriteImageUpload{Error: "Image upload could not be verified: the image read back from the environment does not match the uploaded file."}
	}
	return themeWriteImageUpload{ImageID: imageID}
}

// themeWriteImageTransportFailure maps a failed request the way UploadAsync's catch blocks do.
func themeWriteImageTransportFailure(err error, fileName string) themeWriteImageUpload {
	switch {
	case isAuthenticationError(err):
		return themeWriteImageUpload{Error: "authentication failed for environment while uploading '" + fileName +
			"' — check username and password in env config"}
	case errors.Is(err, context.DeadlineExceeded):
		return themeWriteImageUpload{Error: "Image upload timed out."}
	default:
		return themeWriteImageUpload{Error: "Image upload failed: " + err.Error()}
	}
}

// themeWriteReadImage is SysImageUploader.TryReadImage: the checks in clio's order, then the bounded read.
func themeWriteReadImage(filePath string) ([]byte, string, string) {
	if strings.TrimSpace(filePath) == "" {
		return nil, "", "A path to the image file is required."
	}
	info, err := os.Stat(filePath)
	if err != nil || info.IsDir() {
		return nil, "", "File not found: '" + filePath + "'."
	}
	extension := themeWriteExtension(filePath)
	mimeType, ok := themeWriteImageMimeTypes[strings.ToLower(extension)]
	if !ok {
		supported := make([]string, 0, len(themeWriteImageMimeTypes))
		for key := range themeWriteImageMimeTypes {
			supported = append(supported, key)
		}
		sort.Strings(supported)
		return nil, "", "Unsupported image extension '" + extension + "'. Supported: " + strings.Join(supported, ", ") + "."
	}
	if info.Size() == 0 {
		return nil, "", "File is empty: '" + filePath + "'."
	}
	if info.Size() > themeWriteImageMaxBytes {
		return nil, "", fmt.Sprintf("File exceeds the %s-byte limit: '%s' (%s bytes).",
			themeWriteGroupDigits(themeWriteImageMaxBytes), filePath, themeWriteGroupDigits(info.Size()))
	}
	file, err := os.Open(filePath)
	if err != nil {
		return nil, "", "File not found: '" + filePath + "'."
	}
	defer file.Close()
	payload, err := io.ReadAll(io.LimitReader(file, themeWriteImageMaxBytes+1))
	if err != nil {
		return nil, "", "File not found: '" + filePath + "'."
	}
	if len(payload) == 0 || len(payload) > themeWriteImageMaxBytes {
		return nil, "", fmt.Sprintf("File changed while reading and no longer fits the %s-byte limit: '%s' (%s bytes).",
			themeWriteGroupDigits(themeWriteImageMaxBytes), filePath, themeWriteGroupDigits(int64(len(payload))))
	}
	return payload, mimeType, ""
}

// themeWriteExtension is .NET's Path.GetExtension: a trailing dot is no extension, and a dot inside a
// directory name is not one either.
func themeWriteExtension(path string) string {
	extension := filepath.Ext(path)
	if extension == "." {
		return ""
	}
	return extension
}

// themeWriteFileName is .NET's Path.GetFileName, which splits on both separators on Windows and on '/'
// elsewhere.
func themeWriteFileName(path string) string {
	return filepath.Base(filepath.Clean(path))
}

// themeWriteGroupDigits is .NET's "N0" format under an English culture: digits grouped by three with commas.
func themeWriteGroupDigits(value int64) string {
	text := strconv.FormatInt(value, 10)
	negative := strings.HasPrefix(text, "-")
	text = strings.TrimPrefix(text, "-")
	var grouped strings.Builder
	for index, digit := range text {
		if index > 0 && (len(text)-index)%3 == 0 {
			grouped.WriteByte(',')
		}
		grouped.WriteRune(digit)
	}
	if negative {
		return "-" + grouped.String()
	}
	return grouped.String()
}

// themeWriteEscapeDataString is .NET's Uri.EscapeDataString: every byte outside the RFC 3986 unreserved
// set is percent-encoded (a space is %20, never '+').
func themeWriteEscapeDataString(text string) string {
	var escaped strings.Builder
	for _, b := range []byte(text) {
		switch {
		case b >= 'A' && b <= 'Z', b >= 'a' && b <= 'z', b >= '0' && b <= '9', b == '-', b == '.', b == '_', b == '~':
			escaped.WriteByte(b)
		default:
			fmt.Fprintf(&escaped, "%%%02X", b)
		}
	}
	return escaped.String()
}

// themeWriteNewGUID is a random version-4 GUID in .NET's "D" format.
func themeWriteNewGUID() string {
	var raw [16]byte
	_, _ = rand.Read(raw[:])
	raw[6] = raw[6]&0x0f | 0x40
	raw[8] = raw[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16])
}

// themeWriteImageUploadError is SysImageUploader.TryReadUploadError: a rejection inside a 2xx answer, in
// either shape the live API uses ({"error": ...} or {"success": false, "errorInfo": {...}}). Property names
// match ignoring case; a non-JSON body is not a rejection (the read-back decides).
func themeWriteImageUploadError(body []byte) (string, bool) {
	if strings.TrimSpace(string(body)) == "" {
		return "", false
	}
	var root any
	if err := json.Unmarshal(body, &root); err != nil {
		return "", false
	}
	if plain, ok := themeWriteImageProperty(root, "error"); ok {
		if text := themeWriteImageDisplay(plain); text != nil {
			return *text, true
		}
		return "the image API reported an error.", true
	}
	success, ok := themeWriteImageProperty(root, "success")
	if !ok || success != false {
		return "", false
	}
	errorInfo, _ := themeWriteImageProperty(root, "errorInfo")
	for _, name := range []string{"message", "errorCode"} {
		if value, ok := themeWriteImageProperty(errorInfo, name); ok {
			if text := themeWriteImageDisplay(value); text != nil {
				return *text, true
			}
		}
	}
	return "the image API reported success=false.", true
}

// themeWriteImageProperty reads a property of a JSON object ignoring the name's case; a JSON null counts as
// present only when clio's JsonNode would be non-null, which it never is, so null reads as absent.
func themeWriteImageProperty(node any, name string) (any, bool) {
	object, ok := node.(map[string]any)
	if !ok {
		return nil, false
	}
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if strings.EqualFold(key, name) {
			if object[key] == nil {
				return nil, false
			}
			return object[key], true
		}
	}
	return nil, false
}

// themeWriteImageDisplay is AsDisplayString: a string as is, any other value as its JSON text.
func themeWriteImageDisplay(value any) *string {
	if value == nil {
		return nil
	}
	if text, ok := value.(string); ok {
		return &text
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	text := string(encoded)
	return &text
}

// themeWriteImageRequest sends one authenticated request to a fixed application route outside the service
// roots (the image API). The route is a constant plus a generated GUID, never a tool argument.
func (c *Client) themeWriteImageRequest(ctx context.Context, method, route, rawQuery string, body []byte,
	decorate func(*http.Request)) (int, []byte, error) {
	requestCtx, cancel := context.WithTimeout(ctx, themeWriteImageTimeout)
	defer cancel()
	response, _, err := c.doAuthenticated(requestCtx, c.requestClient(), func() (*http.Request, error) {
		target := c.serviceURL(route)
		if rawQuery != "" {
			target += "?" + rawQuery
		}
		var reader io.Reader
		if body != nil {
			reader = bytes.NewReader(body)
		}
		request, err := http.NewRequestWithContext(requestCtx, method, target, reader)
		if err != nil {
			return nil, err
		}
		if decorate != nil {
			decorate(request)
		}
		return request, nil
	})
	if err != nil {
		if requestCtx.Err() != nil && ctx.Err() == nil {
			return 0, nil, context.DeadlineExceeded
		}
		return 0, nil, err
	}
	payload, err := readResponseLimit(response, themeWriteImageMaxBytes+1)
	if err != nil {
		return 0, nil, err
	}
	return response.StatusCode, payload, nil
}
