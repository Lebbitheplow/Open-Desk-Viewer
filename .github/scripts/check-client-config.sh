#!/usr/bin/env bash
#
# Guard the client build's deployment identity.
#
# Two modes, and the second is the one that matters:
#
#   check-client-config.sh              the inputs are present and sane
#   check-client-config.sh --verify DIR the values are in the built artifacts
#
# The history here is worth keeping, because the previous version of this script
# is a good example of a guard that guards nothing. It required RS_PUB_KEY,
# RENDEZVOUS_SERVERS, API_SERVER and APP_NAME to be set, and nothing anywhere in
# the tree read any of them: libs/hbb_common hardcodes RENDEZVOUS_SERVERS to
# rs-ny.rustdesk.com and RS_PUB_KEY to RustDesk's own key, so every APK this
# project ever produced pointed at RustDesk's infrastructure while CI reported
# the configuration check as passing. It also ran in a workflow that builds no
# APK, and per item 5.8 no workflow in this repository had ever run at all.
#
# So this script now checks the names the compiler actually reads -- the ODV_*
# variables that src/common.rs takes through option_env! -- and, given a build
# output directory, greps the artifacts for the values to confirm they were
# baked in rather than merely exported.

set -euo pipefail

# Run from the repository root regardless of where the script was invoked. This
# used to be a hardcoded absolute path, which failed on every runner and, with
# `set -e`, took the rest of the job down with it.
cd "$(git -C "$(dirname "${BASH_SOURCE[0]}")" rev-parse --show-toplevel)"

fail() {
    echo "ERROR: $*" >&2
    exit 1
}

# ---------------------------------------------------------------------------
# Mode 2: the values took effect
# ---------------------------------------------------------------------------
if [[ "${1:-}" == "--verify" ]]; then
    artifact_dir="${2:-}"
    [[ -n "$artifact_dir" ]] || fail "--verify needs a directory of build artifacts"
    [[ -d "$artifact_dir" ]] || fail "$artifact_dir is not a directory"
    [[ -n "${ODV_API_SERVER:-}" ]] || fail "ODV_API_SERVER is not set, so there is nothing to verify"

    echo "=== Verifying the deployment lock reached the artifacts ==="

    # The Rust half of an APK is a shared object inside the zip, so the strings
    # are not visible in the APK itself without unpacking it. A plain binary is
    # read directly.
    #
    # Note the deliberate absence of `grep -q`. Under `set -o pipefail`, a grep
    # that exits on the first match closes the pipe, `strings` dies of SIGPIPE,
    # and the pipeline reports failure -- so a artifact that *does* carry the
    # value is reported as one that does not, and only sometimes, depending on
    # whether the match came early enough for grep to exit first. That is how
    # this check failed on a 217 MB binary that contained the string twice.
    # Discarding grep's output instead makes it drain the input and return an
    # honest status.
    contains() { grep -F -- "$ODV_API_SERVER" >/dev/null; }

    found=0
    while IFS= read -r -d '' artifact; do
        case "$artifact" in
            *.apk|*.aab)
                if unzip -p "$artifact" 'lib/*/*.so' 2>/dev/null \
                    | strings -a \
                    | contains; then
                    echo "  ✓ $(basename "$artifact") carries ODV_API_SERVER"
                    found=$((found + 1))
                else
                    fail "$(basename "$artifact") does not contain ODV_API_SERVER. The build did not bake in the deployment identity, so this client would fall back to RustDesk's own servers."
                fi
                ;;
            *)
                if strings -a "$artifact" | contains; then
                    echo "  ✓ $(basename "$artifact") carries ODV_API_SERVER"
                    found=$((found + 1))
                else
                    fail "$(basename "$artifact") does not contain ODV_API_SERVER."
                fi
                ;;
        esac
    done < <(find "$artifact_dir" -type f \( -name '*.apk' -o -name '*.aab' -o -name 'rustdesk' -o -name 'opendeskviewer' \) -print0)

    # An empty artifact directory would otherwise pass this check while proving
    # nothing, which is the exact failure mode the old script had.
    (( found > 0 )) || fail "found no artifacts to verify under $artifact_dir"

    echo "=== $found artifact(s) carry the deployment identity ==="
    exit 0
fi

# ---------------------------------------------------------------------------
# Mode 1: the inputs are present and sane
# ---------------------------------------------------------------------------
echo "=== CI Guard: Client Configuration Check ==="

# hbb_common must stay at upstream. Not a style rule: the deployment lock in
# src/common.rs was written the way it was specifically so that no submodule
# patch is needed, and a patched submodule makes every upstream merge expensive.
submodule_status=$(git submodule status libs/hbb_common | cut -c1)
if [[ "$submodule_status" == "-" || "$submodule_status" == "+" ]]; then
    echo "ERROR: hbb_common has local modifications." >&2
    echo "The deployment identity is baked in from src/common.rs via the ODV_* variables;" >&2
    echo "patching hbb_common is neither necessary nor supported." >&2
    exit 1
fi
echo "✓ hbb_common is at upstream (no local patches)"

missing=0
require() {
    local name="$1"
    if [[ -z "${!name:-}" ]]; then
        echo "ERROR: $name is not set" >&2
        missing=$((missing + 1))
    else
        echo "✓ $name is set"
    fi
}

# These four names are the ones src/common.rs reads with option_env!. A build
# missing any of them produces a client with that key unlocked, which behaves
# exactly like upstream: it can be re-pointed by a pushed strategy, a boot
# argument, or a hand-edited config file.
require ODV_RENDEZVOUS_SERVER
require ODV_RELAY_SERVER
require ODV_API_SERVER
require ODV_RS_PUB_KEY

if [[ -n "${ODV_RENDEZVOUS_SERVER:-}" ]] && grep -q 'rustdesk\.com' <<<"$ODV_RENDEZVOUS_SERVER"; then
    echo "ERROR: ODV_RENDEZVOUS_SERVER points at rustdesk.com" >&2
    missing=$((missing + 1))
fi

# The api-server is the channel that can reprogram a client (item 1.3), so a
# plaintext one is worth failing a release build over even though the client
# itself accepts it for a lab deployment.
if [[ -n "${ODV_API_SERVER:-}" ]] && [[ "$ODV_API_SERVER" != https://* ]]; then
    echo "ERROR: ODV_API_SERVER must be https. The heartbeat response carries configuration the client applies, so a plaintext api-server lets an on-path attacker re-point the fleet." >&2
    missing=$((missing + 1))
fi

if (( missing > 0 )); then
    echo "" >&2
    echo "$missing problem(s) with the client build configuration." >&2
    echo "Set ODV_RENDEZVOUS_SERVER, ODV_RELAY_SERVER, ODV_API_SERVER and ODV_RS_PUB_KEY" >&2
    echo "in the build environment. They are read at compile time by src/common.rs." >&2
    exit 1
fi

echo ""
echo "=== Inputs are present. Run with --verify <dir> after the build to confirm they took effect. ==="
