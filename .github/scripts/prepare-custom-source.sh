#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -lt 1 ] || [ "$#" -gt 2 ]; then
  echo "usage: $0 <upstream-tag> [target-branch]" >&2
  exit 2
fi

UPSTREAM_TAG="$1"
TARGET_BRANCH="${2:-custom-source}"
SOURCE_REF="${CUSTOM_SOURCE_REF:-origin/Meta}"
UPSTREAM_REMOTE="${UPSTREAM_REMOTE:-upstream}"
UPSTREAM_URL="${UPSTREAM_URL:-https://github.com/MetaCubeX/mihomo.git}"

echo "::group::Prepare custom source"
echo "Upstream tag: ${UPSTREAM_TAG}"
echo "Source ref: ${SOURCE_REF}"
echo "Target branch: ${TARGET_BRANCH}"

if ! git config user.name >/dev/null; then
  git config user.name "github-actions[bot]"
fi

if ! git config user.email >/dev/null; then
  git config user.email "github-actions[bot]@users.noreply.github.com"
fi

if ! git remote get-url "${UPSTREAM_REMOTE}" >/dev/null 2>&1; then
  git remote add "${UPSTREAM_REMOTE}" "${UPSTREAM_URL}"
fi

git fetch "${UPSTREAM_REMOTE}" --tags --force

if [[ "${SOURCE_REF}" == origin/* ]]; then
  SOURCE_BRANCH="${SOURCE_REF#origin/}"
  git fetch origin "${SOURCE_BRANCH}:refs/remotes/origin/${SOURCE_BRANCH}" --force
fi

UPSTREAM_COMMIT="$(git rev-list -n 1 "${UPSTREAM_TAG}")"
SOURCE_COMMIT="$(git rev-parse "${SOURCE_REF}")"
if ! BASE_COMMIT="$(git merge-base "${SOURCE_REF}" "${UPSTREAM_COMMIT}")"; then
  echo "::error::No merge base found between ${SOURCE_REF} and ${UPSTREAM_TAG}"
  exit 1
fi

echo "Upstream commit: ${UPSTREAM_COMMIT}"
echo "Source commit: ${SOURCE_COMMIT}"
echo "Merge base: ${BASE_COMMIT} ($(git log --oneline -1 "${BASE_COMMIT}"))"
echo "Custom commits to replay:"
if ! git log --oneline --reverse "${BASE_COMMIT}..${SOURCE_REF}"; then
  echo "::error::Failed to list custom commits"
  exit 1
fi

git checkout -B "${TARGET_BRANCH}" "${SOURCE_REF}"
git rebase --onto "${UPSTREAM_COMMIT}" "${BASE_COMMIT}" "${TARGET_BRANCH}"

CUSTOM_SHA="$(git rev-parse HEAD)"
echo "Prepared custom source: ${CUSTOM_SHA}"

if [ -n "${GITHUB_OUTPUT:-}" ]; then
  echo "custom_sha=${CUSTOM_SHA}" >>"${GITHUB_OUTPUT}"
fi

echo "::endgroup::"
