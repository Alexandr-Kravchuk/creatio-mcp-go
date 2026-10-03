package creatio

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// groupIRequest is one request a group-I test server received: its path, raw body and, for a SelectQuery, the
// root schema and the filter column paths with their values.
type groupIRequest struct {
	Path    string
	Query   string
	Body    string
	Root    string
	Filters map[string]any
}

// groupIServer answers Creatio routes through respond; the login route is handled here.
func groupIServer(t *testing.T, respond func(request groupIRequest) (int, string)) (*Client, *[]groupIRequest) {
	t.Helper()
	requests := []groupIRequest{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ServiceModel/AuthService.svc/Login" {
			_, _ = w.Write([]byte(`{"Code":0}`))
			return
		}
		raw, _ := io.ReadAll(r.Body)
		request := groupIRequest{Path: r.URL.Path, Query: r.URL.RawQuery, Body: string(raw), Filters: map[string]any{}}
		if strings.HasSuffix(r.URL.Path, "/SelectQuery") {
			var query struct {
				Root    string `json:"rootSchemaName"`
				Filters struct {
					Items map[string]struct {
						Left struct {
							Path string `json:"columnPath"`
						} `json:"leftExpression"`
						Right struct {
							Parameter struct {
								Value any `json:"value"`
							} `json:"parameter"`
						} `json:"rightExpression"`
					} `json:"items"`
				} `json:"filters"`
			}
			if err := json.Unmarshal(raw, &query); err != nil {
				t.Errorf("SelectQuery body = %s", raw)
			}
			request.Root = query.Root
			for _, item := range query.Filters.Items {
				request.Filters[item.Left.Path] = item.Right.Parameter.Value
			}
		}
		requests = append(requests, request)
		status, body := respond(request)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return newFormsTestClient(t, server.URL), &requests
}

const (
	describeRootUID = "11111111-1111-1111-1111-111111111111"
	describeV2UID   = "22222222-2222-2222-2222-222222222222"
	describePackage = "33333333-3333-3333-3333-333333333333"
)

func describeLibRows(rows ...[4]any) string {
	encoded := []string{}
	for _, row := range rows {
		item, _ := json.Marshal(map[string]any{"UId": row[0], "Name": row[1], "Caption": "Approve order", "VersionParentUId": describeRootUID,
			"IsActiveVersion": row[2], "Version": row[3], "PackageUId": describePackage, "Enabled": true})
		encoded = append(encoded, string(item))
	}
	return `{"success":true,"rows":[` + strings.Join(encoded, ",") + `]}`
}

func TestDescribeBusinessProcessResolvesCaptionAndOverlaysVersionFacts(t *testing.T) {
	family := describeLibRows([4]any{describeRootUID, "UsrApprove", false, 0}, [4]any{describeV2UID, "UsrApprove_v2", true, 1})
	client, requests := groupIServer(t, func(request groupIRequest) (int, string) {
		switch {
		case request.Root == "SysPackage" && len(request.Filters) == 0 && strings.Contains(request.Body, `"UId"`):
			return 200, `{"success":true,"rows":[{"UId":"` + describePackage + `","Name":"UsrPkg","Id":"x"},{"Name":"CrtProcessBuilder","UId":"44444444-4444-4444-4444-444444444444"}]}`
		case request.Root == "SysPackage":
			return 200, `{"success":true,"rows":[{"Name":"CrtProcessBuilder"}]}`
		case request.Root == "VwProcessLib" && request.Filters["Caption"] == "Approve order":
			return 200, family
		case request.Root == "VwProcessLib" && request.Filters["VersionParentUId"] == describeRootUID:
			return 200, family
		case request.Path == "/0/rest/ProcessDesignService/DescribeProcess":
			return 200, `{"DescribeProcessResult":{"success":true,"errorMessage":null,"elements":[{"name":"Start1","type":"ProcessSchemaStartEvent","position":null}],` +
				`"schemaUId":"` + describeV2UID + `","name":"UsrApprove_v2","caption":"Approve <order>","version":99,"flows":[],"extra":"kept"}}`
		}
		t.Errorf("unexpected request %#v", request)
		return 404, ""
	})
	result := client.DescribeBusinessProcess(context.Background(), ProcessDescribeRequest{ProcessCaption: "Approve order"})
	if result.ExitCode != 0 || len(result.Messages) != 1 || result.Messages[0].MessageType != "Info" {
		t.Fatalf("result = %#v", result)
	}
	want := "{\n" +
		"  \"name\": \"UsrApprove_v2\",\n" +
		"  \"caption\": \"Approve \\u003Corder\\u003E\",\n" +
		"  \"schemaUId\": \"" + describeV2UID + "\",\n" +
		"  \"version\": 1,\n" +
		"  \"isActiveVersion\": true,\n" +
		"  \"activeVersionSchemaUId\": \"" + describeV2UID + "\",\n" +
		"  \"activeVersionName\": \"UsrApprove_v2\",\n" +
		"  \"versionRootSchemaUId\": \"" + describeRootUID + "\",\n" +
		"  \"activeVersionSource\": \"process-library-view\",\n" +
		"  \"versions\": [\n" +
		"    {\n      \"schemaUId\": \"" + describeRootUID + "\",\n      \"name\": \"UsrApprove\",\n      \"caption\": \"Approve order\",\n" +
		"      \"version\": 0,\n      \"isActiveVersion\": false,\n      \"isRoot\": true,\n      \"packageUId\": \"" + describePackage + "\",\n" +
		"      \"packageName\": \"UsrPkg\",\n      \"enabled\": true\n    },\n" +
		"    {\n      \"schemaUId\": \"" + describeV2UID + "\",\n      \"name\": \"UsrApprove_v2\",\n      \"caption\": \"Approve order\",\n" +
		"      \"version\": 1,\n      \"isActiveVersion\": true,\n      \"isRoot\": false,\n      \"packageUId\": \"" + describePackage + "\",\n" +
		"      \"packageName\": \"UsrPkg\",\n      \"enabled\": true\n    }\n  ],\n" +
		"  \"elements\": [\n    {\n      \"name\": \"Start1\",\n      \"type\": \"ProcessSchemaStartEvent\"\n    }\n  ],\n" +
		"  \"flows\": [],\n" +
		"  \"extra\": \"kept\"\n" +
		"}"
	if result.Messages[0].Value != want {
		t.Fatalf("graph =\n%s\nwant\n%s", result.Messages[0].Value, want)
	}
	var describe *groupIRequest
	for index := range *requests {
		if (*requests)[index].Path == "/0/rest/ProcessDesignService/DescribeProcess" {
			describe = &(*requests)[index]
		}
	}
	if describe == nil || describe.Body != `{"request":{"name":"UsrApprove_v2","culture":"en-US"}}` {
		t.Fatalf("DescribeProcess request = %#v", describe)
	}
}

func TestDescribeBusinessProcessReportsServerFailureAndIdentityRule(t *testing.T) {
	client, requests := groupIServer(t, func(request groupIRequest) (int, string) {
		switch {
		case request.Root == "SysPackage":
			return 200, `{"success":true,"rows":[{"Name":"crtprocessbuilder"}]}`
		case request.Path == "/0/rest/ProcessDesignService/DescribeProcess":
			return 200, `{"DescribeProcessResult":{"success":false,"errorMessage":"Process 'UsrNope' not found"}}`
		}
		t.Errorf("unexpected request %#v", request)
		return 404, ""
	})
	empty := ""
	result := client.DescribeBusinessProcess(context.Background(), ProcessDescribeRequest{ProcessUID: " abc ", Culture: &empty})
	if result.ExitCode != 1 || result.Messages[0].Value != "Error: Process 'UsrNope' not found." {
		t.Fatalf("result = %#v", result)
	}
	if body := (*requests)[len(*requests)-1].Body; body != `{"request":{"uid":"abc"}}` {
		t.Fatalf("an empty culture must not be sent; body = %s", body)
	}
	result = client.DescribeBusinessProcess(context.Background(), ProcessDescribeRequest{ProcessName: "A", ProcessUID: "B"})
	if result.ExitCode != 1 || result.Messages[0].Value != "Error: provide exactly one of --process-name, --process-uid, or --process-caption." {
		t.Fatalf("two identities = %#v", result)
	}
}

func TestDescribeBusinessProcessRefusesWithoutProcessBuilder(t *testing.T) {
	client, _ := groupIServer(t, func(request groupIRequest) (int, string) {
		return 200, `{"success":true,"rows":[{"Name":"CrtBase"}]}`
	})
	result := client.DescribeBusinessProcess(context.Background(), ProcessDescribeRequest{ProcessName: "UsrApprove"})
	if result.ExitCode != 1 || result.Messages[0].Value != processBuilderMissingMessage {
		t.Fatalf("result = %#v", result)
	}
}

func TestProcessDescribeGapsNamesEveryMissingFact(t *testing.T) {
	row := processDescribeLibRow{UID: describeV2UID, VersionParentID: describeRootUID}
	flagged := []processDescribeLibRow{{UID: "a", Name: "A"}, {UID: "b", Name: "B"}}
	got := processDescribeGaps(row, flagged, flagged, false, false)
	want := "the view established no version number for this schema; the view established no active-version flag for this schema; " +
		"the process library flags 2 active versions in family '" + describeRootUID + "' (A, B), and which one the runtime executes is decided by a key " +
		"this view does not expose; the package names could not be read, so those facts were not established"
	if got != want {
		t.Fatalf("gaps = %q", got)
	}
}
