DOCKER_USERNAME ?=
TAG ?= latest

PROXY_IMAGE := shinel-proxy
ML_IMAGE := shinel-ml-engine

.PHONY: build up down test push

build:
	docker build -f Dockerfile.proxy -t $(PROXY_IMAGE):$(TAG) .
	docker build -f Dockerfile.python -t $(ML_IMAGE):$(TAG) .

up:
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

push:
	@if [ -z "$(DOCKER_USERNAME)" ]; then \
		echo "set DOCKER_USERNAME to your Docker Hub user, e.g. make push DOCKER_USERNAME=you"; \
		exit 1; \
	fi
	docker tag $(PROXY_IMAGE):$(TAG) $(DOCKER_USERNAME)/$(PROXY_IMAGE):$(TAG)
	docker tag $(ML_IMAGE):$(TAG) $(DOCKER_USERNAME)/$(ML_IMAGE):$(TAG)
	docker push $(DOCKER_USERNAME)/$(PROXY_IMAGE):$(TAG)
	docker push $(DOCKER_USERNAME)/$(ML_IMAGE):$(TAG)
