DOCKER_USERNAME ?=
TAG ?= latest

PROXY_IMAGE := shinel-proxy
ML_IMAGE := shinel-ml-engine

ITEST_COMPOSE := docker compose -f docker-compose.itest.yml

# Load `.env` in the recipe shell (`set -a; . .env`) so quoted HF_TOKEN values
# work. Make's `-include .env` would keep the quotes in the token.

.PHONY: build up down test push itest

build:
	docker build -f Dockerfile.proxy -t $(PROXY_IMAGE):$(TAG) .
	@set -a; [ -f .env ] && . ./.env; set +a; \
	if [ -z "$$HF_TOKEN" ]; then \
		echo "warning: HF_TOKEN is unset; HuggingFace may stall the model download"; \
		docker build -f Dockerfile.python -t $(ML_IMAGE):$(TAG) .; \
	else \
		docker build -f Dockerfile.python --secret id=hf_token,env=HF_TOKEN -t $(ML_IMAGE):$(TAG) .; \
	fi

up:
	@set -a; [ -f .env ] && . ./.env; set +a; \
	export HF_TOKEN="$${HF_TOKEN-}"; \
	docker compose up -d

down:
	docker compose down

test:
	go test -race ./...
	@if [ -x ml_engine/.venv/bin/python ]; then \
		ml_engine/.venv/bin/python -m pytest -q ml_engine; \
	else \
		python3 -m pytest -q ml_engine; \
	fi

itest:
	@set -a; [ -f .env ] && . ./.env; set +a; \
	export HF_TOKEN="$${HF_TOKEN-}"; \
	if [ -z "$$HF_TOKEN" ]; then \
		echo "HF_TOKEN is empty. Put HF_TOKEN=hf_... in .env (gitignored) and retry."; \
		exit 1; \
	fi; \
	$(ITEST_COMPOSE) up --build --abort-on-container-exit --exit-code-from tester; \
	st=$$?; $(ITEST_COMPOSE) down; exit $$st

push:
	@if [ -z "$(DOCKER_USERNAME)" ]; then \
		echo "set DOCKER_USERNAME to your Docker Hub user, e.g. make push DOCKER_USERNAME=you"; \
		exit 1; \
	fi
	docker tag $(PROXY_IMAGE):$(TAG) $(DOCKER_USERNAME)/$(PROXY_IMAGE):$(TAG)
	docker tag $(ML_IMAGE):$(TAG) $(DOCKER_USERNAME)/$(ML_IMAGE):$(TAG)
	docker push $(DOCKER_USERNAME)/$(PROXY_IMAGE):$(TAG)
	docker push $(DOCKER_USERNAME)/$(ML_IMAGE):$(TAG)
