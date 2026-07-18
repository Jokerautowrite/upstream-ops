# Production Handoff

This document records the public, reproducible production contract for this
fork. Secrets, host addresses, credentials, and backup locations belong in the
private operations handoff and must not be committed here.

## Authoritative Release

- Code: the head of `discovery-review-20260718` until the review branch is
  explicitly merged.
- Image: `ghcr.io/jokerautowrite/upstream-ops:discovery-review-<short-sha>`.
- Deployment: `docker-compose.production.yml` with the same immutable image tag
  in `.env`.
- Do not merge `main`, approve discovery candidates, or apply candidates as
  part of a routine deployment.

## Account Pool Network

The account-pool target must use Docker DNS instead of a container IP:

```text
http://sub2api:8080
```

`docker-compose.production.yml` attaches UpstreamOps to the external
`sub2api_default` network. This survives Sub2 container recreation and avoids
the `pool_unavailable` failure caused by pinning an ephemeral container IP.

Before deploying, verify that `sub2api_default` exists. Recreate only the
UpstreamOps `app` service; do not restart Sub2, PostgreSQL, or Redis.

## Deployment Invariants

- Back up `.env`, the SQLite database, and the active Compose file first.
- Verify the SQLite backup with `PRAGMA integrity_check`.
- Preserve host-only operations files and credentials.
- Keep `ops/sub2-pool-sync.py` identical to the tracked version.
- Pin `IMAGE_REPOSITORY=ghcr.io/jokerautowrite/upstream-ops` and an immutable
  review-branch image tag.
- After recreation, require `running/healthy`, restart count `0`, local and
  public health checks, and a successful live account-pool refresh.
- Opening the account-pool page reads cache only. Live reads occur on explicit
  refresh and the existing monitor cycle.

## Candidate Safety

- A discovery scan only updates the local candidate queue.
- Do not approve or apply candidates during deployment verification.
- An empty `candidate_ids` apply request means all eligible candidates; never
  send an empty array.
- Candidate count can legitimately change when the operator tests a different
  Top-N filter. Verify that expected candidates remain `pending` with no remote
  ownership traces.

## Verification

Run the repository checks before publishing:

```bash
go test ./...
go vet ./...
go test -race ./backend/discovery ./backend/sub2pool ./backend/api ./backend/storage
pnpm --dir frontend lint
pnpm --dir frontend exec tsc --noEmit
pnpm --dir frontend build
python3 -m unittest ops/test_sub2_pool_sync.py
git diff --check
```

Publish the review image through `.github/workflows/publish.yml`, then deploy
that exact tag with `docker-compose.production.yml`. Final acceptance requires
the local branch, GitHub branch, server checkout, image tag, and container
runtime to point to the same commit.
