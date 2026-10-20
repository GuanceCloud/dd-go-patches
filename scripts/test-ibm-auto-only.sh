#!/usr/bin/env bash
# Verify a genuinely automatic integration: no runtime tracing import in main.go.
set -euo pipefail
repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
build_dir=$(mktemp -d "${TMPDIR:-/tmp}/dd-go-patches-ibm-auto-only.XXXXXX")
mkdir -p "$build_dir/tools" "$build_dir/fixture"
if [[ -n "${ORCHESTRION_BIN:-}" ]]; then
  test -x "$ORCHESTRION_BIN"
  ln -s "$ORCHESTRION_BIN" "$build_dir/tools/orchestrion"
else
  GOBIN="$build_dir/tools" go install github.com/DataDog/orchestrion@v1.12.1
fi
export PATH="$build_dir/tools:$PATH" GOWORK=off CGO_ENABLED=0
export PATCH_IBM_SOURCE_DIR="$repo_root/third_party/ddtrace-ibm-sarama-v2.10.1"
export DD_TRACE_STARTUP_LOGS=false DD_INSTRUMENTATION_TELEMETRY_ENABLED=false
cp "$repo_root/tests/ibm-compat/go.mod" "$repo_root/tests/ibm-compat/go.sum" "$build_dir/fixture/"
cp "$repo_root/tests/ibm-auto-only/"*.go "$build_dir/fixture/"
cd "$build_dir/fixture"
go mod edit -module=example.com/ibm-auto-only \
  -require=github.com/IBM/sarama@v1.44.0 \
  -require=github.com/DataDog/dd-trace-go/contrib/IBM/sarama/v2@v2.10.1 \
  -replace="github.com/DataDog/dd-trace-go/contrib/IBM/sarama/v2=$PATCH_IBM_SOURCE_DIR"
go mod tidy
go run "$repo_root/patches/verify-official.go"
orchestrion go build -buildvcs=false -o "$build_dir/woven" .
go version -m "$build_dir/woven" > "$build_dir/woven.build-info.txt"
if grep -F 'dep' "$build_dir/woven.build-info.txt" | grep -F 'github.com/DataDog/dd-trace-go/contrib/IBM/sarama/v2'; then
  printf 'Fixture unexpectedly has a static Sarama tracing dependency\n' >&2
  exit 1
fi
go tool objdump -s '^main.main$' "$build_dir/woven" > "$build_dir/woven.main.asm"
grep -F 'CALL github.com/DataDog/dd-trace-go/contrib/IBM/sarama/v2.WrapAsyncProducer' "$build_dir/woven.main.asm"
go run "$repo_root/patches/verify-official.go" "$build_dir/woven"
"$build_dir/woven"
orchestrion go build -buildvcs=false -ldflags='-s -w' -o "$build_dir/stripped" .
go run "$repo_root/patches/verify-official.go" "$build_dir/stripped"
go build -buildvcs=false -o "$build_dir/unwoven" .
if go run "$repo_root/patches/verify-official.go" "$build_dir/unwoven" > "$build_dir/unwoven.verify.txt" 2>&1; then
  printf 'Unwoven producer incorrectly accepted\n' >&2
  exit 1
fi
grep -F 'source cannot be verified' "$build_dir/unwoven.verify.txt"
go mod edit -dropreplace=github.com/DataDog/dd-trace-go/contrib/IBM/sarama/v2
go mod tidy
orchestrion go build -buildvcs=false -o "$build_dir/unpatched" .
go mod edit -replace="github.com/DataDog/dd-trace-go/contrib/IBM/sarama/v2=$PATCH_IBM_SOURCE_DIR"
if go run "$repo_root/patches/verify-official.go" "$build_dir/unpatched" > "$build_dir/unpatched.verify.txt" 2>&1; then
  printf 'Unpatched woven producer incorrectly accepted\n' >&2
  exit 1
fi
grep -F 'source cannot be verified' "$build_dir/unpatched.verify.txt"
printf 'IBM_AUTO_ONLY_OK woven=accepted stripped=accepted unwoven=rejected unpatched=rejected outputs=%s\n' "$build_dir"
