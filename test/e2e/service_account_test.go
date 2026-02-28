//go:build integration
// +build integration

package e2e

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestCustomServiceAccountAnnotations(t *testing.T) {
	deployOperator(t)
	dynClient := newDynamicClient(t)
	clientset := newClientset(t)

	applyYAML(t, tofuProgramYAML)
	defer deleteYAML(t, tofuProgramYAML)

	project := `
apiVersion: tofu.example.com/v1alpha1
kind: TofuProject
metadata:
  name: sa-annotation-test
  namespace: default
spec:
  programRef:
    name: hello
  backend:
    secretSuffix: sa-annotation-test
    namespace: default
  autoApprove: true
  serviceAccount:
    annotations:
      eks.amazonaws.com/role-arn: "arn:aws:iam::123456789012:role/test-role"
`
	applyYAML(t, project)
	defer deleteYAML(t, project)

	waitForPhase(t, dynClient, "default", "sa-annotation-test", "Succeeded", 120*time.Second)

	// Verify the auto-created SA has the custom annotation
	sa, err := clientset.CoreV1().ServiceAccounts("default").Get(context.Background(), "tofu-runner", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("failed to get tofu-runner SA: %v", err)
	}
	roleArn, ok := sa.Annotations["eks.amazonaws.com/role-arn"]
	if !ok || roleArn != "arn:aws:iam::123456789012:role/test-role" {
		t.Fatalf("expected IRSA annotation on tofu-runner SA, got annotations: %v", sa.Annotations)
	}
}

func TestCustomServiceAccountName(t *testing.T) {
	deployOperator(t)
	dynClient := newDynamicClient(t)
	clientset := newClientset(t)

	applyYAML(t, tofuProgramYAML)
	defer deleteYAML(t, tofuProgramYAML)

	// Pre-create a custom SA
	customSA := `
apiVersion: v1
kind: ServiceAccount
metadata:
  name: custom-tofu-sa
  namespace: default
`
	applyYAML(t, customSA)
	defer deleteYAML(t, customSA)

	// Also create a RoleBinding for the custom SA so the job can run
	customRB := `
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: custom-tofu-sa
  namespace: default
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: tofu-runner
subjects:
  - kind: ServiceAccount
    name: custom-tofu-sa
    namespace: default
`
	applyYAML(t, customRB)
	defer deleteYAML(t, customRB)

	project := `
apiVersion: tofu.example.com/v1alpha1
kind: TofuProject
metadata:
  name: sa-name-test
  namespace: default
spec:
  programRef:
    name: hello
  backend:
    secretSuffix: sa-name-test
    namespace: default
  autoApprove: true
  serviceAccount:
    name: custom-tofu-sa
`
	applyYAML(t, project)
	defer deleteYAML(t, project)

	// Wait for job to be created
	waitForJobWithLabel(t, "default", "tofu.example.com/project=sa-name-test", 60*time.Second)

	// Verify the job uses the custom SA
	jobs, err := clientset.BatchV1().Jobs("default").List(context.Background(), metav1.ListOptions{
		LabelSelector: "tofu.example.com/project=sa-name-test",
	})
	if err != nil {
		t.Fatalf("failed to list jobs: %v", err)
	}
	if len(jobs.Items) == 0 {
		t.Fatal("no jobs found")
	}
	saName := jobs.Items[0].Spec.Template.Spec.ServiceAccountName
	if saName != "custom-tofu-sa" {
		t.Fatalf("expected job to use custom-tofu-sa, got %q", saName)
	}

	waitForPhase(t, dynClient, "default", "sa-name-test", "Succeeded", 120*time.Second)
}
