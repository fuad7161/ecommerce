# Production deployment

These steps target a single Linux server (2 vCPU / 4 GB RAM is plenty to start) with Docker Engine and the Compose plugin installed, and a domain name pointing at the server.

## 1. Configure

```bash
git clone <repo> shop && cd shop
cp .env.production.example .env.production
nano .env.production        # set DOMAIN, ACME_EMAIL and every password/secret
```

Generate secrets with `openssl rand -hex 32` (for `SECRET_KEY`) and `openssl rand -base64 24` (for the passwords).

## 2. Start

```bash
docker compose -f docker-compose.prod.yml --env-file .env.production up -d --build
docker compose -f docker-compose.prod.yml --env-file .env.production ps
```

- Caddy gets a Let's Encrypt certificate automatically. Ports 80 and 443 must be reachable from the internet.
- Postgres, Redis and MinIO have no published ports.
- The app runs as a non-root user in a distroless image.
- Migrations run automatically on startup (`AUTO_MIGRATE=true`).

## 3. Create the first admin

```bash
docker compose -f docker-compose.prod.yml --env-file .env.production run --rm app \
  create-admin -email owner@yourshop.com -name "Owner" -role admin -password 'a-long-unique-password'
```

Log in at `https://<DOMAIN>/admin`. Then:
- **Settings:** store name, contact details, delivery zones and charges, free-delivery threshold, policies.
- **Homepage:** choose which sections to show.
- **Categories → Products:** add the catalog.
- **Admin users:** create staff accounts (they get orders and customers only).

Don't run `seed` in production; it loads demo data.

## 4. Steadfast

Get the API key and secret from the Steadfast merchant panel, set `STEADFAST_API_KEY` and `STEADFAST_SECRET_KEY` in `.env.production`, and restart the app:

```bash
docker compose -f docker-compose.prod.yml --env-file .env.production up -d app
```

**Settings → Steadfast Courier** should show "Connected" and the balance.

## 5. Updates

```bash
git pull
docker compose -f docker-compose.prod.yml --env-file .env.production up -d --build app
```

Migrations are applied at startup. Take a backup first (see below).

## Backups

- **Database:** the `backup` service writes `backups/db-YYYYMMDD-HHMMSS.sql.gz` every night at `BACKUP_HOUR` (Dhaka time) and keeps `BACKUP_KEEP_DAYS` days. Copy the `backups/` folder off the server regularly, for example with `rclone` to cloud storage.
- **Product images:** they live in the `miniodata` volume. Back it up with:
  ```bash
  docker run --rm -v ecommerce-prod_miniodata:/data -v $PWD/backups:/out alpine \
    tar czf /out/minio-$(date +%F).tgz -C /data .
  ```
- **Restore the database:**
  ```bash
  gunzip -c backups/db-XXXX.sql.gz | docker compose -f docker-compose.prod.yml --env-file .env.production \
    exec -T postgres psql -U "$POSTGRES_USER" "$POSTGRES_DB"
  ```
  Restore into an empty database: run `docker compose … down`, remove the `pgdata` volume, start only `postgres`, then pipe the dump in.

## Operations

| Task | Command (`-f docker-compose.prod.yml --env-file .env.production`) |
|---|---|
| Logs (JSON) | `docker compose … logs -f app` |
| Health | `curl https://<DOMAIN>/healthz` |
| SQL shell | `docker compose … exec postgres psql -U ecommerce ecommerce` |
| Restart one service | `docker compose … restart redis` |

**Scaling:** the app is stateless (sessions live in Postgres, the cache in Redis), so you can run several replicas behind Caddy. The courier sync uses a Postgres advisory lock, so only one replica polls Steadfast.
