#!/usr/bin/env bash
set -euo pipefail
repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
matrix_dir=$(mktemp -d "${TMPDIR:-/tmp}/dd-go-patches-ibm-matrix.XXXXXX")
export GOWORK=off
for profile in v2.3.0 v2.10.1; do
  case "$profile" in
    v2.3.0) source_dir="$repo_root/third_party/ddtrace-ibm-sarama"; versions=(v1.43.2 v1.44.0) ;;
    v2.10.1) source_dir="$repo_root/third_party/ddtrace-ibm-sarama-v2.10.1"; versions=(v1.44.0) ;;
  esac
  cd "$source_dir"
  for version in "${versions[@]}"; do
    modfile="$matrix_dir/${profile}-${version}.mod"
    cp go.mod "$modfile"
    cp go.sum "$matrix_dir/${profile}-${version}.sum"
    go mod edit -modfile="$modfile" \
      -require="github.com/IBM/sarama@$version" \
      -replace="github.com/IBM/sarama=github.com/IBM/sarama@$version"
    go mod tidy -modfile="$modfile"
    go list -mod=readonly -modfile="$modfile" -m -json github.com/IBM/sarama
    go test -mod=readonly -modfile="$modfile" -race -count=10 -timeout=180s ./...
    go vet -mod=readonly -modfile="$modfile" ./...
    printf 'IBM_MATRIX_OK integration=%s effective_version=%s\n' "$profile" "$version"
  done
done
printf 'IBM_MATRIX_OUTPUTS=%s\n' "$matrix_dir"
