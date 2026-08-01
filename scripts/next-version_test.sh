#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
version_script="$script_dir/next-version.sh"
test_root="$(mktemp -d)"
trap 'rm -rf "$test_root"' EXIT

new_repo() {
	local name="$1"
	local repo="$test_root/$name"
	mkdir -p "$repo"
	git -C "$repo" init -q
	git -C "$repo" config user.name "avar test"
	git -C "$repo" config user.email "test@example.com"
	printf '%s\n' base >"$repo/file"
	git -C "$repo" add file
	git -C "$repo" commit -qm 'chore: initial state'
	git -C "$repo" tag v0.1.0
	printf '%s\n' "$repo"
}

new_untagged_repo() {
	local name="$1"
	local repo="$test_root/$name"
	mkdir -p "$repo"
	git -C "$repo" init -q
	git -C "$repo" config user.name "avar test"
	git -C "$repo" config user.email "test@example.com"
	printf '%s\n' base >"$repo/file"
	git -C "$repo" add file
	git -C "$repo" commit -qm 'chore: initial state'
	printf '%s\n' "$repo"
}

commit() {
	local repo="$1" subject="$2" body="${3:-}"
	printf '%s\n' "$subject" >>"$repo/file"
	git -C "$repo" add file
	if [[ -n "$body" ]]; then
		git -C "$repo" commit -qm "$subject" -m "$body"
	else
		git -C "$repo" commit -qm "$subject"
	fi
}

assert_version() {
	local name="$1" expected="$2" repo actual
	repo="$(new_repo "$name")"
	shift 2
	while (($#)); do
		commit "$repo" "$1" "${2:-}"
		shift
		(($#)) && shift
	done
	actual="$(cd "$repo" && "$version_script")"
	if [[ "$actual" != "$expected" ]]; then
		printf '%s\n' "${name}: got ${actual:-<no release>}, want ${expected:-<no release>}" >&2
		exit 1
	fi
}

assert_initial_version() {
	local expected="$1" subject="$2" repo actual
	repo="$(new_untagged_repo initial_release)"
	commit "$repo" "$subject"
	actual="$(cd "$repo" && "$version_script")"
	if [[ "$actual" != "$expected" ]]; then
		printf '%s\n' "initial release: got ${actual:-<no release>}, want $expected" >&2
		exit 1
	fi
}

assert_initial_version v0.1.0 'feat(cli): start Linux shells'
assert_version no_release '' 'docs: clarify installation'
assert_version patch v0.1.1 'fix(cli): preserve guest exit status'
assert_version minor v0.2.0 'fix(cli): preserve guest exit status' '' 'feat(editor): add VS Code support'
assert_version breaking_subject v1.0.0 'feat(api)!: remove deprecated selector'
assert_version breaking_footer v1.0.0 'fix(api): preserve compatibility' 'BREAKING CHANGE: clients must update their selector.'

printf '%s\n' 'next-version tests passed'
