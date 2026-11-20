#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Penguin Wings full gate: vet, lint on Penguin's own packages, the whole test
# suite. Runs on the build runner (see the Penguin card for the lane), with
# every cache under $GATE_DIR. Logs each step to $GATE_DIR/logs and carries on
# past a failure; exits non-zero if any step failed.
set -uo pipefail
GATE_DIR=${GATE_DIR:?set GATE_DIR to a private job directory}
cd "$(dirname "$0")/.."
export GOCACHE=$GATE_DIR/gocache GOMODCACHE=$GATE_DIR/gomod GOPATH=$GATE_DIR/gopath \
  GOTMPDIR=$GATE_DIR/tmp TMPDIR=$GATE_DIR/tmp GOFLAGS=-mod=mod
mkdir -p "$GATE_DIR/logs" "$GATE_DIR/tmp"
fail=0
step() {
  local name=$1; shift
  echo "== $name: start"
  if "$@" >"$GATE_DIR/logs/$name.log" 2>&1 </dev/null; then
    echo "== $name: ok"
  else
    echo "== $name: FAIL (tail below)"; tail -40 "$GATE_DIR/logs/$name.log"; fail=1
  fi
}
step build go build ./...
step vet go vet ./environment/lxc/...
# Upstream packages carry pre-existing copylocks findings (server/resources.go,
# router/downloader, server/filesystem/quotas); vet the ones Penguin touches
# without that one analyzer so a new finding still fails the gate.
step vet-upstream go vet -copylocks=false ./server/... ./router/... ./sftp/... ./config/... ./cmd/...
if command -v golangci-lint >/dev/null; then
  step lint golangci-lint run ./environment/lxc/...
fi
step test go test -p "${GATE_PROCS:-2}" ./...
exit $fail
