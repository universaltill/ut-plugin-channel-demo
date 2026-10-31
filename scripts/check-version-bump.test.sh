#!/usr/bin/env bash
# Tests for scripts/check-version-bump.sh (ut-docs#1940, ut-docs#1948; the
# compiled-plugin variant ported from ut-plugin-tax-de for ut-docs#4032).
#
# The cases every plugin repo shares live in
# scripts/check-version-bump.cases.sh, byte-identical in every ut-plugin-*
# repo (canonical copy: ut-docs scripts/templates/check-version-bump.cases.sh,
# ut-docs#5143) -- never edit it here. This file keeps only what is this
# repo's own: the fixture layout (vb_fixture) and the entries mirror against
# this repo's package.sh and build.sh.
#
# The fixture mirrors the COMPILED-plugin shape (WASI module built from src/
# via go build): bin/ is gitignored and undiffable, so the guard keys off
# src/**, go.mod, go.sum and scripts/build.sh instead of the bundled path
# (see check-version-bump.sh's own header).
set -euo pipefail
cd "$(dirname "$0")/.."

PACKAGE_SH="$(pwd)/scripts/package.sh"
BUILD_SH="$(pwd)/scripts/build.sh"
# Repo-relative form of BUILD_SH -- the build recipe is itself an input to
# the compiled artifact, so SHIPPED_PATTERNS has to cover this exact path
# (see the gitignored-entry branch of the "entries mirror" case below).
BUILD_SH_REL="scripts/build.sh"

# shellcheck disable=SC2034 # read by the sourced cases file
VB_VARIANT=compiled
# shellcheck disable=SC2034
VB_SHIPPED_FILE=locales/de.json

# vb_fixture: this repo's minimal compiled-plugin layout, written into
# $case_dir (a .gitignore excluding bin/, as the real repo has).
vb_fixture() {
    mkdir -p "${case_dir}/src" "${case_dir}/locales"
    cat >"${case_dir}/manifest.json" <<'JSON'
{
  "id": "com.universaltill.channel-demo-fixture",
  "name": "Fixture channel plugin",
  "version": "0.1.0",
  "canonical_type": "channel",
  "runtime": "wasm",
  "entrypoint": "./bin/plugin.wasm"
}
JSON
    printf 'package main\n\nfunc main() {}\n' >"${case_dir}/src/main.go"
    printf 'module github.com/universaltill/channel-demo-fixture\n\ngo 1.23\n' >"${case_dir}/go.mod"
    echo "" >"${case_dir}/go.sum"
    echo '{"a.one": "Eins"}' >"${case_dir}/locales/de.json"
    echo "# Fixture channel plugin" >"${case_dir}/README.md"
    echo "MIT" >"${case_dir}/LICENSE"
    printf '/bin/\n/dist/\n' >"${case_dir}/.gitignore"
    cat >"${case_dir}/scripts/build.sh" <<'BUILD'
#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
GOOS=wasip1 GOARCH=wasm go build -o bin/plugin.wasm ./src
BUILD
    cat >"${case_dir}/scripts/package.sh" <<'PACKAGE'
#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
entries=(manifest.json README.md bin)
[ -f LICENSE ] && entries+=(LICENSE)
[ -d locales ] && entries+=(locales)
tar -czf dist/out.tar.gz "${entries[@]}"
PACKAGE
}

# shellcheck source=scripts/check-version-bump.cases.sh
. scripts/check-version-bump.cases.sh

vb_run_shared_cases

# --- per-repo case: SHIPPED_PATTERNS mirrors package.sh's own bundle entries,
# generalized for a COMPILED plugin -----------------------------------------
# package.sh's `entries=(...)` line is the actual source of truth for what
# ships. For each entry, if it is a path git would actually track (checked
# with `git check-ignore` against THIS FIXTURE's own .gitignore -- the same
# check the real repo's tree would give), require it (or "<entry>/*") in
# SHIPPED_PATTERNS as before. But `bin` is gitignored -- it is build.sh's
# OUTPUT, not a tracked file, and can never appear in `git diff --name-only`
# -- so for a gitignored entry this instead requires that build.sh's own
# `go build ... <SOURCE_DIR>` argument (with a leading "./" stripped) has a
# matching "<source_dir>/*" pattern in SHIPPED_PATTERNS, AND that go.mod,
# go.sum and the build script itself are all covered too (a dependency bump
# or a build-flag change alters the compiled binary without touching
# anything under src/ at all). This is what a real
# change to package.sh's bundle, build.sh's source directory, or
# check-version-bump.sh's SHIPPED_PATTERNS would be caught by drifting from
# each other.
entries_line=$(grep -m1 '^entries=' "$PACKAGE_SH") || entries_line=""
if [ -z "$entries_line" ]; then
    echo "FAIL [entries mirror]: could not find package.sh's entries=(...) line"
    FAILS=$((FAILS + 1))
else
    array_body="${entries_line#entries=(}"
    array_body="${array_body%)}"
    read -r -a bundle_entries <<<"$array_body"
    # Scoped to the SHIPPED_PATTERNS=(...) array body ONLY, with comments
    # stripped. Scanning the whole script for any single-quoted token (this
    # check's original form) let a quoted mention in a COMMENT stand in for a
    # real array entry -- and check-version-bump.sh is a deliberately
    # comment-heavy script. Proved by mutation in this guard's 2026-09-10
    # review: replacing the `'go.sum'` array line with a comment containing
    # `'go.sum'` left this case green, and since LICENSE and go.sum had no
    # behavioural case of their own at the time, the ENTIRE suite stayed green
    # while the guard silently stopped covering them. `# ...` is stripped
    # before the quoted tokens are read (no SHIPPED_PATTERNS entry contains a
    # '#'), so neither a comment outside the array nor one inside it can
    # masquerade as an entry.
    patterns=$(awk '/^SHIPPED_PATTERNS=\(/ {inside = 1; next}
                    inside && /^\)/ {inside = 0}
                    inside' "$REAL_SCRIPT" | sed 's/#.*//' | grep -oE "'[^']*'" | tr -d "'")

    build_line=$(grep -m1 'go build ' "$BUILD_SH") || build_line=""
    if [ -z "$build_line" ]; then
        echo "FAIL [entries mirror]: could not find build.sh's 'go build' line"
        FAILS=$((FAILS + 1))
        build_line=""
    fi
    # Last whitespace-separated token on the go build line is its source
    # package argument (e.g. "./src"); strip a leading "./".
    source_dir=""
    if [ -n "$build_line" ]; then
        source_dir="${build_line##* }"
        source_dir="${source_dir#./}"
    fi

    # A throwaway repo purely to evaluate `git check-ignore` against this
    # fixture's own .gitignore.
    vb_fresh_repo
    mismatch=0
    for entry in "${bundle_entries[@]}"; do
        # Try both the bare name and a trailing-slash form: a directory-only
        # gitignore pattern (e.g. "/bin/") only matches git check-ignore's
        # bare-name query once that path exists on disk as a directory --
        # which a bundled-but-not-yet-built entry like "bin" need not, in a
        # fresh checkout that has never run scripts/build.sh.
        if (cd "$case_dir" && { git check-ignore -q "$entry" 2>/dev/null || git check-ignore -q "$entry/" 2>/dev/null; }); then
            # Gitignored (compiled-artifact) entry: require the source dir
            # that PRODUCES it, plus go.mod/go.sum, instead of the literal
            # (undiffable) entry name.
            if [ -z "$source_dir" ]; then
                echo "FAIL [entries mirror]: '$entry' is gitignored (a compiled artifact) but build.sh's source directory could not be determined"
                mismatch=1
                continue
            fi
            if ! grep -qxF "${source_dir}/*" <<<"$patterns"; then
                echo "FAIL [entries mirror]: package.sh bundles gitignored '$entry' (built from '$source_dir' per build.sh) but SHIPPED_PATTERNS has no '${source_dir}/*' entry"
                mismatch=1
            fi
            if ! grep -qxF "go.mod" <<<"$patterns"; then
                echo "FAIL [entries mirror]: '$entry' is a compiled artifact but SHIPPED_PATTERNS has no 'go.mod' entry (a dependency bump changes the binary without touching $source_dir)"
                mismatch=1
            fi
            if ! grep -qxF "go.sum" <<<"$patterns"; then
                echo "FAIL [entries mirror]: '$entry' is a compiled artifact but SHIPPED_PATTERNS has no 'go.sum' entry (a dependency bump changes the binary without touching $source_dir)"
                mismatch=1
            fi
            # The build RECIPE is an input to the artifact too: a new
            # -ldflags/-trimpath/-tags, a different GOOS/GOARCH or -o path
            # changes the shipped binary with every file under $source_dir
            # byte-identical.
            if ! grep -qxF "$BUILD_SH_REL" <<<"$patterns"; then
                echo "FAIL [entries mirror]: '$entry' is a compiled artifact but SHIPPED_PATTERNS has no '${BUILD_SH_REL}' entry (a build-flag change rebuilds the binary without touching $source_dir)"
                mismatch=1
            fi
        else
            # Tracked entry: same check as the asset-only plugins' guard.
            if ! grep -qxF "$entry" <<<"$patterns" && ! grep -qxF "${entry}/*" <<<"$patterns"; then
                echo "FAIL [entries mirror]: package.sh bundles '$entry' but SHIPPED_PATTERNS in check-version-bump.sh has no matching entry"
                mismatch=1
            fi
        fi
    done
    # LICENSE ships conditionally ([ -f LICENSE ] && entries+=(LICENSE)),
    # so it never appears on package.sh's own entries=(...) line -- checked
    # separately here rather than assumed.
    if ! grep -qxF "LICENSE" <<<"$patterns"; then
        echo "FAIL [entries mirror]: package.sh conditionally bundles LICENSE (when present) but SHIPPED_PATTERNS has no LICENSE entry"
        mismatch=1
    fi
    # locales/ also ships conditionally ([ -d locales ] && entries+=(locales)).
    if ! grep -qxF "locales/*" <<<"$patterns"; then
        echo "FAIL [entries mirror]: package.sh conditionally bundles locales/ (when present) but SHIPPED_PATTERNS has no 'locales/*' entry"
        mismatch=1
    fi
    if [ "$mismatch" -eq 0 ]; then
        echo "ok   [entries mirror] (package.sh: ${bundle_entries[*]} + conditional LICENSE/locales; compiled from: ${source_dir} via ${BUILD_SH_REL})"
    else
        FAILS=$((FAILS + 1))
    fi
fi

vb_finish
