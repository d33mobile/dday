IMAGE ?= dday:latest
PORT  ?= 3329

.PHONY: help run dev build docker up down logs fmt vet tidy

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2}'

run: ## Run the embedded server locally on $(PORT)
	PORT=$(PORT) go run .

dev: ## Run serving index.html from the filesystem (live edits, no rebuild)
	PORT=$(PORT) STATIC_DIR=. go run .

build: ## Build the binary
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o dday .

docker: ## Build the Docker image
	docker build -t $(IMAGE) .

up: ## Start via docker compose (Traefik on dday.hs-ldz.pl)
	docker compose up -d --build

down: ## Stop the compose stack
	docker compose down

logs: ## Tail compose logs
	docker compose logs -f

fmt: ## Format Go code
	gofmt -w .

vet: ## Run go vet
	go vet ./...

tidy: ## Tidy modules
	go mod tidy
