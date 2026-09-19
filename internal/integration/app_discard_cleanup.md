# Prepared artifacts after explicit discard

`CleanupDiscardedAppInboxArtifacts(ctx, inbox, artifacts, projectID, receiptID)`
is a bounded, best-effort follow-up to a **successfully committed** operator
discard. The concrete dependencies are `store.Integrations()` and
`store.Artifacts()`, with the latter configured with the existing blob store.
The helper does not discard receipts or change their plans/progress. Callers
report cleanup failures as warnings while preserving successful discard.

The helper re-reads the receipt and requires `discarded`. Candidate agent/artifact
IDs come from its frozen plan, including uploads that finished before preparation
progress was saved. Every committed slot is skipped, including committed inputs
in a mixed receipt with an abandoned launch. A key belonging to a committed slot
is protected even if another slot also lists it. Failed receipts, including
partial failures retained for retry, are never cleaned by this helper.

`artifactstore.DeleteUnreferencedPreparedArtifact` checks agent ownership and the
existing durable artifact read before deleting the ordinary artifact object key.
Any artifact row preserves its object, including history under archived/deleted
scopes. Database errors prevent deletion; missing objects are harmless. Discard
fences future admission for these frozen IDs, so no database transaction spans
object-store I/O. An admission failure or expired lease alone is insufficient.

The pass has a 30-second deadline and attempts remaining candidates after an
individual deletion failure. It can be repeated while the receipt remains stored.
A process crash, missing blob configuration, failed deletion or stale uploader
finishing after cleanup can leave unused objects behind. This is not automatic
garbage collection or a guaranteed object-retention period. Seven-day terminal
receipt cleanup and deleted-scope cleanup remove database receipts/plans only;
they do not delete blobs and can remove the inventory needed for another pass.

`app_discard_cleanup_integration_test.go` exercises real receipt/discard and
artifact stores against PostgreSQL with an in-memory blob store. It covers
upload-before-Prepare, failed receipt retry, mixed committed-input/failed-launch
discard, durable references, missing objects, deletion failure/retry and failed
database reads.
