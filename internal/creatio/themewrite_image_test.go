package creatio

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// themeWriteTestImage writes a small file with the given name into a temporary directory.
func themeWriteTestImage(t *testing.T, name string, payload []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestUploadImageSendsTheAppearancePageRequestAndVerifiesTheBytes(t *testing.T) {
	payload := []byte("\x89PNG fake image bytes")
	stored := map[string][]byte{}
	var uploadQuery, contentType, contentRange, disposition string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/ServiceModel/AuthService.svc/Login":
			_, _ = w.Write([]byte(`{"Code":0}`))
		case r.URL.Path == "/0/ImageAPIService/upload" && r.Method == http.MethodPost:
			uploadQuery = r.URL.RawQuery
			contentType = r.Header.Get("Content-Type")
			contentRange = r.Header.Get("Content-Range")
			disposition = r.Header.Get("Content-Disposition")
			body, _ := io.ReadAll(r.Body)
			stored[r.URL.Query().Get("fileId")] = body
			_, _ = w.Write([]byte(`{"success":true}`))
		case strings.HasPrefix(r.URL.Path, "/0/img/entity/hash/SysImage/Data/") && r.Method == http.MethodGet:
			_, _ = w.Write(stored[strings.TrimPrefix(r.URL.Path, "/0/img/entity/hash/SysImage/Data/")])
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()
	client, _ := NewClient(Config{BaseURL: server.URL, Login: "example-user", Password: "replace-me"})
	path := themeWriteTestImage(t, "brand logo.PNG", payload)

	result := client.UploadImage(context.Background(), path)

	if !result.Success || result.Error != "" {
		t.Fatalf("result = %#v", result)
	}
	if _, ok := parseGUID(result.ImageID); !ok || stored[result.ImageID] == nil {
		t.Fatalf("image id %q not the uploaded fileId", result.ImageID)
	}
	if !strings.HasPrefix(uploadQuery, "fileapi") || !strings.Contains(uploadQuery, "&totalFileLength=21&fileId="+result.ImageID+"&mimeType=image%2Fpng") {
		t.Errorf("query = %q", uploadQuery)
	}
	if contentType != "image/png" || contentRange != "bytes 0-20/21" || disposition != "attachment; filename=brand%20logo.PNG" {
		t.Errorf("headers = %q %q %q", contentType, contentRange, disposition)
	}
	if !bytes.Equal(stored[result.ImageID], payload) {
		t.Errorf("uploaded body differs")
	}
}

func TestUploadImageReportsClioFailures(t *testing.T) {
	cases := []struct {
		name     string
		upload   func(w http.ResponseWriter)
		readBack func(w http.ResponseWriter)
		want     string
	}{
		{"http status", func(w http.ResponseWriter) { w.WriteHeader(http.StatusInternalServerError) }, nil,
			"Image upload failed: the image API returned HTTP 500."},
		{"plain error", func(w http.ResponseWriter) { _, _ = w.Write([]byte(`{"Error":"File type is not allowed"}`)) }, nil,
			"Image upload failed: File type is not allowed"},
		{"error info", func(w http.ResponseWriter) {
			_, _ = w.Write([]byte(`{"success":false,"errorInfo":{"errorCode":42}}`))
		}, nil, "Image upload failed: 42"},
		{"read-back status", func(w http.ResponseWriter) { _, _ = w.Write([]byte(`{}`)) },
			func(w http.ResponseWriter) { w.WriteHeader(http.StatusNotFound) },
			"Image upload could not be verified: reading the image back returned HTTP 404."},
		{"read-back mismatch", func(w http.ResponseWriter) { _, _ = w.Write([]byte(`not json`)) },
			func(w http.ResponseWriter) { _, _ = w.Write([]byte("other")) },
			"Image upload could not be verified: the image read back from the environment does not match the uploaded file."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/ServiceModel/AuthService.svc/Login":
					_, _ = w.Write([]byte(`{"Code":0}`))
				case r.URL.Path == "/0/ImageAPIService/upload":
					c.upload(w)
				case strings.HasPrefix(r.URL.Path, "/0/img/entity/hash/SysImage/Data/") && c.readBack != nil:
					c.readBack(w)
				default:
					t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
				}
			}))
			defer server.Close()
			client, _ := NewClient(Config{BaseURL: server.URL, Login: "example-user", Password: "replace-me"})
			result := client.UploadImage(context.Background(), themeWriteTestImage(t, "logo.png", []byte("png")))
			if result.Success || result.Error != c.want {
				t.Fatalf("result = %#v, want %q", result, c.want)
			}
		})
	}
}

func TestUploadImageRefusesBeforeAnyRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request %s", r.URL.Path)
	}))
	defer server.Close()
	client, _ := NewClient(Config{BaseURL: server.URL, Login: "example-user", Password: "replace-me"})
	directory := t.TempDir()
	missing := filepath.Join(directory, "absent.png")
	empty := themeWriteTestImage(t, "empty.png", nil)
	text := themeWriteTestImage(t, "notes.txt", []byte("x"))
	cases := map[string]string{
		"":        "A path to the image file is required.",
		missing:   "File not found: '" + missing + "'.",
		directory: "File not found: '" + directory + "'.",
		empty:     "File is empty: '" + empty + "'.",
		text:      "Unsupported image extension '.txt'. Supported: .bmp, .gif, .jpeg, .jpg, .png, .svg, .webp.",
	}
	for path, want := range cases {
		if result := client.UploadImage(context.Background(), path); result.Success || result.Error != want {
			t.Errorf("%q: %#v, want %q", path, result, want)
		}
	}
	oauth, _ := NewClient(Config{BaseURL: server.URL, ClientID: "example-client", Secret: "replace-me", TokenURL: server.URL + "/connect/token"})
	image := themeWriteTestImage(t, "logo.svg", []byte("<svg/>"))
	want := "authentication failed for environment while uploading 'logo.svg' — forms username and password are required in env config"
	if result := oauth.UploadImage(context.Background(), image); result.Error != want {
		t.Errorf("oauth: %#v", result)
	}
	if got := themeWriteGroupDigits(10485760); got != "10,485,760" {
		t.Errorf("N0 = %q", got)
	}
}
