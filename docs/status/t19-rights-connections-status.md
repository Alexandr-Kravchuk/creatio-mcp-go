# T19: object rights, default record rules and connections (clio 8.1.0.142)

Six tools clio 8.1.0.142 added, ported from the 8.1.0.142 tag source:

| Tool | Go | Annotations (clio's `[McpServerTool]`) |
|---|---|---|
| `get-object-rights` | `tool_object_rights.go`, `internal/creatio/objectrights_get.go` | read-only, idempotent |
| `set-object-rights` | `tool_object_rights.go`, `internal/creatio/objectrights_set.go` | destructive, idempotent |
| `set-default-record-rights` | `tool_default_record_rights.go`, `internal/creatio/defaultrecordrights.go` | destructive, idempotent |
| `apply-default-record-rights` | `tool_default_record_rights.go`, `internal/creatio/defaultrecordrights.go` | destructive, not idempotent |
| `get-entity-connections` | `tool_entity_connections.go`, `internal/creatio/entityconnections.go` | read-only, idempotent |
| `update-entity-connections` | `tool_entity_connections.go`, `internal/creatio/entityconnections.go` | destructive, idempotent |

Shared code: `objectrights_support.go` (envelope, line log, request deadline, clio's display rules, SelectQuery),
`objectrights_client.go` (RightManagementService read/save, SysSchema/SysAdminUnit lookups, record count) and
`objectrights_planner.go` (ObjectRightsPlanner, ObjectRightsReadBackVerifier, DefaultRecordRightsPlanner).

## What is ported

- Arguments: clio's binder types (a wrong JSON type is clio's raised `invalid-parameter-type`; `timeout-seconds`
  also reads a numeric string, as clio's binder does), then the unknown-name refusal with each tool's own
  `Valid:` list, then the environment. The rights tools report a resolution failure as
  `[EnvironmentResolutionException] …` (redacted) and the Safe-environment refusal without the type, inside
  `{success,error}`; the connections tools check `entity-schema-name`, `package-name` and `connections` before
  the environment and raise the Safe-environment refusal.
- The commands' validation order and texts, the request deadlines of the MCP tools (30 s / 90 s for the read,
  25 s / 100 s for the writes, the save sent only while 50 s are left, the launch only while 25 s are left), the
  read-modify-write of `SaveAdministratedObject` (unchanged rows and every unknown field round-trip, the
  untouched collections are sent as null, the rule list is sent in full only when it changes), the read-back
  verification, `apply-default-record-rights`' launch (sent once), running-run warning and 2 s polling.
- Confirmation: clio 8.1.0.142 has no `confirm` argument on these tools (the host's approval of the call is the
  confirmation, `preview` is a dry run). A direct call by raw name of the four write tools answers
  `confirmation-required` through the existing gate, as clio does; through `clio-run` they run.
- Contracts: clio's `get-tool-contract` answers for the six tools (tool-names, default detail) are generated
  into `tool_*_contract.go` and served until `docs/clio-inventory.json` is regenerated from 8.1.0.142.

## Verification

- Unit tests: `internal/creatio/objectrights_test.go`, `entityconnections_test.go` (httptest stands; planner,
  read-back and record-planner rules; clio's `EntityConnectionsAddonMetadataTests.Build_*` cases with the server
  merge checked; sanitizer, deadline message), `cmd/creatio-mcp-go/tool_object_rights_test.go` (annotations,
  contracts, raw-name gate, argument refusals).
- No-stand comparison with the clio 8.1.0.142 dll (`dotnet clio.dll mcp-server`, an environment on a closed
  port and a Safe one): 151 calls (missing, wrong-typed, unknown and alias arguments, unknown and Safe
  environments, every validation branch, connection refused); identical except the known differences below.
- Fake stand `scripts/fake-stands/t19/` (stateful: saves are applied, so read-backs see them): 79 cases
  covering reads by code and title, connected objects, grants, revokes, enable/disable, every refusal, failed,
  unverified and error-but-applied saves, record-rule grants/revokes/duplicates/invalid levels, record-rights
  runs (completed, still running, refused, queued, unreadable answer), connections reads, writes, fallback to
  the EntityConnection table and an add-on the server ignores. All 79 answers are identical to clio 8.1.0.142,
  and the two servers sent identical requests on all 10 routes.
- `scripts/write-scenarios/t19-rights.json` (create-app, create-lookup, the rights and connections calls,
  delete-app clean-up) validates with `--plan-only`; it has not run on a stand.

## Known differences

- A call with no `environment-name`: clio 8.1.0.142 refuses it ("Either a configured environment name or an
  explicit URI is required …"); this server uses its default target, as it does for every tool
  (`environments.go`). clio's `get-record-rights` and `get-related-page-addon` answer the same refusal.
- Unknown argument names are listed sorted (Go maps carry no request order); clio lists them in request order.
- `timeout-seconds: 5.0` is refused by clio as not a number; here it reads as 5 (the JSON decoder does not keep
  the literal).
- A direct call with flat arguments (no `args` wrapper) runs here; clio refuses it. Shared behaviour.
- Transport failures other than a refused connection, a timeout and a non-JSON body keep Go's error text.

## Not done

- `docs/clio-inventory.json` / `internal/cliocontract` regeneration (another stream). Until then
  `TestEveryServedToolHasClioContract`, `TestNoServedToolTakesAnArgsArgument` and
  `TestGetToolContractServesClioContracts` fail for these six tools only.
- A live run of the write scenario on the disposable stand.
