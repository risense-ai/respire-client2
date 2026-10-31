#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."

current="${1:?Usage: release-notes.sh <tag> [output]}"
output="${2:-RELEASE_NOTES.md}"
repository="${GITHUB_REPOSITORY:-risense-ai/respire-client2}"
[[ "$current" == v* ]] || { echo "Release tag must start with v: $current" >&2; exit 1; }

current_version="${current#v}"
previous=""
while IFS= read -r tag; do
    [[ "$tag" != "$current" ]] || continue
    tag_version="${tag#v}"
    lower="$(printf '%s\n%s\n' "$tag_version" "$current_version" | sort -V | head -n1)"
    [[ "$lower" == "$tag_version" ]] || continue
    if [[ -z "$previous" ]]; then
        previous="$tag"
        continue
    fi
    previous_version="${previous#v}"
    highest="$(printf '%s\n%s\n' "$previous_version" "$tag_version" | sort -V | tail -n1)"
    if [[ "$highest" == "$tag_version" ]]; then previous="$tag"; fi
done < <(git tag --list 'v*')

range="$current"
if [[ -n "$previous" ]]; then
    range="$previous..$current"
fi
tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

git log --reverse --no-merges --pretty=format:'%H%x09%s' "$range" | while IFS=$'\t' read -r sha subject || [[ -n "${sha:-}" ]]; do
    [[ -n "$sha" ]] || continue
    case "$subject" in
        feat*|Feature*|Add*|add*) file=Features ;;
        fix*|Fix*) file='Bug fixes' ;;
        perf*) file=Performance ;;
        docs*) file=Documentation ;;
        build*|ci*) file='Build and CI' ;;
        refactor*|chore*|style*|test*) file=Maintenance ;;
        *) file='Other changes' ;;
    esac
    printf -- '- %s ([`%s`](https://github.com/%s/commit/%s))\n' "$subject" "${sha:0:7}" "$repository" "$sha" >> "$tmpdir/$file"
done

commit_count="$(git rev-list --count "$range")"
{
    echo '## What changed'
    echo
    if [[ -n "$previous" ]]; then
        echo "$commit_count commits since \`$previous\`."
    else
        echo "$commit_count commits on \`$current\`."
    fi
    for section in Features 'Bug fixes' Performance Documentation 'Build and CI' Maintenance 'Other changes'; do
        file="$tmpdir/$section"
        [[ -s "$file" ]] || continue
        echo
        echo "### $section"
        echo
        cat "$file"
    done
    if [[ -n "$previous" ]]; then
        echo
        echo '## Comparison'
        echo
        echo "[\`$previous...$current\`](https://github.com/$repository/compare/$previous...$current)"
    fi
} > "$output"
