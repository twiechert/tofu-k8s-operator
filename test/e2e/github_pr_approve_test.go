//go:build integration
// +build integration

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

// fakeGitHubServerScript is a Python HTTP server that mimics the GitHub API
// endpoints used by the operator. It also exposes admin endpoints for test
// control (merge/close PRs).
const fakeGitHubServerScript = `
import json, re, threading
from http.server import HTTPServer, BaseHTTPRequestHandler

prs = {}
next_pr = [1]
lock = threading.Lock()

class Handler(BaseHTTPRequestHandler):
    def log_message(self, fmt, *args):
        pass  # silence logs

    def do_GET(self):
        with lock:
            # GET /repos/{owner}/{repo}
            if re.match(r'^/repos/[^/]+/[^/]+$', self.path):
                self._json(200, {"default_branch": "main"})
            # GET /repos/{owner}/{repo}/git/ref/heads/{branch}
            elif '/git/ref/heads/' in self.path:
                self._json(200, {"object": {"sha": "abc123def456"}})
            # GET /repos/{owner}/{repo}/pulls/{number}
            elif m := re.match(r'^/repos/[^/]+/[^/]+/pulls/(\d+)$', self.path):
                pr_num = int(m.group(1))
                if pr_num in prs:
                    pr = prs[pr_num]
                    self._json(200, {"state": pr["state"], "merged": pr["merged"]})
                else:
                    self._json(404, {"message": "not found"})
            # GET /admin/prs — list all PRs (for debugging)
            elif self.path == '/admin/prs':
                self._json(200, prs)
            else:
                self._json(404, {"message": "not found"})

    def do_POST(self):
        with lock:
            body = self._read_body()
            # POST /repos/{owner}/{repo}/git/refs (create branch)
            if '/git/refs' in self.path:
                self._json(201, {"ref": body.get("ref", "")})
            # POST /repos/{owner}/{repo}/pulls (create PR)
            elif re.match(r'^/repos/[^/]+/[^/]+/pulls$', self.path):
                pr_num = next_pr[0]
                next_pr[0] += 1
                prs[pr_num] = {
                    "number": pr_num,
                    "title": body.get("title", ""),
                    "head": body.get("head", ""),
                    "base": body.get("base", ""),
                    "state": "open",
                    "merged": False,
                }
                self._json(201, {
                    "number": pr_num,
                    "html_url": "http://fake-github/pull/%d" % pr_num,
                })
            # POST /repos/{owner}/{repo}/issues/{number}/comments
            elif '/issues/' in self.path and '/comments' in self.path:
                self._json(201, {"id": 1})
            # POST /admin/merge/{number}
            elif m := re.match(r'^/admin/merge/(\d+)$', self.path):
                pr_num = int(m.group(1))
                if pr_num in prs:
                    prs[pr_num]["state"] = "closed"
                    prs[pr_num]["merged"] = True
                    self._json(200, {"ok": True})
                else:
                    self._json(404, {"message": "pr not found"})
            # POST /admin/close/{number}
            elif m := re.match(r'^/admin/close/(\d+)$', self.path):
                pr_num = int(m.group(1))
                if pr_num in prs:
                    prs[pr_num]["state"] = "closed"
                    prs[pr_num]["merged"] = False
                    self._json(200, {"ok": True})
                else:
                    self._json(404, {"message": "pr not found"})
            else:
                self._json(404, {"message": "not found"})

    def do_PUT(self):
        with lock:
            # PUT /repos/{owner}/{repo}/contents/{path} (create file)
            self._json(201, {"content": {"sha": "filesha123"}})

    def do_PATCH(self):
        with lock:
            body = self._read_body()
            # PATCH /repos/{owner}/{repo}/pulls/{number} (close PR)
            if m := re.match(r'^/repos/[^/]+/[^/]+/pulls/(\d+)$', self.path):
                pr_num = int(m.group(1))
                if pr_num in prs and body.get("state") == "closed":
                    prs[pr_num]["state"] = "closed"
                self._json(200, {"state": prs.get(pr_num, {}).get("state", "open")})
            else:
                self._json(404, {"message": "not found"})

    def do_DELETE(self):
        self.send_response(204)
        self.end_headers()

    def _read_body(self):
        length = int(self.headers.get('Content-Length', 0))
        if length > 0:
            return json.loads(self.rfile.read(length))
        return {}

    def _json(self, code, data):
        self.send_response(code)
        self.send_header('Content-Type', 'application/json')
        self.end_headers()
        self.wfile.write(json.dumps(data).encode())

print("Starting fake GitHub API on :8080", flush=True)
HTTPServer(("0.0.0.0", 8080), Handler).serve_forever()
`

const fakeGitHubConfigMapYAML = `
apiVersion: v1
kind: ConfigMap
metadata:
  name: fake-github-script
  namespace: default
data:
  server.py: |
` // script is appended dynamically

const fakeGitHubPodYAML = `
apiVersion: v1
kind: Pod
metadata:
  name: fake-github
  namespace: default
  labels:
    app: fake-github
spec:
  containers:
    - name: server
      image: python:3-alpine
      command: ["python3", "/scripts/server.py"]
      ports:
        - containerPort: 8080
      volumeMounts:
        - name: scripts
          mountPath: /scripts
      readinessProbe:
        httpGet:
          path: /admin/prs
          port: 8080
        initialDelaySeconds: 1
        periodSeconds: 2
  volumes:
    - name: scripts
      configMap:
        name: fake-github-script
`

const fakeGitHubServiceYAML = `
apiVersion: v1
kind: Service
metadata:
  name: fake-github
  namespace: default
spec:
  selector:
    app: fake-github
  ports:
    - port: 8080
      targetPort: 8080
`

// deployFakeGitHub deploys the fake GitHub API server as a Pod+Service and waits for readiness.
// Safe to call from multiple parallel tests — uses apply which is idempotent.
func deployFakeGitHub(t *testing.T) {
	t.Helper()

	// Build the ConfigMap YAML with indented script
	var cmYAML strings.Builder
	cmYAML.WriteString("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: fake-github-script\n  namespace: default\ndata:\n  server.py: |\n")
	for _, line := range strings.Split(fakeGitHubServerScript, "\n") {
		cmYAML.WriteString("    " + line + "\n")
	}

	// Use kubectlMayFail to tolerate already-exists from parallel tests
	applyYAMLMayFail(t, cmYAML.String())
	applyYAMLMayFail(t, fakeGitHubPodYAML)
	applyYAMLMayFail(t, fakeGitHubServiceYAML)

	// Wait for pod readiness
	deadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) {
		out, err := kubectlMayFail("get", "pod", "fake-github", "-n", "default",
			"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}")
		if err == nil && strings.TrimSpace(out) == "True" {
			t.Log("fake-github pod is ready")
			return
		}
		time.Sleep(2 * time.Second)
	}
	// Dump pod info for debugging
	out, _ := kubectlMayFail("describe", "pod", "fake-github", "-n", "default")
	t.Fatalf("fake-github pod not ready within timeout\n%s", out)
}

// cleanupFakeGitHub removes the fake GitHub API server resources.
// In parallel tests, the last test to finish cleans up. Safe to call multiple times.
func cleanupFakeGitHub(t *testing.T) {
	t.Helper()
	deleteYAML(t, fakeGitHubServiceYAML)
	deleteYAML(t, fakeGitHubPodYAML)
	kubectlMayFail("delete", "configmap", "fake-github-script", "-n", "default", "--ignore-not-found")
}

// applyYAMLMayFail applies YAML without failing the test on error (e.g. AlreadyExists from parallel tests).
func applyYAMLMayFail(t *testing.T, yaml string) {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "kubectl", "apply", "--server-side", "--force-conflicts", "-f", "-")
	cmd.Stdin = strings.NewReader(yaml)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Logf("kubectl apply (non-fatal): %v\n%s", err, out)
	}
}

// fakeGitHubAdminExec calls an admin endpoint on the fake server via kubectl exec.
func fakeGitHubAdminExec(t *testing.T, method, path string) {
	t.Helper()
	pyCmd := fmt.Sprintf(
		`import urllib.request; urllib.request.urlopen(urllib.request.Request("http://localhost:8080%s", method="%s"))`,
		path, method,
	)
	out, err := kubectlMayFail("exec", "fake-github", "-n", "default", "--",
		"python3", "-c", pyCmd)
	if err != nil {
		t.Fatalf("admin exec %s %s failed: %v\n%s", method, path, err, out)
	}
}

func TestGitHubPRApproveFlow(t *testing.T) {
	dynClient := newDynamicClient(t)

	deployFakeGitHub(t)
	defer cleanupFakeGitHub(t)

	// Create a Secret with the fake GitHub token
	tokenSecret := `
apiVersion: v1
kind: Secret
metadata:
  name: gh-token-test
  namespace: default
type: Opaque
stringData:
  token: fake-token
`
	applyYAML(t, tokenSecret)
	defer deleteYAML(t, tokenSecret)

	// Create project with GitHub PR approval pointing at the fake server
	prProject := `
apiVersion: tofu.example.com/v1alpha1
kind: TofuProject
metadata:
  name: pr-approve-test
  namespace: default
spec:
  programRef:
    name: hello
  params:
    environment: "staging"
  backend:
    secretSuffix: pr-approve-test
    namespace: default
  autoApprove: false
  approval:
    mode: githubPR
    github:
      tokenSecretRef:
        name: gh-token-test
        key: token
      repo: "test-org/test-repo"
      apiURL: "http://fake-github.default.svc:8080"
      commitDiff: true
      diffPath: "plans/"
`
	applyYAML(t, prProject)
	defer deleteYAML(t, prProject)

	// Wait for plan job
	waitForJobWithLabel(t, "default", "tofu.example.com/job-type=plan,tofu.example.com/project=pr-approve-test", 60*time.Second)

	// Wait for WaitingApproval — the operator should create a PR on the fake server
	waitForPhase(t, dynClient, "default", "pr-approve-test", "WaitingApproval", 120*time.Second)

	// Verify PR status fields are set
	obj, err := dynClient.Resource(tofuProjectGVR).Namespace("default").Get(context.Background(), "pr-approve-test", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("failed to get pr-approve-test: %v", err)
	}
	status, _, _ := unstructured.NestedMap(obj.Object, "status")
	pendingHash, _ := status["pendingPlanHash"].(string)
	if pendingHash == "" {
		t.Fatal("expected pendingPlanHash to be set")
	}
	prNumber, _ := status["pendingPRNumber"].(int64)
	prURL, _ := status["pendingPRURL"].(string)
	t.Logf("Plan hash: %s, PR #%d, URL: %s", pendingHash, prNumber, prURL)

	if prNumber == 0 {
		t.Fatal("expected pendingPRNumber to be set")
	}
	if prURL == "" {
		t.Fatal("expected pendingPRURL to be set")
	}

	// Simulate merging the PR via the fake server's admin endpoint
	fakeGitHubAdminExec(t, "POST", fmt.Sprintf("/admin/merge/%d", prNumber))
	t.Logf("Simulated merge of PR #%d", prNumber)

	// Wait for apply job (operator detects merge → sets annotation → creates apply job)
	waitForJobWithLabel(t, "default", "tofu.example.com/job-type=apply,tofu.example.com/project=pr-approve-test", 90*time.Second)

	// Wait for Succeeded
	waitForPhase(t, dynClient, "default", "pr-approve-test", "Succeeded", 120*time.Second)

	// Verify PR status fields are cleared after apply
	obj, err = dynClient.Resource(tofuProjectGVR).Namespace("default").Get(context.Background(), "pr-approve-test", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("failed to get project after apply: %v", err)
	}
	status, _, _ = unstructured.NestedMap(obj.Object, "status")
	if prNum, _ := status["pendingPRNumber"].(int64); prNum != 0 {
		t.Fatalf("expected pendingPRNumber to be cleared, got %d", prNum)
	}
	if prURL, _ := status["pendingPRURL"].(string); prURL != "" {
		t.Fatalf("expected pendingPRURL to be cleared, got %q", prURL)
	}
	t.Log("GitHub PR approve flow completed successfully")
}

func TestGitHubPRRejectFlow(t *testing.T) {
	dynClient := newDynamicClient(t)

	deployFakeGitHub(t)
	defer cleanupFakeGitHub(t)

	// Create a Secret with the fake GitHub token
	tokenSecret := `
apiVersion: v1
kind: Secret
metadata:
  name: gh-token-reject-test
  namespace: default
type: Opaque
stringData:
  token: fake-token
`
	applyYAML(t, tokenSecret)
	defer deleteYAML(t, tokenSecret)

	// Create project with GitHub PR approval pointing at the fake server
	rejectProject := `
apiVersion: tofu.example.com/v1alpha1
kind: TofuProject
metadata:
  name: pr-reject-test
  namespace: default
spec:
  programRef:
    name: hello
  params:
    environment: "staging"
  backend:
    secretSuffix: pr-reject-test
    namespace: default
  autoApprove: false
  approval:
    mode: githubPR
    github:
      tokenSecretRef:
        name: gh-token-reject-test
        key: token
      repo: "test-org/test-repo"
      apiURL: "http://fake-github.default.svc:8080"
      commitDiff: false
`
	applyYAML(t, rejectProject)
	defer deleteYAML(t, rejectProject)

	// Wait for plan job
	waitForJobWithLabel(t, "default", "tofu.example.com/job-type=plan,tofu.example.com/project=pr-reject-test", 60*time.Second)

	// Wait for WaitingApproval
	waitForPhase(t, dynClient, "default", "pr-reject-test", "WaitingApproval", 120*time.Second)

	// Get PR number
	obj, err := dynClient.Resource(tofuProjectGVR).Namespace("default").Get(context.Background(), "pr-reject-test", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("failed to get pr-reject-test: %v", err)
	}
	status, _, _ := unstructured.NestedMap(obj.Object, "status")
	prNumber, _ := status["pendingPRNumber"].(int64)
	if prNumber == 0 {
		t.Fatal("expected pendingPRNumber to be set")
	}
	t.Logf("PR #%d created, simulating rejection (close without merge)", prNumber)

	// Simulate closing the PR without merging
	fakeGitHubAdminExec(t, "POST", fmt.Sprintf("/admin/close/%d", prNumber))
	t.Log("Simulated close (rejection) of PR")

	// After rejection, the operator clears the plan state (PlanRejected) and immediately
	// re-reconciles. Since the hash isn't applied, it creates a new plan and a new PR.
	// PlanRejected is transient — wait for WaitingApproval with a DIFFERENT PR number.
	deadline := time.Now().Add(90 * time.Second)
	var newPRNumber int64
	for time.Now().Before(deadline) {
		obj, err = dynClient.Resource(tofuProjectGVR).Namespace("default").Get(context.Background(), "pr-reject-test", metav1.GetOptions{})
		if err == nil {
			status, _, _ = unstructured.NestedMap(obj.Object, "status")
			phase, _ := status["phase"].(string)
			currentPR, _ := status["pendingPRNumber"].(int64)
			if phase == "WaitingApproval" && currentPR != 0 && currentPR != prNumber {
				newPRNumber = currentPR
				break
			}
		}
		time.Sleep(2 * time.Second)
	}
	if newPRNumber == 0 {
		t.Fatal("expected a new PR to be created after rejection")
	}
	t.Logf("Old PR #%d rejected, new PR #%d created — reject flow works correctly", prNumber, newPRNumber)
}

func TestGitHubPRStaleClose(t *testing.T) {
	dynClient := newDynamicClient(t)

	deployFakeGitHub(t)
	defer cleanupFakeGitHub(t)

	tokenSecret := `
apiVersion: v1
kind: Secret
metadata:
  name: gh-token-stale-test
  namespace: default
type: Opaque
stringData:
  token: fake-token
`
	applyYAML(t, tokenSecret)
	defer deleteYAML(t, tokenSecret)

	staleProject := `
apiVersion: tofu.example.com/v1alpha1
kind: TofuProject
metadata:
  name: pr-stale-test
  namespace: default
spec:
  programRef:
    name: hello
  params:
    environment: "staging"
  backend:
    secretSuffix: pr-stale-test
    namespace: default
  autoApprove: false
  approval:
    mode: githubPR
    github:
      tokenSecretRef:
        name: gh-token-stale-test
        key: token
      repo: "test-org/test-repo"
      apiURL: "http://fake-github.default.svc:8080"
      commitDiff: false
`
	applyYAML(t, staleProject)
	defer deleteYAML(t, staleProject)

	// Wait for plan and WaitingApproval
	waitForJobWithLabel(t, "default", "tofu.example.com/job-type=plan,tofu.example.com/project=pr-stale-test", 60*time.Second)
	waitForPhase(t, dynClient, "default", "pr-stale-test", "WaitingApproval", 120*time.Second)

	// Get original PR number
	obj, err := dynClient.Resource(tofuProjectGVR).Namespace("default").Get(context.Background(), "pr-stale-test", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("failed to get project: %v", err)
	}
	status, _, _ := unstructured.NestedMap(obj.Object, "status")
	originalPR, _ := status["pendingPRNumber"].(int64)
	if originalPR == 0 {
		t.Fatal("expected pendingPRNumber to be set")
	}
	t.Logf("Original PR #%d created", originalPR)

	// Change spec to invalidate the plan (update params)
	paramPatch := map[string]interface{}{
		"spec": map[string]interface{}{
			"params": map[string]interface{}{
				"environment": "production",
			},
		},
	}
	patchBytes, _ := json.Marshal(paramPatch)
	_, err = dynClient.Resource(tofuProjectGVR).Namespace("default").Patch(
		context.Background(), "pr-stale-test", types.MergePatchType, patchBytes, metav1.PatchOptions{},
	)
	if err != nil {
		t.Fatalf("failed to update spec: %v", err)
	}
	t.Log("Spec updated — old PR should be closed, new plan should start")

	// After spec change, the stale plan is invalidated and the old PR is closed.
	// The operator re-plans (may reuse existing plan job) and creates a new PR.
	// Wait for WaitingApproval with a different PR number.
	deadline := time.Now().Add(120 * time.Second)
	var newPR int64
	for time.Now().Before(deadline) {
		obj, err = dynClient.Resource(tofuProjectGVR).Namespace("default").Get(context.Background(), "pr-stale-test", metav1.GetOptions{})
		if err == nil {
			status, _, _ = unstructured.NestedMap(obj.Object, "status")
			phase, _ := status["phase"].(string)
			currentPR, _ := status["pendingPRNumber"].(int64)
			if phase == "WaitingApproval" && currentPR != 0 && currentPR != originalPR {
				newPR = currentPR
				break
			}
		}
		time.Sleep(2 * time.Second)
	}
	if newPR == 0 {
		t.Fatal("expected a new PR to be created after spec change")
	}
	t.Logf("Stale PR #%d replaced by new PR #%d", originalPR, newPR)
}
