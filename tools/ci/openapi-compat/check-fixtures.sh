#!/usr/bin/env bash

set -euo pipefail

oasdiff_command=("$@")
repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)
cd "$repo_root"
fixture_dir="$repo_root/tools/ci/openapi-compat/testdata"

"${oasdiff_command[@]}" --format text \
	"$fixture_dir/base.yaml" \
	"$fixture_dir/compatible-head.yaml"

expect_breaking() {
	local description=$1
	local base=$2
	local head=$3
	local output
	local status

	set +e
	output=$("${oasdiff_command[@]}" --format text "$fixture_dir/$base" "$fixture_dir/$head" 2>&1)
	status=$?
	set -e
	if [[ $status -ne 1 ]]; then
		printf '%s\nexpected %s fixture to exit 1, got %s\n' "$output" "$description" "$status"
		exit 1
	fi
}

expect_breaking 'added response pattern' base.yaml pattern-breaking-head.yaml
expect_breaking 'changed response pattern' pattern-v1-base.yaml pattern-v2-head.yaml
expect_breaking 'added response enum value' base.yaml enum-breaking-head.yaml
expect_breaking 'removed route' base.yaml route-breaking-head.yaml
