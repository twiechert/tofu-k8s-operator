#!/usr/bin/env bash
# Pretty-print TofuProject status with colors.
# Usage: show-status.sh <name> [namespace]
set -euo pipefail

NAME=${1:?usage: show-status.sh <name> [namespace]}
NS=${2:-default}

# Colors
C='\033[1;36m'  # cyan
G='\033[1;32m'  # green
Y='\033[1;33m'  # yellow
R='\033[1;31m'  # red
B='\033[1m'     # bold
D='\033[0;37m'  # dim
N='\033[0m'     # reset

PHASE=$(kubectl get tofuproject "$NAME" -n "$NS" -o jsonpath='{.status.phase}' 2>/dev/null || true)
SYNC=$(kubectl get tofuproject "$NAME" -n "$NS" -o jsonpath='{.status.syncStatus}' 2>/dev/null || true)
OUTPUTS=$(kubectl get tofuproject "$NAME" -n "$NS" -o jsonpath='{.status.outputs}' 2>/dev/null || true)
DRIFT=$(kubectl get tofuproject "$NAME" -n "$NS" -o jsonpath='{.status.driftDetected}' 2>/dev/null || true)
MSG=$(kubectl get tofuproject "$NAME" -n "$NS" -o jsonpath='{.status.message}' 2>/dev/null || true)

case "$PHASE" in
  Succeeded)                    PC=$G ;;
  WaitingApproval|Planning)     PC=$Y ;;
  Error|DestroyFailed)          PC=$R ;;
  Running|DriftChecking)        PC=$C ;;
  *)                            PC=$D ;;
esac

echo ""
printf "  ${B}Project${N}   %s/%s\n" "$NS" "$NAME"
printf "  ${B}Phase${N}     ${PC}%s${N}\n" "${PHASE:-—}"
[ -n "$SYNC" ] && printf "  ${B}Sync${N}      %s\n" "$SYNC"
if [ -n "$DRIFT" ]; then
  if [ "$DRIFT" = "true" ]; then
    printf "  ${B}Drift${N}     ${R}detected${N}\n"
  else
    printf "  ${B}Drift${N}     ${G}none${N}\n"
  fi
else
  # driftDetected is omitempty; false isn't serialized — show "none" if drift detection is on
  DD=$(kubectl get tofuproject "$NAME" -n "$NS" -o jsonpath='{.spec.driftDetection.enabled}' 2>/dev/null || true)
  [ "$DD" = "true" ] && printf "  ${B}Drift${N}     ${G}none${N}\n"
fi
[ -n "$MSG" ] && printf "  ${B}Message${N}   %s\n" "$MSG"
[ -n "$OUTPUTS" ] && printf "  ${B}Outputs${N}   %s\n" "$OUTPUTS"
echo ""
