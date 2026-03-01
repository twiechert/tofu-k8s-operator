# OpenTofu K8s Operator Demo
#
# Prerequisites:
#   - kind, helm, docker, kubectl, VHS installed
#   - Run from repo root: vhs demo/demo.tape
#
# This tape handles everything: builds the image, creates a kind cluster,
# deploys the operator, runs the demo, and tears down the cluster.

Output demo/demo.gif

Require kind
Require helm
Require docker
Require kubectl

Set Shell "bash"
Set FontSize 16
Set Width 1200
Set Height 700
Set Padding 20
Set Framerate 10
Set TypingSpeed 40ms
Set Theme "Catppuccin Mocha"
Set CursorBlink false

# ── Setup (hidden): build image, create cluster, deploy operator ──────
Hide
Type "kind delete cluster --name demo 2>/dev/null; true"
Enter
Sleep 10s
Type "docker build -t tofu-k8s-operator:demo . 2>&1 | tail -1"
Enter
Sleep 120s
Type "kind create cluster --name demo --wait 60s 2>&1 | tail -1"
Enter
Sleep 60s
Type "kind load docker-image tofu-k8s-operator:demo --name demo 2>&1 | tail -1"
Enter
Sleep 30s
Type "helm install tofu-k8s-operator ./charts/tofu-k8s-operator --set image.repository=tofu-k8s-operator --set image.tag=demo --set image.pullPolicy=Never --wait --timeout 120s 2>&1 | tail -1"
Enter
Sleep 130s
Type "go build -o /tmp/kubectl-tofu ./cmd/kubectl-tofu/"
Enter
Sleep 30s
Type "export PATH=/tmp:$PATH"
Enter
Sleep 2s
Type "clear"
Enter
Sleep 2s
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
Type "bash demo/wait-phase.sh demo WaitingApproval 180 && clear"
Enter
Sleep 180s
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
Type "bash demo/wait-phase.sh demo Succeeded 180 && clear"
Enter
Sleep 180s
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
Type "bash demo/wait-phase.sh demo DriftChecking 90 || true; bash demo/wait-phase.sh demo Succeeded 180 && clear"
Enter
Sleep 180s
Show

# Show drift check result
Type "kubectl get tofuproject demo -o jsonpath='phase={.status.phase} drift={.status.driftDetected}'"
Enter
Sleep 4s

Type "echo '  -> No drift detected'"
Enter
Sleep 4s

# ── Teardown (hidden): delete cluster ─────────────────────────────────
Hide
Type "kind delete cluster --name demo 2>/dev/null; true"
Enter
Sleep 15s
Show

Type "echo 'Done!'"
Enter
Sleep 3s
