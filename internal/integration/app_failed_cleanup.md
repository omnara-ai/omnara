# Prepared artifacts after terminal failure

`CleanupFailedAppInboxArtifacts` is a bounded, best-effort follow-up to a
successfully committed failure. The worker calls it through the inbox consumer;
it never changes the receipt or undoes failure. Database retention is separate.

Candidate agent/artifact IDs come from the frozen plan, including uploads that
finished before preparation progress was saved. Committed slots are skipped.
`artifactstore.DeleteUnreferencedPreparedArtifact` verifies agent ownership and
preserves every durable artifact reference, including archived history. Database
errors prevent deletion, and missing objects are harmless. Failed receipts cannot
resume admission, so no transaction needs to span object-store I/O.

The pass has a 30-second deadline and continues after individual deletion errors.
A crash, missing blob configuration, failed deletion or stale upload finishing
later can leave unused objects. Recovery SQL does not run this provider/blob pass.
Seven-day receipt cleanup deletes database records only; this helper is not a
general garbage collector or a guarantee of blob retention.

`app_failed_cleanup_integration_test.go` covers uploads before preparation,
partial successes, durable references, missing objects and failed deletion/reads.
