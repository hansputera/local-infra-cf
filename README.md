# infra — Docker Compose v2 (project `infra`)

Home-server infra on Arch Linux (ASUS TUF F15). One Compose project,
three modes via profiles, Traefik v3 + Cloudflare Tunnel ingress.

## Modes

| Mode     | Command           | Contents                                     | Resource cap | Public |
|----------|-------------------|----------------------------------------------|--------------|--------|
| desktop  | `./mode desktop`  | everything DOWN (`down` without `-v`)         | 0 (8–10G reserved for dev) | OFF |
| lite     | `./mode lite`     | traefik + cloudflared + whoami + portal      | ≤ 5G         | ON     |
| lab      | `./mode lab`      | lite + dozzle + postgres-lite                | ≤ 10G        | ON     |

- `restart: unless-stopped` for lite services; `"no"` for lab-only (dozzle, postgres).
- Every service has `mem_limit` + `cpus`.
- `desktop` does NOT use `-v` — data & secrets are never deleted by mode switches.

## Public vs Private

| Service            | Router                      | Access                                 |
|--------------------|-----------------------------|----------------------------------------|
| whoami             | `Host(whoami.<PUBLIC_DOMAIN>)` entrypoint `web` | public via cloudflared → traefik:80 |
| Traefik dashboard  | `api@internal` entrypoint `traefik` | localhost:8080 / Tailscale only, never enters tunnel |
| Dozzle             | `Host(dozzle.lan)` entrypoint `traefik` | localhost:8080 / Tailscale only |

Ports are only bound to `127.0.0.1` — never `0.0.0.0:80/443`.
Fully public through a single Cloudflare Tunnel (no router port-forward).

## Honest limits (PHASE 2)

- Public is **down in desktop mode, on sleep, or on battery** — by design.
- TLS at the edge: Cloudflare (Full). Full strict follows once local certs (mkcert)
  in `traefik/certs/` are ready for `*.lan`.
- Cloudflare Access: **default ON** in the Zero Trust dashboard (configure there).
- Tunnel token: fill it in yourself at `secrets/cf-tunnel.yml` (600, gitignored).
  The host unit `cloudflared.service` was briefly world-readable → token is
  considered leaked, **rotate in Cloudflare Zero Trust** before the first `./mode lite`.
- Data still lives in `~/infra/data/` (no 1TB NVMe yet) — ready to migrate to `/srv`
  without changing compose (bind path).

## Portal — Infra Manager (PHASE 4 + PHASE 5)

Go panel (stdlib only, single binary) in `stacks/portal/`: manage hostnames,
detect drift, monitor tunnel, generate stacks, manage tunnel & connectors.

- Access now: `http://127.0.0.1:8300` (bound to localhost) and
  `https://portal.<PUBLIC_DOMAIN>` — hostname already registered in the tunnel
  ingress + DNS, **fronted by Cloudflare Access** (app created manually in the
  Zero Trust dashboard; no login → 302 to the Access login page).
- What it manages: `traefik/dynamic/svc-*.yml`, tunnel ingress (mutexed
  GET-modify-PUT, per tunnel), CNAME DNS, registry in `data/portal/registry.json`
  and managed node/tunnel data in `data/portal/nodes.json` (gitignored).
- Routing source of truth: compose labels when present (portal only writes files
  for hostnames without labels; duplicate files are cleaned up automatically on
  reconcile), rows in the table are tagged `label compose`.
- Old hostnames not yet registered appear as orphans with an **Adopt** button.
- `include` injection and stack file deletion are also handled by the portal
  (`POST /stacks`, `/stacks/<name>/up|stop|delete`).

**PHASE 5 — Tunnel & Node** (page `/nodes`):

- List of account tunnels + connector count; **Add connector** button → wizard:
  give a name → pick a tunnel (default: join the infra tunnel, or create a new
  tunnel per node) → connection instructions for 2 modes with explanation + use case:
  **Docker** (`docker run … --token …`) and **Binary/service**
  (`cloudflared service install`).
- The instruction page polls `/api/nodes` every 3s: new connections are
  auto-bound to the node name; connections that existed before the page opened
  appear as a **Use …** button (manual).
- Node name = local registry (Cloudflare only knows `client_id`). When
  cloudflared restarts and `client_id` changes, status becomes **CHANGE ID** and
  the **Bind to …** button (rebind) moves the name/origin + service reference.
- Hostname form now selects **tunnel + connector + origin**; drift also checks:
  ingress in that tunnel, CNAME direction to `<tunnel-id>.cfargotunnel.com`,
  matching origin, and that the node is still connected.
- Tunnel delete guard: only tunnels tagged `managed` (created by the portal)
  may be deleted, and only when no service uses them. The infra tunnel and
  third-party tunnels in the same account **can never be deleted from the portal**.
- Honest Cloudflare limit: 1 tunnel's traffic is shared across all connectors →
  the origin must be reachable from every node in that tunnel; for per-machine
  isolation, use 1 tunnel per node (can be created from the same page).

Check status without opening a browser:

```bash
curl -s http://127.0.0.1:8300/api/report | jq
curl -s http://127.0.0.1:8300/api/nodes   | jq
```

CLI fallback if the portal is down: `./publish <sub> [origin]`.

## Usage

```bash
cp .env.example .env          # fill in PUBLIC_DOMAIN
chmod 600 .env secrets/cf-tunnel.yml secrets/postgres.env
./mode status                 # environment health check
./mode lite                   # public ON
./mode lab                    # + dozzle, postgres-lite
./mode desktop                # everything down, data safe
```

Validate without running anything:

```bash
docker compose --profile lite --profile lab config -q && echo OK
```

## Layout

```
infra/
├── compose.yaml            # project root: traefik, cloudflared, dozzle, network, logging
├── .env / .env.example     # PUBLIC_DOMAIN, PUID, PGID (600, gitignored)
├── mode                    # desktop | lite | lab | status
├── secrets/                # 600, gitignored (cf-tunnel.yml, postgres.env)
├── traefik/
│   ├── traefik.yml         # static config (entrypoint, provider, ping, log)
│   ├── dynamic/            # dashboard.yml (private router)
│   └── certs/              # mkcert *.lan (later)
├── DESIGN.md               # portal UI design direction (used by antislop)
├── publish                 # CLI fallback: add hostname via Cloudflare API
├── stacks/
│   ├── whoami/             # PUBLIC pattern (profile lite)
│   ├── postgres-lite/      # lab example (profile lab, internal network)
│   └── portal/             # Infra Manager (Go + Dockerfile + compose)
└── data/                   # postgres bind mount (moving to /srv later)
```

Adding a stack: create `stacks/<name>/compose.yaml`, add the path to `include`
in `compose.yaml`, set `profiles` per mode, give it `mem_limit` + `cpus`.
