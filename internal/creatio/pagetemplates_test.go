package creatio

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestListPageTemplatesReadsBothCatalogsAndInjectsWebTemplates(t *testing.T) {
	var requested []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ServiceModel/AuthService.svc/Login":
			_, _ = w.Write([]byte(`{"Code":0}`))
		case "/0/rest/schema.template.api/templates":
			if r.Method != http.MethodGet {
				t.Errorf("method = %s", r.Method)
			}
			requested = append(requested, r.URL.Query().Get("schemaType"))
			if r.URL.Query().Get("schemaType") == "9" {
				_, _ = w.Write([]byte(`{"success":true,"items":[{"uId":"w1","name":"BlankPageTemplate","title":"Blank page","groupName":"Page"},{"uId":"d1","name":"centralareadesktoptemplate","title":"Desk","groupName":"DesktopTemplate"}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"success":true,"items":[{"uId":"m1","name":"BlankMobilePageTemplate","title":null,"groupName":"MobilePage"}]}`))
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	result := newFormsTestClient(t, server.URL).ListPageTemplates(context.Background(), "")
	if strings.Join(requested, ",") != "9,10" {
		t.Fatalf("requested schema types = %v", requested)
	}
	if !result.Success || result.Count != 4 {
		t.Fatalf("result = %#v", result)
	}
	names := []string{}
	for _, item := range result.Items {
		names = append(names, item.Name)
	}
	if strings.Join(names, ",") != "BlankPageTemplate,centralareadesktoptemplate,BaseDashboardTemplate,BlankMobilePageTemplate" {
		t.Fatalf("names = %v", names)
	}
	if desktop := result.Items[1]; desktop.GroupName != "Desktop" || desktop.UID != "d1" || desktop.SchemaType != 9 {
		t.Fatalf("desktop = %#v", desktop)
	}
	if mobile := result.Items[3]; mobile.SchemaType != 10 || mobile.Title != "" {
		t.Fatalf("mobile = %#v", mobile)
	}
}

func TestListPageTemplatesFiltersAndReportsFailures(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/Login") {
			_, _ = w.Write([]byte(`{"Code":0}`))
			return
		}
		if r.URL.Query().Get("schemaType") != "10" {
			t.Errorf("schemaType = %q", r.URL.Query().Get("schemaType"))
		}
		_, _ = w.Write([]byte(`{"success":false,"errorInfo":{"message":"catalog down"}}`))
	}))
	defer server.Close()
	client := newFormsTestClient(t, server.URL)
	if result := client.ListPageTemplates(context.Background(), " Mobile "); result.Success || result.Error != "catalog down" || result.Items != nil {
		t.Fatalf("service failure = %#v", result)
	}
	if result := client.ListPageTemplates(context.Background(), "desktop"); result.Success || result.Error != "Unknown schema-type 'desktop'. Use 'web' or 'mobile'." {
		t.Fatalf("invalid schema-type = %#v", result)
	}
}
