package controllers

import (
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"

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

func TestParseJobTimeout_Default(t *testing.T) {
	d := parseJobTimeout("")
	if d != 30*time.Minute {
		t.Fatalf("expected 30m, got %v", d)
	}
}

func TestParseJobTimeout_Valid(t *testing.T) {
	d := parseJobTimeout("1h")
	if d != time.Hour {
		t.Fatalf("expected 1h, got %v", d)
	}
}

func TestParseJobTimeout_Invalid(t *testing.T) {
	d := parseJobTimeout("notaduration")
	if d != 30*time.Minute {
		t.Fatalf("expected 30m for invalid input, got %v", d)
	}
}

func TestParseJobTimeout_Negative(t *testing.T) {
	d := parseJobTimeout("-5m")
	if d != 30*time.Minute {
		t.Fatalf("expected 30m for negative input, got %v", d)
	}
}

func TestIsStateLockError_AcquiringLock(t *testing.T) {
	logs := "Error: Error acquiring the state lock\nsome other output"
	if !isStateLockError(logs) {
		t.Fatal("expected true for 'Error acquiring the state lock'")
	}
}

func TestIsStateLockError_LockingState(t *testing.T) {
	logs := "Error locking state: some reason"
	if !isStateLockError(logs) {
		t.Fatal("expected true for 'Error locking state'")
	}
}

func TestIsStateLockError_NoLock(t *testing.T) {
	logs := "Apply complete! Resources: 1 added, 0 changed, 0 destroyed."
	if isStateLockError(logs) {
		t.Fatal("expected false for normal output")
	}
}

func TestIsStateLockError_Empty(t *testing.T) {
	if isStateLockError("") {
		t.Fatal("expected false for empty string")
	}
}

func TestSetJobTimeout(t *testing.T) {
	project := &tofuv1alpha1.TofuProject{}
	project.Spec.JobTimeout = "45m"
	job := &batchv1.Job{}
	setJobTimeout(job, project)
	if job.Spec.ActiveDeadlineSeconds == nil {
		t.Fatal("expected ActiveDeadlineSeconds to be set")
	}
	if *job.Spec.ActiveDeadlineSeconds != 2700 {
		t.Fatalf("expected 2700 seconds, got %d", *job.Spec.ActiveDeadlineSeconds)
	}
}

func TestSetJobTimeout_Default(t *testing.T) {
	project := &tofuv1alpha1.TofuProject{}
	job := &batchv1.Job{}
	setJobTimeout(job, project)
	if job.Spec.ActiveDeadlineSeconds == nil {
		t.Fatal("expected ActiveDeadlineSeconds to be set")
	}
	if *job.Spec.ActiveDeadlineSeconds != 1800 {
		t.Fatalf("expected 1800 seconds (30m default), got %d", *job.Spec.ActiveDeadlineSeconds)
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
