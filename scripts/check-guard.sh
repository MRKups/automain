#!/bin/sh
set -eu

root=$(git rev-parse --show-toplevel)
cd "$root"
tmp=$(mktemp -d "${TMPDIR:-/tmp}/automain-guard.XXXXXX")
trap 'rm -rf "$tmp"' 0
trap 'exit 1' HUP INT TERM

: > "$tmp/violations"

# 1. Reject staged gitignored docs or agent instruction files
git diff --cached --name-only | while IFS= read -r staged_path; do
    [ -n "$staged_path" ] || continue
    case "$staged_path" in
        docs/*|work-orders/*|AGENTS.md|agents.md|WATCHDOG.md|watchdog.md|ADVISOR.md|advisor.md|CLAUDE.md|claude.md|GEMINI.md|gemini.md|.agent/*|.agents/*|.claude/*|.cursor/*|.gemini/*)
            printf '%s\tforbidden staged doc or agent file\n' "$staged_path" >> "$tmp/violations"
            ;;
    esac
done

# 2. Reject permanent Go test files under DEV-05
git ls-files --cached --others --exclude-standard -z '*_test.go' | xargs -0 -I{} sh -c '
    file="$1"
    [ -n "$file" ] || exit 0
    printf "%s\tforbidden permanent test file (DEV-05)\n" "$file"
' sh {} >> "$tmp/violations"

# 3. Inspect Go sources for forbidden escape hatches (DEV-03: //nolint, unsafe, panic)
cat > "$tmp/go_guard.awk" <<'AWK'
BEGIN {
    path = ENVIRON["GUARD_PATH"]
}
{
    if ($0 ~ /\/\/[[:space:]]*nolint/) {
        print path ":" NR "\tforbidden linter suppression (//nolint)"
    }
    text = text $0 "\n"
}
END {
    source = text
    # Strip raw string literals (`...`)
    gsub(/`[^`]*`/, "\"\"", source)
    # Strip rune literals
    gsub(/'(\\.|[^'\\])*'/, "''", source)
    # Strip interpreted string literals ("...")
    gsub(/"([^"\\]|\\.)*"/, "\"\"", source)
    # Strip block comments (/* ... */)
    while (match(source, /\/\*/)) {
        start_pos = RSTART
        rest = substr(source, start_pos + 2)
        end_idx = index(rest, "*/")
        if (end_idx > 0) {
            source = substr(source, 1, start_pos - 1) " " substr(rest, end_idx + 2)
        } else {
            source = substr(source, 1, start_pos - 1)
            break
        }
    }
    # Strip line comments (// ...)
    gsub(/\/\/[^\n]*/, "", source)

    if (source ~ /(^|[^a-zA-Z0-9_])unsafe([^a-zA-Z0-9_]|$)/) {
        print path "\tforbidden unsafe usage (DEV-03)"
    }
    if (source ~ /(^|[^a-zA-Z0-9_])panic[[:space:]]*\(/) {
        print path "\tforbidden panic call (DEV-03)"
    }
}
AWK

git ls-files --cached --others --exclude-standard -z '*.go' | xargs -0 -I{} sh -c '
    file="$1"
    [ -f "$file" ] || exit 0
    GUARD_PATH="$file" awk -f "'"$tmp"'/go_guard.awk" < "$file"
' sh {} >> "$tmp/violations"

# 4. Reject em dashes (U+2014) and en dashes (U+2013) in prose and source
em_dash=$(printf '\342\200\224')
en_dash=$(printf '\342\200\223')

check_dashes() {
    target_file="$1"
    [ -f "$target_file" ] || return 0
    case "$target_file" in
        scripts/check-guard.sh) return 0 ;;
    esac
    if grep -n -E "($em_dash|$en_dash)" "$target_file" >/dev/null 2>&1; then
        printf '%s\tforbidden em/en dash detected\n' "$target_file" >> "$tmp/violations"
    fi
}

for doc_file in $(git ls-files --cached --others --exclude-standard) AGENTS.md WATCHDOG.md CLAUDE.md; do
    check_dashes "$doc_file"
done

if [ -d docs ]; then
    find docs -type f -name '*.md' | while IFS= read -r f; do
        check_dashes "$f"
    done
fi

if [ -d work-orders ]; then
    find work-orders -type f -name '*.md' | while IFS= read -r f; do
        check_dashes "$f"
    done
fi

if [ -s "$tmp/violations" ]; then
    printf 'error: source guard violation(s) detected:\n' >&2
    cat "$tmp/violations" >&2
    exit 1
fi
