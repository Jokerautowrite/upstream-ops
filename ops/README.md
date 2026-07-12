# UpstreamOps Sub2 Pool Monitor Sync

This host-side helper imports only URL-level monitor candidates from the active
Sub2 account pool into UpstreamOps.

Rules:

- Requires `credentials.pool_mode=true`.
- Uses the normalized `base_url` as the identity; it does not create a monitor
  channel per Sub2 group or account name.
- Ignores image-only sites and historical `uo-*` generated accounts.
- Creates channels disabled, verifies login, and enables only successful
  channels through the UpstreamOps API.
- Updates the Feishu subscription through the UpstreamOps API.
- Does not call Sub2 account, API-key, group, or priority write APIs.

The service reads `UPSTREAM_OPS_SYNC_USERNAME` and
`UPSTREAM_OPS_SYNC_PASSWORD` from
`/etc/upstream-ops-sub2-pool-sync.env`. The environment file is not tracked.

Run tests before deployment:

```bash
python3 -m unittest -v test_sub2_pool_sync.py
python3 -m py_compile sub2-pool-sync.py
```
