package creatio

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestThemeWriteUploadImageRequestsAndFailure(t *testing.T) {
	image := []byte("image bytes")
	file := filepath.Join(t.TempDir(), "parity.png")
	if err := os.WriteFile(file, image, 0600); err != nil {
		t.Fatal(err)
	}
	var uploaded bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/Login"):
			io.WriteString(w, `{"Code":0}`)
		case strings.HasSuffix(r.URL.Path, "/ImageAPIService/upload"):
			body, _ := io.ReadAll(r.Body)
			if r.Method != http.MethodPost || string(body) != string(image) || r.Header.Get("Content-Type") != "image/png" || r.URL.Query().Get("totalFileLength") != "11" || r.URL.Query().Get("fileId") == "" {
				t.Errorf("upload: %s %s %s %q", r.Method, r.URL, r.Header, body)
			}
			uploaded = true
			io.WriteString(w, `{"success":true}`)
		case strings.Contains(r.URL.Path, "/img/entity/hash/SysImage/Data/"):
			if !uploaded || r.Method != http.MethodGet {
				t.Errorf("verify before upload: %s", r.URL)
			}
			w.Write(image)
		default:
			t.Errorf("unexpected %s", r.URL)
		}
	}))
	defer server.Close()
	result := newFormsTestClient(t, server.URL).UploadImage(context.Background(), file)
	if !result.Success || result.ImageID == "" {
		t.Fatalf("upload=%+v", result)
	}
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if result := newFormsTestClient(t, server.URL).UploadImage(context.Background(), file); result.Success || !strings.Contains(result.Error, "File is empty") {
		t.Fatalf("empty=%+v", result)
	}
}

func TestPkgWriteSyncRequestsAndFailure(t *testing.T) {
	var calls []string
	var disabled bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/Login") {
			io.WriteString(w, `{"Code":0}`)
			return
		}
		calls = append(calls, r.URL.Path)
		if r.Method != http.MethodPost {
			t.Errorf("method=%s", r.Method)
		}
		if strings.HasSuffix(r.URL.Path, "GetIsFileDesignMode") {
			if disabled {
				io.WriteString(w, `{"success":true,"value":false}`)
			} else {
				io.WriteString(w, `{"success":true,"value":true}`)
			}
			return
		}
		io.WriteString(w, `{"success":true}`)
	}))
	defer server.Close()
	client := newFormsTestClient(t, server.URL)
	for _, toDB := range []bool{true, false} {
		if result := client.SyncPackages(context.Background(), toDB); result.ExitCode != 0 {
			t.Fatalf("sync=%+v", result)
		}
	}
	if len(calls) != 4 || !strings.HasSuffix(calls[1], "LoadPackagesToDB") || !strings.HasSuffix(calls[3], "LoadPackagesToFileSystem") {
		t.Fatalf("calls=%v", calls)
	}
	disabled = true
	if result := client.SyncPackages(context.Background(), true); result.ExitCode != 1 || !strings.Contains(result.Messages[0].Value, "disabled file design mode") {
		t.Fatalf("disabled=%+v", result)
	}
}
