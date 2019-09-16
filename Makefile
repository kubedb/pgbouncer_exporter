# Copyright 2019 AppsCode Inc.
# Copyright 2016 The Kubernetes Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

SHELL=/bin/bash -o pipefail

# The binary to build (just the basename).
BIN      := pgbouncer_exporter
COMPRESS ?= no

# Where to push the docker image.
REGISTRY ?= kubedb

# This version-strategy uses git tags to set the version string
git_branch       := $(shell git rev-parse --abbrev-ref HEAD)
git_tag          := $(shell git describe --exact-match --abbrev=0 2>/dev/null || echo "")
commit_hash      := $(shell git rev-parse --verify HEAD)
commit_timestamp := $(shell date --date="@$$(git show -s --format=%ct)" --utc +%FT%T)

VERSION          := $(shell git describe --tags --always --dirty)
version_strategy := commit_hash
ifdef git_tag
	VERSION := $(git_tag)
	version_strategy := tag
else
	ifeq (,$(findstring $(git_branch),master HEAD))
		ifneq (,$(patsubst release-%,,$(git_branch)))
			VERSION := $(git_branch)
			version_strategy := branch
		endif
	endif
endif

###
### These variables should not need tweaking.
###


OS               := $(if $(GOOS),$(GOOS),$(shell go env GOOS))
ARCH             := $(if $(GOARCH),$(GOARCH),$(shell go env GOARCH))
# TAG              := $(VERSION)
TAG              := 0.0.3
PREFIX           ?= $(shell pwd)
DOCKER_IMAGE     ?= $(BIN)

ifeq ($(OS),Windows_NT)
    OS_detected := Windows
else
    OS_detected := $(shell uname -s)
endif

build: $(PROMU)
	@echo ">> building binaries"
	@$(PROMU) build --prefix $(PREFIX)

container:
	@echo ">> building binary"
	@CGO_ENABLED=0 go build -o $(BIN) .
# 	@chmod +x pgbouncer_exporter_tmp_bin
	@echo ">> building docker image"
	@docker build -t "$(REGISTRY)/$(DOCKER_IMAGE):$(TAG)" .
# 	@rm -rf $(BIN)

push: container
	@echo ">> pushing image"
	@docker push "$(REGISTRY)/$(DOCKER_IMAGE):$(TAG)"


.PHONY: all style format build test test-e2e vet tarball docker promu staticcheck

# Declaring the binaries at their default locations as PHONY targets is a hack
# to ensure the latest version is downloaded on every make execution.
# If this is not desired, copy/symlink these binaries to a different path and
# set the respective environment variables.
.PHONY: $(GOPATH)/bin/promu $(GOPATH)/bin/staticcheck
