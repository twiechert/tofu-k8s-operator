package controllers

import (
	"testing"

	tofuv1alpha1 "github.com/twiechert/tofu-k8s-operator/api/v1alpha1"
)

func TestParsePlanCounts(t *testing.T) {
	br := parsePlanCounts("Plan: 2 to add, 0 to change, 1 to destroy.")
	if br == nil {
		t.Fatal("expected non-nil result")
	}
	expect := tofuv1alpha1.BlastRadiusSummary{Add: 2, Change: 0, Destroy: 1, Total: 3}
	if *br != expect {
		t.Fatalf("expected %+v, got %+v", expect, *br)
	}
}

func TestParsePlanCounts_NoChanges(t *testing.T) {
	br := parsePlanCounts("No changes.")
	if br == nil {
		t.Fatal("expected non-nil result for 'No changes.'")
	}
	expect := tofuv1alpha1.BlastRadiusSummary{}
	if *br != expect {
		t.Fatalf("expected %+v, got %+v", expect, *br)
	}
}

func TestParsePlanCounts_Empty(t *testing.T) {
	br := parsePlanCounts("")
	if br != nil {
		t.Fatalf("expected nil for empty string, got %+v", *br)
	}
}

func TestParsePlanCounts_Unparseable(t *testing.T) {
	br := parsePlanCounts("some random text that is not a plan summary")
	if br != nil {
		t.Fatalf("expected nil for unparseable text, got %+v", *br)
	}
}

func TestParsePlanCounts_LargeNumbers(t *testing.T) {
	br := parsePlanCounts("Plan: 100 to add, 50 to change, 25 to destroy.")
	if br == nil {
		t.Fatal("expected non-nil result")
	}
	if br.Total != 175 {
		t.Fatalf("expected total 175, got %d", br.Total)
	}
}
