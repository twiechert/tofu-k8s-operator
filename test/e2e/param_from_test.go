//go:build integration
// +build integration

package e2e

import (
	"context"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

const paramFromProgramYAML = `
apiVersion: tofu.example.com/v1alpha1
kind: TofuProgram
metadata:
  name: param-test-prog
  namespace: default
spec:
  providers:
    - name: random
      source: "hashicorp/random"
      version: "~> 3.6"
  programHCL: |
    variable "seed" {
      type    = string
      default = "default"
    }
    resource "random_pet" "name" {
      keepers = {
        seed = var.seed
      }
    }
    output "pet_name" {
      value = random_pet.name.id
    }
`

func TestParamFromConfigMap(t *testing.T) {
	deployOperator(t)
	dynClient := newDynamicClient(t)
	clientset := newClientset(t)

	applyYAML(t, paramFromProgramYAML)
	defer deleteYAML(t, paramFromProgramYAML)

	// Create a ConfigMap with params
	cmYAML := `
apiVersion: v1
kind: ConfigMap
metadata:
  name: param-from-cm
  namespace: default
data:
  seed: "from-configmap"
`
	applyYAML(t, cmYAML)
	defer deleteYAML(t, cmYAML)

	project := `
apiVersion: tofu.example.com/v1alpha1
kind: TofuProject
metadata:
  name: param-from-cm-test
  namespace: default
spec:
  programRef:
    name: param-test-prog
  paramFrom:
    - configMapRef:
        name: param-from-cm
  backend:
    secretSuffix: param-from-cm-test
    namespace: default
  autoApprove: true
`
	applyYAML(t, project)
	defer deleteYAML(t, project)

	waitForPhase(t, dynClient, "default", "param-from-cm-test", "Succeeded", 120*time.Second)

	// Verify the ConfigMap data was used by checking the tfvars ConfigMap
	cm, err := clientset.CoreV1().ConfigMaps("default").Get(context.Background(), "param-from-cm-test-tf", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("failed to get tf configmap: %v", err)
	}
	vars := cm.Data["terraform.tfvars.json"]
	if vars == "" {
		t.Fatal("expected terraform.tfvars.json in configmap")
	}
}

func TestParamFromSecret(t *testing.T) {
	deployOperator(t)
	dynClient := newDynamicClient(t)

	applyYAML(t, paramFromProgramYAML)
	defer deleteYAML(t, paramFromProgramYAML)

	secretYAML := `
apiVersion: v1
kind: Secret
metadata:
  name: param-from-secret
  namespace: default
stringData:
  seed: "from-secret"
`
	applyYAML(t, secretYAML)
	defer deleteYAML(t, secretYAML)

	project := `
apiVersion: tofu.example.com/v1alpha1
kind: TofuProject
metadata:
  name: param-from-secret-test
  namespace: default
spec:
  programRef:
    name: param-test-prog
  paramFrom:
    - secretRef:
        name: param-from-secret
  backend:
    secretSuffix: param-from-secret-test
    namespace: default
  autoApprove: true
`
	applyYAML(t, project)
	defer deleteYAML(t, project)

	waitForPhase(t, dynClient, "default", "param-from-secret-test", "Succeeded", 120*time.Second)
}

func TestParamBindingsConfigMapKeyRef(t *testing.T) {
	deployOperator(t)
	dynClient := newDynamicClient(t)

	applyYAML(t, paramFromProgramYAML)
	defer deleteYAML(t, paramFromProgramYAML)

	cmYAML := `
apiVersion: v1
kind: ConfigMap
metadata:
  name: binding-cm
  namespace: default
data:
  my_seed: "binding-cm-value"
  other: "ignored"
`
	applyYAML(t, cmYAML)
	defer deleteYAML(t, cmYAML)

	project := `
apiVersion: tofu.example.com/v1alpha1
kind: TofuProject
metadata:
  name: binding-cm-test
  namespace: default
spec:
  programRef:
    name: param-test-prog
  paramBindings:
    - name: seed
      configMapKeyRef:
        name: binding-cm
        key: my_seed
  backend:
    secretSuffix: binding-cm-test
    namespace: default
  autoApprove: true
`
	applyYAML(t, project)
	defer deleteYAML(t, project)

	waitForPhase(t, dynClient, "default", "binding-cm-test", "Succeeded", 120*time.Second)
}

func TestParamBindingsSecretKeyRef(t *testing.T) {
	deployOperator(t)
	dynClient := newDynamicClient(t)

	applyYAML(t, paramFromProgramYAML)
	defer deleteYAML(t, paramFromProgramYAML)

	secretYAML := `
apiVersion: v1
kind: Secret
metadata:
  name: binding-secret
  namespace: default
stringData:
  api_token: "secret-binding-value"
`
	applyYAML(t, secretYAML)
	defer deleteYAML(t, secretYAML)

	project := `
apiVersion: tofu.example.com/v1alpha1
kind: TofuProject
metadata:
  name: binding-secret-test
  namespace: default
spec:
  programRef:
    name: param-test-prog
  paramBindings:
    - name: seed
      secretKeyRef:
        name: binding-secret
        key: api_token
  backend:
    secretSuffix: binding-secret-test
    namespace: default
  autoApprove: true
`
	applyYAML(t, project)
	defer deleteYAML(t, project)

	waitForPhase(t, dynClient, "default", "binding-secret-test", "Succeeded", 120*time.Second)
}

func TestParamPrecedence(t *testing.T) {
	deployOperator(t)
	dynClient := newDynamicClient(t)
	clientset := newClientset(t)

	applyYAML(t, paramFromProgramYAML)
	defer deleteYAML(t, paramFromProgramYAML)

	cmYAML := `
apiVersion: v1
kind: ConfigMap
metadata:
  name: precedence-cm
  namespace: default
data:
  seed: "from-paramFrom"
`
	applyYAML(t, cmYAML)
	defer deleteYAML(t, cmYAML)

	// inline params should override paramFrom
	project := `
apiVersion: tofu.example.com/v1alpha1
kind: TofuProject
metadata:
  name: precedence-test
  namespace: default
spec:
  programRef:
    name: param-test-prog
  paramFrom:
    - configMapRef:
        name: precedence-cm
  params:
    seed: "from-inline"
  backend:
    secretSuffix: precedence-test
    namespace: default
  autoApprove: true
`
	applyYAML(t, project)
	defer deleteYAML(t, project)

	waitForPhase(t, dynClient, "default", "precedence-test", "Succeeded", 120*time.Second)

	// Verify inline param won: check tfvars in the operator-created ConfigMap
	cm, err := clientset.CoreV1().ConfigMaps("default").Get(context.Background(), "precedence-test-tf", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("failed to get tf configmap: %v", err)
	}
	vars := cm.Data["terraform.tfvars.json"]
	if vars == "" {
		t.Fatal("expected terraform.tfvars.json in configmap")
	}
	// The inline value "from-inline" should be present, not "from-paramFrom"
	if !strings.Contains(vars, "from-inline") {
		t.Fatalf("expected inline param to win, got: %s", vars)
	}
}

func TestParamFromConfigMapWatch(t *testing.T) {
	deployOperator(t)
	dynClient := newDynamicClient(t)
	clientset := newClientset(t)

	applyYAML(t, paramFromProgramYAML)
	defer deleteYAML(t, paramFromProgramYAML)

	cmYAML := `
apiVersion: v1
kind: ConfigMap
metadata:
  name: watch-cm
  namespace: default
data:
  seed: "initial-value"
`
	applyYAML(t, cmYAML)
	defer deleteYAML(t, cmYAML)

	project := `
apiVersion: tofu.example.com/v1alpha1
kind: TofuProject
metadata:
  name: watch-cm-test
  namespace: default
spec:
  programRef:
    name: param-test-prog
  paramFrom:
    - configMapRef:
        name: watch-cm
  backend:
    secretSuffix: watch-cm-test
    namespace: default
  autoApprove: true
`
	applyYAML(t, project)
	defer deleteYAML(t, project)

	// Wait for initial apply to succeed
	waitForPhase(t, dynClient, "default", "watch-cm-test", "Succeeded", 120*time.Second)

	// Record the lastAppliedHash before update
	obj, err := dynClient.Resource(tofuProjectGVR).Namespace("default").Get(context.Background(), "watch-cm-test", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("failed to get project: %v", err)
	}
	status, _, _ := unstructured.NestedMap(obj.Object, "status")
	oldHash, _ := status["lastAppliedHash"].(string)
	if oldHash == "" {
		t.Fatal("expected lastAppliedHash to be set")
	}

	// Update ConfigMap — this should trigger re-reconciliation
	patch := []byte(`{"data":{"seed":"updated-value"}}`)
	_, err = clientset.CoreV1().ConfigMaps("default").Patch(
		context.Background(), "watch-cm", types.MergePatchType, patch, metav1.PatchOptions{},
	)
	if err != nil {
		t.Fatalf("failed to update ConfigMap: %v", err)
	}

	// Wait for Succeeded again with a different hash
	time.Sleep(5 * time.Second) // give the watch time to trigger
	waitForPhase(t, dynClient, "default", "watch-cm-test", "Succeeded", 120*time.Second)

	obj, err = dynClient.Resource(tofuProjectGVR).Namespace("default").Get(context.Background(), "watch-cm-test", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("failed to get project after update: %v", err)
	}
	status, _, _ = unstructured.NestedMap(obj.Object, "status")
	newHash, _ := status["lastAppliedHash"].(string)
	if newHash == oldHash {
		t.Fatalf("expected hash to change after ConfigMap update, still %s", oldHash)
	}
}

