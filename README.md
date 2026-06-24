# Kudzu

Kudzu is a small Go service that decides **whether it's safe to deploy** a given
service to a given environment, and lets the **GitHub Enterprise merge queue**
enforce that decision.

In a trunk / merge-queue setup, merging *is* deploying. So the lever for blocking
deploys is the merge queue's **required status checks**. A `merge_group`-triggered
GitHub Actions job asks Kudzu `is it safe to deploy?`; if the answer is no, the job
exits non-zero, the required check fails, and GitHub **ejects** the PR from the queue.

```
PR queued ──> merge_group event ──> "Kudzu Gate" job ──GET /v1/gate──> Kudzu
                                          │                              │
                              allowed? ───┤                       open / frozen / tripped
                                exit 0 (merge) ◄── open
                                exit 1 (eject) ◄── frozen | tripped
```

## Gate states

A gate is keyed by `(service, environment)` and has one effective state, computed
with the precedence **tripped > manual freeze > scheduled freeze > open**:

| State     | Meaning                                                            | Set by |
|-----------|-------------------------------------------------------------------|--------|
| `open`    | Deploys/merges allowed (`allowed: true`).                         | default |
| `frozen`  | Temporarily blocked.                                              | manual freeze, or an active scheduled window |
| `tripped` | A deploy failed and the circuit breaker fired. Sticky until reset.| `POST /v1/deploy-result {status:"failed"}` |

Any non-`open` state ejects PRs from the queue (the chosen *always-eject* behaviour).

## API

Read (token optional, controlled by `KUDZU_REQUIRE_READ_AUTH`):

| Method & path | Purpose |
|---|---|
| `GET /v1/gate?service=&env=` | Effective gate for one service/env (`{state, allowed, reason, source, since, actor}`). The merge-queue check reads `.allowed`. |
| `GET /v1/gates` | All known gates (dashboard). |
| `GET /v1/schedules?service=&env=` | List freeze windows. |
| `GET /healthz` / `GET /readyz` / `GET /metrics` | Liveness / readiness (pings Redis) / Prometheus. |

Write (require a bearer token from `KUDZU_WRITE_TOKENS`):

| Method & path | Body |
|---|---|
| `POST /v1/gate/freeze` | `{service, env, reason, actor, ttl_seconds?}` |
| `POST /v1/gate/unfreeze` | `{service, env, actor}` — clears a manual freeze **and** resets a trip |
| `POST /v1/deploy-result` | `{service, env, status:"success"\|"failed", repo:"owner/name", base?, sha?, run_url?, actor?}` |
| `POST /v1/schedules` | `{service, env, reason, cron, duration_seconds}` (recurring) or `{…, start, end}` (one-off) |
| `DELETE /v1/schedules/{id}?service=&env=` | remove a window |

## Circuit breaker & proactive eviction

`POST /v1/deploy-result {status:"failed"}` increments a consecutive-failure
counter; once it reaches `BREAKER_FAILURE_THRESHOLD` (default `1`) the gate trips.
A `success` resets the counter. The trip is sticky until `POST /v1/gate/unfreeze`.

If a **GitHub App** is configured (`GITHUB_APP_ID`, `GITHUB_APP_INSTALLATION_ID`,
`GITHUB_APP_PRIVATE_KEY[_FILE]`), a trip also lists the repo's
`gh-readonly-queue/<base>/*` branches and posts a `failure` commit status
(context = `REQUIRED_CHECK_CONTEXT`) to each head SHA, evicting in-flight merge
groups immediately. Without the App, a trip is simply caught by the next
`merge_group` gate check. The App needs `statuses:write` and `contents:read`.

## Configuration (environment)

| Var | Default | Notes |
|---|---|---|
| `KUDZU_ADDR` | `:8080` | listen address |
| `REDIS_ADDR` / `REDIS_PASSWORD` / `REDIS_DB` | `localhost:6379` / – / `0` | state store |
| `KUDZU_WRITE_TOKENS` | – | comma-separated bearer tokens for write endpoints (fail-closed if empty) |
| `KUDZU_REQUIRE_READ_AUTH` | `false` | also require a token on reads |
| `BREAKER_FAILURE_THRESHOLD` | `1` | consecutive failures that trip the breaker |
| `REQUIRED_CHECK_CONTEXT` | `kudzu-gate` | commit-status context used for eviction; must match the required check name |
| `GITHUB_APP_ID` / `GITHUB_APP_INSTALLATION_ID` | – | enable eviction |
| `GITHUB_APP_PRIVATE_KEY` or `GITHUB_APP_PRIVATE_KEY_FILE` | – | PEM inline or file path |
| `GITHUB_API_BASE_URL` | `https://api.github.com/` | set to `https://HOST/api/v3/` for GHES |

## Wiring up a repo

1. Enable the merge queue on the trunk branch ruleset.
2. Add `.github/workflows/merge-queue-gate.yml` (see [`github/examples`](github/examples/merge-queue-gate.yml)) and make its job a **required** status check — its name must equal `REQUIRED_CHECK_CONTEXT`.
3. Add the deploy-result hook to your deploy workflow ([example](github/examples/deploy-failure-hook.yml)).
4. Set repo variable `KUDZU_URL` and (if read auth is on) secret `KUDZU_TOKEN`.

## Local development

```sh
make up          # Kudzu + Redis via docker compose on :8080
make test        # unit tests
make run         # run against a local Redis (REDIS_ADDR=localhost:6379)
```

Example session (token `local-dev-token`):

```sh
curl localhost:8080/v1/gate?service=orders\&env=production
curl -X POST localhost:8080/v1/gate/freeze -H 'authorization: Bearer local-dev-token' \
  -H 'content-type: application/json' \
  -d '{"service":"orders","env":"production","reason":"incident","actor":"you"}'
```

## Deploying to Kubernetes

A Helm chart lives in [`deploy/helm/kudzu`](deploy/helm/kudzu). Create the secret
it references first:

```sh
kubectl create secret generic kudzu-secrets \
  --from-literal=write-tokens=tokenA,tokenB \
  --from-literal=redis-password=... \
  --from-literal=github-app-id=123456 \
  --from-literal=github-app-installation-id=7891011 \
  --from-file=github-app-private-key=app.pem

helm upgrade --install kudzu deploy/helm/kudzu \
  --set image.tag=<tag> \
  --set config.redis.addr=redis-master:6379 \
  --set github.evictionEnabled=true
```

Each published release also pushes the packaged chart to GHCR as an OCI
artifact, so you can install a released version without checking out the repo
(the chart's default `image.tag` matches the release):

```sh
helm upgrade --install kudzu oci://ghcr.io/cuotos/charts/kudzu --version <X.Y.Z> \
  --set config.redis.addr=redis-master:6379 \
  --set github.evictionEnabled=true
```

The service is stateless (state lives in Redis) and runs ≥2 replicas with
liveness/readiness probes, a Prometheus `ServiceMonitor`, and an optional HPA
and NetworkPolicy.

### Redis: bundled vs external

The chart bundles a single-node Redis (`redis.enabled: true`, the default) to
get you running quickly. It has **no persistence** — a Redis restart clears all
gate state (freezes, trips, schedules) — so treat it as convenient, not durable.

For production, disable the bundled Redis and point Kudzu at an external/HA
(and ideally persistent) Redis:

```sh
helm upgrade --install kudzu oci://ghcr.io/cuotos/charts/kudzu --version <X.Y.Z> \
  --set redis.enabled=false \
  --set config.redis.addr=my-redis-master:6379
```

When `redis.enabled` is true, `REDIS_ADDR` is derived from the bundled Service
and `config.redis.addr` is ignored.

## Layout

```
cmd/kudzu          service entrypoint (config, wiring, graceful shutdown)
internal/gate      domain: state model, effective-state rules, Service, ports
internal/schedule  freeze-window evaluation (cron + duration / one-off)
internal/store     gate.Store: redis (prod) and memory (tests/local)
internal/github    GitHub App evicter (gate.Evicter)
internal/httpapi   router, handlers, bearer auth, logging/metrics middleware
internal/observability  Prometheus metrics + live gate-state collector
deploy             Dockerfile + Helm chart
github             composite "Kudzu Gate" action + example workflows
```
