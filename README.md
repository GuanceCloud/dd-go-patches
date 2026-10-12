# dd-go-patches

Small, auditable patches for Go tracing integrations. Preserve upstream module
and import paths, fix only the required behavior, and explicitly validate the
dependency combination. This is not an official DataDog release or another
tracer SDK.

## Shopify/Sarama async-producer deadlock patch

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
| Shopify/Sarama | v1.38.1 |
| Official v1 manual mux/gRPC instrumentation | v1.74.8 |

Sources: `third_party/ddtrace-sarama/`. Audit diff and manifest: `patches/`.
Original licenses and copyright notices are retained. IBM/Sarama and legacy v1
Sarama implementations are not covered. Other versions need separate validation;
do not silently downgrade an application's dependency graph to fit this table.

## Dockerfile integration

Keep the application's existing official Orchestrion installation:

```dockerfile
RUN go install github.com/DataDog/orchestrion@v1.12.1
```

Use the versioned Release asset instead of cloning the Git repository. Install
`curl` in the builder's existing package-install step, and insert the following
before `orchestrion pin`. Copy the archive SHA256 from the verified Release's
`SHA256SUMS` into the Dockerfile; the placeholder intentionally fails verification.
Both defaults live in the Dockerfile, so the build command needs no extra arguments.

```dockerfile
ARG DD_GO_PATCHES_VERSION=v0.1.0
ARG DD_GO_PATCHES_SHA256=REPLACE_WITH_VERIFIED_RELEASE_ARCHIVE_SHA256

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
    -replace=github.com/DataDog/dd-trace-go/contrib/Shopify/sarama/v2=./third_party/ddtrace-sarama
RUN orchestrion pin
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
dependencies, unsupported selected versions, patch source changes, incorrect
Orchestrion binaries or missing replacements in the application build info.
If v1 is present, this initial patch requires the tested v1.74.8 combination.
It does not prove that every private library or business endpoint is compatible.

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

## Maintenance

Build a reproducible, allowlisted Release asset from a committed source snapshot:

```bash
bash scripts/build-release.sh v0.1.0 <reviewed-source-commit>
```

Outputs: `dist/v0.1.0/dd-go-patches-v0.1.0.tar.gz` and `SHA256SUMS`.
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
