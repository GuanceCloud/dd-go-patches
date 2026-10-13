#!/usr/bin/env bash
# Test the uncommitted packaging implementation without touching the real index
# or claiming that an immutable release/tag already exists.
set -euo pipefail
repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
fixture=$(mktemp -d "${TMPDIR:-/tmp}/dd-go-patches-package-test.XXXXXX")
cd "$repo_root"
for path in LICENSE NOTICE third_party/ddtrace-sarama third_party/ddtrace-ibm-sarama third_party/ddtrace-ibm-sarama-v2.10.1 \
  patches/verify-official.go patches/shopify-sarama-v2.10.1-deadlock.patch \
  patches/shopify-sarama-v2.10.1.json patches/ibm-sarama-v2.3.0-deadlock.patch \
  patches/ibm-sarama-v2.3.0.json patches/ibm-sarama-v2.10.1-deadlock.patch \
  patches/ibm-sarama-v2.10.1.json scripts/build-release.sh; do
  cp -R --parents "$path" "$fixture/"
done
git -C "$fixture" init -q
git -C "$fixture" add .
GIT_AUTHOR_DATE=2026-10-13T00:00:00Z GIT_COMMITTER_DATE=2026-10-13T00:00:00Z \
  git -C "$fixture" -c user.name='Packaging Test' -c user.email='packaging@example.invalid' commit -qm 'Temporary packaging fixture'
bash "$fixture/scripts/build-release.sh" v0.0.0
bash "$fixture/scripts/build-release.sh" v0.0.0
cd "$fixture/dist/v0.0.0"
sha256sum -c SHA256SUMS
tar -tzf dd-go-patches-v0.0.0.tar.gz > "$fixture/archive-files.txt"
for path in third_party/ddtrace-ibm-sarama/producer.go third_party/ddtrace-ibm-sarama/LICENSE \
  third_party/ddtrace-ibm-sarama-v2.10.1/producer.go third_party/ddtrace-ibm-sarama-v2.10.1/LICENSE \
  third_party/ddtrace-sarama/sarama.go patches/verify-official.go patches/ibm-sarama-v2.3.0.json \
  patches/ibm-sarama-v2.10.1.json; do
  grep -Fx "$path" "$fixture/archive-files.txt"
done
if grep -E '(^|/)(Dockerfile[^/]*|tests|\.git|[^/]*\.log)(/|$)' "$fixture/archive-files.txt"; then
  printf 'Unexpected private/test/build artifact in release archive\n' >&2
  exit 1
fi
printf 'PACKAGE_FIXTURE_OK outputs=%s not-an-official-release=true\n' "$fixture"
