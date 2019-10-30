#!/usr/bin/env bash

# Copyright The KubeDB Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     https://opensource.org/licenses/MIT
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -eou pipefail

if [ -z "${OS:-}" ]; then
    echo "OS must be set"
    exit 1
fi
if [ -z "${ARCH:-}" ]; then
    echo "ARCH must be set"
    exit 1
fi
if [ -z "${VERSION:-}" ]; then
    echo "VERSION must be set"
    exit 1
fi

export CGO_ENABLED=0
export GOARCH="${ARCH}"
export GOOS="${OS}"
export GO111MODULE=on
export GOFLAGS="-mod=vendor"

go install                                              \
    -installsuffix "static"                             \
    -ldflags "                                          \
      -X ${REPO_PKG}/vendor/github.com/prometheus/common/version.Version=${VERSION}        \
      -X ${REPO_PKG}/vendor/github.com/prometheus/common/version.Revision=${commit_hash:-} \
      -X ${REPO_PKG}/vendor/github.com/prometheus/common/version.Branch=${git_branch:-}    \
    "                                                   \
    ./...
