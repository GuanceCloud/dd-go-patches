#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
version=${1:?Usage: build-release.sh vX.Y.Z [source-commit]}
if [[ ! "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  printf 'Invalid release version: expected vX.Y.Z\n' >&2
  exit 1
fi
source_ref=${2:-HEAD}
source_commit=$(git -C "$repo_root" rev-parse --verify --end-of-options "${source_ref}^{commit}")
output_dir="$repo_root/dist/$version"
scratch_dir=$(mktemp -d "${TMPDIR:-/tmp}/dd-go-patches-release.XXXXXX")
asset="dd-go-patches-${version}.tar.gz"
mkdir -p "$output_dir"

# Archive committed, explicitly allowlisted files only. Do not package the
# caller's worktree, customer Dockerfiles, build outputs, credentials or logs.
# git archive fixes metadata to the commit; gzip -n omits time/name metadata.
git -C "$repo_root" archive --format=tar "$source_commit" \
  LICENSE NOTICE \
  third_party/ddtrace-sarama \
  patches/verify-official.go \
  patches/shopify-sarama-v2.10.1-deadlock.patch \
  patches/shopify-sarama-v2.10.1.json \
  | gzip -n > "$scratch_dir/$asset"
(
  cd "$scratch_dir"
  sha256sum "$asset" > SHA256SUMS
)

# Never silently overwrite a previous artifact with different bytes.
for name in "$asset" SHA256SUMS; do
  if [[ -e "$output_dir/$name" ]]; then
    if ! cmp -s "$scratch_dir/$name" "$output_dir/$name"; then
      printf 'Refusing to replace a different artifact: %s\n' "$output_dir/$name" >&2
      exit 1
    fi
  else
    cp "$scratch_dir/$name" "$output_dir/$name"
  fi
done
printf 'RELEASE_PACKAGE_OK version=%s source_commit=%s\n' "$version" "$source_commit"
cat "$output_dir/SHA256SUMS"
