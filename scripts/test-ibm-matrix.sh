#!/usr/bin/env bash
set -euo pipefail
repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
matrix_dir=$(mktemp -d "${TMPDIR:-/tmp}/dd-go-patches-ibm-matrix.XXXXXX")
cd "$repo_root/third_party/ddtrace-ibm-sarama"
export GOWORK=off
for version in v1.43.2 v1.44.0; do
  cp go.mod "$matrix_dir/$version.mod"
  cp go.sum "$matrix_dir/$version.sum"
  go mod edit -modfile="$matrix_dir/$version.mod" \
    -require="github.com/IBM/sarama@$version" \
    -replace="github.com/IBM/sarama=github.com/IBM/sarama@$version"
  go mod tidy -modfile="$matrix_dir/$version.mod"
  go list -mod=readonly -modfile="$matrix_dir/$version.mod" -m -json github.com/IBM/sarama
  go test -mod=readonly -modfile="$matrix_dir/$version.mod" -race -count=10 -timeout=180s ./...
  go vet -mod=readonly -modfile="$matrix_dir/$version.mod" ./...
  printf 'IBM_MATRIX_OK effective_version=%s\n' "$version"
done
printf 'IBM_MATRIX_OUTPUTS=%s\n' "$matrix_dir"
