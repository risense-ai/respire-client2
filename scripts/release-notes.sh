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
    # User-facing announcement: a fenced block the maintainer can copy as-is
    # when notifying users (chat, forum, announcements). Kept last so the
    # commit log above stays the source of truth.
    echo
    echo '## 更新说明（可直接复制给用户）'
    echo
    echo '```text'
    echo "Respire 客户端 $current 已发布。"
    echo
    if [[ -n "$previous" ]]; then
        echo "下载：https://github.com/$repository/releases/tag/$current"
        echo "自 $previous 以来的主要变化："
    else
        echo "下载：https://github.com/$repository/releases"
        echo "本版本主要变化："
    fi
    for section in Features 'Bug fixes' Performance Documentation; do
        file="$tmpdir/$section"
        [[ -s "$file" ]] || continue
        case "$section" in
            Features) label='新功能' ;;
            'Bug fixes') label='问题修复' ;;
            Performance) label='性能' ;;
            Documentation) label='文档' ;;
        esac
        echo
        echo "・$label"
        # Plain-text subjects, no commit links: the announcement targets users.
        sed -E 's/ \(\[`[0-9a-f]+`\]\([^)]*\)\)$//; s/^- /  - /' "$file"
    done
    echo '```'
} > "$output"
