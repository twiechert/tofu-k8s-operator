package controllers

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	tofuv1alpha1 "github.com/twiechert/tofu-k8s-operator/api/v1alpha1"
)

// GitHubClient is a minimal GitHub REST API client for PR-based approvals.
type GitHubClient struct {
	Token      string
	Owner      string
	Repo       string
	BaseURL    string
	HTTPClient *http.Client
}

// NewGitHubClient parses "owner/repo" and returns a configured client.
// apiURL overrides the default GitHub API base URL (pass "" for default).
func NewGitHubClient(token, ownerRepo, apiURL string) (*GitHubClient, error) {
	parts := strings.SplitN(ownerRepo, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return nil, fmt.Errorf("invalid repo format %q, expected owner/repo", ownerRepo)
	}
	baseURL := "https://api.github.com"
	if apiURL != "" {
		baseURL = strings.TrimRight(apiURL, "/")
	}
	return &GitHubClient{
		Token:      token,
		Owner:      parts[0],
		Repo:       parts[1],
		BaseURL:    baseURL,
		HTTPClient: &http.Client{Timeout: 30 * time.Second},
	}, nil
}

func (c *GitHubClient) do(ctx context.Context, method, path string, body interface{}) ([]byte, int, error) {
	var reqBody io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, 0, fmt.Errorf("marshalling request body: %w", err)
		}
		reqBody = bytes.NewReader(data)
	}

	url := fmt.Sprintf("%s%s", c.BaseURL, path)
	req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		return nil, 0, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("executing request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("reading response body: %w", err)
	}
	return respBody, resp.StatusCode, nil
}

// GetDefaultBranch returns the default branch name for the repo.
func (c *GitHubClient) GetDefaultBranch(ctx context.Context) (string, error) {
	body, status, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s", c.Owner, c.Repo), nil)
	if err != nil {
		return "", err
	}
	if status != 200 {
		return "", fmt.Errorf("GET repo returned %d: %s", status, body)
	}
	var repo struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := json.Unmarshal(body, &repo); err != nil {
		return "", fmt.Errorf("parsing repo response: %w", err)
	}
	return repo.DefaultBranch, nil
}

// GetBranchSHA returns the HEAD commit SHA of the given branch.
func (c *GitHubClient) GetBranchSHA(ctx context.Context, branch string) (string, error) {
	body, status, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s/git/ref/heads/%s", c.Owner, c.Repo, branch), nil)
	if err != nil {
		return "", err
	}
	if status != 200 {
		return "", fmt.Errorf("GET ref returned %d: %s", status, body)
	}
	var ref struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if err := json.Unmarshal(body, &ref); err != nil {
		return "", fmt.Errorf("parsing ref response: %w", err)
	}
	return ref.Object.SHA, nil
}

// CreateBranch creates a new branch from the given SHA.
func (c *GitHubClient) CreateBranch(ctx context.Context, branchName, fromSHA string) error {
	payload := map[string]string{
		"ref": "refs/heads/" + branchName,
		"sha": fromSHA,
	}
	body, status, err := c.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/%s/git/refs", c.Owner, c.Repo), payload)
	if err != nil {
		return err
	}
	if status != 201 {
		return fmt.Errorf("create branch returned %d: %s", status, body)
	}
	return nil
}

// CreateOrUpdateFile creates or updates a file on the given branch.
func (c *GitHubClient) CreateOrUpdateFile(ctx context.Context, path, content, message, branch string) error {
	encoded := base64.StdEncoding.EncodeToString([]byte(content))
	payload := map[string]string{
		"message": message,
		"content": encoded,
		"branch":  branch,
	}
	body, status, err := c.do(ctx, http.MethodPut, fmt.Sprintf("/repos/%s/%s/contents/%s", c.Owner, c.Repo, path), payload)
	if err != nil {
		return err
	}
	if status != 201 && status != 200 {
		return fmt.Errorf("create file returned %d: %s", status, body)
	}
	return nil
}

// CreatePR creates a pull request and returns the PR number and URL.
func (c *GitHubClient) CreatePR(ctx context.Context, title, prBody, head, base string) (int, string, error) {
	payload := map[string]string{
		"title": title,
		"body":  prBody,
		"head":  head,
		"base":  base,
	}
	respBody, status, err := c.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/%s/pulls", c.Owner, c.Repo), payload)
	if err != nil {
		return 0, "", err
	}
	if status != 201 {
		return 0, "", fmt.Errorf("create PR returned %d: %s", status, respBody)
	}
	var pr struct {
		Number  int    `json:"number"`
		HTMLURL string `json:"html_url"`
	}
	if err := json.Unmarshal(respBody, &pr); err != nil {
		return 0, "", fmt.Errorf("parsing PR response: %w", err)
	}
	return pr.Number, pr.HTMLURL, nil
}

// PRStatus holds the state of a GitHub PR.
type PRStatus struct {
	State  string // "open", "closed"
	Merged bool
}

// GetPR returns the state and merge status of a PR.
func (c *GitHubClient) GetPR(ctx context.Context, prNumber int) (*PRStatus, error) {
	body, status, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s/pulls/%d", c.Owner, c.Repo, prNumber), nil)
	if err != nil {
		return nil, err
	}
	if status != 200 {
		return nil, fmt.Errorf("GET PR returned %d: %s", status, body)
	}
	var pr struct {
		State  string `json:"state"`
		Merged bool   `json:"merged"`
	}
	if err := json.Unmarshal(body, &pr); err != nil {
		return nil, fmt.Errorf("parsing PR response: %w", err)
	}
	return &PRStatus{State: pr.State, Merged: pr.Merged}, nil
}

// ClosePR closes a PR with a comment.
func (c *GitHubClient) ClosePR(ctx context.Context, prNumber int, comment string) error {
	if comment != "" {
		commentPayload := map[string]string{"body": comment}
		c.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/%s/issues/%d/comments", c.Owner, c.Repo, prNumber), commentPayload)
	}
	payload := map[string]string{"state": "closed"}
	body, status, err := c.do(ctx, http.MethodPatch, fmt.Sprintf("/repos/%s/%s/pulls/%d", c.Owner, c.Repo, prNumber), payload)
	if err != nil {
		return err
	}
	if status != 200 {
		return fmt.Errorf("close PR returned %d: %s", status, body)
	}
	return nil
}

// DeleteBranch deletes a branch by name.
func (c *GitHubClient) DeleteBranch(ctx context.Context, branchName string) error {
	_, status, err := c.do(ctx, http.MethodDelete, fmt.Sprintf("/repos/%s/%s/git/refs/heads/%s", c.Owner, c.Repo, branchName), nil)
	if err != nil {
		return err
	}
	if status != 204 && status != 422 { // 422 = branch already deleted
		return fmt.Errorf("delete branch returned %d", status)
	}
	return nil
}

// --- Reconciler integration methods ---

// resolveGitHubToken reads a GitHub token from the referenced Kubernetes Secret.
func (r *TofuProjectReconciler) resolveGitHubToken(ctx context.Context, namespace string, ref tofuv1alpha1.KeyRef) (string, error) {
	var secret corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Name: ref.Name, Namespace: namespace}, &secret); err != nil {
		return "", fmt.Errorf("fetching token secret %s: %w", ref.Name, err)
	}
	token, ok := secret.Data[ref.Key]
	if !ok {
		return "", fmt.Errorf("key %q not found in secret %s", ref.Key, ref.Name)
	}
	return string(token), nil
}

// newGitHubClientForProject creates a GitHubClient from the project's approval spec.
func (r *TofuProjectReconciler) newGitHubClientForProject(ctx context.Context, project *tofuv1alpha1.TofuProject) (*GitHubClient, error) {
	gh := project.Spec.Approval.GitHub
	token, err := r.resolveGitHubToken(ctx, project.Namespace, gh.TokenSecretRef)
	if err != nil {
		return nil, err
	}
	return NewGitHubClient(token, gh.Repo, gh.APIURL)
}

// isGitHubPRMode returns true if the project uses GitHub PR-based approval.
func isGitHubPRMode(project *tofuv1alpha1.TofuProject) bool {
	return project.Spec.Approval != nil && project.Spec.Approval.Mode == "githubPR"
}

// prBranchName returns the branch name used for a plan PR.
func prBranchName(project *tofuv1alpha1.TofuProject, hash string) string {
	return fmt.Sprintf("tofu-plan/%s/%s/%s", project.Namespace, project.Name, hash[:8])
}

// createApprovalPR creates a GitHub PR with the plan output for approval.
func (r *TofuProjectReconciler) createApprovalPR(ctx context.Context, project *tofuv1alpha1.TofuProject, planOutput, appliedHash string) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)

	ghClient, err := r.newGitHubClientForProject(ctx, project)
	if err != nil {
		log.Error(err, "failed to create GitHub client")
		project.Status.Phase = "Error"
		project.Status.Message = fmt.Sprintf("GitHub client error: %v", err)
		r.updateStatusWithCondition(ctx, project)
		return ctrl.Result{}, nil
	}

	defaultBranch, err := ghClient.GetDefaultBranch(ctx)
	if err != nil {
		log.Error(err, "failed to get default branch")
		project.Status.Phase = "Error"
		project.Status.Message = fmt.Sprintf("GitHub API error: %v", err)
		r.updateStatusWithCondition(ctx, project)
		return ctrl.Result{}, nil
	}

	baseSHA, err := ghClient.GetBranchSHA(ctx, defaultBranch)
	if err != nil {
		log.Error(err, "failed to get branch SHA")
		project.Status.Phase = "Error"
		project.Status.Message = fmt.Sprintf("GitHub API error: %v", err)
		r.updateStatusWithCondition(ctx, project)
		return ctrl.Result{}, nil
	}

	branch := prBranchName(project, appliedHash)

	if err := ghClient.CreateBranch(ctx, branch, baseSHA); err != nil {
		log.Error(err, "failed to create branch")
		project.Status.Phase = "Error"
		project.Status.Message = fmt.Sprintf("GitHub API error: %v", err)
		r.updateStatusWithCondition(ctx, project)
		return ctrl.Result{}, nil
	}

	ghSpec := project.Spec.Approval.GitHub
	if ghSpec.CommitDiff {
		diffPath := ghSpec.DiffPath
		if diffPath == "" {
			diffPath = "plans/"
		}
		if !strings.HasSuffix(diffPath, "/") {
			diffPath += "/"
		}
		filePath := fmt.Sprintf("%s%s/%s/%s.txt", diffPath, project.Namespace, project.Name, appliedHash[:8])
		commitMsg := fmt.Sprintf("tofu plan for %s/%s (%s)", project.Namespace, project.Name, appliedHash[:8])
		if err := ghClient.CreateOrUpdateFile(ctx, filePath, planOutput, commitMsg, branch); err != nil {
			log.Error(err, "failed to commit plan diff (non-fatal)")
		}
	}

	// Build PR body
	var prBody strings.Builder
	prBody.WriteString(fmt.Sprintf("## Tofu Plan for `%s/%s`\n\n", project.Namespace, project.Name))
	if project.Status.PlanSummary != "" {
		prBody.WriteString(fmt.Sprintf("**Summary:** %s\n\n", project.Status.PlanSummary))
	}
	if project.Status.BlastRadius != nil {
		br := project.Status.BlastRadius
		prBody.WriteString(fmt.Sprintf("**Blast Radius:** add=%d, change=%d, destroy=%d (total=%d)\n\n", br.Add, br.Change, br.Destroy, br.Total))
	}
	prBody.WriteString("**Merge this PR to approve the plan and trigger apply.**\n\n")
	prBody.WriteString("<details><summary>Full Plan Output</summary>\n\n```\n")
	// GitHub has a body size limit; truncate if needed
	output := planOutput
	const maxPRBody = 60000
	if len(output) > maxPRBody {
		output = output[len(output)-maxPRBody:]
	}
	prBody.WriteString(output)
	prBody.WriteString("\n```\n</details>\n")

	title := fmt.Sprintf("[tofu-operator] Plan for %s/%s (%s)", project.Namespace, project.Name, appliedHash[:8])
	prNumber, prURL, err := ghClient.CreatePR(ctx, title, prBody.String(), branch, defaultBranch)
	if err != nil {
		log.Error(err, "failed to create PR")
		project.Status.Phase = "Error"
		project.Status.Message = fmt.Sprintf("GitHub API error: %v", err)
		r.updateStatusWithCondition(ctx, project)
		return ctrl.Result{}, nil
	}

	log.Info("created GitHub PR for plan approval", "pr", prNumber, "url", prURL)
	project.Status.PendingPRNumber = prNumber
	project.Status.PendingPRURL = prURL
	project.Status.Phase = "WaitingApproval"
	project.Status.Message = fmt.Sprintf("Plan complete. Approve by merging PR: %s", prURL)
	r.updateStatusWithCondition(ctx, project)
	sendNotification(ctx, project, "plan:complete")
	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

// checkPRMergeStatus checks whether the pending GitHub PR has been merged, closed, or is still open.
func (r *TofuProjectReconciler) checkPRMergeStatus(ctx context.Context, project *tofuv1alpha1.TofuProject, appliedHash string) (merged bool, err error) {
	log := ctrl.LoggerFrom(ctx)

	ghClient, err := r.newGitHubClientForProject(ctx, project)
	if err != nil {
		log.Error(err, "failed to create GitHub client for PR check")
		return false, nil
	}

	prStatus, err := ghClient.GetPR(ctx, project.Status.PendingPRNumber)
	if err != nil {
		log.Error(err, "failed to check PR status (will retry)")
		return false, nil
	}

	if prStatus.Merged {
		log.Info("GitHub PR merged, approving plan", "pr", project.Status.PendingPRNumber)
		// Clean up PR state
		branch := prBranchName(project, appliedHash)
		if delErr := ghClient.DeleteBranch(ctx, branch); delErr != nil {
			log.Error(delErr, "failed to delete PR branch (non-fatal)")
		}
		project.Status.PendingPRNumber = 0
		project.Status.PendingPRURL = ""
		// Set the approved annotation to trigger the existing apply flow
		ann := project.GetAnnotations()
		if ann == nil {
			ann = map[string]string{}
		}
		ann[approvedHashAnnotation] = appliedHash
		project.SetAnnotations(ann)
		if err := r.Update(ctx, project); err != nil {
			return false, err
		}
		return true, nil
	}

	if prStatus.State == "closed" {
		// PR was closed without merging (rejected)
		log.Info("GitHub PR closed without merge, rejecting plan", "pr", project.Status.PendingPRNumber)
		branch := prBranchName(project, appliedHash)
		if delErr := ghClient.DeleteBranch(ctx, branch); delErr != nil {
			log.Error(delErr, "failed to delete PR branch (non-fatal)")
		}
		project.Status.PendingPRNumber = 0
		project.Status.PendingPRURL = ""
		project.Status.PendingPlanHash = ""
		project.Status.PlanOutput = ""
		project.Status.PlanSummary = ""
		project.Status.BlastRadius = nil
		project.Status.Phase = "PlanRejected"
		project.Status.Message = "GitHub PR was closed without merging. A new plan will be created on next reconcile."
		r.updateStatusWithCondition(ctx, project)
		return false, nil
	}

	// Still open — poll again
	return false, nil
}

// closeStaleApprovalPR closes an obsolete GitHub PR when the spec hash changes.
func (r *TofuProjectReconciler) closeStaleApprovalPR(ctx context.Context, project *tofuv1alpha1.TofuProject, newHash string) {
	log := ctrl.LoggerFrom(ctx)

	ghClient, err := r.newGitHubClientForProject(ctx, project)
	if err != nil {
		log.Error(err, "failed to create GitHub client for stale PR cleanup")
		return
	}

	comment := fmt.Sprintf("Superseded by spec change (new hash: %s). A new PR will be created after the next plan.", newHash[:8])
	if err := ghClient.ClosePR(ctx, project.Status.PendingPRNumber, comment); err != nil {
		log.Error(err, "failed to close stale PR (non-fatal)")
	}

	// Try to delete the old branch
	oldBranch := prBranchName(project, project.Status.PendingPlanHash)
	if err := ghClient.DeleteBranch(ctx, oldBranch); err != nil {
		log.Error(err, "failed to delete stale PR branch (non-fatal)")
	}
}
