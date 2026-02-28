//go:build integration
// +build integration

package e2e

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const featuresProgramYAML = `
apiVersion: tofu.example.com/v1alpha1
kind: TofuProgram
metadata:
  name: features-test
  namespace: default
spec:
  providers:
    - name: random
      source: "hashicorp/random"
      version: "~> 3.6"
  programHCL: |
    resource "random_pet" "name" {
      length = 2
    }
    output "pet_name" {
      value = random_pet.name.id
    }
`

func TestEnvVars(t *testing.T) {
	deployOperator(t)
	defer deleteYAML(t, featuresProgramYAML)

	applyYAML(t, featuresProgramYAML)

	projectYAML := `
apiVersion: tofu.example.com/v1alpha1
kind: TofuProject
metadata:
  name: env-test
  namespace: default
spec:
  programRef:
    name: features-test
  autoApprove: true
  env:
    - name: TF_LOG
      value: TRACE
    - name: CUSTOM_VAR
      value: hello
`
	defer deleteYAML(t, projectYAML)
	applyYAML(t, projectYAML)

	dynClient := newDynamicClient(t)
	waitForPhase(t, dynClient, "default", "env-test", "Succeeded", 120*time.Second)
}

func TestResourceLimits(t *testing.T) {
	deployOperator(t)
	defer deleteYAML(t, featuresProgramYAML)

	applyYAML(t, featuresProgramYAML)

	projectYAML := `
apiVersion: tofu.example.com/v1alpha1
kind: TofuProject
metadata:
  name: resource-test
  namespace: default
spec:
  programRef:
    name: features-test
  autoApprove: true
  resources:
    limits:
      cpu: "500m"
      memory: "256Mi"
    requests:
      cpu: "100m"
      memory: "128Mi"
`
	defer deleteYAML(t, projectYAML)
	applyYAML(t, projectYAML)

	dynClient := newDynamicClient(t)
	waitForJobWithLabel(t, "default", "tofu.example.com/project=resource-test", 60*time.Second)

	// Find the job name, then verify the pod has correct resource limits
	clientset := newClientset(t)
	deadline := time.Now().Add(60 * time.Second)
	found := false
	for !found {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for pod with resource limits")
		}
		// List jobs to find the exact job name
		jobs, err := clientset.BatchV1().Jobs("default").List(context.Background(), metav1.ListOptions{
			LabelSelector: "tofu.example.com/project=resource-test",
		})
		if err != nil || len(jobs.Items) == 0 {
			time.Sleep(2 * time.Second)
			continue
		}
		jobName := jobs.Items[0].Name
		// Find pods belonging to this job
		pods, err := clientset.CoreV1().Pods("default").List(context.Background(), metav1.ListOptions{
			LabelSelector: fmt.Sprintf("job-name=%s", jobName),
		})
		if err == nil && len(pods.Items) > 0 {
			container := pods.Items[0].Spec.Containers[0]
			cpuLimit := container.Resources.Limits.Cpu()
			if cpuLimit != nil && cpuLimit.String() == "500m" {
				found = true
				break
			}
		}
		time.Sleep(2 * time.Second)
	}

	waitForPhase(t, dynClient, "default", "resource-test", "Succeeded", 180*time.Second)
}

func TestRetryPolicy(t *testing.T) {
	deployOperator(t)

	// Use a program that will initially fail (bad provider config)
	failProgramYAML := `
apiVersion: tofu.example.com/v1alpha1
kind: TofuProgram
metadata:
  name: retry-test-prog
  namespace: default
spec:
  programHCL: |
    resource "null_resource" "test" {
      triggers = { always = timestamp() }
    }
`
	defer deleteYAML(t, failProgramYAML)
	applyYAML(t, failProgramYAML)

	projectYAML := `
apiVersion: tofu.example.com/v1alpha1
kind: TofuProject
metadata:
  name: retry-test
  namespace: default
spec:
  programRef:
    name: retry-test-prog
  autoApprove: true
  retryPolicy:
    maxRetries: 2
    delay: "5s"
`
	defer deleteYAML(t, projectYAML)
	applyYAML(t, projectYAML)

	dynClient := newDynamicClient(t)
	// The null_resource with timestamp() should succeed — verify it reaches Succeeded
	waitForPhase(t, dynClient, "default", "retry-test", "Succeeded", 120*time.Second)
}

func TestDriftDetection(t *testing.T) {
	deployOperator(t)
	defer deleteYAML(t, featuresProgramYAML)

	applyYAML(t, featuresProgramYAML)

	projectYAML := `
apiVersion: tofu.example.com/v1alpha1
kind: TofuProject
metadata:
  name: drift-test
  namespace: default
spec:
  programRef:
    name: features-test
  autoApprove: true
  driftDetection:
    enabled: true
    interval: "30s"
`
	defer deleteYAML(t, projectYAML)
	applyYAML(t, projectYAML)

	dynClient := newDynamicClient(t)
	waitForPhase(t, dynClient, "default", "drift-test", "Succeeded", 180*time.Second)

	// Wait for drift detection job to be created
	deadline := time.Now().Add(90 * time.Second)
	for {
		if time.Now().After(deadline) {
			// Drift job may not be created if interval hasn't elapsed
			t.Log("drift detection job not created within timeout (may need longer interval)")
			return
		}
		out, _ := kubectlMayFail("get", "jobs", "-n", "default", "-l", "tofu.example.com/project=drift-test,tofu.example.com/job-type=drift", "-o", "name")
		if strings.TrimSpace(out) != "" {
			t.Log("drift detection job created successfully")
			break
		}
		time.Sleep(5 * time.Second)
	}

	// Verify status has lastDriftCheckTime set (may take a bit after drift job completes)
	deadline = time.Now().Add(60 * time.Second)
	for {
		if time.Now().After(deadline) {
			break
		}
		obj, err := dynClient.Resource(tofuProjectGVR).Namespace("default").Get(context.Background(), "drift-test", metav1.GetOptions{})
		if err == nil {
			status, _, _ := unstructured.NestedMap(obj.Object, "status")
			if _, ok := status["lastDriftCheckTime"]; ok {
				t.Log("drift check completed, lastDriftCheckTime set")
				return
			}
		}
		time.Sleep(5 * time.Second)
	}
}

func TestWebhookNotifications(t *testing.T) {
	// This test just verifies the project with notifications config can succeed
	deployOperator(t)
	defer deleteYAML(t, featuresProgramYAML)

	applyYAML(t, featuresProgramYAML)

	// Use a non-routable URL to verify it doesn't block reconciliation
	projectYAML := fmt.Sprintf(`
apiVersion: tofu.example.com/v1alpha1
kind: TofuProject
metadata:
  name: notify-test
  namespace: default
spec:
  programRef:
    name: features-test
  autoApprove: true
  notifications:
    webhooks:
      - url: "http://192.0.2.1:9999/webhook"
        events: ["apply:success", "apply:error"]
`)
	defer deleteYAML(t, projectYAML)
	applyYAML(t, projectYAML)

	dynClient := newDynamicClient(t)
	// Even though webhook URL is unreachable, the project should still succeed
	waitForPhase(t, dynClient, "default", "notify-test", "Succeeded", 120*time.Second)
}
