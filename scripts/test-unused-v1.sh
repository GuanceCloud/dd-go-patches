#!/usr/bin/env bash
# Reproduce tidy restoring legacy v1 metadata in an automatic-only application.
set -euo pipefail
repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
build_dir=$(mktemp -d "${TMPDIR:-/tmp}/dd-go-patches-unused-v1.XXXXXX")
mkdir -p "$build_dir/tools"
if [[ -n "${ORCHESTRION_BIN:-}" ]]; then
  test -x "$ORCHESTRION_BIN"
  ln -s "$ORCHESTRION_BIN" "$build_dir/tools/orchestrion"
else
  GOBIN="$build_dir/tools" go install github.com/DataDog/orchestrion@v1.12.1
fi
export PATH="$build_dir/tools:$PATH" GOWORK=off CGO_ENABLED=0
export DD_TRACE_STARTUP_LOGS=false DD_INSTRUMENTATION_TELEMETRY_ENABLED=false
export PATCH_SOURCE_DIR="$repo_root/third_party/ddtrace-sarama"
cp -R "$repo_root/tests/unused-v1" "$build_dir/fixture"
cd "$build_dir/fixture"
go mod edit -dropreplace=gopkg.in/DataDog/dd-trace-go.v1 \
  -dropreplace=gopkg.in/DataDog/dd-trace-go.v1@v1.74.8 \
  -require=gopkg.in/DataDog/dd-trace-go.v1@v1.74.8
go mod tidy
go list -mod=readonly -m -json gopkg.in/DataDog/dd-trace-go.v1 > "$build_dir/legacy-module.json"
grep -F '"Version": "v1.29.0-alpha.1"' "$build_dir/legacy-module.json"
go list -mod=readonly -deps -f '{{.ImportPath}}' . > "$build_dir/application-packages.txt"
if grep -E '^gopkg.in/DataDog/dd-trace-go\.v1(/|$)' "$build_dir/application-packages.txt"; then
  printf 'Fixture unexpectedly uses manual v1 at runtime\n' >&2
  exit 1
fi
go run "$repo_root/patches/verify-official.go" --inspect > "$build_dir/inspect.json"
grep -F '"manual_tracer_used":false' "$build_dir/inspect.json"
go run "$repo_root/patches/verify-official.go" --apply
go run "$repo_root/patches/verify-official.go"
orchestrion go build -mod=readonly -buildvcs=false -o "$build_dir/auto-only" .
go tool objdump -s '^main.main$' "$build_dir/auto-only" > "$build_dir/main.asm"
grep -F 'CALL github.com/DataDog/dd-trace-go/contrib/Shopify/sarama/v2.WrapAsyncProducer' "$build_dir/main.asm"
go run "$repo_root/patches/verify-official.go" "$build_dir/auto-only"
printf 'UNUSED_V1_OK legacy-metadata=retained manual-runtime-imports=absent automatic-patched-wrapper=verified outputs=%s\n' "$build_dir"
