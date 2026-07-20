IMAGE       ?= dday:latest
PORT        ?= 3329
MATRIX_ENV  ?= matrix.env

.PHONY: help run dev build docker up down logs fmt vet tidy test keys \
        check check-keys matrix-hello matrix-send bot

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-13s\033[0m %s\n", $$1, $$2}'

run: ## Run the web server locally on $(PORT)
	PORT=$(PORT) go run .

dev: ## Run the web server serving index.html from disk (live edits)
	PORT=$(PORT) STATIC_DIR=. go run .

build: ## Build the web server binary
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o dday .

docker: ## Build the Docker image
	docker build -t $(IMAGE) .

up: ## Start via docker compose (Traefik on dday.hs-ldz.pl)
	@test -f config/dday_ed25519 || { echo "config/dday_ed25519 missing — run: make keys"; exit 1; }
	@test -f config/dday_ed25519.pub || { echo "config/dday_ed25519.pub missing — run: make keys"; exit 1; }
	@test -f matrix.env || { echo "matrix.env missing — create matrix.env (see matrix.env.example) for the bot"; exit 1; }
	@tok=$$(grep -E '^INTERNAL_TOKEN=' .env 2>/dev/null | head -n1 | cut -d= -f2-); \
	 if [ -z "$$tok" ]; then tok=$$(openssl rand -hex 32); echo "generated new INTERNAL_TOKEN"; \
	 else echo "reusing existing INTERNAL_TOKEN from .env"; fi; \
	 sec=$$(grep -E '^TOKEN_SECRET=' .env 2>/dev/null | head -n1 | cut -d= -f2-); \
	 if [ -z "$$sec" ]; then sec=$$(openssl rand -hex 32); echo "generated new TOKEN_SECRET"; \
	 else echo "reusing existing TOKEN_SECRET from .env"; fi; \
	 adm=$$(grep -E '^ADMIN_TOKEN=' .env 2>/dev/null | head -n1 | cut -d= -f2-); \
	 if [ -z "$$adm" ]; then adm=$$(openssl rand -hex 32); echo "generated new ADMIN_TOKEN"; \
	 else echo "reusing existing ADMIN_TOKEN from .env"; fi; \
	 ro=$$(grep -E '^REGISTRATION_OPEN=' .env 2>/dev/null | head -n1 | cut -d= -f2-); \
	 if [ -z "$$ro" ]; then ro=1; echo "defaulting REGISTRATION_OPEN=1 (registration OPEN now)"; \
	 else echo "reusing existing REGISTRATION_OPEN=$$ro from .env"; fi; \
	 { printf '# REGISTRATION_OPEN=1 keeps registration OPEN now; set 0 or delete this\n'; \
	   printf '# line to fall back to the built-in time gate (REGISTRATION_OPEN_AT).\n'; \
	   printf 'REGISTRATION_OPEN=%s\n' "$$ro"; \
	   printf 'AGE_KEY_DATA=%s\n' "$$(base64 < config/dday_ed25519 | tr -d '\n')"; \
	   printf 'AGE_PUB_DATA=%s\n' "$$(base64 < config/dday_ed25519.pub | tr -d '\n')"; \
	   printf 'INTERNAL_TOKEN=%s\n' "$$tok"; \
	   printf 'TOKEN_SECRET=%s\n' "$$sec"; \
	   printf '# ADMIN_TOKEN guards the read-only admin view: https://dday.hs-ldz.pl/admin?t=<ADMIN_TOKEN>\n'; \
	   printf '# The URL contains a secret — do not paste it publicly. Empty = /admin disabled (404).\n'; \
	   printf 'ADMIN_TOKEN=%s\n' "$$adm"; } > .env
	@echo "wrote .env (REGISTRATION_OPEN + AGE_KEY_DATA + AGE_PUB_DATA + INTERNAL_TOKEN + TOKEN_SECRET + ADMIN_TOKEN)"
	docker compose up -d --build

down: ## Stop the compose stack
	docker compose down

logs: ## Tail compose logs
	docker compose logs -f

fmt: ## Format Go code
	gofmt -w .

vet: ## Run go vet
	go vet ./...

test: ## Run the full test suite (race detector)
	go test -race ./...

tidy: ## Tidy modules
	go mod tidy

keys: ## Generate the age ed25519 keypair into config/ (gitignored)
	@./scripts/gen-age-key.sh

check: ## Validate matrix.env exists and required vars are set
	@MATRIX_ENV=$(MATRIX_ENV) ./scripts/check-config.sh

check-keys: ## Validate config incl. the age keypair
	@MATRIX_ENV=$(MATRIX_ENV) ./scripts/check-config.sh --keys

matrix-hello: check ## Send "hello world" to the Matrix room
	@MATRIX_ENV=$(MATRIX_ENV) ./scripts/send-matrix.sh

matrix-send: check ## Send a custom message: make matrix-send MSG="..."
	@MATRIX_ENV=$(MATRIX_ENV) ./scripts/send-matrix.sh "$(MSG)"

bot: check-keys ## Run the !start bot (foreground)
	@set -a; . ./$(MATRIX_ENV); set +a; go run ./cmd/bot
