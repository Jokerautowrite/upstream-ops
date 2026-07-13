# Sub2 Account Pool Module

This fork adds an isolated Sub2 account-pool operations module on top of
UpstreamOps v0.0.6.

## Scope

- Reuses the encrypted Sub2 target configured by the built-in upstream-sync
  module.
- Shows the current Sub2 account pool, health, balance, upstream multiplier,
  lowest group, current priority, and suggested priority.
- Matches upstream data by the full SHA-256 API-key fingerprint and normalized
  URL. An exact key match is the primary multiplier source.
- Optionally imports an explicit legacy mapping of `Sub2 account ID + normalized
  URL + UpstreamOps model name` for accounts without a usable key match. It
  never derives a multiplier from an account name, never overrides an exact key
  match, and never falls back after a key mismatch or ambiguous key match.
- Only `apikey` accounts with `credentials.pool_mode=true` participate in
  automatic priority writes.
- Uses unique priorities in steps of `10`; lower upstream multipliers come
  first and debt accounts come last.
- Missing multiplier or balance data is skipped and reported instead of
  stopping other eligible accounts.
- Existing `sub2_pool_changed` subscriptions remain compatible with combined
  account-pool notifications.
- Verified priority outcomes also support explicit opt-in events:
  `sub2_pool_priority_applied` and `sub2_pool_priority_failed`. Their messages
  never include missing-data, balance, guard, or rate-change sections.
- Snapshots expose sanitized `stop_source`, `stop_reason`, and `stop_time`
  fields without changing the writable Sub2 admin-account request model.

## Safety

- Automation is disabled per target until explicitly enabled in the UI.
- Every account-pool API requires a valid administrator token. If global
  authentication is disabled, the module returns `403 auth_required`.
- Preview signatures are checked again immediately before writes.
- Account count, healthy-account drop, unknown channel, duplicate target, and
  maximum-change guards fail closed.
- Priority and schedulable updates use the narrow Sub2 Admin API endpoints and
  are verified before and after each write.
- Priority changes use a persisted `old -> staging -> final` transition. The
  staging priorities are unique and above all current/final values, so swaps
  never expose duplicate priorities and can resume safely after a restart.
- Prepared runs, target state, notifications, and target leases are persisted
  so a process restart does not blindly replay an already completed write.
- Priority write, re-read, and verification failures are queued with stable
  stage/code values and no raw upstream response body. A failed prepared run
  remains recoverable; notification delivery retries only the outbox event.
- Stop reasons are sanitized before persistence: controls are removed, URLs
  and common credential forms are redacted, and output is limited to 512
  UTF-8-safe bytes. Stale reasons on schedulable accounts and expired temporary
  reasons are omitted.
- Six additive SQLite/MySQL tables are created on startup:
  `sub2_pool_target_states`, `sub2_pool_outbox`, `sub2_pool_runs`, and
  `sub2_pool_automation`, plus `sub2_pool_leases` and
  `sub2_pool_account_rate_mappings`.
- Importing a legacy map writes only `sub2_pool_account_rate_mappings` in the
  local UpstreamOps database. It does not create, edit, schedule, or delete a
  Sub2 account or API key.

## Configuration

```dotenv
SUB2_POOL_MIN_ACCOUNT_COUNT=20
SUB2_POOL_MAX_CHANGES=20
SUB2_POOL_LOW_BALANCE_THRESHOLD=10

# Optional one-time migration. These two values must be set together.
SUB2_POOL_ACCOUNT_RATE_MAP_IMPORT_PATH=/app/data/legacy-account-rate-map.json
SUB2_POOL_ACCOUNT_RATE_MAP_IMPORT_TARGET_ID=1
```

The import file is JSON with an `accounts` object. Each entry key is the Sub2
account ID and each value must include `site_url` and `model`; `rate` is an
optional manual fallback when the current monitored snapshots disagree or are
missing. The import is all-or-nothing, runs only when that target has no stored
mappings, and treats existing database rows as authoritative on later starts.

## Upgrade Workflow

1. Rebase the fork on the new official UpstreamOps version.
2. Resolve the small bootstrap hooks in `cmd/server/main.go`,
   `backend/scheduler/scheduler.go`, and the frontend route/header files.
3. Run `go test ./...`, frontend typecheck, lint, and production build.
   If `proxy.golang.org` is unreachable in the build environment, pass
   `--build-arg GOPROXY=https://goproxy.cn,direct`; the default remains the
   official Go proxy.
4. Set `IMAGE_REPOSITORY=ghcr.io/jokerautowrite/upstream-ops` and pin an
   immutable `sha-*` tag built by the fork workflow.
5. Back up the database and current image before recreating only the
   UpstreamOps application container.
6. Keep automation disabled until target connectivity, snapshot, preview, and
   notification smoke tests pass.

The module lives under `backend/sub2pool`, `backend/api/sub2_pool.go`, and the
account-pool frontend files. The intended upgrade path is to keep these files
isolated and limit official-source changes to small bootstrap, scheduler,
notification-event, and route hooks.
