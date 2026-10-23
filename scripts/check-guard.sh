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
        docs/*|work-orders/*|AGENTS.md|agents.md|*/AGENTS.md|*/agents.md|WATCHDOG.md|watchdog.md|*/WATCHDOG.md|*/watchdog.md|ADVISOR.md|advisor.md|*/ADVISOR.md|*/advisor.md|CLAUDE.md|claude.md|*/CLAUDE.md|*/claude.md|GEMINI.md|gemini.md|*/GEMINI.md|*/gemini.md|*.agent.md|*.instructions.md|*.prompt.md|.agent/*|.agents/*|.claude/*|.cursor/*|.gemini/*)
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

# 3. Inspect Go sources for forbidden escape hatches (DEV-03: //nolint, /*nolint*/, unsafe, panic)
cat > "$tmp/go_guard.awk" <<'AWK'
BEGIN {
    path = ENVIRON["GUARD_PATH"]
    nolint_reported = 0
}
{
    text = text $0 "\n"
}
END {
    rest = text
    code = ""

    # Single-pass left-to-right lexical scanner over comments, strings, raw strings, and runes.
    while (length(rest) > 0) {
        if (!match(rest, /(\/\/[^\n]*|\/\*|"([^"\\]|\\.)*"|`[^`]*`|'([^'\\]|\\.)*')/)) {
            code = code rest
            break
        }

        tok_start = RSTART
        tok_len = RLENGTH
        if (tok_start > 1) {
            code = code substr(rest, 1, tok_start - 1)
        }
        tok = substr(rest, tok_start, tok_len)
        prefix2 = substr(tok, 1, 2)

        if (prefix2 == "//") {
            body = substr(tok, 3)
            if (!nolint_reported && body ~ /^[[:space:]]*nolint([^a-zA-Z0-9_]|$)/) {
                print path "\tforbidden linter suppression (//nolint)"
                nolint_reported = 1
            }
            code = code "\n"
            rest = substr(rest, tok_start + tok_len)
        } else if (prefix2 == "/*") {
            after_open = substr(rest, tok_start + 2)
            close_idx = index(after_open, "*/")
            if (close_idx > 0) {
                body = substr(after_open, 1, close_idx - 1)
                if (!nolint_reported && body ~ /(^|[[:space:]])nolint([^a-zA-Z0-9_]|$)/) {
                    print path "\tforbidden linter suppression (/*nolint*/)"
                    nolint_reported = 1
                }
                code = code " "
                rest = substr(after_open, close_idx + 2)
            } else {
                body = after_open
                if (!nolint_reported && body ~ /(^|[[:space:]])nolint([^a-zA-Z0-9_]|$)/) {
                    print path "\tforbidden linter suppression (/*nolint*/)"
                    nolint_reported = 1
                }
                break
            }
        } else {
            quote_char = substr(tok, 1, 1)
            if (quote_char == "\"" || quote_char == "`") {
                inner = substr(tok, 2, tok_len - 2)
                if (inner == "unsafe") {
                    code = code "\"unsafe\""
                } else {
                    code = code "\"\""
                }
            } else {
                code = code "''"
            }
            rest = substr(rest, tok_start + tok_len)
        }
    }

    if (code ~ /(^|[^a-zA-Z0-9_"])unsafe([^a-zA-Z0-9_"]|$)/ ||
        code ~ /(^|[^a-zA-Z0-9_])import[[:space:]]+(([a-zA-Z_][a-zA-Z0-9_]*|\.)[[:space:]]+)?"unsafe"/ ||
        code ~ /(^|[^a-zA-Z0-9_])import[[:space:]]*\([^)]*"unsafe"[^)]*\)/) {
        print path "\tforbidden unsafe usage (DEV-03)"
    }
    if (code ~ /(^|[^a-zA-Z0-9_])panic[[:space:]]*\(/) {
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

git ls-files --cached --others --exclude-standard | while IFS= read -r doc_file; do
    check_dashes "$doc_file"
done

for root_agent_doc in AGENTS.md WATCHDOG.md CLAUDE.md ADVISOR.md GEMINI.md; do
    check_dashes "$root_agent_doc"
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
