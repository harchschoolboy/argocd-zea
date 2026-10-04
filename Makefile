VERSION ?= dev
IMAGE ?= ghcr.io/harchschoolboy/argocd-zea-backend
GO_IMAGE ?= golang:1.26-alpine

# Use local Go when available, otherwise run inside a container.
ifeq ($(shell command -v go 2>/dev/null),)
GO_RUN = docker run --rm -v $(CURDIR)/backend:/src -w /src $(GO_IMAGE)
else
GO_RUN = cd backend &&
endif

.PHONY: all backend-test backend-image ui helm-lint

all: backend-test ui helm-lint

backend-test:
	$(GO_RUN) sh -c 'test -z "$$(gofmt -l .)" || (gofmt -l . && exit 1); go vet ./... && go test ./...'

backend-image:
	docker build --build-arg VERSION=$(VERSION) -t $(IMAGE):$(VERSION) backend

ui:
	cd ui && npm ci && npm run typecheck && npm run build && npm run package

helm-lint:
	helm lint deploy/helm/zea
	helm lint deploy/helm/zea --set proxyToken.source=value --set proxyToken.value=lint
