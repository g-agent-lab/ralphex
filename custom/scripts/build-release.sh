#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
OUT_DIR="${1:-${ROOT_DIR}/dist}"
UPSTREAM_TAG="${UPSTREAM_TAG:-}"
CUSTOM_TAG="${CUSTOM_TAG:-${GITHUB_REF_NAME:-}}"

if [[ -z "${UPSTREAM_TAG}" && -f "${ROOT_DIR}/custom/release/upstream-tag.txt" ]]; then
  UPSTREAM_TAG="$(<"${ROOT_DIR}/custom/release/upstream-tag.txt")"
fi

if [[ -z "${UPSTREAM_TAG}" || -z "${CUSTOM_TAG}" ]]; then
  echo "UPSTREAM_TAG and CUSTOM_TAG must be set" >&2
  exit 1
fi

VERSION="${CUSTOM_TAG#v}"
ARTIFACT_PREFIX="ralphex_${VERSION}"
TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/ralphex-release.XXXXXX")"
trap 'rm -rf "${TMP_DIR}"' EXIT

mkdir -p "${OUT_DIR}"
rm -f "${OUT_DIR}/${ARTIFACT_PREFIX}"*
mkdir -p "${ROOT_DIR}/.custom-work/gocache"

export GOCACHE="${ROOT_DIR}/.custom-work/gocache"

BIN_DIR="${TMP_DIR}/bin"
LOCAL_TOOLS_DIR="${TMP_DIR}/local-tools"
RUNTIME_OVERRIDES_DIR="${TMP_DIR}/runtime-overrides"
mkdir -p "${BIN_DIR}" "${LOCAL_TOOLS_DIR}" "${RUNTIME_OVERRIDES_DIR}"

GOOS=darwin GOARCH=arm64 go build \
  -ldflags "-s -w -X main.revision=${CUSTOM_TAG}" \
  -o "${BIN_DIR}/ralphex" \
  ./cmd/ralphex

cp -R "${ROOT_DIR}/custom/local-bin/." "${LOCAL_TOOLS_DIR}/"
cp -R "${ROOT_DIR}/custom/overrides/." "${RUNTIME_OVERRIDES_DIR}/"

BIN_ARCHIVE="${OUT_DIR}/${ARTIFACT_PREFIX}_darwin_arm64.tar.gz"
LOCAL_TOOLS_ARCHIVE="${OUT_DIR}/${ARTIFACT_PREFIX}_local_tools.tar.gz"
RUNTIME_OVERRIDES_ARCHIVE="${OUT_DIR}/${ARTIFACT_PREFIX}_runtime_overrides.tar.gz"
METADATA_FILE="${OUT_DIR}/release-metadata.json"
CHECKSUMS_FILE="${OUT_DIR}/${ARTIFACT_PREFIX}_checksums.txt"

tar -C "${BIN_DIR}" -czf "${BIN_ARCHIVE}" ralphex
tar -C "${LOCAL_TOOLS_DIR}" -czf "${LOCAL_TOOLS_ARCHIVE}" .
tar -C "${RUNTIME_OVERRIDES_DIR}" -czf "${RUNTIME_OVERRIDES_ARCHIVE}" .

PATCHES_JSON="$(find "${ROOT_DIR}/custom/patches" -maxdepth 1 -type f -name '*.patch' -exec basename {} \; | sort | jq -R . | jq -s .)"
OVERLAYS_JSON="$(find "${ROOT_DIR}/custom/overrides" -type f | sed "s#${ROOT_DIR}/##" | sort | jq -R . | jq -s .)"

jq -n \
  --arg upstream_tag "${UPSTREAM_TAG}" \
  --arg custom_tag "${CUSTOM_TAG}" \
  --arg fork_commit "$(git rev-parse HEAD)" \
  --arg binary_asset "$(basename "${BIN_ARCHIVE}")" \
  --arg local_tools_asset "$(basename "${LOCAL_TOOLS_ARCHIVE}")" \
  --arg runtime_overrides_asset "$(basename "${RUNTIME_OVERRIDES_ARCHIVE}")" \
  --arg checksums_asset "$(basename "${CHECKSUMS_FILE}")" \
  --arg generated_at "$(date -u +"%Y-%m-%dT%H:%M:%SZ")" \
  --argjson applied_patches "${PATCHES_JSON}" \
  --argjson overlay_files "${OVERLAYS_JSON}" \
  '{
    upstream_tag: $upstream_tag,
    custom_tag: $custom_tag,
    fork_commit: $fork_commit,
    generated_at: $generated_at,
    applied_patches: $applied_patches,
    overlay_files: $overlay_files,
    assets: {
      binary: $binary_asset,
      local_tools: $local_tools_asset,
      runtime_overrides: $runtime_overrides_asset,
      checksums: $checksums_asset
    }
  }' > "${METADATA_FILE}"

(cd "${OUT_DIR}" && shasum -a 256 "$(basename "${BIN_ARCHIVE}")" "$(basename "${LOCAL_TOOLS_ARCHIVE}")" "$(basename "${RUNTIME_OVERRIDES_ARCHIVE}")" "$(basename "${METADATA_FILE}")") > "${CHECKSUMS_FILE}"

echo "release assets created in ${OUT_DIR}"
