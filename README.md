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
| `GET /v1/gate?service=&env=` | Effective gate for one service/env (`{state, allowed, reason, source, since, actor, expires_at}`). `expires_at` is set when the state lapses on its own — a freeze TTL or the end of the active schedule window — and absent for a trip or an open-ended freeze. The merge-queue check reads `.allowed`. |
| `GET /v1/gates` | All known gates (dashboard). |
| `GET /v1/schedules` | Every freeze window Kudzu knows about, across all gates. Each entry carries its `service`/`env`, an `active` flag, and `since`/`until` bounds while active. Pass `?service=&env=` to narrow it to one gate. |
| `GET /` and `GET /ui` | The gate board — an HTML dashboard (see below). |
| `GET /docs` | This API, documented (see below). |
| `GET /openapi.json` | The same API as an OpenAPI 3.1 document. |
| `GET /healthz` / `GET /readyz` / `GET /metrics` | Liveness / readiness (pings Redis) / Prometheus. |
| `GET /versionz` | Build info for the running binary: module version, VCS revision and time, dirty flag, Go version, GOOS/GOARCH. Read straight from the Go build info, so nothing has to be stamped in at build time. |

Write (require a bearer token from `KUDZU_WRITE_TOKENS`):

| Method & path | Body |
|---|---|
| `POST /v1/gate/freeze` | `{service, env, reason, actor, ttl_seconds?}` |
| `POST /v1/gate/unfreeze` | `{service, env, actor}` — clears a manual freeze **and** resets a trip |
| `POST /v1/deploy-result` | `{service, env, status:"success"\|"failed", repo:"owner/name", base?, sha?, run_url?, actor?}` |
| `POST /v1/schedules` | `{service, env, reason, cron, duration_seconds}` (recurring) or `{…, start, end}` (one-off) |
| `DELETE /v1/schedules/{id}?service=&env=` | remove a window |

## The gate board (UI)

`GET /` (or `/ui`) serves a dashboard of every known gate. It leads with the one
thing you open it for — how many gates are blocked right now — then lists the
blocked gates in a table — service, environment, state, source, reason, actor,
age — above a quieter service/environment table of the open ones, and below that
every declared freeze window. Anything that lifts by itself (a freeze with a
`ttl_seconds`, or a scheduled window) shows when. It refreshes itself every 15
seconds.

The page is a single `html/template` embedded in the binary: no separate
frontend, no build step, no external assets or fonts, so it works offline and
behind a strict CSP.

### Acting from the board

The board is read-only unless you ask for otherwise. Set **`KUDZU_UI_WRITES=true`**
and it grows a **sign in** button: paste a value from `KUDZU_WRITE_TOKENS`, give
your name, and the controls appear — freeze and unfreeze per gate, freeze a
service/environment Kudzu has never seen, and add or delete freeze windows. They
call the same authenticated endpoints listed above; your name is sent as the
`actor`, so the board still records who did what.

With `KUDZU_UI_WRITES` unset the page carries no controls, no dialogs and no
session script at all — there is nothing to press, rather than something merely
hidden. It defaults off because using it means pasting a write token into a
browser, which should be a decision rather than something an upgrade hands you.

The token is held in `sessionStorage` and is gone when you close the tab; your
name is remembered in `localStorage`. Two consequences worth knowing:

- The token is readable by any script running on the page. Kudzu ships no
  third-party JS and escapes every stored field through `html/template`, but a
  browser is still a worse place to keep a write token than a CI secret store.
- Because the check is client-side, the server cannot tell a signed-in browser
  from a signed-out one. With the controls enabled the buttons are always in the
  HTML and simply return `401` if pressed without a valid token — the
  authorisation itself is entirely server-side, as before.

One gap the controls are honest about: **a gate frozen by a schedule has no
unfreeze button**, because `POST /v1/gate/unfreeze` clears a manual freeze and
resets a trip but does not cancel an active window — the gate would recompute
from the schedule and freeze straight back. Those rows link down to the freeze
windows table instead, where the window itself can be deleted.

It is served under the same auth rules as the other read endpoints, so with
`KUDZU_REQUIRE_READ_AUTH=true` a browser cannot load it (browsers do not send
bearer tokens). If you need both, put Kudzu behind an authenticating
ingress/SSO layer and leave `KUDZU_REQUIRE_READ_AUTH` off.

## API documentation

`GET /docs` renders the API — endpoints grouped by area, parameters, request
bodies, response codes and every schema's fields. `GET /openapi.json` serves the
same content as an OpenAPI 3.1 document, for client generators, API clients and
contract tests. Both ship inside the binary; the page is rendered from the spec,
so the two cannot disagree.

Keeping the spec honest is a test problem, and three tests do the work:

- every route in the router's table is documented, and nothing is documented
  that is not served;
- every write endpoint declares a bearer token, and no read endpoint claims to;
- each schema's fields match the JSON tags of the Go type it describes, by
  reflection — so renaming a field without touching the spec fails the build.

Prose (summaries, descriptions, response-code meanings) is *not* checked. Treat
it as documentation, not a contract.

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
| `KUDZU_UI_WRITES` | `false` | reveal the gate board's freeze/unfreeze/window controls |
| `BREAKER_FAILURE_THRESHOLD` | `1` | consecutive failures that trip the breaker |
| `REQUIRED_CHECK_CONTEXT` | `kudzu-gate` | commit-status context used for eviction; must match the required check name |
| `GITHUB_APP_ID` / `GITHUB_APP_INSTALLATION_ID` | – | enable eviction |
| `GITHUB_APP_PRIVATE_KEY` or `GITHUB_APP_PRIVATE_KEY_FILE` | – | PEM inline or file path |
| `GITHUB_API_BASE_URL` | `https://api.github.com/` | set to `https://HOST/api/v3/` for GHES |

## The Kudzu Gate action

The consumer-side half of Kudzu is a composite action, published to the GitHub
Marketplace from [`action.yml`](action.yml) at the root of this repo. It asks one
gate whether deploys are allowed and exits non-zero when they are not, which
fails the required check and ejects the pull request from the merge queue.

```yaml
name: Merge Queue Gate
on:
  merge_group: {}

jobs:
  kudzu-gate:                          # must equal REQUIRED_CHECK_CONTEXT
    runs-on: ubuntu-latest
    steps:
      - uses: cuotos/kudzu@v0
        with:
          url: ${{ vars.KUDZU_URL }}
          service: ${{ github.event.repository.name }}
          env: production
          token: ${{ secrets.KUDZU_TOKEN }}   # only if read auth is on
```

| Input | Required | Default | Description |
|---|---|---|---|
| `url` | yes | – | Base URL of the Kudzu service. |
| `service` | no | the repository name | Service half of the gate key. |
| `env` | no | `production` | Environment half of the gate key. |
| `token` | no | – | Bearer token, needed only when `KUDZU_REQUIRE_READ_AUTH` is on. |

Pin `@v0` to track the latest `v0.x`, or a full tag like `@v0.4.0` to freeze.
Releases move the floating major and minor git tags, so `@v0` follows along.

The action needs `curl` and `jq`, both present on GitHub-hosted runners.

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
get you running quickly. Out of the box it has **no persistence** — a Redis
restart clears all gate state (freezes, trips, schedules). You can either give
it a volume (below) or point Kudzu at an external Redis.

For production, disable the bundled Redis and point Kudzu at an external/HA
(and ideally persistent) Redis:

```sh
helm upgrade --install kudzu oci://ghcr.io/cuotos/charts/kudzu --version <X.Y.Z> \
  --set redis.enabled=false \
  --set config.redis.addr=my-redis-master:6379
```

When `redis.enabled` is true, `REDIS_ADDR` is derived from the bundled Service
and `config.redis.addr` is ignored.

### Persisting the bundled Redis

`redis.persistence` gives the bundled Redis a volume for `/data`, so gate state
survives a restart. The chart is deliberately agnostic about *where* that
storage comes from — it only needs a volume, and which CSI driver, filesystem
or backing PV provides it is entirely yours to choose. There are three routes;
set exactly one, or the render fails rather than silently picking for you.

**A. Let the chart create a PVC.** The common case — name a StorageClass and
your CSI driver does the rest. For AWS EFS:

```sh
helm upgrade --install kudzu oci://ghcr.io/cuotos/charts/kudzu --version <X.Y.Z> \
  --set redis.persistence.enabled=true \
  --set redis.persistence.storageClass=efs-sc \
  --set 'redis.persistence.accessModes[0]=ReadWriteMany' \
  --set redis.persistence.size=1Gi
```

Set `storageClass: "-"` instead to skip dynamic provisioning and bind to a PV
you created by hand. The PVC carries `helm.sh/resource-policy: keep` by default
(`redis.persistence.keepOnUninstall`), so `helm uninstall` leaves your data
behind.

**B. Bring your own PVC.** The chart creates nothing and mounts what you name:

```yaml
redis:
  persistence:
    enabled: true
    existingClaim: kudzu-redis-data
```

**C. Bring your own volume spec.** The escape hatch, for anything the routes
above cannot express. Spliced verbatim into the pod's `volumes:` entry:

```yaml
redis:
  persistence:
    enabled: true
    volume:
      csi:
        driver: efs.csi.aws.com
        volumeAttributes:
          fileSystemId: fs-0123456789abcdef0
```

Two things to know regardless of route:

- The Redis Deployment is pinned to one replica with `strategy: Recreate`. Two
  Redis processes sharing one `/data` would corrupt each other's AOF, so a
  `ReadWriteMany` volume does **not** mean you can scale it up.
- Enabling persistence switches Redis to the AOF
  (`--appendonly yes --appendfsync everysec`). On a network filesystem like EFS
  that fsync is noticeably slower than local disk; RDB snapshots are gentler,
  and `redis.args` overrides the args entirely:

  ```yaml
  redis:
    args: ["--save", "60 1000", "--appendonly", "no"]
  ```

On EFS specifically, the pod runs as uid/gid 999 with `fsGroup: 999`. If your
PV uses an EFS access point, give it POSIX uid/gid 999 (or matching ownership
on the exported directory) or Redis will not be able to write `/data`.

## Layout

```
cmd/kudzu          service entrypoint (config, wiring, graceful shutdown)
internal/gate      domain: state model, effective-state rules, Service, ports
internal/schedule  freeze-window evaluation (cron + duration / one-off)
internal/store     gate.Store: redis (prod) and memory (tests/local)
internal/github    GitHub App evicter (gate.Evicter)
internal/httpapi   router, handlers, bearer auth, logging/metrics middleware, UI
internal/observability  Prometheus metrics + live gate-state collector
deploy             Dockerfile + Helm chart
action.yml         the "Kudzu Gate" composite action (root: required by Marketplace)
github             example consumer workflows
```
