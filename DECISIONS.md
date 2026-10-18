# Decisions

## Redis as shared state, not Postgres
Chose Redis. Workload is tiny read-modify-writes on single keys with no
durability requirement — losing 200ms of counter state costs a few extra
requests. Postgres MVCC turns a hot counter row into continuous dead-tuple
churn for autovacuum, and row locks serialise contention. Decisive factor:
Redis runs Lua server-side and atomically, which is the mechanism that makes
the decision indivisible.
Rejected: Postgres (MVCC/WAL overhead for data we don't need durable).
Accepted cost: async replication means a failover can roll back recent
increments. Acceptable for rate limiting; would not be for billing.
In production I'd keep *rules* in Postgres (low-volume, want schema and
durability) and counters in Redis. Both are in Redis here to avoid running
two datastores for a 12-rule table.

## Lua over WATCH-retry and distributed locks
Redis single-threading makes each *command* atomic, not a sequence of them.
GET → decide in Go → SET leaves a gap where another instance reads the same
stale value. Lua moves the whole read-decide-write inside Redis's single
thread, so it is one indivisible unit.
Rejected: MULTI/EXEC (cannot branch on a value read inside the transaction);
WATCH + optimistic retry (correct, but retries pile up on hot keys and the
loop lives in app code); distributed lock (extra round trips, plus handling a
holder that dies mid-decision).

## Standard library HTTP router
Go 1.22+ supports method+path patterns natively. No chi/gin/echo — four
endpoints don't justify a framework dependency.

## Config via environment variables
Containers and PaaS expect env vars; no parser, no mounted file, defaults
make `go run` work with zero setup.

## Health check pings Redis
Deep check: reports unhealthy when it genuinely cannot serve. Known risk —
couples instance health to a shared dependency, so a Redis hiccup can pull
every instance from rotation at once. Proper fix is splitting /livez
(shallow) from /readyz (deep); noted as future work.