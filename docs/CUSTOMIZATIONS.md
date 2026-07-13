# Local Customizations

This deployment keeps local changes narrow so upstream upgrades can be
reviewed and replayed without copying unrelated code.

## Data Model

- One monitored channel represents one normalized upstream URL.
- One upstream group/account has one unique API key.
- A Sub2 account is an optional selected copy of an upstream group/account.
- Business-channel labels such as `PLUS`, `Pro`, and `生图` are account-pool
  categories, not monitored upstream channels.

## Source Changes

### Monitor homepage sorting

- Files:
  - `backend/api/channels.go`
  - `backend/storage/channels.go`
  - `frontend/lib/queries.ts`
  - `frontend/components/monitor/channel-cards.tsx`
- Behavior:
  - server-side sorting before pagination;
  - name A-Z / Z-A using case-insensitive ordering;
  - balance low-high / high-low with missing balances last.

### Account-pool business category

- Files:
  - `backend/sub2pool/types.go`
  - `backend/sub2pool/engine.go`
- Behavior:
  - image group names are classified before generic `gpt` / `PLUS` matching;
  - the user-facing category is `生图`.

### Account-pool refresh cost

- Files:
  - `backend/api/sub2_pool.go`
  - `backend/scheduler/scheduler.go`
  - `frontend/app/account-pool-page.tsx`
- Behavior:
  - the manual page refresh reads current Sub2 account/group/status data while
    reusing cached monitor balance and rate matches;
  - the independent five-minute account-pool refresh is removed;
  - automatic cached refresh remains coupled to the 15-minute monitor cycle.

### Account-pool stop reasons and priority notifications

- Files:
  - `backend/connector/sub2api/pool.go`
  - `backend/sub2pool`
  - `backend/api/sub2_pool.go`
  - `backend/notify`
  - `backend/storage/model.go`
- Behavior:
  - Sub2 runtime stop reasons and reset/until timestamps are read outside the
    writable `AdminAccount` model;
  - persisted stop reasons strip controls, redact credentials and URLs, and are
    limited to 512 UTF-8-safe bytes;
  - snapshots expose `stop_source`, `stop_reason`, and `stop_time` while keeping
    the existing `schedulable_reason`;
  - `sub2_pool_priority_applied` and `sub2_pool_priority_failed` are explicit
    opt-in events; legacy `sub2_pool_changed` subscribers remain compatible;
  - priority failure notifications contain only stable stage/code metadata, and
    notification retries never replay Sub2 priority writes.

### Webhook identity fields

- File:
  - `backend/notify/webhook.go`
- Behavior:
  - generic webhook payloads include `channel_id` and `model_name` so the
    external notification guard can deduplicate reliably.

## External Operations Layer

The following production resources live outside the UpstreamOps source tree:

- `/opt/upstream-ops-local/notify_guard.py`
- `/opt/upstream-ops-local/strict_notify.py`
- `/opt/upstream-ops/ops/sub2-pool-sync.py`
- `upstream-notify-guard.service`
- `upstream-strict-notify.service`
- `upstream-strict-notify.timer`
- `upstream-ops-sub2-pool-sync.service`
- `upstream-ops-sub2-pool-sync.timer`

Policy:

- Sub2 URL import runs daily at 12:00 Asia/Shanghai and always sends a result.
- Monitor balance and rates remain automatic every 15 minutes.
- Identical rate-change notifications are suppressed for one hour.
- Low-balance notifications send at most three times, then cool down for three
  hours.
- Notifications are quiet from 00:00 through 08:59 Asia/Shanghai; queued
  events are summarized after the quiet window.
- Cached strict-health checks do not contact upstream providers.

## Upgrade Checklist

1. Record the new upstream tag and commit.
2. Rebase or replay only the source changes listed above.
3. Run:

   ```bash
   go test ./backend/notify ./backend/storage ./backend/api ./backend/sub2pool ./backend/scheduler
   cd frontend && pnpm lint && pnpm build
   ```

4. Verify the external operations layer is still installed and active.
5. Verify one monitored row per normalized URL.
6. Verify account-pool automation and notification timers independently before
   enabling writes.
