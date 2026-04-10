#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CUSTOM_DIR="${ROOT_DIR}/custom"
PATCH_DIR="${CUSTOM_DIR}/patches"
OVERRIDES_DIR="${CUSTOM_DIR}/overrides"

apply_patch_file() {
  local patch_file="$1"

  if git -C "${ROOT_DIR}" apply --check "${patch_file}" >/dev/null 2>&1; then
    git -C "${ROOT_DIR}" apply "${patch_file}"
    return
  fi

  if git -C "${ROOT_DIR}" apply --reverse --check "${patch_file}" >/dev/null 2>&1; then
    echo "patch already applied: ${patch_file}"
    return
  fi

  echo "failed to apply patch cleanly: ${patch_file}" >&2
  exit 1
}

copy_override_dir() {
  local source_dir="$1"
  local target_dir="$2"

  mkdir -p "${target_dir}"
  find "${target_dir}" -mindepth 1 -maxdepth 1 -type f -delete
  cp -R "${source_dir}/." "${target_dir}/"
}

if [[ -d "${PATCH_DIR}" ]]; then
  shopt -s nullglob
  for patch_file in "${PATCH_DIR}"/*.patch; do
    apply_patch_file "${patch_file}"
  done
fi

if [[ -f "${OVERRIDES_DIR}/config" ]]; then
  install -m 0644 "${OVERRIDES_DIR}/config" "${ROOT_DIR}/pkg/config/defaults/config"
fi

if [[ -d "${OVERRIDES_DIR}/prompts" ]]; then
  copy_override_dir "${OVERRIDES_DIR}/prompts" "${ROOT_DIR}/pkg/config/defaults/prompts"
fi

if [[ -d "${OVERRIDES_DIR}/agents" ]]; then
  copy_override_dir "${OVERRIDES_DIR}/agents" "${ROOT_DIR}/pkg/config/defaults/agents"
fi

echo "customizations applied"
