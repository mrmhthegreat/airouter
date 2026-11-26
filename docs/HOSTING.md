# Hosting guide

There are **two different things** you can host:

| What | Where it can live | Needs a backend? |
|------|-------------------|------------------|
| **These docs** (`docs/index.html`, `swagger.html`, `openapi.yaml`) | GitHub Pages (free) | No, static files only |
| **The airouter server** (the real API, `airouter serve`) | Your PC, a VPS, Docker, Fly.io, Render, ... | Yes, a running Go process with a disk |

GitHub Pages can only serve static files, so it **cannot** run the API. Host the docs on Pages and run the server somewhere else.

---

## Part 1: Put the docs on GitHub Pages

The `docs/` folder is ready to publish. It contains:

```
docs/
  index.html      main docs site (endpoints, CLI, curl examples with live URL/key fields)
  swagger.html    Swagger UI (reads openapi.json)
  openapi.yaml    API spec (source of truth)
  openapi.json    same spec as JSON (used by swagger.html)
  API.md  CLI.md  HOSTING.md   the same docs as Markdown (readable on GitHub)
  tools/          build_docs.py + index.template.html (rebuilds index.html from openapi.yaml)
  ELEVENLABS.md  EMBEDDING.md   voice/cloning guide, Go library + multi-process guide
  .nojekyll       tells GitHub Pages to serve files as they are
```

### Option A: Deploy from the `/docs` folder (simplest)

1. Add the files and push:
   ```bash
   git add docs
   git commit -m "Add API/CLI docs and GitHub Pages site"
   git push origin main
   ```
2. On GitHub open your repo, then **Settings** -> **Pages**.
3. Under **Build and deployment**, set **Source** to **Deploy from a branch**.
4. Choose branch **main** and folder **/docs**, then **Save**.
5. Wait about a minute. Your site is at:
   - `https://mrmhthegreat.github.io/airouter/` (docs)
   - `https://mrmhthegreat.github.io/airouter/swagger.html` (Swagger UI)

Every push to `main` that changes `docs/` re-publishes automatically.

### Option B: Deploy with GitHub Actions

Use this if you want the deploy to be a visible workflow run. The file `.github/workflows/docs.yml` is included.

1. Push the repo with the workflow file.
2. **Settings** -> **Pages** -> **Source: GitHub Actions**.
3. Go to the **Actions** tab. The "Deploy docs" workflow runs on each push that changes `docs/` (or run it manually with **Run workflow**).

### Check it works

- Open the URL. You should see the "airouter documentation" page with a sidebar.
- Open `/swagger.html`. The endpoint list should load.
- Open `/openapi.yaml`. You should see the raw spec.

### Troubleshooting

| Problem | Fix |
|---------|-----|
| 404 page | Pages is not enabled, or the folder is wrong. Re-check Settings -> Pages (branch `main`, folder `/docs`). First deploy can take a few minutes. |
| Page works but Swagger is blank | `swagger.html` loads Swagger UI from cdnjs. If you are offline or the CDN is blocked it will not load. Also make sure `openapi.json` was pushed. |
| Swagger "Try it out" says *Failed to fetch* | The browser blocked the call. Start your server with `-cors https://mrmhthegreat.github.io` (or `-cors "*"` for testing), and use an `https://` server URL (browsers block `http://` calls from an `https://` page, except `http://localhost`). |
| Old content shows | Hard refresh (Ctrl+Shift+R). Pages caches for a few minutes. |
| `.md` links download instead of render | On Pages, `.md` files are served raw. Link to the GitHub view instead: `https://github.com/mrmhthegreat/airouter/blob/main/docs/API.md`. |

### Custom domain (optional)

1. Add a file `docs/CNAME` containing only your domain, for example `docs.example.com`.
2. At your DNS provider add a `CNAME` record: `docs` -> `mrmhthegreat.github.io`.
3. **Settings** -> **Pages** -> enter the custom domain and tick **Enforce HTTPS**.

### Keep the docs in sync with the API

`openapi.yaml` is the source of truth. After you change it (or the CLI list inside the script), rebuild `index.html` and `openapi.json` with one command:

```bash
pip install pyyaml
python3 docs/tools/build_docs.py
```

`docs/tools/index.template.html` is the page template; `build_docs.py` also holds the CLI command list shown on the site.

---

## Part 2: Host the actual server

Whatever you choose, remember:

- Run **one** server process per database if you can. Several processes on one SQLite file work (tested: no lock errors, no lost counters), but the per-minute rate limit is per process, see [EMBEDDING.md](EMBEDDING.md#several-processes-on-one-sqlite-file).
- Keep the database on a **persistent disk** (`AIROUTER_DB`).
- Set `AIROUTER_SECRET` (encrypts stored provider keys) and **back it up separately** from the DB. If you lose it, stored provider keys cannot be decrypted.
- Set `AIROUTER_ADMIN_TOKEN` to a long random value, or leave it unset to disable the admin API.
- Put the server behind **HTTPS**. Tenant keys travel in headers.
- If a website (such as the Pages docs) calls it from the browser, set `-cors https://mrmhthegreat.github.io` (or `AIROUTER_CORS`).

Generate secrets:

```bash
openssl rand -hex 32     # use one value for AIROUTER_SECRET
openssl rand -hex 24     # another for AIROUTER_ADMIN_TOKEN
```

### A. Your own machine

```bash
make build
export AIROUTER_ADMIN_TOKEN=change-me AIROUTER_SECRET=$(openssl rand -hex 32)
./bin/airouter bootstrap
./bin/airouter tenant add me
./bin/airouter serve -addr :8080
```

### B. Docker

```bash
docker build -t airouter .
docker run -d --name airouter --restart unless-stopped -p 8080:8080 \
  -v $(pwd)/data:/data \
  -e AIROUTER_SECRET=$(openssl rand -hex 32) \
  -e AIROUTER_ADMIN_TOKEN=change-me \
  -e AIROUTER_CORS=https://mrmhthegreat.github.io \
  -e GROQ_API_KEY=gsk_... -e GEMINI_API_KEY=... \
  airouter

# one-time setup inside the container
docker exec airouter airouter bootstrap -store-keys
docker exec airouter airouter tenant add acme -limit 2000
```

Use the **same** `AIROUTER_SECRET` every time you start the container with the same `data/` folder.

### C. A VPS (Ubuntu) with systemd and HTTPS (Caddy)

1. Copy the binary: `scp dist/airouter-*-linux-amd64 you@server:/usr/local/bin/airouter` (build it with `make release`).
2. Create `/etc/airouter.env`:
   ```
   AIROUTER_DB=/var/lib/airouter/airouter.db
   AIROUTER_SECRET=...
   AIROUTER_ADMIN_TOKEN=...
   AIROUTER_CORS=https://mrmhthegreat.github.io
   GROQ_API_KEY=...
   ```
   Then `chmod 600 /etc/airouter.env`.
3. Create `/etc/systemd/system/airouter.service`:
   ```ini
   [Unit]
   Description=airouter
   After=network.target

   [Service]
   EnvironmentFile=/etc/airouter.env
   StateDirectory=airouter
   ExecStart=/usr/local/bin/airouter serve -addr 127.0.0.1:8080
   Restart=always
   DynamicUser=yes

   [Install]
   WantedBy=multi-user.target
   ```
4. `sudo systemctl enable --now airouter`
5. Caddy (`/etc/caddy/Caddyfile`) gives automatic HTTPS:
   ```
   api.example.com {
     reverse_proxy 127.0.0.1:8080
   }
   ```
   The server allows requests up to 4 minutes, so keep the proxy timeout at least that long.

### D. Fly.io / Render / Railway (Docker based)

These build the repo's `Dockerfile`. Set the same environment variables as above and attach a **persistent volume mounted at `/data`** (without one, the database and tenants are wiped on every deploy). Run a single instance.

Fly.io example:

```bash
fly launch --no-deploy
fly volumes create data --size 1
# in fly.toml add:  [mounts] source = "data"  destination = "/data"
fly secrets set AIROUTER_SECRET=$(openssl rand -hex 32) AIROUTER_ADMIN_TOKEN=... AIROUTER_CORS=https://mrmhthegreat.github.io GROQ_API_KEY=...
fly deploy
```

Free tiers and their limits change. Check each provider's current pricing page.

### After the server is up

Use its URL in three places:

1. **OpenAI SDKs:** `base_url = https://api.example.com/v1`, `api_key = air_...`
2. **The docs site:** type the URL in the "Server URL" box on `index.html` (curl examples update) and in the API server box on `swagger.html`.
3. **The CLI from any machine:** `export AIROUTER_URL=https://api.example.com AIROUTER_KEY=air_...` then `airouter me list`.
