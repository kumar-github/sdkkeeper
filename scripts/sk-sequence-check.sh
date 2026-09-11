#!/usr/bin/env zsh
#
# sk-sequence-check.sh -- a real, sequence-based regression guide for
# sk, built directly from the actual bug pattern found across this
# whole project: almost every real bug came from a SEQUENCE of
# commands within one shell session, never from any single command
# tested in isolation. `sk use v1`, then `sk use v2`, then `sk remove
# v2` is where PATH accumulation bugs live; a bare `sk remove v2` on
# its own never exposes them.
#
# Two things every section does:
#   1. Runs a REALISTIC sequence, in the order a real session would.
#   2. Asserts sk's OWN report against INDEPENDENTLY-VERIFIED ground
#      truth (command -v, $JAVA_HOME, $PATH) -- most bugs this project
#      has hit were exactly this kind of divergence: sk reporting one
#      thing while the shell's real, resolved state was something
#      else.
#
# GENUINELY AUTOMATED, not just descriptive: every check below is a
# real assert_* call that increments a pass/fail tally and this script
# EXITS NON-ZERO if anything failed -- a real regression will fail this
# script's own exit code, not just print text a human has to notice.
# This is a real fix for a real gap: an earlier version of this script
# only ever printed "-> EXPECT: ..." descriptions for a human to
# compare against the output above it, with no programmatic check at
# all for most sections -- meaning a genuine regression could pass
# through unnoticed if run in CI (or by anyone not reading closely),
# since the script itself always exited 0 regardless of whether
# reality matched the description.
#
# This does NOT attempt full combinatorial coverage -- that's
# infeasible for a tool with this many dimensions (tool x version
# state x vendor count x session history x shell). It covers the
# SPECIFIC state dimensions that have actually caused real bugs so
# far, and is meant to grow: when a new bug is found, the fix belongs
# here too, as a new section, not just in the code.
#
# SAFE TO RUN: uses an isolated, throwaway HOME for the whole run --
# never touches your real ~/.sdkkeeper. Uses lightweight, fake `java`
# scripts (not real JDK downloads), so this runs in seconds and needs
# no network access.
#
# Usage:
#   chmod +x sk-sequence-check.sh
#   ./sk-sequence-check.sh /path/to/sk    # or just ./sk-sequence-check.sh
#                                          # if `sk` is already on PATH
# Exit code: 0 if every assertion passed, 1 if any failed OR if sk
# itself could not be found -- safe to use directly as a CI gate.

set -u

SK_BIN="${1:-sk}"
if ! command -v "$SK_BIN" >/dev/null 2>&1 && [[ ! -x "$SK_BIN" ]]; then
    echo "Could not find sk binary at '$SK_BIN' -- pass its path as the first argument."
    exit 1
fi
SK_BIN="$(command -v "$SK_BIN" 2>/dev/null || echo "$SK_BIN")"
SK_DIR="$(dirname "$SK_BIN")"

TEST_HOME="$(mktemp -d)"
trap 'rm -rf "$TEST_HOME"' EXIT

# --- assertion framework -------------------------------------------

CHECKS=0
FAILURES=0

# assert_contains OUTPUT PATTERN DESCRIPTION -- OUTPUT must contain
# PATTERN as a literal substring (not a regex -- avoids escaping
# headaches with this project's own glyphs like '✓'/'✗'/'→').
assert_contains() {
    local output="$1" pattern="$2" description="$3"
    CHECKS=$((CHECKS + 1))
    if [[ "$output" == *"$pattern"* ]]; then
        print "    ✓ PASS: $description"
    else
        print "    ✗ FAIL: $description"
        print "      expected to find: $pattern"
        print "      actual output was:"
        print -r -- "$output" | sed 's/^/        /'
        FAILURES=$((FAILURES + 1))
    fi
}

# assert_not_contains -- the inverse: OUTPUT must NOT contain PATTERN.
assert_not_contains() {
    local output="$1" pattern="$2" description="$3"
    CHECKS=$((CHECKS + 1))
    if [[ "$output" != *"$pattern"* ]]; then
        print "    ✓ PASS: $description"
    else
        print "    ✗ FAIL: $description"
        print "      expected NOT to find: $pattern"
        print "      actual output was:"
        print -r -- "$output" | sed 's/^/        /'
        FAILURES=$((FAILURES + 1))
    fi
}

# assert_equal ACTUAL EXPECTED DESCRIPTION -- exact string match, for
# cases where "contains" is too loose (e.g. an exact count).
assert_equal() {
    local actual="$1" expected="$2" description="$3"
    CHECKS=$((CHECKS + 1))
    if [[ "$actual" == "$expected" ]]; then
        print "    ✓ PASS: $description"
    else
        print "    ✗ FAIL: $description"
        print "      expected: $expected"
        print "      actual:   $actual"
        FAILURES=$((FAILURES + 1))
    fi
}

# assert_true CONDITION DESCRIPTION -- CONDITION must be the literal
# string "true" (build it with `[[ ... ]] && echo true || echo false`
# at the call site).
assert_true() {
    local condition="$1" description="$2"
    CHECKS=$((CHECKS + 1))
    if [[ "$condition" == "true" ]]; then
        print "    ✓ PASS: $description"
    else
        print "    ✗ FAIL: $description"
        FAILURES=$((FAILURES + 1))
    fi
}

# --- other helpers ---------------------------------------------------

section() {
    print ""
    print "════════════════════════════════════════════════════════════"
    print "  $1"
    print "════════════════════════════════════════════════════════════"
}

step() {
    print ""
    print "── $1"
}

# Prints sk's own report next to independently-verified ground truth,
# purely for human-readable log context -- the actual PASS/FAIL
# verdict always comes from an assert_* call, not from a human reading
# this.
compare_state() {
    local tool_env_var="$1"
    print "  sk current java:      $($SK_BIN current java 2>&1 | tail -1)"
    print "  \$JAVA_HOME:            ${(P)tool_env_var:-<unset>}"
    print "  command -v java:       $(command -v java 2>/dev/null || echo '<not found>')"
    if command -v java >/dev/null 2>&1; then
        print "  java (fake) reports:   $(java -version 2>&1)"
    else
        print "  java (fake) reports:   <not found>"
    fi
}

# Creates a lightweight, fake JDK -- a real, executable `java` script
# that identifies itself, not a real download. Fast, network-free,
# and still exercises real PATH/JAVA_HOME resolution end to end.
fake_jdk() {
    local dir="$1" label="$2"
    mkdir -p "$dir/bin"
    cat > "$dir/bin/java" << EOF
#!/bin/sh
echo "FAKE JDK: $label"
EOF
    chmod +x "$dir/bin/java"
}

# The real wrapper function sk init zsh generates -- kept in sync
# manually. If sk init zsh's own output ever changes, update this too
# (or better: swap this for \$($SK_BIN init zsh) directly once this
# script is trusted not to be running against a version of sk with
# its own bugs in `init` itself).
sk() {
    if [[ "$1" == "use" || "$1" == "remove" ]]; then
        eval "$(command "$SK_BIN" "$@")"
    else
        command "$SK_BIN" "$@"
    fi
}

export HOME="$TEST_HOME"
export PATH="$SK_DIR:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
unset JAVA_HOME

# capture_sk CMD... -- runs the sk wrapper function DIRECTLY (never
# inside a command-substitution subshell) so its internal `eval` (for
# `use`/`remove`) genuinely mutates THIS shell's environment, exactly
# as a real user's session would -- then leaves the combined
# stdout+stderr in the global $SK_OUT for assertions.
#
# A real, confirmed bug caught while building this exact assertion
# framework: `out=$(sk use ...)`-style capture ALWAYS forks a
# subshell (command substitution is a subshell in any POSIX-family
# shell, zsh included) -- confirmed directly with a minimal
# reproduction. `sk use`/`sk remove`'s entire real effect is the
# internal `eval` mutating JAVA_HOME/PATH; wrapped in $(...), that
# mutation happens ONLY inside the forked subshell and is silently
# discarded the instant it exits, never reaching the actual calling
# shell at all -- meaning every assertion checking JAVA_HOME/PATH
# after such a capture would have been checking STALE, unchanged
# state, not sk's real effect. Redirecting to a file and reading it
# back avoids this entirely: redirection alone does not fork a
# subshell, so the eval runs directly in this shell, same as it
# would for a real user typing the same command.
capture_sk() {
    local tmpfile
    tmpfile=$(mktemp)
    sk "$@" > "$tmpfile" 2>&1
    SK_OUT=$(cat "$tmpfile")
    rm -f "$tmpfile"
}

CANDIDATES="$TEST_HOME/.sdkkeeper/candidates/java"

# ════════════════════════════════════════════════════════════════
section "A. Single-version lifecycle (install → use → list → remove → current)"
# ════════════════════════════════════════════════════════════════
fake_jdk "$CANDIDATES/JDK-21.0.2-temurin" "21.0.2-temurin"

step "use (no prior state)"
sk use java 21.0.2-temurin
compare_state JAVA_HOME
assert_true "$([[ "$(command -v java)" == *JDK-21.0.2-temurin* ]] && echo true || echo false)" \
    "java resolves through the newly activated JDK"

step "list"
list_out=$(sk list java 2>&1)
print -r -- "$list_out"
assert_contains "$list_out" "21.0.2-temurin" "list shows the installed version"

step "remove the one we just activated"
capture_sk remove java 21.0.2-temurin
remove_out="$SK_OUT"
print -r -- "$remove_out"
compare_state JAVA_HOME
# NOT asserting java is unfindable OUTRIGHT -- a real, live-caught
# false failure: this sandbox (like some real machines) has its own,
# genuine, external system java, which java correctly, legitimately
# falls through to once the sk-managed entry is gone -- that's
# correct behavior, not a bug (the same nuance already documented in
# section K below). What actually matters is that java does NOT
# resolve to the SPECIFIC entry that was just removed.
assert_not_contains "$(command -v java 2>/dev/null || echo '')" "JDK-21.0.2-temurin" \
    "java no longer resolves to the specific JDK that was just removed"
assert_true "$([[ -z "${JAVA_HOME:-}" ]] && echo true || echo false)" \
    "JAVA_HOME is unset after removing the active JDK"

# ════════════════════════════════════════════════════════════════
section "B. Multi-version switching in ONE session (the PATH-accumulation bug class)"
# ════════════════════════════════════════════════════════════════
unset JAVA_HOME
fake_jdk "$CANDIDATES/JDK-20-temurin" "20-temurin"
fake_jdk "$CANDIDATES/JDK-26.0.2.1-liberica" "26.0.2.1-liberica"

step "use v1 (20-temurin)"
sk use java 20-temurin
compare_state JAVA_HOME

step "use v2 (switch to 26.0.2.1-liberica, WITHOUT removing v1 first)"
sk use java 26.0.2.1-liberica
java_report=$(java -version 2>&1)
compare_state JAVA_HOME
assert_contains "$java_report" "26.0.2.1-liberica" "java reports the more recently activated version"

step "remove the ACTIVE one (v2 / liberica)"
sk remove java 26.0.2.1-liberica
compare_state JAVA_HOME
assert_true "$([[ -z "${JAVA_HOME:-}" ]] && echo true || echo false)" "JAVA_HOME unset after removing the active one"
# v1 (20-temurin) genuinely still exists and was never removed --
# java resolving to it is CORRECT, not a bug. The real thing this
# guards against is PATH getting silently wiped, or 'sk' itself
# becoming unfindable.
assert_true "$([[ -n "$(command -v sk 2>/dev/null)" ]] && echo true || echo false)" \
    "'sk' itself is still findable on PATH (not wiped out)"

step "cleanup: remove v1 too, confirm a clean end state"
sk remove java 20-temurin
compare_state JAVA_HOME
# Same nuance as section A's own fix above -- a genuine, external
# system java (if this machine has one) is a correct fallthrough, not
# a bug. What matters is neither sk-managed entry is still resolved.
final_java=$(command -v java 2>/dev/null || echo "")
assert_not_contains "$final_java" "JDK-20-temurin" "java does not resolve to the removed v1 entry"
assert_not_contains "$final_java" "JDK-26.0.2.1-liberica" "java does not resolve to the removed v2 entry"

step "B2. Same scenario, but remove the NON-active (earlier) one instead"
unset JAVA_HOME
fake_jdk "$CANDIDATES/JDK-20-temurin" "20-temurin"
fake_jdk "$CANDIDATES/JDK-26.0.2.1-liberica" "26.0.2.1-liberica"
sk use java 20-temurin >/dev/null
sk use java 26.0.2.1-liberica >/dev/null
sk remove java 20-temurin
java_report=$(java -version 2>&1)
compare_state JAVA_HOME
assert_contains "$java_report" "26.0.2.1-liberica" \
    "removing a non-active version leaves the currently active one completely undisturbed"
sk remove java 26.0.2.1-liberica >/dev/null 2>&1

# ════════════════════════════════════════════════════════════════
section "C. Prerequisite chains (Maven requires Java) -- 4 real combinations"
# ════════════════════════════════════════════════════════════════
unset JAVA_HOME

step "C1. Neither Maven nor Java installed, bare 'use maven'"
capture_sk use maven
out="$SK_OUT"
print -r -- "$out"
assert_contains "$out" "Maven" "message mentions Maven"
assert_not_contains "$out" "JDK" "no mention of JDK when Java was never even reached"

step "C2. Neither installed, EXPLICIT 'use maven 3.9.14'"
capture_sk use maven 3.9.14
out="$SK_OUT"
print -r -- "$out"
assert_contains "$out" "Maven" "Maven-specific not-found message"
assert_not_contains "$out" "JDK" "still no mention of JDK"

MAVEN_CANDIDATES="$TEST_HOME/.sdkkeeper/candidates/maven"
mkdir -p "$MAVEN_CANDIDATES/apache-maven-3.9.14"

step "C3. Maven installed, Java NOT -- bare 'use maven'"
capture_sk use maven
out="$SK_OUT"
print -r -- "$out"
assert_contains "$out" "JDK" "the JDK prerequisite message appears now that Java is genuinely needed"

fake_jdk "$CANDIDATES/JDK-21.0.2-temurin" "21.0.2-temurin"

step "C4. Both installed -- full happy path"
sk use java 21.0.2-temurin >/dev/null
capture_sk use maven 3.9.14
out="$SK_OUT"
print -r -- "$out"
assert_contains "$out" "Maven" "Maven activates cleanly"
assert_not_contains "$out" "No JDK" "no JDK prompt when Java is already active"

sk remove java 21.0.2-temurin >/dev/null 2>&1
sk remove maven 3.9.14 >/dev/null 2>&1

# ════════════════════════════════════════════════════════════════
section "D. remove with no version argument (should show a picker, or a clear message)"
# ════════════════════════════════════════════════════════════════
step "D1. Nothing installed"
capture_sk remove java
out="$SK_OUT"
print -r -- "$out"
assert_contains "$out" "No JDK versions found" "clear message, not a silent, empty exit"

fake_jdk "$CANDIDATES/JDK-21.0.2-temurin" "21.0.2-temurin"
step "D2. Something installed (picker needs a real TTY -- confirm INTERACTIVELY,"
print "     not piped/redirected, to actually see the picker)"
print "  NOTE: this specific case cannot be asserted non-interactively -- a real"
print "  TTY is required to drive the picker. Confirm manually on a real terminal."
sk remove java 21.0.2-temurin >/dev/null 2>&1

# ════════════════════════════════════════════════════════════════
section "E. default + list interaction (the (current)/(default) tags)"
# ════════════════════════════════════════════════════════════════
unset JAVA_HOME
fake_jdk "$CANDIDATES/JDK-21.0.2-temurin" "21.0.2-temurin"
fake_jdk "$CANDIDATES/JDK-17.0.3-temurin" "17.0.3-temurin"

step "set a default, then use a DIFFERENT version"
sk default java 21.0.2-temurin >/dev/null
sk use java 17.0.3-temurin >/dev/null
list_out=$(sk list java 2>&1)
print -r -- "$list_out"
default_line=$(print -r -- "$list_out" | grep "21.0.2-temurin")
current_line=$(print -r -- "$list_out" | grep "17.0.3-temurin")
assert_contains "$default_line" "(default)" "21.0.2-temurin's OWN line is tagged (default)"
assert_not_contains "$default_line" "(current)" "21.0.2-temurin's line is NOT also tagged (current)"
assert_contains "$current_line" "(current)" "17.0.3-temurin's OWN line is tagged (current)"
assert_not_contains "$current_line" "(default)" "17.0.3-temurin's line is NOT also tagged (default)"

step "remove the version that's the stored default (not the active one)"
capture_sk remove java 21.0.2-temurin
out="$SK_OUT"
print -r -- "$out"
assert_contains "$out" "default" "confirmation mentions the default was cleared"

sk remove java 17.0.3-temurin >/dev/null 2>&1

# ════════════════════════════════════════════════════════════════
section "F. Vendor grouping in 'list' (multi-vendor tool)"
# ════════════════════════════════════════════════════════════════
fake_jdk "$CANDIDATES/JDK-23.0.2-temurin" "23.0.2-temurin"
fake_jdk "$CANDIDATES/JDK-26.0.2-liberica" "26.0.2-liberica"
fake_jdk "$CANDIDATES/JDK-26.0.1-liberica" "26.0.1-liberica"

step "list java with multiple vendors AND multiple patch versions per vendor"
list_out=$(sk list java 2>&1)
print -r -- "$list_out"
assert_contains "$list_out" "Temurin:" "grouped under a 'Temurin:' header"
assert_contains "$list_out" "Liberica:" "grouped under a 'Liberica:' header"
liberica_202_line=$(print -r -- "$list_out" | grep -n "26.0.2-liberica" | head -1 | cut -d: -f1)
liberica_201_line=$(print -r -- "$list_out" | grep -n "26.0.1-liberica" | head -1 | cut -d: -f1)
assert_true "$([[ "$liberica_202_line" -lt "$liberica_201_line" ]] && echo true || echo false)" \
    "26.0.2-liberica appears ABOVE 26.0.1-liberica (newest first, correctly compared)"

sk remove java 23.0.2-temurin >/dev/null 2>&1
sk remove java 26.0.2-liberica >/dev/null 2>&1
sk remove java 26.0.1-liberica >/dev/null 2>&1

# ════════════════════════════════════════════════════════════════
section "G. 'add' (external/not-managed) entries stay flat, even with a vendor-looking label"
# ════════════════════════════════════════════════════════════════
EXTERNAL_DIR="$(mktemp -d)"
sk add java 17.0.3-temurin "$EXTERNAL_DIR" >/dev/null
list_out=$(sk list java 2>&1)
print -r -- "$list_out"
assert_contains "$list_out" "Not managed by SDK Keeper:" "appears under the 'Not managed' section"
assert_not_contains "$list_out" "Temurin:" "NOT grouped under a 'Temurin:' sub-heading, even though the label looks like one"
assert_contains "$list_out" "$EXTERNAL_DIR" "shows the REAL external location"
assert_not_contains "$list_out" ".sdkkeeper/candidates" "never shows sk's own internal bookkeeping path for a not-managed entry"
sk remove java 17.0.3-temurin >/dev/null 2>&1
rm -rf "$EXTERNAL_DIR"

# ════════════════════════════════════════════════════════════════
section "H. Bad argument counts across EVERY command -- clear usage message, never silent, never a crash"
# ════════════════════════════════════════════════════════════════
step "add with too few args"
out=$(sk add java 2>&1)
print -r -- "$out"
assert_contains "$out" "usage: sk add <tool> <version> <path>" \
    "clear usage message -- not silence, not a nil-pointer panic"

step "remove with too many args"
capture_sk remove java 21.0.2-temurin extra
out="$SK_OUT"
print -r -- "$out"
assert_contains "$out" "usage: sk remove <tool> [version]" "clear usage message"

step "doctor with an unexpected arg (it takes none at all)"
out=$(sk doctor unexpected 2>&1)
print -r -- "$out"
assert_contains "$out" "usage: sk doctor" "clear usage message"

step "search with too few args"
out=$(sk search java 2>&1)
print -r -- "$out"
assert_contains "$out" "usage: sk search <tool> <vendor> [major]" "clear usage message"

# ════════════════════════════════════════════════════════════════
section "I. Action-specific wording for 'not found'/'nothing selected' messages"
# ════════════════════════════════════════════════════════════════
unset JAVA_HOME
step "use with nothing installed at all"
capture_sk use java
out="$SK_OUT"
print -r -- "$out"
assert_contains "$out" "No JDK versions found to use." \
    "action-specific wording, not a bare, generic 'No JDK versions found.'"

step "use with an explicit, non-existent version"
fake_jdk "$CANDIDATES/JDK-21.0.2-temurin" "21.0.2-temurin"
capture_sk use java 99.99-temurin
out="$SK_OUT"
print -r -- "$out"
assert_contains "$out" "not found — nothing to use" "correct not-found wording"
assert_contains "$out" "21.0.2-temurin" "the available version is listed below it"

step "use with an explicit, non-existent version, AND nothing installed at all"
sk remove java 21.0.2-temurin >/dev/null 2>&1
capture_sk use java 99.99-temurin
out="$SK_OUT"
print -r -- "$out"
assert_contains "$out" "not found — nothing to use" "correct not-found wording"
assert_contains "$out" "No JDK versions installed or added yet." \
    "clear 'nothing installed' message, not an empty 'Available versions:' header"

step "remove with nothing installed at all"
capture_sk remove java
out="$SK_OUT"
print -r -- "$out"
assert_contains "$out" "No JDK versions found to remove." "action-specific wording"

step "remove with an explicit, non-existent version"
fake_jdk "$CANDIDATES/JDK-21.0.2-temurin" "21.0.2-temurin"
capture_sk remove java 99.99-temurin
out="$SK_OUT"
print -r -- "$out"
assert_contains "$out" "not found — nothing to remove" "correct not-found wording"
sk remove java 21.0.2-temurin >/dev/null 2>&1

step "remove with an explicit, non-existent version, AND nothing installed at all"
capture_sk remove java 99.99-temurin
out="$SK_OUT"
print -r -- "$out"
assert_contains "$out" "not found — nothing to remove" "correct not-found wording"
assert_contains "$out" "No JDK versions installed or added yet." "same fix as the 'use' case above"

print ""
print "  NOTE: the picker-CANCELLED wording ('No JDK version selected TO USE.',"
print "  '... TO REMOVE.', 'No JDK vendor selected TO INSTALL.', etc.) can only"
print "  be triggered by actually pressing Esc/Ctrl-C inside a real picker --"
print "  this script cannot drive that non-interactively. Confirm those"
print "  manually, interactively, on a real terminal."

# ════════════════════════════════════════════════════════════════
section "J. 'list' with absolutely nothing installed or added"
# ════════════════════════════════════════════════════════════════
step "list on a tool with zero managed AND zero external entries"
out=$(sk list java 2>&1)
print -r -- "$out"
assert_contains "$out" "No JDK versions found to list." \
    "clear message, not silent, empty output that looks like the command hung"

# ════════════════════════════════════════════════════════════════
section "K. PATH deduplication -- repeated/switched activation must never accumulate"
# ════════════════════════════════════════════════════════════════
unset JAVA_HOME
fake_jdk "$CANDIDATES/JDK-11" "11"
fake_jdk "$CANDIDATES/JDK-25.0.1" "25.0.1"

step "switch between two versions repeatedly within ONE session"
sk use java 11 >/dev/null 2>&1
sk use java 25.0.1 >/dev/null 2>&1
sk use java 11 >/dev/null 2>&1
sk use java 25.0.1 >/dev/null 2>&1
java_count=$(print -r -- "$PATH" | tr ':' '\n' | grep -c "$CANDIDATES")
print "  Number of java entries left in PATH after 4 activations: $java_count"
assert_equal "$java_count" "1" \
    "exactly 1 entry, not 2/4/accumulating -- 'use' used to only ever PREPEND, never cleaning up an earlier prepend for the same tool"

step "NOW remove the currently-active one -- must resolve to NOTHING sk-managed"
sk remove java 25.0.1
current_java=$(command -v java 2>/dev/null || echo "")
compare_state JAVA_HOME
assert_not_contains "$current_java" "$CANDIDATES/JDK-11" \
    "java does NOT resolve to the earlier, still-accumulated JDK-11 entry (the exact real bug: PATH accumulation from 'use' itself, not a gap in 'remove')"
sk remove java 11 >/dev/null 2>&1

# ════════════════════════════════════════════════════════════════
section "L. Color consistency -- every '✗' the same red, every '⚠' a genuinely different color, (current)/(default) visibly distinct"
# ════════════════════════════════════════════════════════════════
print "  This sandbox/terminal may have no real color support at all, so raw"
print "  ANSI codes are forced via CLICOLOR_FORCE/FORCE_COLOR to actually see"
print "  them -- lipgloss correctly suppresses color for non-TTY output"
print "  otherwise, which would make every comparison below trivially pass"
print "  for the wrong reason (no color anywhere, so nothing to compare)."
export CLICOLOR_FORCE=1
export FORCE_COLOR=1

step "two DIFFERENT '✗' messages -- must be the EXACT same color"
usage_err=$(sk add java 2>&1)
tool_err=$(sk current bogus 2>&1)
usage_code=$(print -r -- "$usage_err" | grep -o '\[[0-9;]*m' | head -1)
tool_code=$(print -r -- "$tool_err" | grep -o '\[[0-9;]*m' | head -1)
print "  'sk add' usage error color code:      $usage_code"
print "  'sk current' unknown-tool error code: $tool_code"
assert_true "$([[ -n "$usage_code" ]] && echo true || echo false)" "the usage error is genuinely styled (not plain, unstyled text)"
assert_equal "$usage_code" "$tool_code" "both '✗' errors use the identical color code"

step "doctor's '⚠' warning vs a genuine '✗' error -- must be DIFFERENT colors"
mkdir -p "$TEST_HOME/.sdkkeeper/tmp/leftover-dir"
warning_out=$(sk doctor 2>&1)
warning_code=$(print -r -- "$warning_out" | grep -A1 "temp director" | grep -o '\[[0-9;]*m' | head -1)
print "  doctor's warning color code: $warning_code"
assert_true "$([[ -n "$warning_code" ]] && echo true || echo false)" "the warning is genuinely styled"
assert_true "$([[ "$warning_code" != "$usage_code" ]] && echo true || echo false)" \
    "warning color is genuinely DIFFERENT from error color (doctor's '⚠' used to render in the exact same red as '✗')"
rm -rf "$TEST_HOME/.sdkkeeper/tmp/leftover-dir"

step "list's (current) vs (default) tags -- must be visibly distinct colors"
fake_jdk "$CANDIDATES/JDK-21.0.2-temurin" "21.0.2-temurin"
fake_jdk "$CANDIDATES/JDK-17.0.3-temurin" "17.0.3-temurin"
sk default java 21.0.2-temurin >/dev/null 2>&1
export JAVA_HOME="$CANDIDATES/JDK-17.0.3-temurin"
sk use java 17.0.3-temurin >/dev/null 2>&1
list_out=$(sk list java 2>&1)
unset JAVA_HOME
default_code=$(print -r -- "$list_out" | grep -o '\[[0-9;]*m(default)' | grep -o '\[[0-9;]*m')
current_code=$(print -r -- "$list_out" | grep -o '\[[0-9;]*m(current)' | grep -o '\[[0-9;]*m')
print "  (default) color code: $default_code"
print "  (current) color code: $current_code"
assert_true "$([[ -n "$default_code" && -n "$current_code" ]] && echo true || echo false)" \
    "both tags are genuinely styled (not plain, unstyled text)"
assert_true "$([[ "$default_code" != "$current_code" ]] && echo true || echo false)" \
    "(current) and (default) use visibly distinct colors"
sk remove java 21.0.2-temurin >/dev/null 2>&1
sk remove java 17.0.3-temurin >/dev/null 2>&1

step "a 'sole output' Neutral message vs a genuinely subordinate Detail message -- distinct source colors, verified at the source"
print "  NOTE: comparing RENDERED ANSI codes (like the checks above) isn't"
print "  reliable for this specific pair -- Neutral (blue) and Detail (a muted"
print "  blue-grey) are deliberately close in HUE, and can downsample to the"
print "  SAME 16-color terminal approximation on a limited-color terminal."
print "  Checking the source values directly instead, which this sandbox's"
print "  own terminal limitation cannot affect."
STYLE_GO="${0:A:h}/../internal/term/style.go"
if [[ -f "$STYLE_GO" ]]; then
    neutral_hex=$(grep 'colorBlue' "$STYLE_GO" | grep -o 'Dark: "#[0-9a-f]*"' | grep -o '#[0-9a-f]*')
    detail_hex=$(grep 'colorSubtext' "$STYLE_GO" | grep -o 'Dark: "#[0-9a-f]*"' | grep -o '#[0-9a-f]*')
    print "  Neutral's source color (colorBlue, dark side): $neutral_hex"
    print "  Detail's source color (colorSubtext, dark side): $detail_hex"
    assert_true "$([[ -n "$neutral_hex" && -n "$detail_hex" ]] && echo true || echo false)" \
        "both source colors were found in style.go"
    assert_true "$([[ "$neutral_hex" != "$detail_hex" ]] && echo true || echo false)" \
        "genuinely distinct colors at the source, regardless of any terminal's own rendering limits"
else
    print "  SKIPPED: style.go not found relative to this script (running against"
    print "    a binary copied outside its own source tree) -- run this check from"
    print "    within the repo, or verify visually on a real, truecolor-capable"
    print "    terminal instead."
fi

unset CLICOLOR_FORCE FORCE_COLOR

# ════════════════════════════════════════════════════════════════
section "M. 'use <tool> null' -- clear active state without touching disk"
# ════════════════════════════════════════════════════════════════
unset JAVA_HOME

step "null with nothing active at all"
capture_sk use java null
out="$SK_OUT"
print -r -- "$out"
assert_contains "$out" "No JDK currently active in this shell — nothing to clear." \
    "the neutral 'nothing to clear' message"
assert_not_contains "$out" "cleared for this shell" \
    "NOT the 'cleared' success message -- nothing was ever active"

step "activate a real version, then null"
fake_jdk "$CANDIDATES/JDK-21.0.2-temurin" "21.0.2-temurin"
sk use java 21.0.2-temurin >/dev/null 2>&1
capture_sk use java null
out="$SK_OUT"
print -r -- "$out"
compare_state JAVA_HOME
assert_contains "$out" "JAVA_HOME cleared for this shell — no JDK currently active" "correct clear confirmation"
assert_true "$([[ -z "${JAVA_HOME:-}" ]] && echo true || echo false)" "JAVA_HOME is genuinely empty"
assert_not_contains "$(command -v java 2>/dev/null || echo '')" "JDK-21.0.2-temurin" \
    "java does not resolve to the version that was just cleared"

step "the version is STILL on disk after null -- this only touched the shell"
list_out=$(sk list java 2>&1)
print -r -- "$list_out"
assert_contains "$list_out" "21.0.2-temurin" \
    "STILL listed under 'Managed by SDK Keeper' -- 'null' clears activation state only, never removes anything real"
sk remove java 21.0.2-temurin >/dev/null 2>&1

step "null on an unknown tool"
capture_sk use bogus null
out="$SK_OUT"
print -r -- "$out"
assert_contains "$out" "unknown tool: bogus" \
    "clear, reported error -- NOT a silent, unreported failure (this path originally bypassed every error-printing path use.go's normal flow has)"

# ════════════════════════════════════════════════════════════════
section "N. Batch fixes: remove announcements, list wording, doctor order, default wording, vendor capitalization"
# ════════════════════════════════════════════════════════════════
unset JAVA_HOME

step "removing the CURRENT JDK now announces the role, matching the default line's own shape"
fake_jdk "$CANDIDATES/JDK-21.0.2-temurin" "21.0.2-temurin"
export JAVA_HOME="$CANDIDATES/JDK-21.0.2-temurin"
capture_sk remove java 21.0.2-temurin
out="$SK_OUT"
unset JAVA_HOME
print -r -- "$out"
assert_contains "$out" "was the current JDK — current cleared" "role announcement, matching 'was the default JDK — default cleared'"
assert_contains "$out" "JAVA_HOME cleared for this shell" "the JAVA_HOME line still appears separately"

step "removing a JDK that is BOTH default and current shows all three lines"
fake_jdk "$CANDIDATES/JDK-17.0.3-temurin" "17.0.3-temurin"
sk default java 17.0.3-temurin >/dev/null 2>&1
export JAVA_HOME="$CANDIDATES/JDK-17.0.3-temurin"
capture_sk remove java 17.0.3-temurin
out="$SK_OUT"
unset JAVA_HOME
print -r -- "$out"
default_idx=$(print -r -- "$out" | grep -n "was the default JDK" | cut -d: -f1)
current_idx=$(print -r -- "$out" | grep -n "was the current JDK" | cut -d: -f1)
javahome_idx=$(print -r -- "$out" | grep -n "JAVA_HOME cleared for this shell" | cut -d: -f1)
assert_true "$([[ -n "$default_idx" && -n "$current_idx" && -n "$javahome_idx" ]] && echo true || echo false)" \
    "all three lines are present"
assert_true "$([[ "$default_idx" -lt "$current_idx" && "$current_idx" -lt "$javahome_idx" ]] && echo true || echo false)" \
    "the three lines appear in the correct order: default, then current, then JAVA_HOME"

step "list with nothing installed uses the same 'found to X' pattern as use/remove"
out=$(sk list java 2>&1)
print -r -- "$out"
assert_contains "$out" "No JDK versions found to list." "consistent 'found to X' wording"

step "sk default's confirmation clarifies this applies in EACH new shell"
fake_jdk "$CANDIDATES/JDK-21.0.2-temurin" "21.0.2-temurin"
out=$(sk default java 21.0.2-temurin 2>&1)
print -r -- "$out"
assert_contains "$out" "in each new shell to activate it" "the repetition cue is present, not just 'to activate it'"

step "the bare 'sk default java' query carries the SAME reminder as the set confirmation"
out=$(sk default java 2>&1)
print -r -- "$out"
assert_contains "$out" "Default JDK: 21.0.2-temurin" "shows the stored default"
assert_contains "$out" "in each new shell to activate it" "same activation reminder as the set confirmation -- a real gap found via actual use: this bare, query-only path used to drop it entirely"

sk default java null >/dev/null 2>&1
sk remove java 21.0.2-temurin >/dev/null 2>&1

step "vendor names are capitalized in 'vendors' output"
out=$(sk vendors java 2>&1)
print -r -- "$out"
assert_contains "$out" "Temurin" "capitalized vendor name"
assert_contains "$out" "Liberica" "capitalized vendor name"
assert_not_contains "$out" $'\n  temurin' "NOT the lowercase internal identifier as a standalone list item"

step "doctor's tool-iterating checks use Java, Maven, Gradle order -- not alphabetical"
print "    (alphabetically 'gradle' < 'java' < 'maven', which used to put"
print "    Gradle-related output FIRST -- a real, confirmed regression of the"
print "    same class providersByTool's own vendor ordering already had to"
print "    catch once before)"
# The order is directly observable via vendor reachability, which
# needs real network access this sandbox doesn't have -- but doctor's
# OTHER tool-iterating checks (dangling registrations etc.) use the
# exact same sortedTools() function, so this confirms the order
# without needing network: create dangling registrations for gradle
# AND java, and confirm java's issue is reported before gradle's.
mkdir -p "$TEST_HOME/.sdkkeeper/candidates/gradle"
ln -s "/nonexistent-gradle-target" "$TEST_HOME/.sdkkeeper/candidates/gradle/gradle-9.0.0" 2>/dev/null
mkdir -p "$TEST_HOME/.sdkkeeper/candidates/java"
ln -s "/nonexistent-java-target" "$TEST_HOME/.sdkkeeper/candidates/java/JDK-dangling-test" 2>/dev/null
doctor_out=$(sk doctor 2>&1)
print -r -- "$doctor_out" | head -6
java_idx=$(print -r -- "$doctor_out" | grep -n "JDK dangling-test" | head -1 | cut -d: -f1)
gradle_idx=$(print -r -- "$doctor_out" | grep -n "Gradle 9.0.0" | head -1 | cut -d: -f1)
assert_true "$([[ -n "$java_idx" && -n "$gradle_idx" ]] && echo true || echo false)" \
    "both dangling registrations are detected"
assert_true "$([[ "$java_idx" -lt "$gradle_idx" ]] && echo true || echo false)" \
    "Java's issue is reported BEFORE Gradle's (Java, Maven, Gradle order, not alphabetical)"
rm -f "$TEST_HOME/.sdkkeeper/candidates/gradle/gradle-9.0.0" "$TEST_HOME/.sdkkeeper/candidates/java/JDK-dangling-test"

# ════════════════════════════════════════════════════════════════
section "Summary"
# ════════════════════════════════════════════════════════════════
print ""
print "  $CHECKS checks run, $FAILURES failed."
print ""
if [[ "$FAILURES" -gt 0 ]]; then
    print "  ✗ FAIL -- see the FAIL lines above for exactly what diverged."
    exit 1
else
    print "  ✓ ALL CHECKS PASSED"
    exit 0
fi
