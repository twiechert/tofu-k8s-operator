package controllers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	tofuv1alpha1 "github.com/twiechert/tofu-k8s-operator/api/v1alpha1"
)

func TestNewGitHubClient(t *testing.T) {
	tests := []struct {
		name      string
		ownerRepo string
		wantErr   bool
		wantOwner string
		wantRepo  string
	}{
		{"valid", "org/repo", false, "org", "repo"},
		{"with dots", "my-org/my-repo.name", false, "my-org", "my-repo.name"},
		{"missing slash", "orgrepo", true, "", ""},
		{"empty owner", "/repo", true, "", ""},
		{"empty repo", "org/", true, "", ""},
		{"empty", "", true, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := NewGitHubClient("tok", tt.ownerRepo, "")
			if (err != nil) != tt.wantErr {
				t.Fatalf("NewGitHubClient(%q) error = %v, wantErr %v", tt.ownerRepo, err, tt.wantErr)
			}
			if err == nil {
				if c.Owner != tt.wantOwner || c.Repo != tt.wantRepo {
					t.Fatalf("got owner=%q repo=%q, want %q/%q", c.Owner, c.Repo, tt.wantOwner, tt.wantRepo)
				}
			}
		})
	}
}

func TestNewGitHubClientAPIURL(t *testing.T) {
	tests := []struct {
		name    string
		apiURL  string
		wantURL string
	}{
		{"default", "", "https://api.github.com"},
		{"custom", "https://github.example.com/api/v3", "https://github.example.com/api/v3"},
		{"trailing slash", "https://ghe.corp.com/api/v3/", "https://ghe.corp.com/api/v3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := NewGitHubClient("tok", "o/r", tt.apiURL)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if c.BaseURL != tt.wantURL {
				t.Fatalf("got BaseURL=%q, want %q", c.BaseURL, tt.wantURL)
			}
		})
	}
}

func TestIsGitHubPRMode(t *testing.T) {
	tests := []struct {
		name     string
		approval *tofuv1alpha1.ApprovalSpec
		want     bool
	}{
		{"nil approval", nil, false},
		{"empty mode", &tofuv1alpha1.ApprovalSpec{}, false},
		{"annotation mode", &tofuv1alpha1.ApprovalSpec{Mode: "annotation"}, false},
		{"githubPR mode", &tofuv1alpha1.ApprovalSpec{Mode: "githubPR"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &tofuv1alpha1.TofuProject{
				Spec: tofuv1alpha1.TofuProjectSpec{Approval: tt.approval},
			}
			if got := isGitHubPRMode(p); got != tt.want {
				t.Fatalf("isGitHubPRMode() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPRBranchName(t *testing.T) {
	p := &tofuv1alpha1.TofuProject{
		ObjectMeta: metav1.ObjectMeta{Name: "myproj", Namespace: "staging"},
	}
	got := prBranchName(p, "abcdef1234567890")
	want := "tofu-plan/staging/myproj/abcdef12"
	if got != want {
		t.Fatalf("prBranchName() = %q, want %q", got, want)
	}
}

func TestGitHubClientGetDefaultBranch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/org/repo" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Fatalf("unexpected auth: %s", r.Header.Get("Authorization"))
		}
		json.NewEncoder(w).Encode(map[string]string{"default_branch": "main"})
	}))
	defer srv.Close()

	c := &GitHubClient{
		Token:      "test-token",
		Owner:      "org",
		Repo:       "repo",
		BaseURL:    srv.URL,
		HTTPClient: srv.Client(),
	}

	branch, err := c.GetDefaultBranch(context.Background())
	if err != nil {
		t.Fatalf("GetDefaultBranch() error: %v", err)
	}
	if branch != "main" {
		t.Fatalf("got %q, want %q", branch, "main")
	}
}

func TestGitHubClientGetPR(t *testing.T) {
	tests := []struct {
		name       string
		state      string
		merged     bool
		wantMerged bool
		wantState  string
	}{
		{"open", "open", false, false, "open"},
		{"merged", "closed", true, true, "closed"},
		{"closed", "closed", false, false, "closed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(map[string]interface{}{
					"state":  tt.state,
					"merged": tt.merged,
				})
			}))
			defer srv.Close()

			c := &GitHubClient{
				Token: "tok", Owner: "o", Repo: "r",
				BaseURL: srv.URL, HTTPClient: srv.Client(),
			}
			status, err := c.GetPR(context.Background(), 42)
			if err != nil {
				t.Fatalf("GetPR() error: %v", err)
			}
			if status.State != tt.wantState || status.Merged != tt.wantMerged {
				t.Fatalf("got state=%q merged=%v, want %q/%v", status.State, status.Merged, tt.wantState, tt.wantMerged)
			}
		})
	}
}

func TestGitHubClientCreatePR(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("unexpected method: %s", r.Method)
		}
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"number":   123,
			"html_url": "https://github.com/org/repo/pull/123",
		})
	}))
	defer srv.Close()

	c := &GitHubClient{
		Token: "tok", Owner: "org", Repo: "repo",
		BaseURL: srv.URL, HTTPClient: srv.Client(),
	}
	num, url, err := c.CreatePR(context.Background(), "title", "body", "head", "main")
	if err != nil {
		t.Fatalf("CreatePR() error: %v", err)
	}
	if num != 123 {
		t.Fatalf("got number=%d, want 123", num)
	}
	if url != "https://github.com/org/repo/pull/123" {
		t.Fatalf("got url=%q", url)
	}
}

func TestGitHubClientClosePR(t *testing.T) {
	var commentPosted bool
	var prClosed bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost:
			commentPosted = true
			w.WriteHeader(201)
			json.NewEncoder(w).Encode(map[string]string{"id": "1"})
		case r.Method == http.MethodPatch:
			prClosed = true
			json.NewEncoder(w).Encode(map[string]string{"state": "closed"})
		}
	}))
	defer srv.Close()

	c := &GitHubClient{
		Token: "tok", Owner: "o", Repo: "r",
		BaseURL: srv.URL, HTTPClient: srv.Client(),
	}
	if err := c.ClosePR(context.Background(), 1, "superseded"); err != nil {
		t.Fatalf("ClosePR() error: %v", err)
	}
	if !commentPosted || !prClosed {
		t.Fatalf("comment=%v closed=%v", commentPosted, prClosed)
	}
}

func TestGitHubClientCreateBranch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("unexpected method: %s", r.Method)
		}
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		if body["ref"] != "refs/heads/tofu-plan/default/test/abc12345" {
			t.Fatalf("unexpected ref: %s", body["ref"])
		}
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(map[string]string{"ref": body["ref"]})
	}))
	defer srv.Close()

	c := &GitHubClient{
		Token: "tok", Owner: "o", Repo: "r",
		BaseURL: srv.URL, HTTPClient: srv.Client(),
	}
	if err := c.CreateBranch(context.Background(), "tofu-plan/default/test/abc12345", "sha123"); err != nil {
		t.Fatalf("CreateBranch() error: %v", err)
	}
}
