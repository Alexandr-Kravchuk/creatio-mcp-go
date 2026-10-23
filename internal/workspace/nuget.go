package workspace

// CreatioSdkOnline: the CreatioSDK versions published on api.nuget.org, newest first, read once per call
// with a 15 s timeout. An unreadable feed is logged as clio logs it and leaves the version unknown.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// NuGetBaseURL is api.nuget.org; tests point it at a local server.
var NuGetBaseURL = "https://api.nuget.org"

const sdkVersionsUnavailable = "Creatio SDK versions could not be read from https://api.nuget.org, so the latest SDK version is " +
	"unknown. Check connectivity to api.nuget.org (a proxy or a corporate perimeter can block it), or " +
	"set ApplicationVersion in .clio/workspaceSettings.json explicitly."

// dotnetVersion is System.Version: two to four numeric components.
type dotnetVersion []int

func parseDotnetVersion(text string) (dotnetVersion, error) {
	parts := strings.Split(text, ".")
	if len(parts) < 2 || len(parts) > 4 {
		return nil, fmt.Errorf("Version string portion was too short or too long.")
	}
	version := make(dotnetVersion, len(parts))
	for index, part := range parts {
		number, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || number < 0 {
			return nil, fmt.Errorf("Input string was not in a correct format.")
		}
		version[index] = number
	}
	return version, nil
}

func (v dotnetVersion) component(index int) int {
	if index < len(v) {
		return v[index]
	}
	return -1
}

func (v dotnetVersion) compare(other dotnetVersion) int {
	for index := 0; index < 4; index++ {
		if left, right := v.component(index), other.component(index); left != right {
			if left < right {
				return -1
			}
			return 1
		}
	}
	return 0
}

func (v dotnetVersion) String() string {
	parts := make([]string, len(v))
	for index, number := range v {
		parts[index] = strconv.Itoa(number)
	}
	return strings.Join(parts, ".")
}

// threePart is new Version(major, minor, build).ToString().
func (v dotnetVersion) threePart() string {
	build := v.component(2)
	if build < 0 {
		build = -1
	}
	if build < 0 {
		return fmt.Sprintf("%d.%d", v[0], v[1])
	}
	return fmt.Sprintf("%d.%d.%d", v[0], v[1], build)
}

// SDKVersions is one call's ICreatioSdk: the list is fetched on first use and an empty answer is fetched
// again on the next use, as clio does.
type SDKVersions struct {
	ctx      context.Context
	log      *Log
	versions []dotnetVersion
}

// NewSDKVersions is CreatioSdkOnline for one call; failures are logged into log.
func NewSDKVersions(ctx context.Context, log *Log) *SDKVersions {
	return &SDKVersions{ctx: ctx, log: log}
}

func (s *SDKVersions) list() []dotnetVersion {
	if len(s.versions) == 0 {
		versions, err := fetchSDKVersions(s.ctx)
		if err != nil {
			s.log.Error("Error while getting Creatio SDK versions from NuGet: " + err.Error())
			versions = nil
		}
		s.versions = versions
	}
	return s.versions
}

// Last is CreatioSdkOnline.LastVersion.
func (s *SDKVersions) Last() (dotnetVersion, error) {
	versions := s.list()
	if len(versions) == 0 {
		return nil, errors.New(sdkVersionsUnavailable)
	}
	return versions[0], nil
}

func fetchSDKVersions(ctx context.Context) ([]dotnetVersion, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, NuGetBaseURL+"/v3/registration5-semver1/creatiosdk/index.json", nil)
	if err != nil {
		return nil, err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	var model struct {
		Items []struct {
			Items []struct {
				CatalogEntry struct {
					Version string `json:"version"`
				} `json:"catalogEntry"`
			} `json:"items"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &model); err != nil {
		return nil, err
	}
	if len(model.Items) == 0 || model.Items[0].Items == nil {
		return nil, errors.New("NuGet registration response for creatiosdk did not contain any catalog items.")
	}
	var versions []dotnetVersion
	for _, item := range model.Items[0].Items {
		version, err := parseDotnetVersion(item.CatalogEntry.Version)
		if err != nil {
			return nil, err
		}
		versions = append(versions, version)
	}
	sort.SliceStable(versions, func(i, j int) bool { return versions[i].compare(versions[j]) > 0 })
	return versions, nil
}
