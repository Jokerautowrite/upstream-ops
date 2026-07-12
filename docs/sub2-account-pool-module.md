# Sub2 Account Pool Module

This fork adds an isolated Sub2 account-pool operations module on top of
UpstreamOps v0.0.6.

## Scope

- Reuses the encrypted Sub2 target configured by the built-in upstream-sync
  module.
- Shows the current Sub2 account pool, health, balance, upstream multiplier,
  lowest group, current priority, and suggested priority.
- Matches upstream data by the full SHA-256 API-key fingerprint and normalized
  URL. Accounts without an exact fingerprint are not assigned an upstream
  multiplier.
- Only `apikey` accounts with `credentials.pool_mode=true` participate in
  automatic priority writes.
- Uses unique priorities in steps of `10`; lower upstream multipliers come
  first and debt accounts come last.
- Missing multiplier or balance data is skipped and reported instead of
  stopping other eligible accounts.
- Rate changes and priority results are combined into one dedicated
  `sub2_pool_changed` notification.

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
- Five additive SQLite/MySQL tables are created on startup:
  `sub2_pool_target_states`, `sub2_pool_outbox`, `sub2_pool_runs`, and
  `sub2_pool_automation`, plus `sub2_pool_leases`.

## Configuration

```dotenv
SUB2_POOL_MIN_ACCOUNT_COUNT=20
SUB2_POOL_MAX_CHANGES=20
SUB2_POOL_LOW_BALANCE_THRESHOLD=10
```

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
