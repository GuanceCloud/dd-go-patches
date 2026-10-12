#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
build_dir=$(mktemp -d "${TMPDIR:-/tmp}/dd-go-patches-compat.XXXXXX")
mkdir -p "$build_dir/tools"
GOBIN="$build_dir/tools" go install github.com/DataDog/orchestrion@v1.12.1
export PATH="$build_dir/tools:$PATH"
export DD_TRACE_AGENT_URL="${DD_TRACE_AGENT_URL:-http://127.0.0.1:9529}"
export DD_TRACE_STARTUP_LOGS=false
export DD_INSTRUMENTATION_TELEMETRY_ENABLED=false

cd "$repo_root/tests/official-compat"
orchestrion go test -c -o "$build_dir/compat.test" .
"$build_dir/compat.test" -test.v -test.count=3 -test.timeout=60s
CGO_ENABLED=0 orchestrion go build -o "$build_dir/smoke" ./cmd/smoke
PATCH_SOURCE_DIR=../../third_party/ddtrace-sarama \
  go run ../../patches/verify-official.go "$build_dir/smoke"
"$build_dir/smoke"
printf 'COMPAT_CHECK_OK outputs=%s\n' "$build_dir"
