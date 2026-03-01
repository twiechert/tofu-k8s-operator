#!/usr/bin/env bash
# Wait for a TofuProject to reach a given phase.
# Usage: wait-phase.sh <name> <phase> [timeout-seconds]
set -euo pipefail

NAME=${1:?usage: wait-phase.sh <name> <phase> [timeout]}
PHASE=${2:?usage: wait-phase.sh <name> <phase> [timeout]}
TIMEOUT=${3:-120}
NS=${4:-default}

for ((i=0; i<TIMEOUT; i++)); do
  current=$(kubectl get tofuproject "$NAME" -n "$NS" -o jsonpath='{.status.phase}' 2>/dev/null || true)
  [ "$current" = "$PHASE" ] && exit 0
  sleep 1
done

echo "Timed out waiting for $NAME to reach phase $PHASE (current: $current)" >&2
exit 1
