// Package hosttools contains the small local-host operations used by Clio's R1 portability probe.
package hosttools

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	defaultPortRangeStart = 40000
	defaultPortRangeEnd   = 42000
)

// Runner is the narrow seam around native process invocation. On Windows this uses appcmd/netstat;
// it does not load Microsoft.Web.Administration, System.Management, PowerShell, or a .NET helper.
type Runner interface {
	Output(ctx context.Context, executable string, args ...string) ([]byte, error)
	Start(executable string, args []string, workingDirectory string) (int, error)
}

// OSRunner invokes native programs directly, without a shell.
type OSRunner struct{}

func (OSRunner) Output(ctx context.Context, executable string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, executable, args...).Output()
}

func (OSRunner) Start(executable string, args []string, workingDirectory string) (int, error) {
	cmd := exec.Command(executable, args...)
	cmd.Dir = workingDirectory
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid
	if err := cmd.Process.Release(); err != nil {
		return pid, fmt.Errorf("release background process handle: %w", err)
	}
	return pid, nil
}

type PortDiscoveryResult struct {
	Status             string `json:"status"`
	Summary            string `json:"summary"`
	RangeStart         int    `json:"rangeStart"`
	RangeEnd           int    `json:"rangeEnd"`
	FirstAvailablePort *int   `json:"firstAvailablePort,omitempty"`
	IISBoundPortCount  int    `json:"iisBoundPortCount"`
	ActiveTCPPortCount int    `json:"activeTcpPortCount"`
}

// FindEmptyIISPort mirrors Clio's Windows-only scan. The Windows implementation reads IIS bindings
// from appcmd XML and active TCP endpoints from netstat, both built-in native executables.
func FindEmptyIISPort(ctx context.Context, runner Runner) PortDiscoveryResult {
	if runtime.GOOS != "windows" {
		return unavailablePortResult("IIS port discovery is only available on Windows hosts.", defaultPortRangeStart, defaultPortRangeEnd)
	}
	return FindEmptyIISPortForWindows(ctx, runner, defaultPortRangeStart, defaultPortRangeEnd)
}

// FindEmptyIISPortForWindows is exported for deterministic tests and for the platform-specific
// implementation. It does not claim IIS parity on non-Windows hosts.
func FindEmptyIISPortForWindows(ctx context.Context, runner Runner, rangeStart, rangeEnd int) PortDiscoveryResult {
	if runner == nil {
		runner = OSRunner{}
	}
	if rangeStart < 1 || rangeEnd > 65535 || rangeEnd < rangeStart {
		return unavailablePortResult("The requested port range is invalid.", rangeStart, rangeEnd)
	}
	iisOutput, err := runner.Output(ctx, appCmdPath(), "list", "sites", "/xml")
	if err != nil {
		return unavailablePortResult("IIS site bindings could not be read on this host.", rangeStart, rangeEnd)
	}
	iisPorts, err := parseIISBindingPorts(iisOutput, rangeStart, rangeEnd)
	if err != nil {
		return unavailablePortResult("IIS site bindings returned an unreadable response.", rangeStart, rangeEnd)
	}
	netstatOutput, err := runner.Output(ctx, netstatPath(), "-ano", "-p", "tcp")
	if err != nil {
		return unavailablePortResult("Active TCP ports could not be read on this host.", rangeStart, rangeEnd)
	}
	tcpPorts, err := parseNetstatPorts(netstatOutput, rangeStart, rangeEnd)
	if err != nil {
		return unavailablePortResult("Active TCP ports returned an unreadable response.", rangeStart, rangeEnd)
	}
	reserved := make(map[int]struct{}, len(iisPorts)+len(tcpPorts))
	for _, port := range iisPorts {
		reserved[port] = struct{}{}
	}
	for _, port := range tcpPorts {
		reserved[port] = struct{}{}
	}
	for port := rangeStart; port <= rangeEnd; port++ {
		if _, found := reserved[port]; !found {
			return PortDiscoveryResult{
				Status: "available", Summary: fmt.Sprintf("Port %d is the first free IIS deployment port between %d and %d.", port, rangeStart, rangeEnd),
				RangeStart: rangeStart, RangeEnd: rangeEnd, FirstAvailablePort: &port,
				IISBoundPortCount: len(iisPorts), ActiveTCPPortCount: len(tcpPorts),
			}
		}
	}
	return PortDiscoveryResult{
		Status: "unavailable", Summary: fmt.Sprintf("No free IIS deployment port was found between %d and %d.", rangeStart, rangeEnd),
		RangeStart: rangeStart, RangeEnd: rangeEnd,
		IISBoundPortCount: len(iisPorts), ActiveTCPPortCount: len(tcpPorts),
	}
}

func unavailablePortResult(summary string, rangeStart, rangeEnd int) PortDiscoveryResult {
	return PortDiscoveryResult{Status: "unavailable", Summary: summary, RangeStart: rangeStart, RangeEnd: rangeEnd}
}

type appcmdSites struct {
	Sites []appcmdSite `xml:"SITE"`
}

type appcmdSite struct {
	Name     string `xml:"SITE.NAME,attr"`
	State    string `xml:"state,attr"`
	Bindings struct {
		Items []appcmdBinding `xml:"binding"`
	} `xml:"bindings"`
}

type appcmdBinding struct {
	Information string `xml:"bindingInformation,attr"`
}

func parseIISBindingPorts(data []byte, rangeStart, rangeEnd int) ([]int, error) {
	var response appcmdSites
	if err := xml.Unmarshal(data, &response); err != nil {
		return nil, err
	}
	ports := map[int]struct{}{}
	for _, site := range response.Sites {
		for _, binding := range site.Bindings.Items {
			information := strings.TrimSuffix(strings.TrimSpace(binding.Information), ":")
			if information == "" {
				return nil, errors.New("IIS binding has no bindingInformation")
			}
			separator := strings.LastIndexByte(information, ':')
			if separator < 0 {
				return nil, errors.New("IIS binding has no port component")
			}
			port, err := strconv.Atoi(information[separator+1:])
			if err != nil || port < 1 || port > 65535 {
				return nil, errors.New("IIS binding has an invalid port")
			}
			if port >= rangeStart && port <= rangeEnd {
				ports[port] = struct{}{}
			}
		}
	}
	return sortedPorts(ports), nil
}

func parseNetstatPorts(data []byte, rangeStart, rangeEnd int) ([]int, error) {
	ports := map[int]struct{}{}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || !(strings.EqualFold(fields[0], "TCP") || strings.EqualFold(fields[0], "TCPv6")) {
			continue
		}
		localEndpoint := fields[1]
		separator := strings.LastIndexByte(localEndpoint, ':')
		if separator < 0 {
			return nil, fmt.Errorf("TCP endpoint %q has no port", localEndpoint)
		}
		port, err := strconv.Atoi(strings.Trim(localEndpoint[separator+1:], "[]"))
		if err != nil || port < 1 || port > 65535 {
			return nil, fmt.Errorf("TCP endpoint %q has an invalid port", localEndpoint)
		}
		if port >= rangeStart && port <= rangeEnd {
			ports[port] = struct{}{}
		}
	}
	return sortedPorts(ports), nil
}

func sortedPorts(ports map[int]struct{}) []int {
	result := make([]int, 0, len(ports))
	for port := range ports {
		result = append(result, port)
	}
	// Sorting isn't needed to pick a port, but it makes helper outputs deterministic in tests.
	for i := 1; i < len(result); i++ {
		for j := i; j > 0 && result[j] < result[j-1]; j-- {
			result[j], result[j-1] = result[j-1], result[j]
		}
	}
	return result
}

func appCmdPath() string {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = os.Getenv("windir")
	}
	if root == "" {
		root = `C:\Windows`
	}
	return filepath.Join(root, "System32", "inetsrv", "appcmd.exe")
}

func netstatPath() string {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = os.Getenv("windir")
	}
	if root == "" {
		root = `C:\Windows`
	}
	return filepath.Join(root, "System32", "netstat.exe")
}

type StartResult struct {
	Status      string `json:"status"`
	Environment string `json:"environmentName"`
	StartedBy   string `json:"startedBy"`
	ProcessID   int    `json:"processId,omitempty"`
	IISSite     string `json:"iisSite,omitempty"`
	IISAppPool  string `json:"iisAppPool,omitempty"`
	PingWarning string `json:"pingWarning,omitempty"`
	Summary     string `json:"summary"`
}

type StartOptions struct {
	Home            string
	EnvironmentName string
	Platform        string
	Runner          Runner
	HTTPClient      *http.Client
	Sleep           func(context.Context, time.Duration) error
	Progress        func(progress, total float64, message string) error
}

type settingsFile struct {
	ActiveEnvironmentKey string                       `json:"ActiveEnvironmentKey"`
	Environments         map[string]environmentRecord `json:"Environments"`
}

type environmentRecord struct {
	URI             string `json:"Uri"`
	EnvironmentPath string `json:"EnvironmentPath"`
}

// StartCreatio resolves a registered local environment, then starts its IIS site through appcmd or
// launches the Creatio .NET host process through dotnet. The latter runtime belongs to Creatio itself;
// this tool has no .NET client/helper dependency.
func StartCreatio(ctx context.Context, options StartOptions) (StartResult, error) {
	platform := options.Platform
	if platform == "" {
		platform = runtime.GOOS
	}
	runner := options.Runner
	if runner == nil {
		runner = OSRunner{}
	}
	home := options.Home
	if home == "" {
		var err error
		home, err = resolveClioHome(platform)
		if err != nil {
			return StartResult{}, err
		}
	}
	settings, err := readSettings(filepath.Join(home, "appsettings.json"))
	if err != nil {
		return StartResult{}, err
	}
	environmentName, environment, err := resolveEnvironment(settings, options.EnvironmentName)
	if err != nil {
		return StartResult{}, err
	}
	if options.Progress != nil {
		if err := options.Progress(1, 3, "Obtained environment to start"); err != nil {
			return StartResult{}, err
		}
	}
	if strings.TrimSpace(environment.EnvironmentPath) == "" {
		return StartResult{}, fmt.Errorf("environment %q has no EnvironmentPath", environmentName)
	}
	info, err := os.Stat(environment.EnvironmentPath)
	if err != nil || !info.IsDir() {
		return StartResult{}, fmt.Errorf("environment path is missing or is not a directory")
	}

	result := StartResult{Status: "started", Environment: environmentName}
	if platform == "windows" {
		site, findErr := findIISSiteByPath(ctx, runner, environment.EnvironmentPath)
		if findErr != nil {
			return StartResult{}, fmt.Errorf("could not inspect IIS configuration: %w", findErr)
		}
		if site != nil {
			if err := startIISSite(ctx, runner, site); err != nil {
				return StartResult{}, err
			}
			result.StartedBy, result.IISSite, result.IISAppPool = "iis", site.Name, site.AppPool
		} else {
			result, err = startDotnetHost(runner, environmentName, environment.EnvironmentPath)
			if err != nil {
				return StartResult{}, err
			}
		}
	} else {
		result, err = startDotnetHost(runner, environmentName, environment.EnvironmentPath)
		if err != nil {
			return StartResult{}, err
		}
	}
	result.Environment = environmentName
	if options.Progress != nil {
		if err := options.Progress(2, 3, "Started IIS site or Creatio host process"); err != nil {
			return StartResult{}, err
		}
	}

	if strings.TrimSpace(environment.URI) != "" {
		sleep := options.Sleep
		if sleep == nil {
			sleep = sleepContext
		}
		if err := sleep(ctx, 2*time.Second); err != nil {
			return StartResult{}, err
		}
		httpClient := options.HTTPClient
		if httpClient == nil {
			httpClient = &http.Client{Timeout: 30 * time.Second}
		}
		if err := ping(ctx, httpClient, environment.URI); err != nil {
			result.PingWarning = "Site started but ping did not confirm readiness: " + err.Error()
		}
	}
	if options.Progress != nil {
		if err := options.Progress(3, 3, "Start operation complete"); err != nil {
			return StartResult{}, err
		}
	}
	result.Summary = fmt.Sprintf("Creatio environment %q was started through %s.", environmentName, result.StartedBy)
	return result, nil
}

func resolveClioHome(platform string) (string, error) {
	if home := strings.TrimSpace(os.Getenv("CLIO_HOME")); home != "" {
		return home, nil
	}
	var base string
	if platform == "windows" {
		base = os.Getenv("LOCALAPPDATA")
	} else {
		base = os.Getenv("HOME")
	}
	if base == "" {
		return "", errors.New("could not resolve clio settings directory; set CLIO_HOME")
	}
	return filepath.Join(base, "creatio", "clio"), nil
}

func readSettings(path string) (settingsFile, error) {
	file, err := os.Open(path)
	if err != nil {
		return settingsFile{}, fmt.Errorf("could not open clio settings file: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 4<<20))
	var settings settingsFile
	if err := decoder.Decode(&settings); err != nil {
		return settingsFile{}, fmt.Errorf("could not parse clio settings file: %w", err)
	}
	if len(settings.Environments) == 0 {
		return settingsFile{}, errors.New("clio settings contain no registered environments")
	}
	return settings, nil
}

func resolveEnvironment(settings settingsFile, requested string) (string, environmentRecord, error) {
	if requested == "" {
		requested = settings.ActiveEnvironmentKey
	}
	if requested != "" {
		for name, environment := range settings.Environments {
			if strings.EqualFold(name, requested) {
				return name, environment, nil
			}
		}
		return "", environmentRecord{}, fmt.Errorf("environment %q is not registered", requested)
	}
	return "", environmentRecord{}, errors.New("no active clio environment is configured")
}

type iisSite struct {
	Name      string
	State     string
	AppPool   string
	PoolState string
}

func findIISSiteByPath(ctx context.Context, runner Runner, environmentPath string) (*iisSite, error) {
	output, err := runner.Output(ctx, appCmdPath(), "list", "sites", "/xml")
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// Clio treats a missing/unusable IIS command as "no matching site" and then tries dotnet.
		// Preserve that fallback while still surfacing cancellation and malformed successful output.
		return nil, nil
	}
	if strings.TrimSpace(string(output)) == "" {
		return nil, nil
	}
	var sites appcmdSites
	if err := xml.Unmarshal(output, &sites); err != nil {
		return nil, err
	}
	for _, candidate := range sites.Sites {
		if candidate.Name == "" {
			continue
		}
		physical, err := runner.Output(ctx, appCmdPath(), "list", "vdir", candidate.Name+"/", "/text:physicalPath")
		if err != nil || !windowsPathsOverlap(strings.TrimSpace(string(physical)), environmentPath) {
			continue
		}
		appOutput, err := runner.Output(ctx, appCmdPath(), "list", "app", candidate.Name+"/", "/xml")
		if err != nil {
			return nil, err
		}
		pool := appPoolFromXML(appOutput)
		if pool == "" {
			return nil, fmt.Errorf("IIS site %q has no application pool", candidate.Name)
		}
		poolOutput, err := runner.Output(ctx, appCmdPath(), "list", "apppool", pool, "/xml")
		if err != nil {
			return nil, err
		}
		return &iisSite{Name: candidate.Name, State: candidate.State, AppPool: pool, PoolState: appPoolStateFromXML(poolOutput)}, nil
	}
	// Clio also considers IIS applications beneath a site, not just each site's root vdir.
	appsOutput, err := runner.Output(ctx, appCmdPath(), "list", "app", "/xml")
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, nil
	}
	if strings.TrimSpace(string(appsOutput)) == "" {
		return nil, nil
	}
	var apps appcmdApps
	if err := xml.Unmarshal(appsOutput, &apps); err != nil {
		return nil, err
	}
	for _, app := range apps.Apps {
		if app.Name == "" || !strings.Contains(app.Name, "/") || strings.HasSuffix(app.Name, "/") {
			continue
		}
		parts := strings.SplitN(app.Name, "/", 2)
		siteName := parts[0]
		physical, err := runner.Output(ctx, appCmdPath(), "list", "vdir", app.Name+"/", "/text:physicalPath")
		if err != nil || !windowsPathsOverlap(strings.TrimSpace(string(physical)), environmentPath) {
			continue
		}
		state := "Unknown"
		for _, candidate := range sites.Sites {
			if candidate.Name == siteName {
				state = candidate.State
				break
			}
		}
		if app.AppPool == "" {
			return nil, fmt.Errorf("IIS application %q has no application pool", app.Name)
		}
		poolOutput, err := runner.Output(ctx, appCmdPath(), "list", "apppool", app.AppPool, "/xml")
		if err != nil {
			return nil, err
		}
		return &iisSite{Name: siteName, State: state, AppPool: app.AppPool, PoolState: appPoolStateFromXML(poolOutput)}, nil
	}
	return nil, nil
}

type appcmdApps struct {
	Apps []struct {
		Name    string `xml:"APP.NAME,attr"`
		AppPool string `xml:"APPPOOL.NAME,attr"`
	} `xml:"APP"`
}

type appcmdApp struct {
	AppPool string `xml:"APPPOOL.NAME,attr"`
}

type appcmdAppRoot struct {
	App appcmdApp `xml:"APP"`
}

type appcmdPoolRoot struct {
	Pool struct {
		State string `xml:"state,attr"`
	} `xml:"APPPOOL"`
}

func appPoolFromXML(data []byte) string {
	var root appcmdAppRoot
	_ = xml.Unmarshal(data, &root)
	return root.App.AppPool
}

func appPoolStateFromXML(data []byte) string {
	var root appcmdPoolRoot
	_ = xml.Unmarshal(data, &root)
	return root.Pool.State
}

func windowsPathsOverlap(first, second string) bool {
	first = normalizeWindowsPath(first)
	second = normalizeWindowsPath(second)
	if first == "" || second == "" {
		return false
	}
	separator := "\\"
	return first == second || strings.HasPrefix(first, second+separator) || strings.HasPrefix(second, first+separator)
}

func normalizeWindowsPath(value string) string {
	value = strings.ReplaceAll(strings.TrimSpace(value), "/", "\\")
	value = strings.TrimRight(value, "\\")
	return strings.ToLower(value)
}

func startIISSite(ctx context.Context, runner Runner, site *iisSite) error {
	if site.PoolState != "Started" {
		if site.PoolState == "" || strings.EqualFold(site.PoolState, "NotFound") {
			return fmt.Errorf("IIS application pool %q could not be resolved", site.AppPool)
		}
		if output, err := runner.Output(ctx, appCmdPath(), "start", "apppool", site.AppPool); err != nil {
			return fmt.Errorf("could not start IIS application pool %q: %w", site.AppPool, err)
		} else if !appcmdSucceeded(output) {
			return fmt.Errorf("could not start IIS application pool %q", site.AppPool)
		}
	}
	if site.State != "Started" {
		if output, err := runner.Output(ctx, appCmdPath(), "start", "site", site.Name); err != nil {
			return fmt.Errorf("could not start IIS site %q: %w", site.Name, err)
		} else if !appcmdSucceeded(output) {
			return fmt.Errorf("could not start IIS site %q", site.Name)
		}
	}
	return nil
}

func appcmdSucceeded(output []byte) bool {
	text := strings.TrimSpace(string(output))
	return text != "" && !strings.Contains(strings.ToUpper(text), "ERROR")
}

func startDotnetHost(runner Runner, environmentName, environmentPath string) (StartResult, error) {
	dll := filepath.Join(environmentPath, "Terrasoft.WebHost.dll")
	if _, err := os.Stat(dll); err != nil {
		return StartResult{}, fmt.Errorf("Terrasoft.WebHost.dll was not found in the registered environment")
	}
	pid, err := runner.Start("dotnet", []string{"Terrasoft.WebHost.dll"}, environmentPath)
	if err != nil {
		return StartResult{}, fmt.Errorf("could not start Creatio host process: %w", err)
	}
	return StartResult{Status: "started", Environment: environmentName, StartedBy: "process", ProcessID: pid}, nil
}

func ping(ctx context.Context, client *http.Client, baseURL string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/ping", nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1024))
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("HTTP %d", response.StatusCode)
	}
	return nil
}

func sleepContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
