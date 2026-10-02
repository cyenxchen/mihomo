#!/usr/bin/env bash
set -euo pipefail

: "${GITHUB_REPOSITORY:?GITHUB_REPOSITORY must be set}"

# Fetch every page before deleting anything. Keep the five most recently
# published stable releases; drafts, prereleases and Git tags are untouched.
releases=$(gh api --paginate --slurp "repos/${GITHUB_REPOSITORY}/releases?per_page=100")
obsolete=$(jq -r '
  add
  | map(select(.draft == false and .prerelease == false))
  | sort_by([.published_at, .id]) | reverse
  | .[5:][]
  | [.id, .tag_name] | @tsv
' <<< "$releases")

while IFS=$'\t' read -r release_id tag; do
  [ -n "$release_id" ] || continue
  echo "Deleting old release: ${tag} (${release_id})"
  gh api --method DELETE "repos/${GITHUB_REPOSITORY}/releases/${release_id}"
done <<< "$obsolete"
