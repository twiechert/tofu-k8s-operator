package controllers

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	tofuv1alpha1 "github.com/twiechert/tofu-k8s-operator/api/v1alpha1"
)

func newTestReconciler(objs ...runtime.Object) *TofuProjectReconciler {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = tofuv1alpha1.AddToScheme(scheme)

	clientObjs := make([]runtime.Object, len(objs))
	copy(clientObjs, objs)

	cb := fake.NewClientBuilder().WithScheme(scheme)
	for _, o := range clientObjs {
		cb = cb.WithRuntimeObjects(o)
	}
	return &TofuProjectReconciler{
		Client: cb.Build(),
		Scheme: scheme,
	}
}

func TestResolveExternalParams_ParamFrom_ConfigMap(t *testing.T) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "shared-config", Namespace: "default"},
		Data:       map[string]string{"region": "us-east-1", "env": "prod"},
	}
	r := newTestReconciler(cm)

	project := &tofuv1alpha1.TofuProject{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		Spec: tofuv1alpha1.TofuProjectSpec{
			ParamFrom: []tofuv1alpha1.ParamFromSource{
				{ConfigMapRef: &tofuv1alpha1.ObjectRef{Name: "shared-config"}},
			},
		},
	}

	resolved, hashStr, err := r.resolveExternalParams(context.Background(), project)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolved["region"] != "us-east-1" {
		t.Fatalf("expected region=us-east-1, got %q", resolved["region"])
	}
	if resolved["env"] != "prod" {
		t.Fatalf("expected env=prod, got %q", resolved["env"])
	}
	if hashStr == "" {
		t.Fatal("expected non-empty hash string")
	}
}

func TestResolveExternalParams_ParamFrom_Secret(t *testing.T) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "db-creds", Namespace: "default"},
		Data:       map[string][]byte{"password": []byte("s3cret"), "username": []byte("admin")},
	}
	r := newTestReconciler(secret)

	project := &tofuv1alpha1.TofuProject{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		Spec: tofuv1alpha1.TofuProjectSpec{
			ParamFrom: []tofuv1alpha1.ParamFromSource{
				{SecretRef: &tofuv1alpha1.ObjectRef{Name: "db-creds"}},
			},
		},
	}

	resolved, _, err := r.resolveExternalParams(context.Background(), project)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolved["password"] != "s3cret" {
		t.Fatalf("expected password=s3cret, got %q", resolved["password"])
	}
	if resolved["username"] != "admin" {
		t.Fatalf("expected username=admin, got %q", resolved["username"])
	}
}

func TestResolveExternalParams_ParamBindings_ConfigMapKeyRef(t *testing.T) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "app-config", Namespace: "default"},
		Data:       map[string]string{"database_host": "db.example.com", "other": "ignored"},
	}
	r := newTestReconciler(cm)

	project := &tofuv1alpha1.TofuProject{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		Spec: tofuv1alpha1.TofuProjectSpec{
			ParamBindings: []tofuv1alpha1.ParamBinding{
				{
					Name:            "db_host",
					ConfigMapKeyRef: &tofuv1alpha1.KeyRef{Name: "app-config", Key: "database_host"},
				},
			},
		},
	}

	resolved, _, err := r.resolveExternalParams(context.Background(), project)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolved["db_host"] != "db.example.com" {
		t.Fatalf("expected db_host=db.example.com, got %q", resolved["db_host"])
	}
	if _, ok := resolved["other"]; ok {
		t.Fatal("should not include non-referenced keys")
	}
}

func TestResolveExternalParams_ParamBindings_SecretKeyRef(t *testing.T) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "api-keys", Namespace: "default"},
		Data:       map[string][]byte{"token": []byte("abc123")},
	}
	r := newTestReconciler(secret)

	project := &tofuv1alpha1.TofuProject{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		Spec: tofuv1alpha1.TofuProjectSpec{
			ParamBindings: []tofuv1alpha1.ParamBinding{
				{
					Name:         "api_token",
					SecretKeyRef: &tofuv1alpha1.KeyRef{Name: "api-keys", Key: "token"},
				},
			},
		},
	}

	resolved, _, err := r.resolveExternalParams(context.Background(), project)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolved["api_token"] != "abc123" {
		t.Fatalf("expected api_token=abc123, got %q", resolved["api_token"])
	}
}

func TestResolveExternalParams_Precedence(t *testing.T) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "bulk", Namespace: "default"},
		Data:       map[string]string{"key1": "from-paramFrom", "key2": "bulk-value"},
	}
	cmBinding := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "binding-cm", Namespace: "default"},
		Data:       map[string]string{"override": "from-paramBindings"},
	}
	r := newTestReconciler(cm, cmBinding)

	project := &tofuv1alpha1.TofuProject{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		Spec: tofuv1alpha1.TofuProjectSpec{
			ParamFrom: []tofuv1alpha1.ParamFromSource{
				{ConfigMapRef: &tofuv1alpha1.ObjectRef{Name: "bulk"}},
			},
			ParamBindings: []tofuv1alpha1.ParamBinding{
				{
					Name:            "key1",
					ConfigMapKeyRef: &tofuv1alpha1.KeyRef{Name: "binding-cm", Key: "override"},
				},
			},
		},
	}

	resolved, _, err := r.resolveExternalParams(context.Background(), project)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// paramBindings should override paramFrom for key1
	if resolved["key1"] != "from-paramBindings" {
		t.Fatalf("expected key1=from-paramBindings, got %q", resolved["key1"])
	}
	// key2 should remain from paramFrom
	if resolved["key2"] != "bulk-value" {
		t.Fatalf("expected key2=bulk-value, got %q", resolved["key2"])
	}
}

func TestResolveExternalParams_MissingConfigMap(t *testing.T) {
	r := newTestReconciler()

	project := &tofuv1alpha1.TofuProject{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		Spec: tofuv1alpha1.TofuProjectSpec{
			ParamFrom: []tofuv1alpha1.ParamFromSource{
				{ConfigMapRef: &tofuv1alpha1.ObjectRef{Name: "does-not-exist"}},
			},
		},
	}

	_, _, err := r.resolveExternalParams(context.Background(), project)
	if err == nil {
		t.Fatal("expected error for missing ConfigMap")
	}
}

func TestResolveExternalParams_MissingKey(t *testing.T) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "app-config", Namespace: "default"},
		Data:       map[string]string{"existing": "value"},
	}
	r := newTestReconciler(cm)

	project := &tofuv1alpha1.TofuProject{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		Spec: tofuv1alpha1.TofuProjectSpec{
			ParamBindings: []tofuv1alpha1.ParamBinding{
				{
					Name:            "missing",
					ConfigMapKeyRef: &tofuv1alpha1.KeyRef{Name: "app-config", Key: "nonexistent"},
				},
			},
		},
	}

	_, _, err := r.resolveExternalParams(context.Background(), project)
	if err == nil {
		t.Fatal("expected error for missing key")
	}
}

func TestResolveExternalParams_DeterministicHash(t *testing.T) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "config", Namespace: "default"},
		Data:       map[string]string{"b": "2", "a": "1", "c": "3"},
	}
	r := newTestReconciler(cm)

	project := &tofuv1alpha1.TofuProject{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		Spec: tofuv1alpha1.TofuProjectSpec{
			ParamFrom: []tofuv1alpha1.ParamFromSource{
				{ConfigMapRef: &tofuv1alpha1.ObjectRef{Name: "config"}},
			},
		},
	}

	// Run twice — hash must be identical
	_, hash1, err1 := r.resolveExternalParams(context.Background(), project)
	_, hash2, err2 := r.resolveExternalParams(context.Background(), project)
	if err1 != nil || err2 != nil {
		t.Fatalf("unexpected errors: %v, %v", err1, err2)
	}
	if hash1 != hash2 {
		t.Fatalf("hash not deterministic: %q vs %q", hash1, hash2)
	}
	if hash1 == "" {
		t.Fatal("expected non-empty hash")
	}
}

func TestProjectReferencesConfigMap(t *testing.T) {
	project := &tofuv1alpha1.TofuProject{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		Spec: tofuv1alpha1.TofuProjectSpec{
			ParamFrom: []tofuv1alpha1.ParamFromSource{
				{ConfigMapRef: &tofuv1alpha1.ObjectRef{Name: "shared-config"}},
			},
			ParamBindings: []tofuv1alpha1.ParamBinding{
				{Name: "val", ConfigMapKeyRef: &tofuv1alpha1.KeyRef{Name: "binding-cm", Key: "k"}},
			},
		},
	}

	if !projectReferencesConfigMap(project, "shared-config", "default") {
		t.Fatal("expected project to reference shared-config via paramFrom")
	}
	if !projectReferencesConfigMap(project, "binding-cm", "default") {
		t.Fatal("expected project to reference binding-cm via paramBindings")
	}
	if projectReferencesConfigMap(project, "other-cm", "default") {
		t.Fatal("should not reference unrelated ConfigMap")
	}
	if projectReferencesConfigMap(project, "shared-config", "other-ns") {
		t.Fatal("should not match different namespace")
	}
}

func TestProjectReferencesSecret(t *testing.T) {
	project := &tofuv1alpha1.TofuProject{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		Spec: tofuv1alpha1.TofuProjectSpec{
			ParamFrom: []tofuv1alpha1.ParamFromSource{
				{SecretRef: &tofuv1alpha1.ObjectRef{Name: "db-creds"}},
			},
			ParamBindings: []tofuv1alpha1.ParamBinding{
				{Name: "tok", SecretKeyRef: &tofuv1alpha1.KeyRef{Name: "api-keys", Key: "token"}},
			},
		},
	}

	if !projectReferencesSecret(project, "db-creds", "default") {
		t.Fatal("expected project to reference db-creds via paramFrom")
	}
	if !projectReferencesSecret(project, "api-keys", "default") {
		t.Fatal("expected project to reference api-keys via paramBindings")
	}
	if projectReferencesSecret(project, "other-secret", "default") {
		t.Fatal("should not reference unrelated Secret")
	}
	if projectReferencesSecret(project, "db-creds", "other-ns") {
		t.Fatal("should not match different namespace")
	}
}
