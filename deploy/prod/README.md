# Docker Production Deployment

`docker-compose.yml` in this directory runs the `osctrl` production stack
from multi-stage builds (Go binaries compiled from source, Vite bundle built
with node, shipped on minimal runtime images):

| Service | Image | Role |
| --- | --- | --- |
| `osctrl-edge` | `osctrl/edge` | nginx TLS termination for the osquery endpoint |
| `osctrl-frontend` | `osctrl/frontend` | nginx serving the React SPA, `/api/*` → `osctrl-api` |
| `osctrl-tls` | `osctrl/tls` | osquery remote API (enroll/config/log/queries/carves) |
| `osctrl-api` | `osctrl/api` | REST API for the operator UI (JWT auth) |
| `osctrl-cli` | `osctrl/cli` | one-shot bootstrap: environment + admin user |
| `osctrl-postgres` | `postgres` | primary datastore |
| `osctrl-valkey` | `valkey/valkey` | required runtime cache / shared state |
| `osquery-node-1` | `osctrl/osquery` | optional in-stack agent node (profile `osquery`) |

## Topology

Two compose networks enforce the boundary:

- `backend` — postgres, valkey, bootstrap, agents. `internal: true`: no egress
  and unreachable from the edge network.
- `edge` — the two nginxs plus `osctrl-api`/`osctrl-tls` so the proxies can
  reach them. The only network with egress (log-sink integrations may need
  outbound traffic).

Neither postgres nor valkey publishes a host port; only the two public
entrypoints do (`osctrl-edge` on `${OSCTRL_TLS_PORT}`, `osctrl-frontend` on
`${OSCTRL_UI_PORT}`). Startup order is enforced with healthchecks: the
bootstrap must complete successfully before `osctrl-tls`/`osctrl-api` start.

## Prerequisites

- Docker with the Compose v2 plugin and BuildKit
- OpenSSL

## 1. Environment

```bash
cp .env.example .env
chmod 600 .env
```

Set at minimum: `OSCTRL_TLS_HOST`, `OSCTRL_PASS`, `POSTGRES_DB_PASSWORD`,
`VALKEY_PASSWORD`, and a strong `JWT_SECRET`:

```bash
openssl rand -hex 32
```

## 2. TLS certificates

Certificates are mounted read-only into `osctrl-edge`, `osctrl-frontend`,
and `osctrl-cli` — they are never baked into images. `./secrets/` holds:

- `osctrl.crt` — full chain (leaf + intermediates), CN/SAN covering
  `${OSCTRL_TLS_HOST}` (the osquery endpoint hostname the environment is
  created with)
- `osctrl.key` — matching private key

For a self-signed certificate for testing:

```bash
openssl req -x509 -newkey rsa:4096 -sha256 -days 825 -nodes \
  -keyout secrets/osctrl.key -out secrets/osctrl.crt \
  -addext "subjectAltName=DNS:${OSCTRL_TLS_HOST},IP:127.0.0.1"
```

In production use a real certificate for `${OSCTRL_TLS_HOST}`. The admin UI
(`${OSCTRL_UI_PORT}`) uses the same mounted certificate, so include its
hostname in the SANs if you terminate TLS in these containers.

## 3. Build and start

```bash
# from this directory
docker compose build
docker compose up -d
```

The `osctrl-cli` container runs the one-shot bootstrap (idempotent) and
exits; `osctrl-tls` and `osctrl-api` start once it has succeeded. Re-running
`up` after a stop re-runs the bootstrap and is a no-op.

Optional in-stack osquery agent, to validate enrollment end-to-end:

```bash
docker compose --profile osquery up -d
```

## Endpoints

| Surface | URL | Purpose |
| --- | --- | --- |
| Operator frontend | `https://<ui-host>:${OSCTRL_UI_PORT}` | React SPA and proxied `/api/*` |
| osquery TLS endpoint | `https://${OSCTRL_TLS_HOST}:${OSCTRL_TLS_PORT}` | Enroll, config, log, distributed query, and carve traffic |

The browser will warn about a self-signed certificate unless the issuing CA
is trusted.

## Operations

```bash
docker compose ps            # service state (watch the health column)
docker compose logs osctrl-api
docker compose logs osctrl-cli   # bootstrap output

# update
docker compose build osctrl-api osctrl-tls
docker compose up -d osctrl-api osctrl-tls
# safe without reloading the nginxs: both proxy the upstreams per-request
# through Docker's embedded DNS (resolver 127.0.0.11), so a recreated
# backend container (new IP) is picked up on the next request

# tear down (keeps volumes)
docker compose down
# tear down including data
docker compose down -v
```

Back up `postgres-data` with `pg_dump` and rotate the certificates under
`./secrets/` before they expire; the stack picks up new cert files on the
next `docker compose up -d`.

## Security notes

- `osctrl-tls` and `osctrl-api` run on `gcr.io/distroless/static:nonroot`
  — no shell, no package manager, no libc; the image contains the static
  binary, the osquery table catalog (api), the CA bundle, and the health
  probe only. Their compose healthchecks use the baked-in
  `healthcheck` binary because distroless has no `wget`/`sh`.
- `osctrl-cli` keeps a small alpine runtime on purpose: the bootstrap is a
  POSIX shell script and the container exits after a few seconds, so the
  long-lived-attack-surface argument for distroless does not apply.
- `osctrl-frontend`/`osctrl-edge` use the official `nginx:alpine` images —
  there is no distroless nginx; the master process runs as root (required
  to bind :443) and workers run as `nginx`.
- `osctrl-valkey` is the Linux Foundation's open-source Redis fork
  (RESP-compatible); osctrl's `go-redis` client speaks plain RESP, so it
  serves as the required cache/shared-state backend unchanged. The
  `REDIS_HOST`/`REDIS_PASS` environment names are osctrl binary flags.
- Everything else runs as an unprivileged user; postgres/valkey use vendor
  defaults and the osquery test node's bootstrap needs root inside its
  container (it is an optional harness).
- `.env` and `./secrets/` are git-ignored; never commit them.
- `SERVICE_AUTH=jwt` on the API is non-negotiable for production; `auth=none`
  is a development-only mode and is not wired up here.
- If you add your own ingress in front of `osctrl-edge`/`osctrl-frontend`,
  review `OSCTRL_TRUSTED_PROXIES` and HSTS accordingly.
