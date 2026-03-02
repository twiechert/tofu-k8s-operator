package controllers

import (
	"testing"
	"time"

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

func TestIsWithinApplyWindow_InWindow(t *testing.T) {
	spec := &tofuv1alpha1.ApplyScheduleSpec{
		Schedule: "0 * * * *", // every hour on the hour
		Window:   "30m",
	}
	// 15 minutes past the hour — should be within the 30m window
	now := time.Date(2025, 1, 1, 2, 15, 0, 0, time.UTC)
	inWindow, nextWindow := isWithinApplyWindow(spec, now)
	if !inWindow {
		t.Fatal("expected to be within apply window at 15m past the hour with 30m window")
	}
	// Next window should be the next hour
	expected := time.Date(2025, 1, 1, 3, 0, 0, 0, time.UTC)
	if !nextWindow.Equal(expected) {
		t.Fatalf("expected next window at %v, got %v", expected, nextWindow)
	}
}

func TestIsWithinApplyWindow_OutsideWindow(t *testing.T) {
	spec := &tofuv1alpha1.ApplyScheduleSpec{
		Schedule: "0 * * * *", // every hour on the hour
		Window:   "30m",
	}
	// 45 minutes past the hour — outside the 30m window
	now := time.Date(2025, 1, 1, 2, 45, 0, 0, time.UTC)
	inWindow, nextWindow := isWithinApplyWindow(spec, now)
	if inWindow {
		t.Fatal("expected to be outside apply window at 45m past the hour with 30m window")
	}
	expected := time.Date(2025, 1, 1, 3, 0, 0, 0, time.UTC)
	if !nextWindow.Equal(expected) {
		t.Fatalf("expected next window at %v, got %v", expected, nextWindow)
	}
}

func TestIsWithinApplyWindow_DefaultWindow(t *testing.T) {
	spec := &tofuv1alpha1.ApplyScheduleSpec{
		Schedule: "0 * * * *", // every hour on the hour
		// no Window specified — defaults to 1h
	}
	// 45 minutes past the hour — within the default 1h window
	now := time.Date(2025, 1, 1, 2, 45, 0, 0, time.UTC)
	inWindow, _ := isWithinApplyWindow(spec, now)
	if !inWindow {
		t.Fatal("expected to be within apply window at 45m past the hour with default 1h window")
	}
}

func TestIsWithinApplyWindow_InvalidCron(t *testing.T) {
	spec := &tofuv1alpha1.ApplyScheduleSpec{
		Schedule: "not a cron expression",
	}
	now := time.Now()
	inWindow, nextWindow := isWithinApplyWindow(spec, now)
	if inWindow {
		t.Fatal("expected false for invalid cron expression")
	}
	if !nextWindow.IsZero() {
		t.Fatalf("expected zero time for invalid cron, got %v", nextWindow)
	}
}
