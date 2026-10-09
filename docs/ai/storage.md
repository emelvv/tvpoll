# AI artifact: PostgreSQL storage implementation

This file records the storage subtask assigned to an AI coding agent and the
reasoning that informed the implementation. It is part of the submitted source.

## Prompt received

Implement only `internal/store/*` and its tests for the Go service. Use existing
`internal/model` types and pgx/v5. Provide `New`, `Close`, `Ping`, `CreatePoll`,
`GetPoll`, `ListPolls`, `WriteBatch`, `Results`, and `Shards`. Persist immutable
poll metadata and idempotent creation in a control PostgreSQL database. Persist
votes in independently configured PostgreSQL shards. Embed startup migrations,
serialized using transaction advisory locks. Insert ballots with `ON CONFLICT
DO NOTHING RETURNING`; update per-lane aggregate counters atomically and only
for newly inserted ballots. Exactly one concurrent duplicate must be accepted.
Results must sum every shard in parallel and fail if any shard fails. Include
optional real-PostgreSQL integration tests for concurrency, retries, multi-choice
votes, idempotency conflicts, persistence, and shard unavailability. Do not send
messages to external services or people.

Follow-up review instruction: detect when distinct shard URLs refer to the same
physical database by persisting a random singleton shard UUID during migration,
reject repeated identities in `New`, and add a real-database alias test.

## Decisions and review notes

- Canonical creation JSON excludes the generated ID, normalizes timestamps to
  UTC, and retains option ordering. SHA-256 identifies the payload associated
  with each unique idempotency key. Key reservation and metadata insertion share
  a transaction; a deferred foreign key prevents dangling successful reservations.
- A conflicting key insert waits for its winner. A **subsequent** READ COMMITTED
  statement reads the committed winner; reading it from the same statement's
  snapshot would be incorrect under a race.
- `WriteBatch` first chooses the first ordinal for repeated `(poll_id, digest)`
  input, then orders receipt inserts by key. This gives deterministic selections
  and avoids opposite insert orders deadlocking concurrent batches.
- A data-modifying CTE increments counters from `INSERT ... RETURNING` alone.
  It does not attempt to distinguish duplicates by selecting an existing vote
  in the same CTE. Total and selected-option increments commit together with
  receipts; replay cannot increment counters a second time.
- Worker-specific lanes distribute counter row contention. Counter keys are
  updated in a stable order. The total uses `option_index = -1`; selections use
  bits 0–31. There is no cross-database foreign key or per-vote control query.
- Every anonymous voter must be routed consistently to one shard by the service.
  Changing shard count or the routing secret during an active poll can violate
  deduplication and requires an explicit migration plan.
- Pool limits apply per database, so deployment capacity planning must multiply
  by shard count and API replica count. Queries use caller contexts and deadlines.
- Review feedback identified that distinct connection URLs can still address one
  physical database. Each shard now persists a singleton random UUID identity;
  startup rejects repeated identities before accepting traffic. A cloned shard
  retains its identity and must receive a deliberately new identity before it is
  added as an independent empty shard. Connection pools must consistently reach
  the configured database; a load balancer mixing independent databases is invalid.
- Live results are a sum of reads taken at different shard instants. They are
  not advertised as a globally synchronized snapshot. Any shard failure produces
  an error instead of a plausible but incomplete total.
- Integration tests run only when both `TEST_CONTROL_DATABASE_URL` and
  `TEST_SHARD_DATABASE_URLS` are set. Their cleanup deletes only UUIDs created by
  the test fixture, never truncates a shared database, and never prints URLs.

## Validation record

The parent task records the final commands and test outcomes in the repository's
main AI work log. The storage tests explicitly exercise 24 racing poll creators,
16 racing vote batches with reversed input order, duplicate ballots within one
batch, changed-payload creation conflicts, multi-choice bit 31, persisted counts
after pool replacement, rejected/cancelled writes, rollback of receipts after a
counter overflow fails the SQL statement, canonical uppercase UUID handling,
all-or-error behavior for an unavailable shard, and rejection of different URLs
reaching the same database.

## Primary sources used

- PostgreSQL documentation, INSERT and `ON CONFLICT`:
  <https://www.postgresql.org/docs/current/sql-insert.html>
- PostgreSQL documentation, READ COMMITTED transaction isolation:
  <https://www.postgresql.org/docs/current/transaction-iso.html>
- PostgreSQL documentation, data-modifying CTEs:
  <https://www.postgresql.org/docs/current/queries-with.html>
- pgx pool API:
  <https://pkg.go.dev/github.com/jackc/pgx/v5/pgxpool>

These are design references, not claims that the small local test setup measures
or proves the required nationwide peak throughput.
