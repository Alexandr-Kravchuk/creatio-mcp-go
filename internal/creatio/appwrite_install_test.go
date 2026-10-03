package creatio

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf16"
)

// appWriteReadGzip decodes clio's package stream into relative path -> content.
func appWriteReadGzip(t *testing.T, data []byte) map[string]string {
	t.Helper()
	reader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for {
		var length int32
		if err := binary.Read(reader, binary.LittleEndian, &length); err == io.EOF {
			return files
		} else if err != nil {
			t.Fatal(err)
		}
		units := make([]uint16, length)
		_ = binary.Read(reader, binary.LittleEndian, units)
		var size int32
		_ = binary.Read(reader, binary.LittleEndian, &size)
		content := make([]byte, size)
		_, _ = io.ReadFull(reader, content)
		files[string(utf16.Decode(units))] = string(content)
	}
}

func TestAppWriteInstallApplicationFromFolder(t *testing.T) {
	saved := appWriteInstallTimings
	appWriteInstallTimings.logInterval, appWriteInstallTimings.chunk = time.Millisecond, 8
	t.Cleanup(func() { appWriteInstallTimings = saved })
	root := t.TempDir()
	pkg := filepath.Join(root, "UsrPkg")
	_ = os.MkdirAll(filepath.Join(pkg, "Schemas", "UsrA"), 0o755)
	_ = os.WriteFile(filepath.Join(pkg, "descriptor.json"), []byte(`{"Descriptor":{"Name":"UsrPkg"}}`), 0o644)
	_ = os.WriteFile(filepath.Join(pkg, "Schemas", "UsrA", "metadata.json"), []byte("{}"), 0o644)
	_ = os.WriteFile(filepath.Join(pkg, "Ignored.txt"), []byte("x"), 0o644)
	report := filepath.Join(root, "report.txt")

	for _, fail := range []bool{false, true} {
		var mu sync.Mutex
		var uploaded bytes.Buffer
		var ranges, routes []string
		var install map[string]any
		logReads := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			mu.Lock()
			defer mu.Unlock()
			route := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
			routes = append(routes, route)
			switch route {
			case "Login":
				io.WriteString(w, `{"Code":0}`)
			case "UploadPackage":
				if r.URL.Query().Get("fileName") != "UsrPkg.gz" || r.URL.Query().Get("mimeType") != "application/gzip" || r.Header.Get("Content-Type") != "application/gzip" {
					t.Errorf("upload %s %v", r.URL.RawQuery, r.Header)
				}
				ranges = append(ranges, r.Header.Get("Content-Range"))
				uploaded.Write(body)
				io.WriteString(w, `{"success":true}`)
			case "CreatePackageBackup":
				if string(body) != `{"Name":"UsrPkg","Code":"UsrPkg","ZipPackageName":"UsrPkg.gz","LastUpdate":0}` {
					t.Errorf("backup %s", body)
				}
				io.WriteString(w, `{"success":true}`)
			case "GetLogFile":
				logReads++
				if logReads == 1 {
					io.WriteString(w, "old line\n")
				} else if fail {
					io.WriteString(w, "old line\nerror CS0103: x\nPackage installation finished\n")
				} else {
					io.WriteString(w, "old line\nInstalling UsrPkg\n")
				}
			case "InstallAppFromFile":
				_ = json.Unmarshal(body, &install)
				time.Sleep(10 * time.Millisecond)
				if fail {
					io.WriteString(w, `{"success":false,"errorInfo":{"message":"Packages installation failed."}}`)
				} else {
					io.WriteString(w, `{"success":true}`)
				}
			default:
				t.Errorf("unexpected %s", route)
			}
		}))
		check := true
		result := newFormsTestClient(t, server.URL).InstallApplication(context.Background(), AppInstallRequest{Name: pkg, ReportPath: report,
			CheckCompilationErrors: &check, EnvironmentURI: "http://stand"})
		server.Close()
		messages := result.Messages
		last := messages[len(messages)-1]
		if fail {
			if result.ExitCode != 1 || last.MessageType != "Error" || last.Value != `Failed package: "`+pkg+`".` ||
				messages[len(messages)-2].Value != "Package installation failed: the configuration failed to compile. error CS0103: x" {
				t.Fatalf("failure %+v", result)
			}
			continue
		}
		if result.ExitCode != 0 || last.Value != "Done" || last.MessageType != "Info" {
			t.Fatalf("success %+v", result)
		}
		if install["Name"] != "UsrPkg" || install["ZipPackageName"] != "UsrPkg.gz" || install["LastUpdateString"] != float64(0) || install["CheckCompilationErrors"] != true {
			t.Fatalf("install body %v", install)
		}
		if len(ranges) < 2 || !strings.HasPrefix(ranges[0], "bytes 0-7/") {
			t.Fatalf("chunks %v", ranges)
		}
		files := appWriteReadGzip(t, uploaded.Bytes())
		if len(files) != 2 || files["descriptor.json"] == "" || files[filepath.Join("Schemas", "UsrA", "metadata.json")] != "{}" {
			t.Fatalf("packed files %v", files)
		}
		if _, err := os.Stat(pkg + ".gz"); !os.IsNotExist(err) {
			t.Fatalf("packed file left behind: %v", err)
		}
		written, _ := os.ReadFile(report)
		if !bytes.HasPrefix(written, []byte{0xEF, 0xBB, 0xBF}) || !strings.Contains(string(written), "Installing UsrPkg") {
			t.Fatalf("report %q", written)
		}
		joined := strings.Join(routes, ",")
		if !strings.Contains(joined, "UploadPackage,CreatePackageBackup,GetLogFile") || strings.Contains(joined, "UnloadAppDomain") {
			t.Fatalf("routes %s", joined)
		}
	}
}

func TestAppWriteInstallApplicationMissingPackage(t *testing.T) {
	result := (&Client{}).InstallApplication(context.Background(), AppInstallRequest{Name: filepath.Join(t.TempDir(), "none.gz")})
	if result.ExitCode != 1 || len(result.Messages) != 2 || !strings.HasPrefix(result.Messages[0].Value, "Specified package not found by path ") ||
		result.Messages[0].MessageType != "None" || !strings.HasPrefix(result.Messages[1].Value, "Failed package: ") {
		t.Fatalf("%+v", result)
	}
}
