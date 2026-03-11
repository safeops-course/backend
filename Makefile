SHELL := /bin/bash

BIN_DIR := $(CURDIR)/bin
PORT ?= 8080
GO_VERSION ?= 1.24.0
APP_VERSION ?= $(shell git describe --tags --abbrev=0 2>/dev/null || echo 0.0.0)
APP_COMMIT ?= $(shell git rev-parse HEAD 2>/dev/null || echo dev)
APP_COMMIT_SHORT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo dev)
BUILD_DATE ?= $(shell date -u +'%Y-%m-%dT%H:%M:%SZ')
REGISTRY_HOST ?= ghcr.io
REGISTRY_NAMESPACE ?= ldbl
REGISTRY ?= $(REGISTRY_HOST)/$(REGISTRY_NAMESPACE)
IMAGE_NAME ?= sre-backend
TAG ?= $(APP_VERSION)
REMOTE_IMAGE ?= $(REGISTRY)/$(IMAGE_NAME):$(TAG)
LOCAL_IMAGE ?= $(IMAGE_NAME):$(TAG)
DOCKER_USER ?= $(shell git config github.user 2>/dev/null || echo $(USER))
LDFLAGS := -s -w \
	-X github.com/ldbl/sre/backend/pkg/version.Version=$(APP_VERSION) \
	-X github.com/ldbl/sre/backend/pkg/version.Commit=$(APP_COMMIT) \
	-X github.com/ldbl/sre/backend/pkg/version.ShortCommit=$(APP_COMMIT_SHORT) \
	-X github.com/ldbl/sre/backend/pkg/version.BuildDate=$(BUILD_DATE)

.PHONY: build run image publish

build:
	@mkdir -p $(BIN_DIR)
	@echo "[build] Compiling backend binary"
	go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/backend ./cmd/api

run:
	@echo "[run] Starting backend on port $(PORT)"
	PORT=$(PORT) go run -ldflags "$(LDFLAGS)" ./cmd/api

image:
	@echo "[image] Building Docker image $(REMOTE_IMAGE)"
	docker build \
		--build-arg GO_VERSION=$(GO_VERSION) \
		--build-arg APP_VERSION=$(APP_VERSION) \
		--build-arg APP_COMMIT=$(APP_COMMIT) \
		--build-arg APP_COMMIT_SHORT=$(APP_COMMIT_SHORT) \
		--build-arg BUILD_DATE=$(BUILD_DATE) \
		-t $(REMOTE_IMAGE) \
		-t $(LOCAL_IMAGE) .

publish: image ## Push the backend image to the configured registry
	@if ! docker info >/dev/null 2>&1; then \
		echo "Docker daemon not available"; exit 1; \
	fi
	@if [ -n "$(DOCKER_PAT)" ]; then \
		echo "Logging in to $(REGISTRY_HOST) as $(DOCKER_USER)"; \
		echo "$(DOCKER_PAT)" | docker login $(REGISTRY_HOST) -u "$(DOCKER_USER)" --password-stdin; \
	fi
	@echo "[publish] Pushing $(REMOTE_IMAGE)"
	docker push $(REMOTE_IMAGE)
