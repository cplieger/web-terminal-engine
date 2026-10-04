#!/usr/bin/env bash
# Fetch the esctest2 VT conformance suite (GPL-2.0) into a gitignored checkout
# and run it against the engine's VT as a PTY subprocess. It is never vendored
# or linked: MPL-2.0 §3.3 lets MPL code enter a GPL work, not the reverse.
# Usage: bash scripts/esctest.sh [-v]; ESCTEST_INCLUDE=<class> narrows the run,
# ESCTEST_OPTIONS=none drops the default --options xtermWinopsEnabled. The gate
# passes when the FAIL set matches internal/esctest/known_failures.txt; reseed it
# from the FAIL lines of an ESCTEST_LOGCOPY run.
set -eu

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
dest="${ESCTEST2_DIR:-${repo_root}/.esctest2}"
# Pinned for a reproducible gate; bump deliberately when adopting newer tests.
pin="664be3c"
url="https://github.com/ThomasDickey/esctest2.git"

if ! command -v python3 >/dev/null 2>&1; then
  echo "esctest: python3 is required to run the suite" >&2
  exit 1
fi

if [ ! -d "${dest}/.git" ]; then
  echo "esctest: cloning ${url} -> ${dest}"
  git clone "${url}" "${dest}"
fi
git -C "${dest}" fetch --quiet origin
git -C "${dest}" checkout --quiet "${pin}"

echo "esctest: running conformance gate against ${dest}"
cd "${repo_root}"
ESCTEST2_DIR="${dest}" go test ./internal/esctest/ -run Conformance -timeout 1200s "$@"
