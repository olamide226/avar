#!/usr/bin/env bash
# Print the next stable semantic version implied by Conventional Commit messages
# since the most recent stable vMAJOR.MINOR.PATCH tag. Print nothing when no
# release-worthy commit is present.
set -euo pipefail

latest_stable_tag() {
	local tag
	while IFS= read -r tag; do
		if [[ "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
			printf '%s\n' "$tag"
			return
		fi
	done < <(git tag --merged HEAD --list 'v[0-9]*' --sort=-v:refname)
}

next_version() {
	local last_tag range commit subject body bump=none version major minor patch
	last_tag="$(latest_stable_tag || true)"
	if [[ -n "$last_tag" ]]; then
		range="$last_tag..HEAD"
		version="${last_tag#v}"
	else
		range="HEAD"
		version="0.0.0"
	fi

	while IFS= read -r commit; do
		subject="$(git show -s --format=%s "$commit")"
		body="$(git show -s --format=%b "$commit")"

		if printf '%s\n' "$subject" | grep -Eq '^[[:alnum:]]+(\([^)]+\))?!:' ||
			printf '%s\n' "$body" | grep -qE '^BREAKING[ -]CHANGE:'; then
			bump=major
		elif [[ "$bump" != major ]] && printf '%s\n' "$subject" | grep -Eq '^feat(\([^)]+\))?:'; then
			bump=minor
		elif [[ "$bump" == none ]] && printf '%s\n' "$subject" | grep -Eq '^(fix|perf)(\([^)]+\))?:'; then
			bump=patch
		fi
	done < <(git log --format=%H "$range")

	if [[ "$bump" == none ]]; then
		return
	fi

	IFS=. read -r major minor patch <<<"$version"
	case "$bump" in
	major)
		printf 'v%d.0.0\n' "$((major + 1))"
		;;
	minor)
		printf 'v%d.%d.0\n' "$major" "$((minor + 1))"
		;;
	patch)
		printf 'v%d.%d.%d\n' "$major" "$minor" "$((patch + 1))"
		;;
	esac
}

next_version
