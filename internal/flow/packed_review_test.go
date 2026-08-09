package flow

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"sophon/internal/domain"
	"sophon/internal/readcode"
	"sophon/internal/store"
)

// TestPackedBrowserProductIngestAndDeliveryGate is opt-in release evidence.
// The harness opens and drives an explicitly packed Read the Code product in
// a real browser, then supplies only its non-secret repo/revision identity.
// This test idempotently resumes that exact session through the public CLI,
// ingests the browser submissions into a fresh Sophon home, and proves the
// required-review delivery gate. It never reads product state directly.
func TestPackedBrowserProductIngestAndDeliveryGate(t *testing.T) {
	binary := strings.TrimSpace(os.Getenv("SOPHON_READ_CODE_BROWSER_BIN"))
	repo := strings.TrimSpace(os.Getenv("SOPHON_READ_CODE_BROWSER_REPO"))
	base := strings.TrimSpace(os.Getenv("SOPHON_READ_CODE_BROWSER_BASE"))
	head := strings.TrimSpace(os.Getenv("SOPHON_READ_CODE_BROWSER_HEAD"))
	if binary == "" || repo == "" || base == "" || head == "" {
		t.Skip("set packed browser product binary, repo, base, and head environment values")
	}
	useHome(t)
	rig := newRig()
	rig.git.baseSHA = base
	rig.git.headSHA = head
	rig.leases.alloc.WorktreePath = repo
	rig.flow.deps.ReviewProduct = readcode.Client{Binary: binary}
	ctx := context.Background()
	mission, err := rig.flow.CreateMission(ctx, repo, "Packed browser review", "Prove the real product integration.")
	if err != nil {
		t.Fatal(err)
	}
	task, err := rig.flow.CreateTask(ctx, mission.ID, "Deliver reviewed browser proof",
		"Ingest and honor the exact browser review.", "feature/browser-proof", "", domain.DeliveryBranch,
		"go test ./...", domain.ReviewRequired)
	if err != nil {
		t.Fatal(err)
	}
	spawn, err := rig.flow.Spawn(ctx, task.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	home := os.Getenv("SOPHON_DATA_HOME")
	resultPath := writeResult(t, home, spawn, validResult)
	if _, err := rig.flow.PublishResult(ctx, task.ID, spawn.Attempt, head, resultPath); err != nil {
		t.Fatal(err)
	}
	rig.leaseStatus(spawn)
	if _, err := rig.flow.VerifyComplete(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := rig.flow.Validate(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	opened, err := rig.flow.ReviewOpen(ctx, task.ID, spawn.Attempt, true)
	if err != nil {
		t.Fatal(err)
	}
	if opened.BaseSHA != base || opened.HeadSHA != head || !opened.Resumed {
		t.Fatalf("packed browser review did not resume exact revision: %+v", opened)
	}
	reconciled, err := rig.flow.ReviewReconcile(ctx, task.ID)
	if err != nil || reconciled.Cursor != 2 || len(reconciled.Ingested) != 2 {
		t.Fatalf("packed browser reconcile = %+v, %v", reconciled, err)
	}
	feedback, err := rig.flow.ReviewFeedback(task.ID, 0, 10)
	if err != nil || len(feedback.Events) != 1 || len(feedback.Events[0].Comments) != 2 {
		t.Fatalf("packed browser feedback = %+v, %v", feedback, err)
	}
	comments := feedback.Events[0].Comments
	if comments[0].Scope != "line" || comments[0].Body != "testing" || comments[0].Path != "README.md" ||
		comments[0].Anchor == nil || comments[0].Anchor.Side != "new" || comments[0].Anchor.StartLine != 95 ||
		comments[1].Scope != "general" || comments[1].Body != "general browser proof" {
		t.Fatalf("packed browser comments lost exact scope/anchor/body: %+v", comments)
	}
	if _, err := rig.flow.Deliver(ctx, task.ID, true); !errors.Is(err, ErrReviewRequired) {
		t.Fatalf("unclassified packed feedback did not block delivery: %v", err)
	}
	if _, err := rig.flow.ClassifyReviewFeedback(ctx, task.ID, 1, store.ReviewDispositionNonActionable); err != nil {
		t.Fatal(err)
	}
	status := DeriveReview(mustReloadTask(t, task))
	if !status.ApprovalEligible || status.LatestApprovalSequence != 2 || status.HeadSHA != head {
		t.Fatalf("packed browser exact-head approval is not eligible: %+v", status)
	}
	if _, err := rig.flow.Deliver(ctx, task.ID, false); !errors.Is(err, ErrNotConfirmed) {
		t.Fatalf("packed approval replaced explicit delivery confirmation: %v", err)
	}
	if _, err := rig.flow.Deliver(ctx, task.ID, true); err != nil {
		t.Fatal(err)
	}
	if rig.remote.pushes != 1 {
		t.Fatalf("confirmed packed exact-head delivery pushes = %d", rig.remote.pushes)
	}
}
