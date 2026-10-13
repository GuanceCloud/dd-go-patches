#!/usr/bin/env bash
# This explicitly fault-tests only the local reproduction Kafka container.
set -euo pipefail
repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
build_dir=$(mktemp -d "${TMPDIR:-/tmp}/dd-go-patches-ibm-pipeline.XXXXXX")
version=${1:-v1.43.2}
case "$version" in v1.43.2|v1.44.0) ;; *) printf 'Unsupported IBM pipeline version\n' >&2; exit 1 ;; esac
integration_version=${COMPAT_INTEGRATION_VERSION:-v2.3.0}
case "$integration_version:$version" in
  v2.3.0:v1.43.2|v2.3.0:v1.44.0|v2.10.1:v1.44.0) ;;
  *) printf 'Unsupported IBM pipeline profile\n' >&2; exit 1 ;;
esac
cp -R "$repo_root/tests/ibm-pipeline" "$build_dir/fixture"
cd "$build_dir/fixture"
export GOWORK=off REAL_KAFKA_REPRO=1
export DD_TRACE_AGENT_URL="${DD_TRACE_AGENT_URL:-http://127.0.0.1:9529}"
export DD_TRACE_STARTUP_LOGS=false DD_INSTRUMENTATION_TELEMETRY_ENABLED=false
case "$integration_version" in
  v2.3.0) ibm_source_dir="$repo_root/third_party/ddtrace-ibm-sarama" ;;
  v2.10.1) ibm_source_dir="$repo_root/third_party/ddtrace-ibm-sarama-v2.10.1" ;;
esac
go mod edit -require="github.com/DataDog/dd-trace-go/contrib/IBM/sarama/v2@$integration_version"
go mod edit -replace="github.com/DataDog/dd-trace-go/contrib/IBM/sarama/v2=$ibm_source_dir"
go mod edit -require="github.com/IBM/sarama@$version"
go mod tidy
cp go.mod baseline.mod
cp go.sum baseline.sum
go mod edit -modfile=baseline.mod -dropreplace=github.com/DataDog/dd-trace-go/contrib/IBM/sarama/v2
go mod tidy -modfile=baseline.mod
go test -mod=readonly -modfile=baseline.mod -c -o "$build_dir/baseline.test" .
go test -mod=readonly -c -o "$build_dir/patched.test" .
for scenario in baseline patched unwrapped; do
  run_id="ibm-${version//./-}-${scenario}-$(date +%Y%m%dT%H%M%S)"
  binary=$build_dir/patched.test
  test_name=TestRealKafkaConsumerPipeline
  recovery=1
  if [[ "$scenario" == baseline ]]; then binary=$build_dir/baseline.test; recovery=0; fi
  if [[ "$scenario" == unwrapped ]]; then test_name=TestRealKafkaConsumerPipelineWithoutDDTrace; fi
  go version -m "$binary" > "$build_dir/$scenario.build-info.txt"
  EXPECT_DDTRACE_RECOVERY="$recovery" REPRO_RUN_ID="$run_id" "$binary" \
    -test.run "^${test_name}$" -test.v -test.timeout=90s | tee "$build_dir/$scenario.log"
done
printf 'IBM_PIPELINE_OK outputs=%s\n' "$build_dir"
