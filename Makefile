SHELL := /bin/bash
COMPOSE := docker compose
TAILWIND_VERSION := v3.4.17
TAILWIND := bin/tailwindcss

.PHONY: help setup up down restart logs ps build seed create-admin migrate psql redis-cli \
        test test-integration e2e css css-watch run fmt vet backup restore clean

help: ## Show available commands
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

setup: ## Create .env from the example (first run)
	@test -f .env || (cp .env.example .env && echo "Created .env — review the secrets in it.")

# ---------- Docker stack ----------
up: setup ## Build and start all services (app, postgres, redis, minio)
	$(COMPOSE) up -d --build

down: ## Stop all services (data volumes are kept)
	$(COMPOSE) down

restart: ## Rebuild and restart only the app
	$(COMPOSE) up -d --build app

logs: ## Follow logs (make logs s=app for one service)
	$(COMPOSE) logs -f $(s)

ps: ## Show service status
	$(COMPOSE) ps

build: ## Build the app image
	$(COMPOSE) build app

seed: ## Load demo catalog and create the first admin (SEED_ADMIN_* in .env)
	$(COMPOSE) run --rm app seed

create-admin: ## Create an admin: make create-admin email=you@x.com name="You" password=... role=admin
	$(COMPOSE) run --rm app create-admin -email "$(email)" -name "$(name)" -password "$(password)" -role "$(or $(role),admin)"

migrate: ## Apply database migrations
	$(COMPOSE) run --rm app migrate

psql: ## Open a psql shell
	$(COMPOSE) exec postgres sh -c 'psql -U $$POSTGRES_USER $$POSTGRES_DB'

redis-cli: ## Open a redis-cli shell
	$(COMPOSE) exec redis redis-cli

# ---------- Tests ----------
test: ## Run unit tests
	go test ./...

test-integration: ## Run all tests including database integration tests (needs postgres running)
	@source .env && TEST_DATABASE_URL="postgres://$$POSTGRES_USER:$$POSTGRES_PASSWORD@localhost:$${POSTGRES_PORT:-5432}/ecommerce_test?sslmode=disable" \
		go test -count=1 -race -p 1 ./...

# ---------- Local development without the app container ----------
$(TAILWIND):
	@mkdir -p bin
	@arch=$$(uname -m); case $$arch in aarch64|arm64) a=arm64;; *) a=x64;; esac; \
	os=$$(uname -s | tr A-Z a-z); [ "$$os" = darwin ] && os=macos; \
	curl -fsSL -o $(TAILWIND) https://github.com/tailwindlabs/tailwindcss/releases/download/$(TAILWIND_VERSION)/tailwindcss-$$os-$$a
	@chmod +x $(TAILWIND)

css: $(TAILWIND) ## Build CSS once
	$(TAILWIND) -c tailwind.config.js -i web/styles/input.css -o web/static/css/app.css --minify

css-watch: $(TAILWIND) ## Rebuild CSS on template changes
	$(TAILWIND) -c tailwind.config.js -i web/styles/input.css -o web/static/css/app.css --watch

run: css ## Run the app on the host against the dockerised postgres/redis/minio
	@source .env && \
	DATABASE_URL="postgres://$$POSTGRES_USER:$$POSTGRES_PASSWORD@localhost:$${POSTGRES_PORT:-5432}/$$POSTGRES_DB?sslmode=disable" \
	REDIS_URL="redis://localhost:$${REDIS_PORT:-6379}/0" MINIO_ENDPOINT="localhost:$${MINIO_API_PORT:-9000}" \
	MINIO_ACCESS_KEY="$$MINIO_ROOT_USER" MINIO_SECRET_KEY="$$MINIO_ROOT_PASSWORD" \
	go run ./cmd/server serve

fmt: ## Format code
	gofmt -w cmd internal web

vet: ## Static checks
	go vet ./...

# ---------- Backups ----------
backup: ## Dump the database to backups/<timestamp>.sql.gz
	@mkdir -p backups
	$(COMPOSE) exec -T postgres sh -c 'pg_dump -U $$POSTGRES_USER $$POSTGRES_DB' | gzip > backups/db-$$(date +%Y%m%d-%H%M%S).sql.gz
	@ls -1t backups | head -1

restore: ## Restore a dump: make restore file=backups/db-....sql.gz (overwrites data!)
	@test -n "$(file)" || (echo "usage: make restore file=backups/xxx.sql.gz" && exit 1)
	gunzip -c $(file) | $(COMPOSE) exec -T postgres sh -c 'psql -U $$POSTGRES_USER $$POSTGRES_DB'

clean: ## Stop services AND delete all data volumes
	$(COMPOSE) down -v

e2e: ## Run Playwright browser tests against the running stack (make up && make seed first)
	docker run --rm --network host --user $$(id -u):$$(id -g) -e HOME=/tmp -e BASE_URL=$${BASE_URL:-http://localhost:8080} \
		-v $(CURDIR)/tests/e2e:/e2e -w /e2e mcr.microsoft.com/playwright:v1.48.2-jammy \
		sh -c "npm install --no-audit --no-fund --loglevel=error && npx playwright test"
