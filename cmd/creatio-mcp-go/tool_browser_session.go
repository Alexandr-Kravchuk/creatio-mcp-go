package main

// get-browser-session and clear-browser-session: clio 8.1.0.142's GetBrowserSessionTool and
// ClearBrowserSessionTool over BrowserSessionService (internal/browsersession). The session is the Playwright
// storageState file clio keeps under <clio settings folder>/sessions; its cookie values never reach the answer.

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/browsersession"
	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// BrowserSessionResult is GetBrowserSessionResult; a null session-file-path is left out, as clio leaves it.
type BrowserSessionResult struct {
	Success         bool    `json:"success"`
	SessionFilePath *string `json:"session-file-path,omitempty"`
	Error           string  `json:"error,omitempty"`
}

// ClearBrowserSessionResult is ClearBrowserSessionResult.
type ClearBrowserSessionResult struct {
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
	Error   string `json:"error,omitempty"`
}

const clearBrowserSessionFailure = "Failed to clear the browser session."

// browserSessionTransport is the transport of the session's login; tests replace it.
var browserSessionTransport = creatio.RegistrationTransport

// browserSessionGate serializes the two tools on the cache inside this process (clio gates them on the
// browser-session-cache file resource); clio's writes are last-write-wins and atomic across processes.
var browserSessionGate sync.Mutex

func init() {
	registerTool(map[string]any{"name": "get-browser-session"}, invokeGetBrowserSession,
		withAnnotations(toolAnnotations{ReadOnly: false, Destructive: false, Idempotent: false, OpenWorld: false}))
	registerTool(map[string]any{"name": "clear-browser-session"}, invokeClearBrowserSession,
		withAnnotations(toolAnnotations{ReadOnly: false, Destructive: true, Idempotent: true, OpenWorld: false}))
}

// browserSessionEnvironment resolves the environment the way the tools' command resolver does: a blank name
// names no target, an unknown one is clio's not-found failure, a Safe one is refused (safe = true).
func browserSessionEnvironment(envs *environments, name string) (browsersession.Environment, error, bool) {
	if strings.TrimSpace(name) == "" {
		return browsersession.Environment{}, errors.New(missingTargetMessage), false
	}
	settings, _ := envs.snapshot()
	found, ok := settings.Find(name)
	if !ok {
		return browsersession.Environment{}, errors.New(environmentNotFoundMessage(name, settings)), false
	}
	if found.Safe != nil && *found.Safe {
		return browsersession.Environment{}, safeEnvironmentError(found.URI), true
	}
	pw := found.Password
	return browsersession.Environment{URI: found.URI, Login: found.Login, Password: pw, ClientID: found.ClientID,
		IsNetCore: found.IsNetCore}, nil, false
}

func invokeGetBrowserSession(ctx context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
	name, err := optionalStringArg(args, "get-browser-session", "environment-name")
	if err != nil {
		return nil, err
	}
	forceRefresh, err := requiredBoolArg(args, "get-browser-session", "force-refresh")
	if err != nil {
		return nil, err
	}
	environment, failure, safe := browserSessionEnvironment(envs, name)
	if failure != nil {
		message := failure.Error()
		if !safe {
			message = "Failed to obtain the browser session: " + message
		}
		return structuredToolResult(BrowserSessionResult{Error: message}), nil
	}
	browserSessionGate.Lock()
	defer browserSessionGate.Unlock()
	service := browsersession.Service{Env: environment, Transport: browserSessionTransport, ClioHome: filepath.Dir(envs.settingsPath())}
	path, err := service.SessionPath(ctx, forceRefresh)
	switch {
	case err == nil:
		return structuredToolResult(BrowserSessionResult{Success: true, SessionFilePath: &path}), nil
	case ctx.Err() != nil:
		return nil, ctx.Err()
	case browsersession.IsAuthenticationError(err):
		return structuredToolResult(BrowserSessionResult{Error: err.Error()}), nil
	}
	return structuredToolResult(BrowserSessionResult{Error: "Failed to obtain the browser session: " + err.Error()}), nil
}

func invokeClearBrowserSession(_ context.Context, envs *environments, args map[string]any) (*mcp.CallToolResult, error) {
	name, err := optionalStringArg(args, "clear-browser-session", "environment-name")
	if err != nil {
		return nil, err
	}
	environment, failure, safe := browserSessionEnvironment(envs, name)
	if failure != nil {
		if safe {
			return structuredToolResult(ClearBrowserSessionResult{Error: failure.Error()}), nil
		}
		return structuredToolResult(ClearBrowserSessionResult{Error: clearBrowserSessionFailure}), nil
	}
	browserSessionGate.Lock()
	defer browserSessionGate.Unlock()
	service := browsersession.Service{Env: environment, ClioHome: filepath.Dir(envs.settingsPath())}
	if err := service.Clear(); err != nil {
		return structuredToolResult(ClearBrowserSessionResult{Error: clearBrowserSessionFailure}), nil
	}
	return structuredToolResult(ClearBrowserSessionResult{Success: true, Message: "Browser session for '" + name + "' cleared."}), nil
}
