GO     ?= GO15VENDOREXPERIMENT=1 go
GOPATH := $(firstword $(subst :, ,$(shell $(GO) env GOPATH)))

PROMU       ?= $(GOPATH)/bin/promu
pkgs         = $(shell $(GO) list ./... | grep -v /vendor/)

PREFIX                  ?= $(shell pwd)
BIN_DIR                 ?= $(shell pwd)
REGISTRY       ?= kubedb
DOCKER_IMAGE_NAME       ?= pgbouncer_exporter
# DOCKER_IMAGE_TAG        ?= $(subst /,-,$(shell git rev-parse --abbrev-ref HEAD))
DOCKER_IMAGE_TAG        ?= latest


ifeq ($(OS),Windows_NT)
    OS_detected := Windows
else
    OS_detected := $(shell uname -s)
endif

build: $(PROMU)
	@echo ">> building binaries"
	@$(PROMU) build --prefix $(PREFIX)

docker:
	@echo ">> building binary"
	@CGO_ENABLED=0 go build -o pgbouncer_exporter_tmp_bin .
# 	@chmod +x pgbouncer_exporter_tmp_bin
	@echo ">> building docker image"
	@docker build -t "$(REGISTRY)/$(DOCKER_IMAGE_NAME):$(DOCKER_IMAGE_TAG)" .
	@rm -rf pgbouncer_exporter_tmp_bin
 	@docker push "$(REGISTRY)/$(DOCKER_IMAGE_NAME):$(DOCKER_IMAGE_TAG)"

$(GOPATH)/bin/promu promu:
	@GOOS= GOARCH= $(GO) get -u github.com/prometheus/promu

$(GOPATH)/bin/staticcheck:
	@GOOS= GOARCH= $(GO) get -u honnef.co/go/tools/cmd/staticcheck

.PHONY: all style format build test test-e2e vet tarball docker promu staticcheck

# Declaring the binaries at their default locations as PHONY targets is a hack
# to ensure the latest version is downloaded on every make execution.
# If this is not desired, copy/symlink these binaries to a different path and
# set the respective environment variables.
.PHONY: $(GOPATH)/bin/promu $(GOPATH)/bin/staticcheck
