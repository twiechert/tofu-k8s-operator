//go:build integration
// +build integration

package e2e

import (
	"testing"
	"time"
)

func TestTofuOperatorE2E(t *testing.T) {
	t.Parallel()

	// Apply example TofuProgram and TofuProject
	kubectl(t, "apply", "-k", "../../examples/")
	// Only clean up the project, not the shared program
	defer kubectlMayFail("delete", "tofuproject", "hello-run", "-n", "default", "--ignore-not-found")

	dynClient := newDynamicClient(t)
	waitForPhase(t, dynClient, "default", "hello-run", "Succeeded", 120*time.Second)
}
