# dd-go-patches

Small, auditable patches for Go tracing integrations. Preserve upstream module
and import paths, fix only the required behavior, and explicitly validate the
dependency combination. This is not an official DataDog release or another
tracer SDK.

## v0.2.2 candidate: IBM integration v2.10.1 profile

The next release candidate adds a distinct official IBM/Sarama integration
v2.10.1 patch. It preserves upstream APIs and newer cluster ID, DSM, service
name-source, and custom-tag behavior, changing only async producer forwarding.
The verifier selects the profile from actual application imports and the
resolved integration version. Supported profile: official core v2.10.1,
IBM integration v2.10.1, effective IBM/Sarama v1.44.0, Orchestrion v1.12.1.
This profile is separate from v2.3.0. The locally built candidate archive has
SHA256 `c6de22b6b0fd2d566fbd470ad4c40f86058a7c5c230bc092a8931a1932cdf98b`.
It is not customer-usable until that archive is uploaded to the v0.2.2 Release;
see the precise validation boundary in the [v0.2.2 release notes](docs/releases/v0.2.2.md).

## Published v0.2.1: Shopify and IBM/Sarama support

The [v0.2.1 Release](https://github.com/GuanceCloud/dd-go-patches/releases/tag/v0.2.1)
contains independent official-namespace patches for Shopify and IBM Sarama.
Download [dd-go-patches-v0.2.1.tar.gz](https://github.com/GuanceCloud/dd-go-patches/releases/download/v0.2.1/dd-go-patches-v0.2.1.tar.gz)
and verify SHA256 `042a13483990f8b2336417ab2085e5ded9ccf5fd1ea3c8fe37ad2f38946827ca`.
The archive contains both patch directories and the verifier.

IBM is validated with integration v2.3.0, core v2.10.1, and effective IBM/Sarama
v1.43.2 or v1.44.0. Shopify remains validated with integration/core v2.10.1 and
effective Shopify/Sarama v1.23.0 or v1.38.1. This is a local backport, not a
DataDog-published tracer release.

The customer Dockerfile for v0.2.2 must copy both IBM source directories from
the archive and must not hard-code `PATCH_IBM_SOURCE_DIR`; the verifier chooses
the source directory from the resolved official integration version after
`orchestrion pin`.

The verifier discovers actual dependencies of the build entrypoint `.`; use
`PATCH_APP_TARGET=./cmd/application` consistently for another build target.
It selects Shopify, IBM, or both when both are actually used. An unused module
or historical replacement does not select a patch. Each selected integration
is replaced and explicitly enabled atomically; selected/effective business
dependency drift rolls back the operation. Binary verification checks every
selected raw client and patched integration, not just Shopify.

Known API mismatch: actual official net/http integration v2.3.0 does not compile
against core v2.10.1 (`httpsec.ProtectRoundTrip`). If the application uses that
integration, explicitly align the **instrumentation module** to v2.10.1 before
pin/apply, and review the graph diff. `--apply` will not silently upgrade it:

```dockerfile
RUN go mod edit -require=github.com/DataDog/dd-trace-go/contrib/net/http/v2@v2.10.1
```

This does not change business imports, Sarama requirements/replacements or a
customer's gRPC replacement. Compatibility of private libraries remains a
customer regression obligation. An old HTTP module merely present in the tools
graph is not grounds for rejection. Other IBM/core versions are not inferred
to work and are not silently upgraded/downgraded to match this profile.

For a Dockerfile integration, pin the release version and checksum, copy both
patch directories, and explicitly align HTTP instrumentation to v2.10.1 when it
is used. See [the v0.2.1 migration notes](docs/releases/v0.2.1.md) for the
release-specific Dockerfile steps.

Run from a source checkout:

```bash
go test -race -count=10 ./patches/verify-official.go ./patches/verify-official_test.go
(cd third_party/ddtrace-ibm-sarama && go test -race -count=10 ./... && go vet ./...)
bash scripts/test-ibm-matrix.sh
# New profile: upstream IBM integration v2.10.1 + IBM/Sarama v1.44.0
COMPAT_INTEGRATION_VERSION=v2.10.1 COMPAT_SARAMA_VERSION=v1.44.0 bash scripts/test-ibm-compat.sh
# Explicitly opt-in: requires the local reproduction broker/container.
bash scripts/test-ibm-pipeline.sh
bash scripts/test-ibm-compat.sh
COMPAT_GRPC_VERSION=v1.57.2 bash scripts/test-ibm-compat.sh
COMPAT_SARAMA_VERSION=v1.44.0 COMPAT_GRPC_VERSION=v1.57.2 bash scripts/test-ibm-compat.sh
bash scripts/test-release-package.sh
```

The upstream IBM integration suite can additionally use
`INTEGRATION=1 KAFKA_BROKERS=127.0.0.1:19092 go test -race ./...` from the patch
module. The local fault runner temporarily pauses only the reproduction Kafka
container, preserves run logs and exits without synchronously closing deadlocked
producer goroutines. Applications must continue draining successes/errors;
recovery counts do not establish exactly-once delivery.

Release packaging includes both patch source directories and manifests. A
Dockerfile must copy `third_party/ddtrace-ibm-sarama` as well as the Shopify
directory if it needs both. Keep the existing application name/runtime/ENV/CMD.

First pin can select IBM/Sarama v1.44.0 from an initial v1.43.2 requirement.
Both exact versions are covered; --apply preserves the post-pin graph and does
not force the client back to an earlier version. Review the pin upgrade itself.

## Published v0.2.0 dependency-preserving adapter (Shopify only)

v0.2.0 adds a multi-profile adapter. **These commands are not in the published
v0.1.0/v0.1.1 archives.** Those releases remain immutable. Use the
[v0.2.0 Release](https://github.com/GuanceCloud/dd-go-patches/releases/tag/v0.2.0)
and its pinned checksum; never relabel or replace an old asset.

| Dimension | Currently supported adapter profiles |
|---|---|
| Official Orchestrion | v1.12.1 |
| Official core / Shopify integration | v2.10.1 / v2.10.1 |
| Effective official Shopify/Sarama | v1.23.0 **or** v1.38.1; not the interval between them |
| Manual tracer, if present | Unmodified official v1.74.8 |
| Other libraries / private business code | Application regression required; not certified by the adapter |

### Historical v0.2.0 Dockerfile download

Keep official `go install github.com/DataDog/orchestrion@v1.12.1`, the application's
working directory `/app`, runtime image, output binary name, ENV and CMD. Add
`curl` to the builder's existing package installation. Download **inside** the
Dockerfile; no extra build arguments or files supplied by the customer are needed:

```dockerfile
ARG DD_GO_PATCHES_VERSION=v0.2.0
ARG DD_GO_PATCHES_SHA256=65cb0bcec4ac4da0a401ecaee85bbed400d435ee178391eb3dd33bc3fe573ec1

RUN test "${#DD_GO_PATCHES_SHA256}" -eq 64 \
    && case "$DD_GO_PATCHES_SHA256" in *[!0-9a-f]*) exit 1 ;; esac \
    && curl --fail --location --retry 3 --connect-timeout 15 --max-time 120 \
      "https://github.com/GuanceCloud/dd-go-patches/releases/download/${DD_GO_PATCHES_VERSION}/dd-go-patches-${DD_GO_PATCHES_VERSION}.tar.gz" \
      --output /tmp/dd-go-patches.tar.gz \
    && printf '%s  %s\n' "$DD_GO_PATCHES_SHA256" /tmp/dd-go-patches.tar.gz | sha256sum -c - \
    && test ! -e /app/third_party/ddtrace-sarama \
    && test ! -e /app/patches/verify-official.go \
    && mkdir -p /tmp/dd-go-patches /app/third_party /app/patches \
    && tar -xzf /tmp/dd-go-patches.tar.gz -C /tmp/dd-go-patches \
    && cp -R /tmp/dd-go-patches/third_party/ddtrace-sarama /app/third_party/ddtrace-sarama \
    && cp /tmp/dd-go-patches/patches/verify-official.go /app/patches/verify-official.go
```

Source commit: `1259c55a55afa311bdfa05057a95725d07a5388e`.
[Source CI](https://github.com/GuanceCloud/dd-go-patches/actions/runs/38340514883).
The archive is allowlisted and reproducible; customer files are not published.

After extracting the **new** archive into the application, explicitly select
the tested instrumentation baseline without overwriting another existing
tracer version. This does not set business Sarama/gRPC versions. Replace the
old unconditional business-version edits with:

```dockerfile
RUN go run ./patches/verify-official.go --check-baseline
RUN go mod edit \
    -require=github.com/DataDog/dd-trace-go/v2@v2.10.1 \
    -require=github.com/DataDog/dd-trace-go/contrib/Shopify/sarama/v2@v2.10.1
RUN orchestrion pin
RUN go run ./patches/verify-official.go --inspect
RUN go run ./patches/verify-official.go --apply
RUN go run ./patches/verify-official.go
# Keep the customer's existing output path/name here.
RUN orchestrion go build -o /go/bin/application .
RUN go run ./patches/verify-official.go /go/bin/application
```

- `--check-baseline` checks existing official core/integration versions and
  rejects forks or other selected versions/replacements before the explicit
  Dockerfile bootstrap. Missing modules may be added at v2.10.1; unsupported
  existing versions require deliberate review, not an automatic downgrade.
- `--inspect` resolves modules and actual application package dependencies with
  `-mod=readonly`, prints an exact profile and effective gRPC version, and fails
  for unsupported combinations. A module in the graph, or an unused `replace`,
  is not treated as evidence of the application's Kafka implementation.
- `--apply` adds only the official Shopify integration's local replacement and
  explicit tool import. It does **not** set business Sarama/gRPC versions or
  upgrade/downgrade core/manual tracer/integration versions. They must already
  match a tested profile. A different existing integration replacement is not
  overwritten. Changes to existing selected/effective dependencies trigger
  rollback of touched files. Additional module-graph metadata is reported for
  review; it is not mistaken for proof that application code uses those modules.
- Applications actually using IBM/Sarama or legacy v1 Sarama wrappers are
  rejected, including mixed Shopify/IBM applications. Unused IBM modules in a
  tools dependency graph alone are not grounds for rejection. Separate adapters
  and fault-recovery tests are needed for those paths.
- Existing requirements/replacements (including private common libraries and
  gRPC) are retained. Retention is **not** a compatibility guarantee: for example,
  a customer's gRPC v1.57.2 replacement still needs business validation.
- Use the full project, the same `GOFLAGS`/build tags and target platform as the
  real build. Package discovery excludes tools-only imports; the final woven
  binary is checked separately. A successful binary check proves module identity
  and patch inclusion, not every runtime path or endpoint.
- Go workspaces and vendor-only workflows are not automatically adapted. Apply
  in the intended standalone module (`GOWORK=off` when appropriate). Review
  `orchestrion pin` changes separately: the adapter preserves the graph it sees
  **after** pin, not the application's pre-pin dependency graph.

The standalone patch module keeps v1.23.0 as its minimum requirement/test
default. Its own replacement is not inherited by a main application. The
application's effective version selects the profile; no universal version is
forced onto every project.

Run both adapter profiles without rewriting the maintained fixtures:

```bash
go test -race ./patches/verify-official.go ./patches/verify-official_test.go
bash scripts/test-sarama-matrix.sh
# Optional real local Kafka + official automatic weaving:
bash scripts/test-official-compat.sh v1.23.0
bash scripts/test-official-compat.sh v1.38.1
```

## Published v0.1.1: Shopify/Sarama fixed legacy profile

The wrapper could synchronously block forwarding a message into Sarama while
Sarama needed the same wrapper to drain an earlier success/error. The patch
keeps at most one pending input and forwards it in a select case, allowing
results to be drained during input backpressure. Pending span shutdown is also
covered. Applications must still drain result channels as required by Sarama.

| Component | Validated version |
|---|---|
| Official Orchestrion | v1.12.1 |
| Official dd-trace-go core | v2.10.1 |
| Official Shopify/Sarama integration | v2.10.1 + this patch |
| Shopify/Sarama (effective implementation) | v1.23.0 |
| Official v1 manual mux/gRPC instrumentation | v1.74.8 |

Sources: `third_party/ddtrace-sarama/`. Audit diff and manifest: `patches/`.
Original licenses and copyright notices are retained. IBM/Sarama and legacy v1
Sarama implementations are not covered. Other versions need separate validation;
do not silently downgrade an application's dependency graph to fit this table.

Release profiles are explicit: v0.1.1 targets effective Sarama v1.23.0, while
[v0.1.0](https://github.com/GuanceCloud/dd-go-patches/releases/tag/v0.1.0)
remains unchanged for the previously tested v1.38.1 profile. A selected nominal
requirement such as v1.38.1 may coexist with an official v1.23.0 replacement;
the verifier checks the replacement and the built binary, not just `require`.

## Historical v0.1.1 Dockerfile integration

Keep the application's existing official Orchestrion installation:

```dockerfile
RUN go install github.com/DataDog/orchestrion@v1.12.1
```

Use the versioned Release asset instead of cloning the Git repository. Install
`curl` in the builder's existing package-install step, and insert the following
before `orchestrion pin`. The configuration below uses the published
[v0.1.1 Release](https://github.com/GuanceCloud/dd-go-patches/releases/tag/v0.1.1)
and its publicly downloaded, verified archive SHA256. Review the Release and
checksum before adoption.
Both defaults live in the Dockerfile, so the build command needs no extra arguments.

```dockerfile
ARG DD_GO_PATCHES_VERSION=v0.1.1
ARG DD_GO_PATCHES_SHA256=215d9be750ab9800be9a3c2a464ba709a20b51719d0def2c35715572414b869c

RUN test "${#DD_GO_PATCHES_SHA256}" -eq 64 \
    && case "$DD_GO_PATCHES_SHA256" in *[!0-9a-f]*) exit 1 ;; esac \
    && curl --fail --location --retry 3 --connect-timeout 15 --max-time 120 \
      "https://github.com/GuanceCloud/dd-go-patches/releases/download/${DD_GO_PATCHES_VERSION}/dd-go-patches-${DD_GO_PATCHES_VERSION}.tar.gz" \
      --output /tmp/dd-go-patches.tar.gz \
    && printf '%s  %s\n' "$DD_GO_PATCHES_SHA256" /tmp/dd-go-patches.tar.gz | sha256sum -c - \
    && test ! -e /app/third_party/ddtrace-sarama \
    && test ! -e /app/patches/verify-official.go \
    && mkdir -p /tmp/dd-go-patches /app/third_party /app/patches \
    && tar -xzf /tmp/dd-go-patches.tar.gz -C /tmp/dd-go-patches \
    && cp -R /tmp/dd-go-patches/third_party/ddtrace-sarama /app/third_party/ddtrace-sarama \
    && cp /tmp/dd-go-patches/patches/verify-official.go /app/patches/verify-official.go

RUN go mod edit \
    -require=github.com/DataDog/dd-trace-go/v2@v2.10.1 \
    -require=github.com/DataDog/dd-trace-go/contrib/Shopify/sarama/v2@v2.10.1 \
    -replace=github.com/Shopify/sarama=github.com/Shopify/sarama@v1.23.0 \
    -replace=github.com/DataDog/dd-trace-go/contrib/Shopify/sarama/v2=./third_party/ddtrace-sarama
RUN orchestrion pin
RUN go run ./patches/verify-official.go --enable-sarama
RUN go run ./patches/verify-official.go
RUN orchestrion go build -o /go/bin/application .
RUN go run ./patches/verify-official.go /go/bin/application
```

This example assumes the application's working directory is `/app` and curl and
trusted CA certificates are available. Adapt only the working directory/output
path to the existing application. Runtime image, environment and command do not
need changing. Pin both the Release version and archive SHA256; never download
`latest` or trust a checksum fetched dynamically alongside the archive as the
only integrity check. Never embed credentials in URLs, build arguments, images
or logs. This public Release needs no credentials. Existing same-name patch files
cause a failure, not a merge.

The verifier fails closed for module-resolution errors, fork tracer/tool
dependencies, unsupported effective versions, patch source changes, incorrect
Orchestrion binaries or missing replacements in the application build info.
If v1 is present, this release still requires the tested v1.74.8 combination.
It does not prove that every private library or business endpoint is compatible.

The `--enable-sarama` preparation step adds only a blank import of the official
patched Shopify integration to `orchestrion.tool.go`, preserving existing rules
and imports. Run it after pin: upstream pin can prune its aggregate `all/v2`
entry when resolving repository-relative replacements. This makes the target
integration explicit; it does not restore or certify every other integration.
The normal verifier remains fail-closed and requires this explicit import.
Pin can also upgrade tracer v1 and transitive dependencies: review the final
module/config diff and validate the actual application before deployment.

Keep official manual instrumentation and official automatic instrumentation in
the same implementation. Reconcile existing GuanceCloud tracer imports/configs
before adoption; do not merely rename the CLI installation. This prevents the
known two-implementation contextStack conflict source but does not promise that
all manual/automatic combinations avoid duplicate instrumentation.

## Tests

Unit tests, including deterministic input/result backpressure regression cases:

```bash
cd third_party/ddtrace-sarama
go test -race -count=10 -timeout=120s ./...
```

Optional real official-weaving compatibility checks (run locally with a test
Kafka broker on `127.0.0.1:19092`; do not target production):

```bash
bash scripts/test-official-compat.sh
```

These preserve v1 manual HTTP mux/gRPC imports, exercise concurrent RPCs and
cross-goroutine parent linkage, and assert that the Sarama constructor is
automatically wrapped using the official patched integration. A CGO-disabled
application is also built, inspected and run. The script uses temporary outputs;
test source contains no customer private libraries or credentials. The
`inventory-item-delete` test topic must exist (for example, pre-create it using
your local Kafka tooling). Mocktracer assertions are not cloud-ingestion proof.

The legacy fixture uses Kafka protocol 2.3.0, Sarama v1.23.0's maximum, against
the local test broker. This is not a promise of support for every broker version
or features introduced in newer Sarama releases. Security review of legacy
Sarama and its transitive dependencies is separate from this deadlock fix.

v0.1.1 source commit: `152bb80f320471314865353ec06bef5f68e4656b`.
[CI](https://github.com/GuanceCloud/dd-go-patches/actions/runs/38334430578)
passed on Go 1.26.3/1.26.4. The public archive passed anonymous download,
pinned-checksum verification, explicit configuration, effective-version checks
and unit/race tests. Against the same two-second local broker pause, the patched
and unwrapped cases each completed 5,000 handlers and 10,000 successful produces
with no producer errors, while the unpatched wrapper stalled all 10 workers.
These assertions do not validate a customer's private application or cloud
ingestion. Tracer startup `available_version` may report the nominal requirement;
use Go's module replacement and binary build info for the effective version.

## Maintenance

### Dependency security boundary

As of 2026-10-12, GitHub reports 11 open Dependabot alerts (4 high, 6 moderate,
1 low), all associated with `tests/official-compat/go.mod`. The compatibility
fixture is excluded from the Release archive, but exclusion alone does not prove
that an application's dependency graph is unaffected. This behavioral patch is
not a dependency-security clearance. Review the
[repository security alerts](https://github.com/GuanceCloud/dd-go-patches/security/dependabot)
and the actual application dependency graph before deployment. Upgrades require
separate compatibility validation; this release does not silently upgrade the
tested baseline.

### Reproducible packaging

Build a reproducible, allowlisted Release asset from a committed source snapshot:

```bash
bash scripts/build-release.sh v0.2.0 1259c55a55afa311bdfa05057a95725d07a5388e
```

Outputs: `dist/v0.2.0/dd-go-patches-v0.2.0.tar.gz` and `SHA256SUMS`.
The archive has no customer Dockerfiles, local logs or compatibility binaries.
The script refuses to overwrite different artifacts. Publish both files on a
GitHub Release targeting that exact source commit, verify the public download,
and only then adopt its fixed SHA256. Do not overwrite published assets/tags;
issue a new version for any change. Git commit checkout remains an optional
alternative, but it must also pin and verify a reviewed full commit.

- Pin upstream baseline, patch revision, source checksum and tested versions.
- Keep patches small and add a deterministic regression test for each fix.
- Run unit/race tests and official-weaving compatibility tests before adoption.
- Preserve backward-reproducible snapshots; never rewrite a published tag.
- Record upstream issues and verify official replacements before retirement.
- Keep customer endpoints, private source, credentials and raw incident logs out
  of this repository.
- Roll back the Dockerfile commit, replacement and related dependency/config
  changes together. Removing only the directory leaves a broken replacement;
  returning to the vulnerable wrapper restores its deadlock risk.

Upstream tracking: https://github.com/DataDog/dd-trace-go/issues/5361.
There is deliberately no root Go module: test the integration and compatibility
fixture in their respective module directories.
