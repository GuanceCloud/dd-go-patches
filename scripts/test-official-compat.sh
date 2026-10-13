#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
build_dir=$(mktemp -d "${TMPDIR:-/tmp}/dd-go-patches-compat.XXXXXX")
sarama_version=${1:-v1.23.0}
case "$sarama_version" in
  v1.23.0|v1.38.1) ;;
  *) printf 'Unsupported Sarama test profile\n' >&2; exit 1 ;;
esac
mkdir -p "$build_dir/tools"
if [[ -n "${ORCHESTRION_BIN:-}" ]]; then
  test -x "$ORCHESTRION_BIN"
  ln -s "$ORCHESTRION_BIN" "$build_dir/tools/orchestrion"
else
  GOBIN="$build_dir/tools" go install github.com/DataDog/orchestrion@v1.12.1
fi
export PATH="$build_dir/tools:$PATH"
export DD_TRACE_AGENT_URL="${DD_TRACE_AGENT_URL:-http://127.0.0.1:9529}"
export DD_TRACE_STARTUP_LOGS=false
export DD_INSTRUMENTATION_TELEMETRY_ENABLED=false
export PATCH_APP_TARGET=./cmd/smoke

# Run in a disposable copy so neither profile rewrites the maintained fixture.
cp -R "$repo_root/tests/official-compat" "$build_dir/fixture"
cd "$build_dir/fixture"
go mod edit \
  -replace="github.com/Shopify/sarama=github.com/Shopify/sarama@$sarama_version" \
  -dropreplace="github.com/DataDog/dd-trace-go/contrib/Shopify/sarama/v2"
go mod tidy
export PATCH_SOURCE_DIR="$repo_root/third_party/ddtrace-sarama"
go run "$repo_root/patches/verify-official.go" --inspect
go run "$repo_root/patches/verify-official.go" --apply
go run "$repo_root/patches/verify-official.go"
orchestrion go test -c -o "$build_dir/compat.test" .
"$build_dir/compat.test" -test.v -test.count=3 -test.timeout=60s
CGO_ENABLED=0 orchestrion go build -o "$build_dir/smoke" ./cmd/smoke
go run "$repo_root/patches/verify-official.go" "$build_dir/smoke"
"$build_dir/smoke"
printf 'COMPAT_CHECK_OK effective_sarama=%s outputs=%s\n' "$sarama_version" "$build_dir"
