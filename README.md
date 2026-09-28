# infra — Docker Compose v2 (project `infra`)

Home-server infra di Arch Linux (ASUS TUF F15). Satu project Compose,
tiga mode lewat profile, ingress Traefik v3 + Cloudflare Tunnel.

## Mode

| Mode     | Command           | Isi                                          | Resource cap | Publik |
|----------|-------------------|----------------------------------------------|--------------|--------|
| desktop  | `./mode desktop`  | semua DOWN (`down` tanpa `-v`)               | 0 (reserved 8–10G untuk dev) | OFF |
| lite     | `./mode lite`     | traefik + cloudflared + whoami + portal      | ≤ 5G         | ON     |
| lab      | `./mode lab`      | lite + dozzle + postgres-lite                | ≤ 10G        | ON     |

- `restart: unless-stopped` untuk service lite; `"no"` untuk lab-only (dozzle, postgres).
- Semua service punya `mem_limit` + `cpus`.
- `desktop` TIDAK pakai `-v` — data & secrets tidak pernah dihapus oleh mode switch.

## Public vs Private

| Service            | Router                      | Akses                                  |
|--------------------|-----------------------------|----------------------------------------|
| whoami             | `Host(whoami.<PUBLIC_DOMAIN>)` entrypoint `web` | publik via cloudflared → traefik:80 |
| Traefik dashboard  | `api@internal` entrypoint `traefik` | localhost:8080 / Tailscale saja, tidak pernah masuk tunnel |
| Dozzle             | `Host(dozzle.lan)` entrypoint `traefik` | localhost:8080 / Tailscale saja |

Port hanya di-bind `127.0.0.1` — tidak pernah `0.0.0.0:80/443`.
Publik sepenuhnya lewat satu Cloudflare Tunnel (bukan port-forward router).

## Batas jujur (FASE 2)

- Publik **mati saat desktop mode, sleep, atau baterai** — itu by design.
- TLS di edge: Cloudflare (Full). Full strict menyusul setelah cert lokal (mkcert)
  di `traefik/certs/` siap untuk `*.lan`.
- Cloudflare Access: **default ON** di dashboard Zero Trust (atur di sana).
- Token tunnel: isi sendiri di `secrets/cf-tunnel.yml` (600, gitignored).
  Unit host `cloudflared.service` sempat world-readable → token dianggap bocor,
  **rotate di Cloudflare Zero Trust** sebelum `./mode lite` pertama.
- Data masih di `~/infra/data/` (NVMe 1TB belum ada) — siap migrasi ke `/srv`
  tanpa mengubah compose (bind path).

## Portal — Infra Manager (FASE 4 + FASE 5)

Panel Go (stdlib saja, satu biner) di `stacks/portal/`: kelola hostname,
deteksi drift, monitor tunnel, generator stack, kelola tunnel & connector.

- Akses sekarang: `http://127.0.0.1:8300` (bind localhost) dan
  `https://portal.<PUBLIC_DOMAIN>` — hostname sudah terdaftar di ingress
  tunnel + DNS, **di depannya Cloudflare Access** (app dibuat manual di
  dashboard Zero Trust; tanpa login → 302 ke halaman login Access).
- Yang dikelola: `traefik/dynamic/svc-*.yml`, ingress tunnel (GET-modify-PUT
  ber-mutex, per tunnel), CNAME DNS, registry di `data/portal/registry.json`
  dan node/tunnel managed di `data/portal/nodes.json` (gitignored).
- Sumber kebenaran routing: label compose bila ada (portal menulis file hanya
  untuk hostname tanpa label; file ganda otomatis dibersihkan saat reconcile),
  baris di tabel ditandai `label compose`.
- Hostname lama yang belum terdaftar muncul sebagai orphan + tombol **Adopt**.
- Sisipan `include` dan hapus file stack juga ditangani portal
  (`POST /stacks`, `/stacks/<nama>/up|stop|delete`).

**FASE 5 — Tunnel & Node** (halaman `/nodes`):

- Daftar tunnel akun + jumlah connector; tombol **Tambah connector** → wizard
  beri nama → pilih tunnel (default join tunnel infra, atau bikin tunnel baru
  per node) → instruksi koneksi 2 mode dengan penjelasan + use case:
  **Docker** (`docker run … --token …`) dan **Binary/service**
  (`cloudflared service install`).
- Halaman instruksi polling `/api/nodes` tiap 3 dtk: koneksi baru otomatis
  diikat ke nama node; koneksi yang sudah ada sebelum halaman dibuka muncul
  sebagai tombol **Pakai …** (manual).
- Nama node = registry lokal (Cloudflare hanya mengenal `client_id`). Saat
  cloudflared restart dan `client_id` berubah, status jadi **GANTI ID** dan
  tombol **Ikat ke …** (rebind) memindahkan nama/origin + referensi service.
- Form hostname kini memilih **tunnel + connector + origin**; drift ikut
  mengecek: ingress di tunnel tsb, arah CNAME ke `<tunnel-id>.cfargotunnel.com`,
  origin cocok, dan node masih tersambung.
- Guard hapus tunnel: hanya tunnel bertanda `managed` (dibuat portal) yang
  boleh dihapus, dan harus tidak dipakai service. Tunnel infra serta tunnel
  pihak lain di akun yang sama **tidak akan pernah bisa dihapus dari portal**.
- Batas jujur Cloudflare: trafik 1 tunnel dibagi ke semua connector → origin
  harus terjangkau dari semua node di tunnel itu; untuk isolasi per mesin,
  pakai 1 tunnel per node (bisa dibuat dari halaman yang sama).

Cek status tanpa buka browser:

```bash
curl -s http://127.0.0.1:8300/api/report | jq
curl -s http://127.0.0.1:8300/api/nodes   | jq
```

Cadangan CLI bila portal mati: `./publish <sub> [origin]`.

## Cara pakai

```bash
cp .env.example .env          # isi PUBLIC_DOMAIN
chmod 600 .env secrets/cf-tunnel.yml secrets/postgres.env
./mode status                 # health check lingkungan
./mode lite                   # publik ON
./mode lab                    # + dozzle, postgres-lite
./mode desktop                # semua turun, data aman
```

Validasi tanpa menjalankan apa pun:

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
│   ├── dynamic/            # dashboard.yml (router private)
│   └── certs/              # mkcert *.lan (nanti)
├── DESIGN.md               # arah desain UI portal (dipakai antislop)
├── publish                 # CLI fallback: tambah hostname via API Cloudflare
├── stacks/
│   ├── whoami/             # pattern PUBLIC (profile lite)
│   ├── postgres-lite/      # contoh lab (profile lab, network internal)
│   └── portal/             # Infra Manager (Go + Dockerfile + compose)
└── data/                   # bind mount postgres (nanti pindah /srv)
```

Menambah stack: bikin `stacks/<nama>/compose.yaml`, tambahkan path ke `include`
di `compose.yaml`, set `profiles` sesuai mode, kasih `mem_limit` + `cpus`.
