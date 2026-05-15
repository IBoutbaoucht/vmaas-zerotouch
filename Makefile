# VMaaS — convenience targets.
#
# All commands run from the implementation/ directory.
#   make help        list targets
#   make build       compile the backend locally (no docker)
#   make test        run backend unit tests
#   make images      build the docker images
#   make up          docker compose up --build (foreground)
#   make down        stop the stack
#   make logs        tail backend + frontend logs
#   make e2e         run scripts/e2e.sh against the running stack
#   make clean       wipe local bbolt state (DANGEROUS — also removes any
#                    inventory we recorded; VMs on ESXi are NOT touched)

SHELL := /bin/bash
.DEFAULT_GOAL := help

API_TOKEN := $(shell awk -F= '/^VMAAS_TOKEN=/{print $$2}' .env 2>/dev/null)

.PHONY: help build test images up down logs e2e clean

help:
	@awk -F':.*## ' '/^[a-zA-Z_-]+:.*## /{printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## Compile the backend (Go) locally
	cd backend && go build -trimpath -o vmaasd ./cmd/vmaasd

test: ## Run backend unit tests
	cd backend && go test ./...

images: ## Build the backend and frontend docker images
	docker compose build

up: ## Bring the stack up (build + foreground)
	@test -f .env || { echo "Missing .env — copy .env.example and edit"; exit 1; }
	docker compose up --build

down: ## Stop the stack and remove containers
	docker compose down

logs: ## Tail logs from both services
	docker compose logs -f --tail=100

e2e: ## End-to-end: provision a VM, SSH into it, delete it
	@test -n "$(API_TOKEN)" || { echo "VMAAS_TOKEN not set in .env"; exit 1; }
	./scripts/e2e.sh

clean: ## Wipe local bbolt state. ESXi-side VMs are NOT removed.
	@read -p "Delete ./data/state.db? [y/N] " ans && [[ "$$ans" == "y" ]] && rm -f data/state.db || echo "skipped"
