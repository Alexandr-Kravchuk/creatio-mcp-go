package main

// knowledgeServerInstructions is the text clio sends as "instructions" in its initialize answer
// (McpServerInstructions.Text), byte for byte, CRLF line ends included: a thin pointer that makes
// get-guidance core-rules and routing mandatory before any operation.
const knowledgeServerInstructions = "clio is the CLI + MCP server for the Creatio low-code platform (environments, packages, schemas, apps).\r\n" +
	"Most tools need a registered environment-name (list-environments / reg-web-app). Read docs://help/command/{name} for any command.\r\n" +
	"\r\n" +
	"## ALWAYS read these FIRST (mandatory on every operation; skipping either is a defect, not a shortcut)\r\n" +
	"- get-guidance name=core-rules - the non-negotiable invariants (compile/restart, long-running await, profile culture, destructive confirmation, verification in browser preference, correlation-id); apply them to every action.\r\n" +
	"- get-guidance name=routing - the routing map from the task (pages, entities, data, applications) to the exact guide(s) to read before acting.\r\n" +
	"An unknown name returns availableGuides.\r\n" +
	"\r\n" +
	"## Product telemetry (for AI-assisted Creatio work you do here; skip only for non-agent use - plain scripts or CI)\r\n" +
	"Note the scope: an agent doing Creatio work through this server is IN scope even when no skill file is loaded. \"No skill loaded\" is not \"ad-hoc use\".\r\n" +
	"get-telemetry-consent (read-only consent check) -> send-telemetry (stores one event once consent is granted) -> withdraw-telemetry-consent (stop and discard the local outbox).\r\n" +
	"Telemetry applies to EVERY Creatio workflow, not just app creation. Event names are flow-agnostic stages; WHICH flow it was travels in the `workflow` field (for example classic-to-freedom-migration, mobile-page-conversion, branding, app-creation, app-maintenance). A migration or branding run is not exempt just because it skips the app-creation gates - it emits the same stages with its own `workflow` value.\r\n" +
	"Call get-tool-contract for the authoritative event_name list, payload shape and emission order. Emission points are owned by the skill/contract when one is active; the CONSENT PROMPT is yours either way - when get-telemetry-consent reads unknown, ask the developer yourself and persist their answer once, and until you have a real answer send without telemetry_consent (a telemetry-consent-required rejection is the expected reply, not a failure)."
