#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Penguin Wings gate: build, vet and test the packages a change touches. Runs on
# the build runner (see the Penguin card for the lane) and stays under the
# runner's 15-minute job cap. Runner layout: GATE_DIR holds the persistent
# caches (/var/cache/runner/<dir>); GATE_OUT holds logs and scratch and is
# wipeable (/var/tmp/<dir>; defaults to $GATE_DIR/out). Set GATE_PKGS to the
# touched packages (e.g. "./environment/lxc/... ./router/..."); the default is
# Penguin's own packages plus the upstream ones it branches in. Logs each step
# to $GATE_OUT/logs and carries on past a failure; exits non-zero if any step
# failed.
set -uo pipefail
# Whole job capped at 15 minutes.
if [[ -z "${GATE_CAPPED:-}" ]]; then exec env GATE_CAPPED=1 timeout 900 "$0" "$@"; fi
GATE_DIR=${GATE_DIR:?set GATE_DIR to the persistent cache directory}
GATE_OUT=${GATE_OUT:-$GATE_DIR/out}
cd "$(dirname "$0")/.."
export GOCACHE=$GATE_DIR/gocache GOMODCACHE=$GATE_DIR/gomod GOPATH=$GATE_DIR/gopath \
  GOTMPDIR=$GATE_OUT/tmp TMPDIR=$GATE_OUT/tmp GOFLAGS=-mod=mod
mkdir -p "$GATE_OUT/logs" "$GATE_OUT/tmp"
PKGS=${GATE_PKGS:-./environment/lxc/... ./router/... ./server/... ./sftp/...}
fail=0
step() {
  local name=$1; shift
  echo "== $name: start"
  if timeout "${GATE_STEP_TIMEOUT:-600}" "$@" >"$GATE_OUT/logs/$name.log" 2>&1 </dev/null; then
    echo "== $name: ok"
  else
    echo "== $name: FAIL (tail below)"; tail -40 "$GATE_OUT/logs/$name.log"; fail=1
  fi
}
# shellcheck disable=SC2086
step build go build $PKGS
step vet go vet ./environment/lxc/...
# Upstream packages carry pre-existing copylocks findings (server/resources.go,
# router/downloader, server/filesystem/quotas); vet the ones Penguin touches
# without that one analyzer so a new finding still fails the gate.
step vet-upstream go vet -copylocks=false ./server/... ./router/... ./sftp/... ./config/... ./cmd/...
if command -v golangci-lint >/dev/null; then
  step lint golangci-lint run ./environment/lxc/...
fi
# shellcheck disable=SC2086
step test go test -p "${GATE_PROCS:-2}" $PKGS
exit $fail
