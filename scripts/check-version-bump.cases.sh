#!/usr/bin/env bash
# shellcheck shell=bash
#
# Shared test cases for scripts/check-version-bump.sh (ut-docs#5143; the
# guard is ut-docs#1940, its shared body ut-docs#4489).
#
# This file is SOURCED by a plugin repo's scripts/check-version-bump.test.sh
# after that test has `cd`-ed to the repo root and defined:
#   VB_VARIANT       "assets" (bundled files are tracked) or "compiled" (a
#                    gitignored bin/ built from src/, go.mod, go.sum and
#                    scripts/build.sh -- the fixture must carry all four).
#   VB_SHIPPED_FILE  a path, other than manifest.json/README.md, that the
#                    fixture writes and SHIPPED_PATTERNS covers.
#   vb_fixture       a function writing the repo's minimal layout into
#                    "$case_dir": manifest.json with a plain X.Y.Z version,
#                    README.md, LICENSE, VB_SHIPPED_FILE (+ the compiled
#                    inputs). scripts/, .github/workflows/ (with ci.yml) and
#                    docs/code-reviews/ already exist, and the real wrapper
#                    and body are already copied in.
# The test then calls vb_run_shared_cases, runs its own per-repo cases (the
# entries mirror against its package.sh/build.sh), and ends with vb_finish.
# Sourcing sets FAILS, REAL_SCRIPT/REAL_BODY and an EXIT trap that removes
# the fixture dir: it replaces any EXIT trap the test set before sourcing.
#
# The self-check proves this copy matches its own hash line, so a casual
# edit fails the repo's CI. Only sync-version-bump-body.sh --check (and a
# local cycle-start) compares a copy with the canonical file itself.
#
# It is byte-identical in every ut-plugin-* repo. The canonical copy lives in
# ut-docs at scripts/templates/check-version-bump.cases.sh -- NEVER edit a
# plugin's copy: edit the canonical one, then run
#   scripts/templates/sync-version-bump-body.sh --rehash
#   scripts/templates/sync-version-bump-body.sh <plugin-repo-dir>...
# The self-check below fails the test on any hand edit.
#
# cases-sha256 is the sha256 of this file with the cases-sha256 line removed.
# cases-sha256: ccbf119c014526fbb4d5c61b363d89d409765a701ec0d2ddf843a85d643026c8

_vbc_hash_of() {
    if command -v sha256sum >/dev/null 2>&1; then
        grep -v '^# cases-sha256: ' "$1" | sha256sum | cut -d' ' -f1
    else
        grep -v '^# cases-sha256: ' "$1" | shasum -a 256 | cut -d' ' -f1
    fi
}
_vbc_recorded=$(sed -n 's/^# cases-sha256: //p' "${BASH_SOURCE[0]}" | head -n 1)
if [ "$(_vbc_hash_of "${BASH_SOURCE[0]}")" != "$_vbc_recorded" ]; then
    echo "FAIL: scripts/check-version-bump.cases.sh differs from the canonical copy (ut-docs scripts/templates/check-version-bump.cases.sh) -- edit that copy and run its sync script, never this file (ut-docs#5143)" >&2
    exit 1
fi
unset -f _vbc_hash_of
unset _vbc_recorded

VB_CASES_FILE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/$(basename "${BASH_SOURCE[0]}")"
REAL_SCRIPT="$(pwd)/scripts/check-version-bump.sh"
REAL_BODY="$(pwd)/scripts/check-version-bump.body.sh"
FAILS=0

case "${VB_VARIANT:-}" in
    assets | compiled) ;;
    *)
        echo "FAIL: VB_VARIANT must be 'assets' or 'compiled' (got '${VB_VARIANT:-}')" >&2
        exit 1
        ;;
esac
if [ -z "${VB_SHIPPED_FILE:-}" ] || ! declare -F vb_fixture >/dev/null; then
    echo "FAIL: define VB_SHIPPED_FILE and vb_fixture before sourcing check-version-bump.cases.sh" >&2
    exit 1
fi

work_dir=""
_vb_cleanup() {
    # `|| true`-style: a trap's own status must never become the exit code.
    [ -n "$work_dir" ] && rm -rf "$work_dir"
    true
}
trap _vb_cleanup EXIT

# vb_fresh_repo: a throwaway git repo ($case_dir) with one commit holding
# the repo's fixture; sets $base_sha and $base_version. The wrapper is copied
# in, not symlinked: it `cd`s relative to its own path.
vb_fresh_repo() {
    [ -n "$work_dir" ] && rm -rf "$work_dir"
    work_dir="$(mktemp -d)"
    case_dir="${work_dir}/repo"
    mkdir -p "${case_dir}/scripts" "${case_dir}/.github/workflows" "${case_dir}/docs/code-reviews"
    cp "$REAL_SCRIPT" "${case_dir}/scripts/check-version-bump.sh"
    cp "$REAL_BODY" "${case_dir}/scripts/check-version-bump.body.sh"
    chmod +x "${case_dir}/scripts/check-version-bump.sh"
    echo "name: CI" >"${case_dir}/.github/workflows/ci.yml"
    vb_fixture
    local f
    for f in manifest.json README.md "$VB_SHIPPED_FILE"; do
        if [ ! -f "${case_dir}/${f}" ]; then
            echo "FAIL: vb_fixture did not write ${f}" >&2
            exit 1
        fi
    done
    if [ "$VB_VARIANT" = "compiled" ]; then
        for f in src/main.go go.mod go.sum scripts/build.sh; do
            if [ ! -f "${case_dir}/${f}" ]; then
                echo "FAIL: compiled variant: vb_fixture did not write ${f}" >&2
                exit 1
            fi
        done
    fi
    (
        cd "$case_dir"
        git init -q
        git config user.email test@example.com
        git config user.name Test
        git config commit.gpgsign false
        git add -A
        git commit -q -m "initial"
    )
    base_sha=$(cd "$case_dir" && git rev-parse HEAD)
    base_version=$(python3 -c 'import json, sys; print(json.load(open(sys.argv[1]))["version"])' "${case_dir}/manifest.json")
}

# vb_commit_change: commit whatever the caller edited; sets $head_sha.
vb_commit_change() {
    (cd "$case_dir" && git add -A && git commit -q -m "change")
    head_sha=$(cd "$case_dir" && git rev-parse HEAD)
}

# vb_set_version <version>: rewrite manifest.json's version in $case_dir.
vb_set_version() {
    python3 - "${case_dir}/manifest.json" "$1" <<'PY'
import json, sys
m = json.load(open(sys.argv[1]))
m["version"] = sys.argv[2]
json.dump(m, open(sys.argv[1], "w"))
PY
}

# vb_next_version: base_version with its patch number raised by one.
vb_next_version() {
    python3 -c 'import sys; a, b, c = sys.argv[1].split("-")[0].split("."); print(f"{a}.{b}.{int(c) + 1}")' "$base_version"
}

# vb_touch <path> <tag>: change a file's content (any file type: the guard
# only reads the diff, never parses the shipped file).
vb_touch() {
    mkdir -p "$(dirname "${case_dir}/$1")"
    echo "changed: $2" >>"${case_dir}/$1"
}

vb_run_check() {
    (cd "$case_dir" && BASE_SHA="$base_sha" HEAD_SHA="$head_sha" bash scripts/check-version-bump.sh)
}

vb_fail() {
    echo "FAIL [$1]: $2"
    FAILS=$((FAILS + 1))
}

vb_assert_pass() {
    local name="$1" out rc
    set +e
    out="$(vb_run_check 2>&1)"
    rc=$?
    set -e
    if [ "$rc" -ne 0 ]; then
        vb_fail "$name" "expected exit 0, got $rc. Output:
$out"
        return
    fi
    echo "ok   [$name]"
}

# vb_assert_fail_containing <name> <needle>...: non-zero exit AND every
# needle in the output -- a check failing for the wrong reason is no pass.
vb_assert_fail_containing() {
    local name="$1" out rc needle
    shift
    set +e
    out="$(vb_run_check 2>&1)"
    rc=$?
    set -e
    if [ "$rc" -eq 0 ]; then
        vb_fail "$name" "expected non-zero exit, got 0. Output:
$out"
        return
    fi
    for needle in "$@"; do
        if ! grep -qF -- "$needle" <<<"$out"; then
            vb_fail "$name" "exited non-zero (good) but output did not mention '$needle'. Output:
$out"
            return
        fi
    done
    echo "ok   [$name] (exit $rc, mentions: $*)"
}

vb_run_shared_cases() {
    local next mergebase_sha main_tip_sha fork first_version rc out

    # A shipped file changed without a bump fails and names the file.
    vb_fresh_repo
    vb_touch "$VB_SHIPPED_FILE" one
    vb_commit_change
    vb_assert_fail_containing "shipped file changed, no bump" \
        "$VB_SHIPPED_FILE" "manifest.json" "$base_version" "FAIL"

    vb_fresh_repo
    vb_touch "$VB_SHIPPED_FILE" one
    vb_set_version "$(vb_next_version)"
    vb_commit_change
    vb_assert_pass "shipped file changed, version bumped"

    vb_fresh_repo
    vb_touch README.md one
    vb_commit_change
    vb_assert_fail_containing "README changed, no bump" "README.md" "FAIL"

    # LICENSE ships conditionally ([ -f LICENSE ] && entries+=(LICENSE)), so
    # it needs a behavioural case of its own, not only the entries mirror.
    vb_fresh_repo
    vb_touch LICENSE one
    vb_commit_change
    vb_assert_fail_containing "LICENSE changed, no bump" "LICENSE" "FAIL"

    # The bump is itself a shipped-file change; no second file is required.
    vb_fresh_repo
    vb_set_version "$(vb_next_version)"
    vb_commit_change
    vb_assert_pass "manifest-only version bump"

    vb_fresh_repo
    echo "# review" >"${case_dir}/docs/code-reviews/2026-01-01-example.md"
    vb_commit_change
    vb_assert_pass "docs-only change, no bump required"

    vb_fresh_repo
    echo "name: CI (updated)" >"${case_dir}/.github/workflows/ci.yml"
    vb_commit_change
    vb_assert_pass "workflow-only change, no bump required"

    # manifest.json changed, version did not: still a shipped change.
    vb_fresh_repo
    python3 - "${case_dir}/manifest.json" <<'PY'
import json, sys
m = json.load(open(sys.argv[1]))
m["description"] = "updated blurb"
json.dump(m, open(sys.argv[1], "w"))
PY
    vb_commit_change
    vb_assert_fail_containing "manifest changed without version bump" "manifest.json" "FAIL"

    # main moved forward with its own shipped change + bump; this PR, forked
    # from the original base, did not bump. A two-dot diff against the moving
    # base would let main's bump cover for it (ut-docs#1940-review B1).
    vb_fresh_repo
    mergebase_sha="$base_sha"
    next="$(vb_next_version)"
    vb_touch "$VB_SHIPPED_FILE" main
    vb_set_version "$next"
    (cd "$case_dir" && git add -A && git commit -q -m "main advances, bumped")
    main_tip_sha=$(cd "$case_dir" && git rev-parse HEAD)
    (cd "$case_dir" && git checkout -q "$mergebase_sha")
    vb_touch "$VB_SHIPPED_FILE" pr
    (cd "$case_dir" && git add -A && git commit -q -m "PR change, no bump")
    head_sha=$(cd "$case_dir" && git rev-parse HEAD)
    base_sha="$main_tip_sha"
    vb_assert_fail_containing "base moved forward independently, PR itself didn't bump" \
        "$VB_SHIPPED_FILE" "FAIL"

    # A pre-release suffix must not crash past the FAIL path (review B2).
    vb_fresh_repo
    vb_set_version "${base_version}-beta.1"
    (cd "$case_dir" && git add -A && git commit -q -m "adopt pre-release version scheme")
    base_sha=$(cd "$case_dir" && git rev-parse HEAD)
    vb_touch "$VB_SHIPPED_FILE" one
    vb_commit_change
    vb_assert_fail_containing "pre-release version suffix, no bump" "FAIL"

    # A bump to a version that is already tagged never ships either (N1).
    vb_fresh_repo
    next="$(vb_next_version)"
    (cd "$case_dir" && git tag "v${next}")
    vb_touch "$VB_SHIPPED_FILE" one
    vb_set_version "$next"
    vb_commit_change
    vb_assert_fail_containing "bumped to an already-tagged version" "FAIL" "v${next}" "ALREADY TAGGED"

    # First PR of a new repo: no manifest.json at the merge-base (#4484).
    vb_fresh_repo
    (cd "$case_dir" && git rm -q manifest.json && git commit -q -m "repo start: no manifest yet")
    base_sha=$(cd "$case_dir" && git rev-parse HEAD)
    (cd "$case_dir" && git checkout -q HEAD~1 -- manifest.json)
    vb_touch "$VB_SHIPPED_FILE" first
    vb_commit_change
    vb_assert_pass "first PR: no manifest.json at the merge-base"

    # ... but the first-release path still refuses an already-tagged version.
    vb_fresh_repo
    first_version="$base_version"
    (cd "$case_dir" && git rm -q manifest.json && git commit -q -m "repo start: no manifest yet" && git tag "v${first_version}")
    base_sha=$(cd "$case_dir" && git rev-parse HEAD)
    (cd "$case_dir" && git checkout -q HEAD~1 -- manifest.json)
    vb_touch README.md first
    vb_commit_change
    vb_assert_fail_containing "first PR: version already tagged" "FAIL" "v${first_version}" "ALREADY TAGGED"

    # Present but unparseable at the merge-base fails closed: only a truly
    # ABSENT manifest is a first release.
    vb_fresh_repo
    echo '{ not json' >"${case_dir}/manifest.json"
    (cd "$case_dir" && git add -A && git commit -q -m "broken manifest")
    base_sha=$(cd "$case_dir" && git rev-parse HEAD)
    (cd "$case_dir" && git checkout -q HEAD~1 -- manifest.json)
    vb_touch README.md one
    vb_commit_change
    vb_assert_fail_containing "unparseable base manifest still fails closed" \
        "could not read manifest.json version at merge-base"

    # A stale branch forked before the first manifest must not ride the
    # first-release exemption. main's manifest is deliberately untagged, so
    # only the stale-branch check can reject it.
    vb_fresh_repo
    (cd "$case_dir" && git rm -q manifest.json && git commit -q -m "repo start: no manifest yet")
    fork=$(cd "$case_dir" && git rev-parse HEAD)
    (cd "$case_dir" && git checkout -q HEAD~1 -- manifest.json && git commit -q -m "first release")
    base_sha=$(cd "$case_dir" && git rev-parse HEAD)
    (cd "$case_dir" && git checkout -q -b stale "$fork" && git checkout -q "$base_sha" -- manifest.json)
    vb_touch README.md later
    vb_commit_change
    vb_assert_fail_containing "stale branch forked before the first manifest" "FAIL" "forked before manifest.json existed"

    # No manifest at the merge-base OR the head: nothing releases, no bump.
    vb_fresh_repo
    (cd "$case_dir" && git rm -q manifest.json && git commit -q -m "repo start: no manifest yet")
    base_sha=$(cd "$case_dir" && git rev-parse HEAD)
    vb_touch README.md early
    vb_commit_change
    vb_assert_pass "no manifest.json at the merge-base or the head"

    # scripts/package.sh is the packaging recipe: an entry or tar-flag change
    # changes the artifact with no bundled file touched (ut-docs#5147).
    vb_fresh_repo
    printf '#!/usr/bin/env bash\nentries=(manifest.json README.md)\n' >"${case_dir}/scripts/package.sh"
    (cd "$case_dir" && git add -A && git commit -q -m "package.sh at base")
    base_sha=$(cd "$case_dir" && git rev-parse HEAD)
    echo 'entries+=(docs)' >>"${case_dir}/scripts/package.sh"
    vb_commit_change
    vb_assert_fail_containing "package.sh recipe changed, no bump" "scripts/package.sh" "FAIL"

    vb_fresh_repo
    printf '#!/usr/bin/env bash\nentries=(manifest.json README.md)\n' >"${case_dir}/scripts/package.sh"
    (cd "$case_dir" && git add -A && git commit -q -m "package.sh at base")
    base_sha=$(cd "$case_dir" && git rev-parse HEAD)
    echo 'entries+=(docs)' >>"${case_dir}/scripts/package.sh"
    vb_set_version "$(vb_next_version)"
    vb_commit_change
    vb_assert_pass "package.sh recipe changed, version bumped"

    # The shared body is byte-identical to ut-docs's canonical copy (#4489).
    vb_fresh_repo
    printf '# hand edit\n' >>"${case_dir}/scripts/check-version-bump.body.sh"
    head_sha="$base_sha"
    vb_assert_fail_containing "hand-edited body fails its self-check" "differs from the canonical copy"

    # So is this file: a hand-edited copy refuses to run (ut-docs#5143).
    mkdir -p "${work_dir}/cases"
    cp "$VB_CASES_FILE" "${work_dir}/cases/check-version-bump.cases.sh"
    printf '# hand edit\n' >>"${work_dir}/cases/check-version-bump.cases.sh"
    rc=0
    out=$(bash -c '. "$1"' _ "${work_dir}/cases/check-version-bump.cases.sh" 2>&1) || rc=$?
    if [ "$rc" -ne 0 ] && grep -qF "check-version-bump.cases.sh differs from the canonical copy" <<<"$out"; then
        echo "ok   [hand-edited shared cases fail their self-check] (exit $rc)"
    else
        vb_fail "hand-edited shared cases fail their self-check" "rc=$rc, output: $out"
    fi

    [ "$VB_VARIANT" = "compiled" ] || return 0

    # Compiled variant: bin/ is gitignored and never in a diff, so the guard
    # keys off what builds it -- src/, go.mod, go.sum, scripts/build.sh.
    vb_fresh_repo
    echo '// changed' >>"${case_dir}/src/main.go"
    vb_commit_change
    vb_assert_fail_containing "src/*.go changed, no bump" "src/main.go" "FAIL"

    vb_fresh_repo
    echo '// changed' >>"${case_dir}/src/main.go"
    vb_set_version "$(vb_next_version)"
    vb_commit_change
    vb_assert_pass "src/*.go changed, version bumped"

    vb_fresh_repo
    printf '\nrequire example.com/newdep v1.0.0\n' >>"${case_dir}/go.mod"
    vb_commit_change
    vb_assert_fail_containing "go.mod changed, no bump" "go.mod" "FAIL"

    # A `go mod tidy` that rewrites go.sum alone still relinks the binary.
    vb_fresh_repo
    echo "example.com/newdep v1.0.0 h1:abc=" >>"${case_dir}/go.sum"
    vb_commit_change
    vb_assert_fail_containing "go.sum changed, no bump" "go.sum" "FAIL"

    # src/* crosses into subdirectories.
    vb_fresh_repo
    mkdir -p "${case_dir}/src/vbnested"
    echo 'package vbnested' >"${case_dir}/src/vbnested/vbnested.go"
    vb_commit_change
    vb_assert_fail_containing "nested src/ file added, no bump" "src/vbnested/vbnested.go" "FAIL"

    # The build recipe is an input to the binary (-trimpath, -ldflags, ...).
    vb_fresh_repo
    echo '# rebuilt with -trimpath from here on' >>"${case_dir}/scripts/build.sh"
    vb_commit_change
    vb_assert_fail_containing "build.sh recipe changed, no bump" "scripts/build.sh" "FAIL"

    # A file renamed OUT of src/ really leaves the package; the guard diffs
    # with --no-renames so the src/ side shows up as a delete.
    vb_fresh_repo
    mkdir -p "${case_dir}/src/vbnested"
    echo 'package vbnested' >"${case_dir}/src/vbnested/vbnested.go"
    (cd "$case_dir" && git add -A && git commit -q -m "add a nested source file")
    base_sha=$(cd "$case_dir" && git rev-parse HEAD)
    (cd "$case_dir" && git mv src/vbnested/vbnested.go docs/vbnested.go)
    vb_commit_change
    vb_assert_fail_containing "source file renamed out of src/, no bump" \
        "src/vbnested/vbnested.go" "FAIL"
}

vb_finish() {
    if [ "$FAILS" -ne 0 ]; then
        echo ""
        echo "$FAILS case(s) failed."
        exit 1
    fi
    echo ""
    echo "All check-version-bump.sh cases passed."
}
