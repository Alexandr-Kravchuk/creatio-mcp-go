# Business-rule write migration checkpoint

Implemented: `delete-entity-business-rules`, `delete-page-business-rules`.
Both tools use clio's captured contracts and confirmation gate, batch results,
case-insensitive parent-rule matching, duplicate-name refusal, child-rule/caption
cleanup, resource-key normalization and a single add-on save per batch. Schema
extension fields survive the save. Failed items do not prevent valid deletions;
a failed save marks every pending item failed. Post-save script cache reset and
static-content rebuild are best effort, as in clio.

Verification: HTTP integration tests cover entity/page target resolution, the
actual designer payload, partial batches, durable-save rejection, caption/child
cleanup and failed post-save refreshes. MCP tests cover direct-call refusal and
both executors. Four validation/environment cases match installed clio
8.1.0.134 exactly (`scripts/parity-cases/t10.json`).

Live deletion remains unverified: the disposable stand could not resolve through
DNS during this checkpoint. Successful business-rule writes refresh shared
client configuration, so their live success scenarios need the dedicated
verification window after the parallel write tests finish.

Remaining: `create-entity-business-rules`, `update-entity-business-rules`,
`create-page-business-rules`, `update-page-business-rules`. These require the full
simple-to-metadata converters, schema/lookup/formula validators and block-identity
preservation; they are not registered as placeholders.
