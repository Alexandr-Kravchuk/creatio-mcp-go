package creatio

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// workspaceLinkServer answers every non-login request with the handler's status and body and records it.
func workspaceLinkServer(t *testing.T, answer func(path, body string) (int, string)) (*Client, *[]string) {
	t.Helper()
	calls := &[]string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/Login") {
			_, _ = io.WriteString(w, `{"Code":0}`)
			return
		}
		body, _ := io.ReadAll(r.Body)
		*calls = append(*calls, r.Method+" "+r.URL.Path+" "+string(body))
		status, text := answer(r.URL.Path, string(body))
		w.WriteHeader(status)
		_, _ = io.WriteString(w, text)
	}))
	t.Cleanup(server.Close)
	return newFormsTestClient(t, server.URL), calls
}

func TestWorkspaceLinkUnlockSendsTheListAndReadsClioGatesAnswer(t *testing.T) {
	answer := "true"
	client, calls := workspaceLinkServer(t, func(string, string) (int, string) { return 200, answer })
	site := client.WorkspaceLinkSite()
	if err := site.UnlockPackages(context.Background(), []string{"PkgA", "Pkg+B"}); err != nil {
		t.Fatal(err)
	}
	if err := site.UnlockPackages(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if (*calls)[0] != `POST /0/rest/CreatioApiGateway/UnlockPackages {"unlockPackages":["PkgA","Pkg\u002BB"]}` ||
		(*calls)[1] != `POST /0/rest/CreatioApiGateway/UnlockPackages {"unlockPackages":null}` {
		t.Fatalf("calls = %q", *calls)
	}
	for body, want := range map[string]string{
		"":      "ClioGate UnlockPackages returned an empty response. Check the Creatio application logs (Error.log) and verify the installed cliogate version is current.",
		"<html": "ClioGate UnlockPackages returned a non-JSON response (likely an HTTP error page). Check the Creatio application logs (Error.log) and verify the installed cliogate version is current.",
		"false": "ClioGate UnlockPackages returned false. Check the Creatio application logs for details.",
	} {
		answer = body
		if err := site.UnlockPackages(context.Background(), []string{"PkgA"}); err == nil || err.Error() != want {
			t.Errorf("answer %q: err = %v", body, err)
		}
	}
}

func TestWorkspaceLinkUnlockedPackagesQueriesCustomerPackages(t *testing.T) {
	client, calls := workspaceLinkServer(t, func(string, string) (int, string) {
		return 200, `{"success":true,"rows":[{"Name":"PkgA","UId":"x","Maintainer":"Acme","Version":"1.0.0"},{"Name":null,"Version":null}]}`
	})
	packages, err := client.WorkspaceLinkSite().UnlockedPackages(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(packages) != 2 || packages[0] != (WorkspaceLinkPackage{Name: "PkgA", Version: "1.0.0"}) || packages[1] != (WorkspaceLinkPackage{}) {
		t.Fatalf("packages = %+v", packages)
	}
	// The body clio 8.1.0.142's CLI sends for the same query, captured from a fake site.
	want := `POST /0/DataService/json/SyncReply/SelectQuery {"rootSchemaName":"SysPackage","operationType":0,"allColumns":false,` +
		`"isDistinct":false,"ignoreDisplayValues":false,"rowCount":10000,"rowsOffset":-1,"isPageable":false,"columns":{"items":{` +
		`"Name":{"expression":{"expressionType":0,"columnPath":"Name"},"orderDirection":0,"orderPosition":-1,"isVisible":true},` +
		`"UId":{"expression":{"expressionType":0,"columnPath":"UId"},"orderDirection":0,"orderPosition":-1,"isVisible":true},` +
		`"Maintainer":{"expression":{"expressionType":0,"columnPath":"Maintainer"},"orderDirection":0,"orderPosition":-1,"isVisible":true},` +
		`"Version":{"expression":{"expressionType":0,"columnPath":"Version"},"orderDirection":0,"orderPosition":-1,"isVisible":true}}},` +
		`"filters":{"filterType":6,"isEnabled":true,"trimDateTimeParameterToDate":false,"logicalOperation":0,"items":{"filter0":{` +
		`"filterType":1,"comparisonType":3,"isEnabled":true,"trimDateTimeParameterToDate":false,"leftExpression":{"expressionType":0,` +
		`"columnPath":"InstallType"},"rightExpression":{"expressionType":2,"parameter":{"value":0,"dataValueType":4}}}}}}`
	if len(*calls) != 1 || (*calls)[0] != want {
		t.Fatalf("calls = %q", *calls)
	}
}

func TestWorkspaceLinkPreparationCalls(t *testing.T) {
	client, calls := workspaceLinkServer(t, func(path, body string) (int, string) {
		switch {
		case strings.HasSuffix(path, "QuerySysSettings"):
			return 200, `{"success":true,"values":{"Maintainer":{"value":"Customer"}}}`
		case strings.HasSuffix(path, "SelectQuery"):
			return 200, `{"success":true,"rows":[]}`
		case strings.HasSuffix(path, "PostSysSettingsValues"):
			return 200, `{"saveResult":{"Maintainer":true},"success":true}`
		case strings.HasSuffix(path, "GetIsFileDesignMode"):
			return 200, `{"success":true,"value":true}`
		}
		return 200, `{"success":true}`
	})
	site := client.WorkspaceLinkSite()
	if value, err := site.SysSettingValue(context.Background(), "Maintainer"); err != nil || value != "Customer" {
		t.Fatalf("maintainer = %q, %v", value, err)
	}
	if updated, err := site.UpdateSysSetting(context.Background(), "Maintainer", "Acme"); err != nil || !updated {
		t.Fatalf("update = %v, %v", updated, err)
	}
	log, outcome, err := site.LoadPackagesToFileSystem(context.Background())
	if err != nil || outcome != "completed" || len(log) != 2 || log[0].Value != "Start load packages to file system on a web application" {
		t.Fatalf("load = %+v %q %v", log, outcome, err)
	}
	joined := strings.Join(*calls, "\n")
	for _, want := range []string{
		`/PostSysSettingsValues {"isPersonal":false,"sysSettingsValues":{"Maintainer":"Acme"}}`,
		"/WorkspaceExplorerService.svc/GetIsFileDesignMode ",
		"/AppInstallerService.svc/LoadPackagesToFileSystem ",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("no %q in\n%s", want, joined)
		}
	}
}
