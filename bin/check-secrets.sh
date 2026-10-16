#!/usr/bin/env bash
#
# Secret gate — refuse to commit key material.
#
# Modes:
#   bin/check-secrets.sh --staged            # added lines of the staged diff
#   bin/check-secrets.sh --range A..B        # added lines of a revision range (CI)
#   bin/check-secrets.sh --self-test         # prove the patterns still catch keys
#   bin/check-secrets.sh --tree              # audit every tracked file (noisy:
#                                            # the repo already ships fixtures)
#
# Only added lines are scanned, so an existing fixture cannot block an unrelated
# edit. A new line that legitimately carries a key-shaped string (a redaction
# example, a test fixture) opts out with the marker "secret-guard:allow".
#
# Enable the commit hook in a fresh clone with:
#   git config core.hooksPath .githooks
set -uo pipefail

# "label|pattern" pairs. Patterns are ERE fragments matched against the text of
# one added line, so ^ and $ anchor that line.
readonly RULES=(
  'api-key|(^|[^A-Za-z0-9])sk-[A-Za-z0-9_-]{20,}'
  'new-api-token|(^|[^A-Za-z0-9])nap_[A-Za-z0-9_-]{20,}'
  'github-token|(^|[^A-Za-z0-9])gh[pousr]_[A-Za-z0-9]{20,}'
  'github-pat|(^|[^A-Za-z0-9])github_pat_[A-Za-z0-9_]{20,}'
  'aws-access-key|(^|[^A-Za-z0-9])AKIA[0-9A-Z]{16}([^0-9A-Z]|$)'
  'slack-token|(^|[^A-Za-z0-9])xox[abprs]-[A-Za-z0-9-]{20,}'
  'google-api-key|(^|[^A-Za-z0-9])AIza[0-9A-Za-z_-]{35}'
  'pem-private-key|-----BEGIN [A-Z ]*PRIVATE KEY-----'
  'bearer-token|[Bb]earer[[:space:]]+[A-Za-z0-9._/+=-]{24,}'
  'assigned-quoted-secret|(password|passwd|secret|api[_-]?key|access[_-]?token|admin[_-]?key|super[_-]?key|client[_-]?secret|private[_-]?key)[A-Za-z0-9_]*["'"'"']?[[:space:]]*[:=][[:space:]]*["'"'"'][A-Za-z0-9._/+=-]{24,}["'"'"']'
  'assigned-line-secret|^[[:space:]]*["'"'"']?[A-Za-z0-9_]*?(password|passwd|secret|api[_-]?key|access[_-]?token|admin[_-]?key|super[_-]?key|client[_-]?secret|private[_-]?key)[A-Za-z0-9_]*["'"'"']?[[:space:]]*[:=][[:space:]]*[A-Za-z0-9._/+=-]{24,}([[:space:]]*#.*)?$'
)

# Files that cannot carry a credential and are expensive to scan.
readonly SKIP_PATH_REGEX='(^|/)(bun\.lock|package-lock\.json|yarn\.lock|go\.sum)$'

readonly ALLOW_MARKER='secret-guard:allow'
readonly ADDED_LINE_AWK='
/^\+\+\+ / { file = substr($0, 7); next }
/^@@ / { line = 0; if (match($0, /\+[0-9]+/)) line = substr($0, RSTART + 1, RLENGTH - 1) + 0; next }
/^\+/ { if (file != "" && file != "/dev/null") printf "%s:%d:%s\n", file, line, substr($0, 2); line++; next }
'

FINDINGS=0

# combined_pattern joins the rules. The prefilter drops ^/$ anchors so it can
# match a "path:line:text" record; the full set is applied to the text alone,
# where the anchors are meaningful.
combined_pattern() {
  local mode="${1:-full}" combined="" rule pattern
  for rule in "${RULES[@]}"; do
    pattern="${rule#*|}"
    if [ "$mode" = "prefilter" ]; then
      pattern="${pattern#^}"
      pattern="${pattern%\$}"
    fi
    combined="${combined:+$combined|}"
    combined="$combined($pattern)"
  done
  printf '%s' "$combined"
}

# mask keeps a finding readable without echoing the secret into a terminal or a
# CI log.
mask() {
  local value="$1"
  if [ "${#value}" -le 8 ]; then
    printf '****'
  else
    printf '%s… (len=%d, masked)' "${value:0:6}" "${#value}"
  fi
}

label_for_line() {
  local text="$1" rule pattern
  for rule in "${RULES[@]}"; do
    pattern="${rule#*|}"
    if printf '%s' "$text" | grep -qiE -e "$pattern"; then
      printf '%s' "${rule%%|*}"
      return
    fi
  done
  printf 'unknown'
}

# scan_findings reads "path:line:text" records and reports the ones whose text
# looks like key material.
scan_findings() {
  local full prefilter hits record path line text value
  full="$(combined_pattern full)"
  prefilter="$(combined_pattern prefilter)"
  hits="$(grep -iE -e "$prefilter" 2>/dev/null)"
  [ -z "$hits" ] && return 0
  while IFS= read -r record; do
    case "$record" in
      *:*:*) ;;
      *) continue ;;
    esac
    path="${record%%:*}"
    line="${record#*:}"
    line="${line%%:*}"
    text="${record#*:*:}"
    case "$text" in *"$ALLOW_MARKER"*) continue ;; esac
    # Re-check the text alone so anchored rules keep their meaning.
    value="$(printf '%s' "$text" | grep -oiE -e "$full" | head -1)"
    [ -z "$value" ] && continue
    printf '  %s:%s  %-16s %s\n' "$path" "$line" "$(label_for_line "$text")" "$(mask "$value")"
    FINDINGS=$((FINDINGS + 1))
  done <<<"$hits"
}

# added_records turns a git diff into "path:line:text" records of added lines.
added_records() {
  git diff --unified=0 --no-color --diff-filter=ACMR "$@" | awk "$ADDED_LINE_AWK"
}

scan_tree() {
  local path records
  while IFS= read -r -d '' path; do
    [ -f "$path" ] || continue
    [[ "$path" =~ $SKIP_PATH_REGEX ]] && continue
    records="$(awk -v f="$path" '{ printf "%s:%d:%s\n", f, NR, $0 }' "$path")"
    scan_findings <<<"$records"
  done < <(git ls-files -z)
}

self_test() {
  local tmp records hits before
  tmp="$(mktemp)"
  records="$(mktemp)"
  # Fixtures are generated at runtime, so this script stores no key-shaped
  # literal of its own.
  {
    printf 'const fake = "sk-%s"\n' "$(printf 'a%.0s' $(seq 1 32))"
    printf 'Authorization: Bearer %s\n' "$(printf 'b%.0s' $(seq 1 40))"
    printf 'SESSION_SECRET=%s\n' "$(printf 'c%.0s' $(seq 1 48))"
    printf 'apiKey: "%s"\n' "$(printf 'd%.0s' $(seq 1 40))"
    printf 'MY_API_KEY=%s\n' "$(printf 'e%.0s' $(seq 1 40))"
    printf 'let token = "not-a-secret"\n'
    printf 'SESSION_SECRET=short\n'
    printf 'setting.SuperKey = originSetting.SuperKeyUserID\n'
  } >"$tmp"
  awk '{ printf "fixture:%d:%s\n", NR, $0 }' "$tmp" >"$records"
  before="$FINDINGS"
  scan_findings <"$records"
  hits=$((FINDINGS - before))
  rm -f "$tmp" "$records"
  if [ "$hits" -ne 5 ]; then
    printf '✖ self-test failed: expected 5 key-shaped fixtures to be caught, got %s\n' "$hits"
    return 1
  fi
  FINDINGS=$before
  printf '✓ self-test passed: the patterns still catch fresh key-shaped values\n'
}

report() {
  printf '\n✖ secret gate: %d key-like value(s) found.\n' "$FINDINGS"
  printf '  检测到疑似密钥，已阻止提交。处理方式：\n'
  printf '    1) 把值移到环境变量或未跟踪的密钥文件（如 .env，已在 .gitignore 中）\n'
  printf '    2) 若该行确实是示例或测试夹具，在行内加 %s 明确放行\n\n' "$ALLOW_MARKER"
}

main() {
  local mode="${1:---staged}" records
  case "$mode" in
    --staged)
      records="$(added_records --cached)"
      scan_findings <<<"$records"
      ;;
    --range)
      [ $# -ge 2 ] || { printf 'usage: %s --range A..B\n' "$0" >&2; return 2; }
      records="$(added_records "$2")"
      scan_findings <<<"$records"
      ;;
    --tree) scan_tree ;;
    --self-test) self_test; return $? ;;
    *)
      printf 'usage: %s [--staged|--range A..B|--tree|--self-test]\n' "$0" >&2
      return 2
      ;;
  esac
  if [ "$FINDINGS" -gt 0 ]; then
    report
    return 1
  fi
  printf '✓ secret gate: no key-like value found\n'
  return 0
}

main "$@"
