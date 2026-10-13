#!/usr/bin/env bash
set -euo pipefail
repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
build_dir=$(mktemp -d "${TMPDIR:-/tmp}/dd-go-patches-ibm-compat.XXXXXX")
mkdir -p "$build_dir/tools"
if [[ -n "${ORCHESTRION_BIN:-}" ]]; then
  test -x "$ORCHESTRION_BIN"
  ln -s "$ORCHESTRION_BIN" "$build_dir/tools/orchestrion"
else
  GOBIN="$build_dir/tools" go install github.com/DataDog/orchestrion@v1.12.1
fi
export PATH="$build_dir/tools:$PATH" GOWORK=off CGO_ENABLED=0
integration_version=${COMPAT_INTEGRATION_VERSION:-v2.3.0}
case "$integration_version" in
  v2.3.0) ibm_source_dir="$repo_root/third_party/ddtrace-ibm-sarama" ;;
  v2.10.1) ibm_source_dir="$repo_root/third_party/ddtrace-ibm-sarama-v2.10.1" ;;
  *) printf 'Unsupported IBM integration fixture version\n' >&2; exit 1 ;;
esac
export PATCH_IBM_SOURCE_DIR="$ibm_source_dir"
export PATCH_APP_TARGET=./cmd/smoke
export DD_TRACE_AGENT_URL="${DD_TRACE_AGENT_URL:-http://127.0.0.1:9529}"
export DD_TRACE_STARTUP_LOGS=false DD_INSTRUMENTATION_TELEMETRY_ENABLED=false
cp -R "$repo_root/tests/ibm-compat" "$build_dir/fixture"
cd "$build_dir/fixture"
go mod edit -dropreplace=github.com/DataDog/dd-trace-go/contrib/IBM/sarama/v2
go mod edit -require="github.com/DataDog/dd-trace-go/contrib/IBM/sarama/v2@$integration_version"
case "${COMPAT_SARAMA_VERSION:-v1.43.2}:$integration_version" in \
  v1.43.2:v2.3.0|v1.44.0:v2.3.0|v1.44.0:v2.10.1) ;; \
  *) printf 'Unsupported IBM/Sarama fixture profile\n' >&2; exit 1 ;;
esac
go mod edit -require="github.com/IBM/sarama@${COMPAT_SARAMA_VERSION:-v1.43.2}"
if [[ -n "${COMPAT_GRPC_VERSION:-}" ]]; then
  case "$COMPAT_GRPC_VERSION" in v1.57.2|v1.82.1) ;; *) printf 'Unsupported gRPC fixture version\n' >&2; exit 1 ;; esac
  go mod edit -replace="google.golang.org/grpc=google.golang.org/grpc@$COMPAT_GRPC_VERSION"
fi
go mod tidy
go run "$repo_root/patches/verify-official.go" --inspect
go run "$repo_root/patches/verify-official.go" --apply
go run "$repo_root/patches/verify-official.go"
orchestrion go test -c -o "$build_dir/compat.test" .
"$build_dir/compat.test" -test.v -test.count=3 -test.timeout=90s
orchestrion go build -o "$build_dir/smoke" ./cmd/smoke
go run "$repo_root/patches/verify-official.go" "$build_dir/smoke"
go version -m "$build_dir/smoke"
"$build_dir/smoke"
printf 'IBM_COMPAT_OK outputs=%s\n' "$build_dir"
