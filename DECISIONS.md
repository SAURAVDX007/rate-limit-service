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

## Lazy refill, not a background ticker
Tokens are computed on arrival from (now − LastRefill) × rate, not added by
a scheduled job. Ticker cost scales with buckets that exist; lazy cost
scales with requests served — and in production those differ by orders of
magnitude, with the gap widening as tenants are added.
Rejected: background refill goroutine. Beyond cost, it needs leader election
once there are multiple instances (otherwise every bucket refills N times
per second), it prevents TTL-based cleanup of idle keys, and its resolution
is capped by the tick interval.
Trade-off accepted: every allowed request performs a write. Still cheaper
than a ticker at any traffic level.

## New buckets start full
A missing key must be indistinguishable from a full bucket — that
equivalence is what makes TTL-based eviction of idle keys safe. Start at
anything less and a tenant's effective limit depends on whether Redis
happened to evict their key, which is invisible and non-deterministic.
Rejected: start empty (new tenants wait before their first request);
start at 50% (breaks the missing-key equivalence above).
If new tenants shouldn't get a full burst, that belongs in a different
*rule* for them, not in bucket initialisation.

