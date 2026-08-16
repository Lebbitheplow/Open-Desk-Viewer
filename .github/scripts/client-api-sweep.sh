#!/usr/bin/env bash
#
# Regenerate or verify the list of API paths the RustDesk client actually calls.
#
# The client's API surface is a closed set, derived by sweeping the sources
# rather than from memory. Diffing that sweep against a checked-in baseline
# turns "we think we have implemented everything" into "we are told when we
# stop having done so", which is what an upstream RustDesk merge would
# otherwise hide.
#
#   client-api-sweep.sh            print the current surface
#   client-api-sweep.sh --check    fail if it differs from the baseline
#   client-api-sweep.sh --write    update the baseline
#
# Two cautions, learned by running it:
#
#   - The Rust sweep also catches MSDN documentation URLs in comments
#     (/api/winuser/..., /api/dxgiformat/... and so on). Those are filtered
#     below; they are not endpoints.
#   - Some paths are assembled rather than written literally. sync.rs builds
#     sysinfo by url.replace("heartbeat", "sysinfo"), and common.rs builds
#     /api/audit/{typ}. A grep for the literal string finds neither, which is
#     exactly how PUT /api/audit and the audit guid were missed by earlier
#     reviews. Those are listed in the baseline explicitly.

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# The baseline lives under .github/ rather than docs/. docs/ is upstream
# RustDesk's own tree (code of conduct, contributing, README translations), and
# a fork-specific CI artifact placed there is one more thing to reconcile on
# every upstream merge.
baseline="${repo_root}/.github/client-api-surface.txt"

# Paths that are constructed at runtime and so cannot be found by grepping for
# the literal. Each names the source that builds it.
declare -a assembled=(
  "/api/audit/conn"     # src/common.rs builds /api/audit/{typ}
  "/api/audit/file"     # src/common.rs builds /api/audit/{typ}
  "/api/sysinfo"        # src/hbbs_http/sync.rs replaces "heartbeat" with "sysinfo"
  "/api/sysinfo_ver"    # src/hbbs_http/sync.rs, same construction
)

sweep() {
  {
    if [[ -d "${repo_root}/flutter/lib" ]]; then
      grep -rho '/api/[a-zA-Z0-9_/{}$.-]*' "${repo_root}/flutter/lib/" --include=*.dart 2>/dev/null \
        | sed 's/\${[a-zA-Z._]*}/{id}/g' || true
    fi

    if [[ -d "${repo_root}/src" ]]; then
      grep -rho '/api/[a-zA-Z0-9_/-]*' "${repo_root}/src/" --include=*.rs 2>/dev/null || true
      # plugin-sign lives under /lic/web/api/, not /api/. An allowlist that
      # assumes a single /api prefix misses it.
      grep -rho '/lic/web/api/[a-zA-Z0-9_/-]*' "${repo_root}/src/" --include=*.rs 2>/dev/null || true
    fi

    printf '%s\n' "${assembled[@]}"
  } \
    | sed 's#/$##' \
    | filter_non_endpoints \
    | LC_ALL=C sort -u
    # LC_ALL=C is load-bearing. A UTF-8 locale collates punctuation as though it
    # were absent, so /api/ab/peer/{id} and /api/ab/peers swap places between a
    # developer machine (en_US.UTF-8) and the runner. The sweep then reports an
    # identical set as drift, and --check fails on every pull request.
}

# filter_non_endpoints drops the matches that are documentation URLs rather than
# endpoints this deployment has to answer.
filter_non_endpoints() {
  # MSDN reference URLs in Rust comments all take the form
  # /api/<header>/<nf|nn|ns|ne|nc|nl>-<symbol>, for example
  # /api/aclapi/nf-aclapi-setentriesinaclw. No RustDesk endpoint looks like
  # that, so the shape is a reliable discriminator.
  grep -vE '^/api/[a-z0-9_]+/(nf|nn|ns|ne|nc|nl|nz)-' \
    | grep -vE '^/api/lib/' `# Flutter API docs links` \
    | grep -vE '^/api$' \
    | grep -vE '^/api/plugin-sign$' `# substring of /lic/web/api/plugin-sign`
}

case "${1:---print}" in
  --check)
    if [[ ! -f "${baseline}" ]]; then
      echo "baseline ${baseline} does not exist; run client-api-sweep.sh --write" >&2
      exit 1
    fi
    if ! diff -u "${baseline}" <(sweep); then
      cat >&2 <<'MESSAGE'

The client's API surface has changed.

An upstream merge has added or removed a path the RustDesk client calls. Decide
what the new path should do, implement it or refuse it deliberately, then run:

    .github/scripts/client-api-sweep.sh --write

and commit the updated .github/client-api-surface.txt.
MESSAGE
      exit 1
    fi
    echo "client API surface matches the baseline"
    ;;
  --write)
    mkdir -p "$(dirname "${baseline}")"
    sweep > "${baseline}"
    echo "wrote ${baseline}"
    ;;
  *)
    sweep
    ;;
esac
