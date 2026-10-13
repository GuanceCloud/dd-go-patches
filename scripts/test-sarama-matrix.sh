#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
matrix_dir=$(mktemp -d "${TMPDIR:-/tmp}/dd-go-patches-matrix.XXXXXX")
cd "$repo_root/third_party/ddtrace-sarama"
export GOWORK=off

# Alternate modfiles isolate each tested business implementation. Never rewrite
# the repository module or force one version on applications using the patch.
for version in v1.23.0 v1.38.1; do
  cp go.mod "$matrix_dir/$version.mod"
  cp go.sum "$matrix_dir/$version.sum"
  go mod edit -modfile="$matrix_dir/$version.mod" \
    -require="github.com/Shopify/sarama@$version" \
    -replace="github.com/Shopify/sarama=github.com/Shopify/sarama@$version"
  go mod tidy -modfile="$matrix_dir/$version.mod"
  go test -mod=readonly -modfile="$matrix_dir/$version.mod" -race -count=10 -timeout=120s ./...
  go vet -mod=readonly -modfile="$matrix_dir/$version.mod" ./...
  printf 'SARAMA_MATRIX_OK effective_version=%s\n' "$version"
done
printf 'MATRIX_OUTPUTS=%s\n' "$matrix_dir"
