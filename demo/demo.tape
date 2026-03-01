# OpenTofu K8s Operator Demo
#
# Prerequisites:
#   1. kind cluster with operator deployed (helm install)
#   2. kubectl-tofu plugin in PATH (go build -o /usr/local/bin/kubectl-tofu ./cmd/kubectl-tofu/)
#   3. VHS installed (brew install vhs)
#
# Generate: vhs demo/demo.tape

Output demo/demo.gif

Require kubectl
Require kubectl-tofu

Set Shell "bash"
Set FontSize 16
Set Width 1200
Set Height 700
Set Padding 20
Set Framerate 10
Set TypingSpeed 40ms
Set Theme "Catppuccin Mocha"
Set CursorBlink false

# ── Cleanup previous run ──────────────────────────────────────────────
Hide
Type "kubectl delete tofuproject demo --ignore-not-found 2>/dev/null; kubectl delete tofuprogram demo-program --ignore-not-found 2>/dev/null; sleep 5; clear"
Enter
Sleep 12s
Show

# ══════════════════════════════════════════════════════════════════════
#  ACT 1 — Plan & Approve
# ══════════════════════════════════════════════════════════════════════

Type "echo '=== Plan & Approve Workflow ==='"
Enter
Sleep 2s

# Create the TofuProgram
Type "cat demo/program.yaml"
Enter
Sleep 4s

Type "kubectl apply -f demo/program.yaml"
Enter
Sleep 3s

# Create the TofuProject (autoApprove: false)
Type "cat demo/project.yaml"
Enter
Sleep 4s

Type "kubectl apply -f demo/project.yaml"
Enter
Sleep 3s

# Show initial status
Type "kubectl get tofuproject demo -o jsonpath='{.status.phase}'"
Enter
Sleep 3s

# Wait for plan to finish (hidden)
Hide
Type "bash demo/wait-phase.sh demo WaitingApproval 120 && clear"
Enter
Sleep 120s
Show

# Show WaitingApproval status
Type "kubectl get tofuproject demo -o jsonpath='{.status.phase}'"
Enter
Sleep 3s

# Show plan output via kubectl plugin
Type "kubectl tofu plan demo"
Enter
Sleep 6s

# Approve the plan
Type "kubectl tofu approve demo"
Enter
Sleep 3s

# Wait for apply to finish (hidden)
Hide
Type "bash demo/wait-phase.sh demo Succeeded 120 && clear"
Enter
Sleep 120s
Show

# Show Succeeded status
Type "kubectl get tofuproject demo -o jsonpath='{.status.phase}'"
Enter
Sleep 3s

# Show outputs
Type "kubectl get tofuproject demo -o jsonpath='{.status.outputs}'"
Enter
Sleep 5s

# ══════════════════════════════════════════════════════════════════════
#  ACT 2 — Drift Detection
# ══════════════════════════════════════════════════════════════════════

Type ""
Enter
Type "echo '=== Drift Detection ==='"
Enter
Sleep 2s

# Enable drift detection with a short interval
Type @"kubectl patch tofuproject demo --type=merge -p '{"spec":{"driftDetection":{"enabled":true,"interval":"30s"}}}'"
Enter
Sleep 3s

# Wait for drift check to complete (hidden)
Hide
Type "bash demo/wait-phase.sh demo DriftChecking 60 || true; bash demo/wait-phase.sh demo Succeeded 120 && clear"
Enter
Sleep 120s
Show

# Show drift check result
Type "kubectl get tofuproject demo -o jsonpath='phase={.status.phase} drift={.status.driftDetected}'"
Enter
Sleep 4s

Type "echo '  -> No drift detected'"
Enter
Sleep 4s

# ── Cleanup ───────────────────────────────────────────────────────────

Type ""
Enter
Type "kubectl delete tofuproject demo"
Enter
Sleep 5s

Type "echo 'Done!'"
Enter
Sleep 3s
