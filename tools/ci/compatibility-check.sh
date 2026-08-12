#!/usr/bin/env bash

set -euo pipefail

base_sha=$1
remote=$2
shift 2
make_command=("$@")

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
cd "$repo_root"

release_ref_root="refs/omnara/compatibility/$$"

cleanup_compatibility_refs() {
	git for-each-ref --format='delete %(refname)' "$release_ref_root/" | git update-ref --stdin
}

handle_compatibility_signal() {
	local signal=$1
	trap - EXIT "$signal"
	cleanup_compatibility_refs || :
	kill -s "$signal" "$$"
}

cleanup_compatibility_refs
trap cleanup_compatibility_refs EXIT
trap 'handle_compatibility_signal HUP' HUP
trap 'handle_compatibility_signal INT' INT
trap 'handle_compatibility_signal TERM' TERM

if [[ -z "$base_sha" ]]; then
	base_sha=$(git ls-remote --exit-code "$remote" refs/heads/main | awk 'NR == 1 { print $1 }')
	if [[ -z "$base_sha" ]]; then
		printf 'cannot resolve %s/main\n' "$remote"
		exit 2
	fi
fi

fetch_options=(--no-tags)
if [[ $(git rev-parse --is-shallow-repository) == true ]]; then
	fetch_options+=(--depth=1)
fi
if ! git cat-file -e "$base_sha^{commit}" 2>/dev/null; then
	git fetch "${fetch_options[@]}" "$remote" "$base_sha"
fi
git cat-file -e "$base_sha^{commit}"
git fetch "${fetch_options[@]}" --force "$remote" \
	"+refs/tags/cluster-v*:$release_ref_root/cluster-v*" \
	"+refs/tags/omnarad-v*:$release_ref_root/omnarad-v*"

"${make_command[@]}" --no-print-directory \
	migration-compat-check MIGRATION_RELEASE_REF_ROOT="$release_ref_root"
"${make_command[@]}" --no-print-directory \
	openapi-compat-check COMPAT_BASE_SHA="$base_sha"
