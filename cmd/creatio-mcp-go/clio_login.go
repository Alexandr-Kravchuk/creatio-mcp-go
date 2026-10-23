package main

import (
	"context"

	"github.com/Alexandr-Kravchuk/creatio-mcp-go/internal/creatio"
)

// clioLogsInPerCall names the tools whose clio 8.1.0.142 counterpart logs in again on every call instead of
// reusing the session the way clio's other tools do (measured against a local stand: each of two calls posted
// a login; list-pages, list-app-sections and execute-esq run in clio's worker processes). A host that goes
// away is met by that login, so these tools report a connect fault with the login's wording even while this
// server holds a session for the environment.
var clioLogsInPerCall = map[string]bool{
	"compile-status": true, "execute-esq": true, "get-app-info": true, "get-component-info": true,
	"get-fsm-mode": true, "get-process-signature": true, "get-request-info": true, "get-schema-name-prefix": true,
	"get-sys-setting": true, "get-target-package": true, "get-user-culture": true, "list-app-sections": true,
	"list-apps": true, "list-pages": true, "list-sys-settings": true,
}

// withClioLogin marks the call of a tool in clioLogsInPerCall.
func withClioLogin(ctx context.Context, tool string) context.Context {
	if clioLogsInPerCall[tool] {
		return creatio.WithLoginBeforeEachCall(ctx)
	}
	return ctx
}
