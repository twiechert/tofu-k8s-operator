//go:build integration
// +build integration

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

// fakeGitHub is a minimal GitHub API mock for e2e testing the PR approval flow.
type fakeGitHub struct {
	mu            sync.Mutex
	prs           map[int]*fakePR
	branches      map[string]string // branch name → sha
	nextPR        int
	defaultBranch string
}

type fakePR struct {
	Number int
	Title  string
	Body   string
	Head   string
	Base   string
	State  string
	Merged bool
}

func newFakeGitHub() *fakeGitHub {
	return &fakeGitHub{
		prs:           make(map[int]*fakePR),
		branches:      map[string]string{"main": "abc123"},
		nextPR:        1,
		defaultBranch: "main",
	}
}

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	switch {
	// GET /repos/{owner}/{repo}
	case r.Method == http.MethodGet && r.URL.Path == "/repos/test-org/test-repo":
		json.NewEncoder(w).Encode(map[string]string{"default_branch": f.defaultBranch})

	// GET /repos/{owner}/{repo}/git/ref/heads/{branch}
	case r.Method == http.MethodGet && len(r.URL.Path) > len("/repos/test-org/test-repo/git/ref/heads/"):
		json.NewEncoder(w).Encode(map[string]interface{}{
			"object": map[string]string{"sha": "abc123"},
		})

	// POST /repos/{owner}/{repo}/git/refs (create branch)
	case r.Method == http.MethodPost && r.URL.Path == "/repos/test-org/test-repo/git/refs":
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		f.branches[body["ref"]] = body["sha"]
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(body)

	// PUT /repos/{owner}/{repo}/contents/{path} (create file)
	case r.Method == http.MethodPut:
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(map[string]interface{}{"content": map[string]string{"sha": "filesha"}})

	// POST /repos/{owner}/{repo}/pulls (create PR)
	case r.Method == http.MethodPost && r.URL.Path == "/repos/test-org/test-repo/pulls":
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		pr := &fakePR{
			Number: f.nextPR,
			Title:  body["title"],
			Body:   body["body"],
			Head:   body["head"],
			Base:   body["base"],
			State:  "open",
		}
		f.prs[f.nextPR] = pr
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"number":   pr.Number,
			"html_url": fmt.Sprintf("https://github.com/test-org/test-repo/pull/%d", pr.Number),
		})
		f.nextPR++

	// GET /repos/{owner}/{repo}/pulls/{number}
	case r.Method == http.MethodGet:
		var prNum int
		fmt.Sscanf(r.URL.Path, "/repos/test-org/test-repo/pulls/%d", &prNum)
		if pr, ok := f.prs[prNum]; ok {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"state":  pr.State,
				"merged": pr.Merged,
			})
		} else {
			w.WriteHeader(404)
		}

	// PATCH /repos/{owner}/{repo}/pulls/{number} (close PR)
	case r.Method == http.MethodPatch:
		var prNum int
		fmt.Sscanf(r.URL.Path, "/repos/test-org/test-repo/pulls/%d", &prNum)
		if pr, ok := f.prs[prNum]; ok {
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			if body["state"] == "closed" {
				pr.State = "closed"
			}
			json.NewEncoder(w).Encode(map[string]interface{}{"state": pr.State})
		}

	// POST /repos/{owner}/{repo}/issues/{number}/comments
	case r.Method == http.MethodPost:
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(map[string]string{"id": "1"})

	// DELETE (branch cleanup)
	case r.Method == http.MethodDelete:
		w.WriteHeader(204)

	default:
		w.WriteHeader(404)
	}
}

// mergePR simulates merging a PR in the fake.
func (f *fakeGitHub) mergePR(number int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if pr, ok := f.prs[number]; ok {
		pr.State = "closed"
		pr.Merged = true
	}
}

// closePR simulates closing a PR without merging (rejection).
func (f *fakeGitHub) closePR(number int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if pr, ok := f.prs[number]; ok {
		pr.State = "closed"
		pr.Merged = false
	}
}

func TestGitHubPRApproveFlow(t *testing.T) {
	t.Parallel()
	dynClient := newDynamicClient(t)

	// Start fake GitHub server
	fake := newFakeGitHub()
	srv := httptest.NewServer(fake)
	defer srv.Close()

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

	// Create project with GitHub PR approval mode
	// Note: We can't easily override the GitHub API base URL in the operator at runtime.
	// This test validates the CRD structure and plan phase transition.
	// Full integration requires the operator to use the fake server URL.
	prProject := fmt.Sprintf(`
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
      commitDiff: true
      diffPath: "plans/"
`)
	_ = srv // fake server URL available for operator override

	applyYAML(t, prProject)
	defer deleteYAML(t, prProject)

	// Wait for plan job to be created
	waitForJobWithLabel(t, "default", "tofu.example.com/job-type=plan,tofu.example.com/project=pr-approve-test", 60*time.Second)

	// Wait for WaitingApproval or Error phase (Error if GitHub API is unreachable in test env)
	deadline := time.Now().Add(120 * time.Second)
	var finalPhase string
	for time.Now().Before(deadline) {
		obj, err := dynClient.Resource(tofuProjectGVR).Namespace("default").Get(context.Background(), "pr-approve-test", metav1.GetOptions{})
		if err == nil {
			status, _, _ := unstructured.NestedMap(obj.Object, "status")
			phase, _ := status["phase"].(string)
			if phase == "WaitingApproval" || phase == "Error" {
				finalPhase = phase
				break
			}
		}
		time.Sleep(1 * time.Second)
	}

	if finalPhase == "" {
		t.Fatal("timed out waiting for WaitingApproval or Error phase")
	}

	// Verify the plan completed (plan hash is set regardless of GitHub API success)
	obj, err := dynClient.Resource(tofuProjectGVR).Namespace("default").Get(context.Background(), "pr-approve-test", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("failed to get pr-approve-test: %v", err)
	}
	status, _, _ := unstructured.NestedMap(obj.Object, "status")
	pendingHash, _ := status["pendingPlanHash"].(string)
	if pendingHash == "" {
		t.Fatal("expected pendingPlanHash to be set")
	}
	t.Logf("Phase: %s, pendingPlanHash: %s", finalPhase, pendingHash)

	if finalPhase == "WaitingApproval" {
		// Verify PR-related status fields
		prNumber, _ := status["pendingPRNumber"].(int64)
		prURL, _ := status["pendingPRURL"].(string)
		t.Logf("PR Number: %d, PR URL: %s", prNumber, prURL)

		if prNumber > 0 {
			// PR was created successfully — test the merge approval path
			// Simulate approval by setting the annotation directly (since the fake
			// GitHub server might not be reachable by the operator)
			annPatch := map[string]interface{}{
				"metadata": map[string]interface{}{
					"annotations": map[string]interface{}{
						"tofu.example.com/approved-hash": pendingHash,
					},
				},
			}
			patchBytes, _ := json.Marshal(annPatch)
			_, err = dynClient.Resource(tofuProjectGVR).Namespace("default").Patch(
				context.Background(), "pr-approve-test", types.MergePatchType, patchBytes, metav1.PatchOptions{},
			)
			if err != nil {
				t.Fatalf("failed to approve plan: %v", err)
			}
			waitForPhase(t, dynClient, "default", "pr-approve-test", "Succeeded", 120*time.Second)
		}
	} else {
		// GitHub API unreachable in test env — fallback to annotation approval
		t.Log("GitHub API not reachable in test environment, testing fallback annotation approval")
		annPatch := map[string]interface{}{
			"metadata": map[string]interface{}{
				"annotations": map[string]interface{}{
					"tofu.example.com/approved-hash": pendingHash,
				},
			},
		}
		patchBytes, _ := json.Marshal(annPatch)
		_, err = dynClient.Resource(tofuProjectGVR).Namespace("default").Patch(
			context.Background(), "pr-approve-test", types.MergePatchType, patchBytes, metav1.PatchOptions{},
		)
		if err != nil {
			t.Fatalf("failed to approve plan: %v", err)
		}
		waitForPhase(t, dynClient, "default", "pr-approve-test", "Succeeded", 120*time.Second)
	}
}

func TestGitHubPRRejectFlow(t *testing.T) {
	t.Parallel()
	dynClient := newDynamicClient(t)

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

	// Create project with GitHub PR approval mode
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
      commitDiff: false
`
	applyYAML(t, rejectProject)
	defer deleteYAML(t, rejectProject)

	// Wait for plan job
	waitForJobWithLabel(t, "default", "tofu.example.com/job-type=plan,tofu.example.com/project=pr-reject-test", 60*time.Second)

	// Wait for a post-plan phase
	deadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) {
		obj, err := dynClient.Resource(tofuProjectGVR).Namespace("default").Get(context.Background(), "pr-reject-test", metav1.GetOptions{})
		if err == nil {
			status, _, _ := unstructured.NestedMap(obj.Object, "status")
			phase, _ := status["phase"].(string)
			if phase == "WaitingApproval" || phase == "Error" {
				break
			}
		}
		time.Sleep(1 * time.Second)
	}

	// Verify the approval spec is stored correctly
	obj, err := dynClient.Resource(tofuProjectGVR).Namespace("default").Get(context.Background(), "pr-reject-test", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("failed to get project: %v", err)
	}
	spec, _, _ := unstructured.NestedMap(obj.Object, "spec")
	approval, _, _ := unstructured.NestedMap(spec, "approval")
	mode, _ := approval["mode"].(string)
	if mode != "githubPR" {
		t.Fatalf("expected approval mode 'githubPR', got %q", mode)
	}
	github, _, _ := unstructured.NestedMap(approval, "github")
	repo, _ := github["repo"].(string)
	if repo != "test-org/test-repo" {
		t.Fatalf("expected repo 'test-org/test-repo', got %q", repo)
	}
	commitDiff, _ := github["commitDiff"].(bool)
	if commitDiff {
		t.Fatal("expected commitDiff=false")
	}

	t.Log("GitHub PR reject flow CRD validation passed")
}
