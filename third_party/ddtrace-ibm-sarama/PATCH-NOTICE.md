# Local official-namespace IBM/Sarama patch

Baseline: `github.com/DataDog/dd-trace-go/contrib/IBM/sarama/v2@v2.3.0`.
Retain the original licenses, copyright notices, module/import paths and APIs.
This is a local backport, not a DataDog-published release.

- `producer.go`: at most one pending input; send through a select case so
  successes/errors remain drainable during underlying input backpressure.
  Preserve input order and finish an unsent pending span on shutdown.
- `backpressure_patch_test.go`: deterministic success/error backpressure,
  bounded pending input, order, span finish timing and shutdown coverage.
- `go.mod` / `go.sum`: standalone validation uses official core v2.10.1 and
  IBM/Sarama v1.43.2. Remove the upstream repository-relative core replacement.
- `sarama_test.go`: allow the upstream integration suite's broker address to
  be set with KAFKA_BROKERS; default localhost:9092 remains unchanged.

This profile does not upgrade/downgrade an application's existing modules.
Other IBM integration/raw client/core versions require separate validation.
The exact raw-client matrix is v1.43.2 and v1.44.0, not an inferred interval.
Use official Orchestrion v1.12.1; official v1 manual tracing is tested at
v1.74.8. This does not implement isolation between official and fork tracers.
Applications must continue draining producer successes/errors. No exactly-once
guarantee or private-application compatibility certification is provided.
