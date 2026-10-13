# Local official-namespace Shopify/Sarama patch

Baseline: `github.com/DataDog/dd-trace-go/contrib/Shopify/sarama/v2@v2.10.1`.
The original Apache-2.0 LICENSE and source copyright notices are retained.
This is a locally maintained patch, NOT a DataDog-published release.

Changes:

- `sarama.go`: keep at most one pending producer input; send it using a select
  case while continuing to drain underlying successes/errors. Preserve input
  order and finish a pending span with ErrShuttingDown when the wrapper exits.
- `backpressure_patch_test.go`: success/error backpressure, bounded pending
  input, order, finish timing, and shutdown coverage.
- `go.mod`: remove the upstream repository-relative core replacement
  (`../../..`) so this copied submodule can be tested independently; retain
  Shopify/Sarama v1.23.0 as the effective implementation for this release.
- `go.sum`: add official core checksums needed for independent tests.

Official module/import paths, APIs and `orchestrion.yml` are unchanged. This
patch does not implement runtime isolation between official and GuanceCloud
tracers. Deploy with official Orchestrion/core and reconcile any fork leftovers.

Validated target: official core/integration v2.10.1, Shopify/Sarama v1.23.0,
official Orchestrion v1.12.1. Other core versions, IBM/Sarama, or legacy v1
Sarama implementations are not covered by this patch. Applications must still
drain successes/errors according to the Sarama contract.
