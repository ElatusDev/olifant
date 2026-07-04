//go:build integration

package validate_test

import (
	"context"
	"testing"
	"time"

	"github.com/ElatusDev/olifant/internal/challenge"
	"github.com/ElatusDev/olifant/internal/livetest"
	"github.com/ElatusDev/olifant/internal/validate"
)

// TestLive_ValidateRun runs the post-Claude claim-vs-diff validator against the
// live stack with grounding retrieval + the Ollama synth backend.
func TestLive_ValidateRun(t *testing.T) {
	rt := livetest.RequireStack(t)
	platformRoot, kbRoot := livetest.RequireKB(t)

	validator, err := challenge.NewCiteValidator(platformRoot, kbRoot)
	if err != nil {
		t.Fatalf("NewCiteValidator: %v", err)
	}

	// Up to two synth calls (initial + one retry on weak assessment); give
	// headroom beyond a single-call budget.
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	const diff = `diff --git a/InvoiceDataModel.java b/InvoiceDataModel.java
+@SQLDelete(sql = "UPDATE invoice SET deleted = true WHERE tenant_id = ? AND invoice_id = ?")
+public class InvoiceDataModel {
+  Long tenantId;
+  Long invoiceId;
+}`

	res, err := validate.Run(ctx, validate.Config{
		Claim:              "Added a TenantScoped InvoiceDataModel with a composite key and soft delete.",
		Diff:               diff,
		OllamaURL:          rt.OllamaURL,
		ChromaURL:          rt.ChromaURL,
		Embedder:           rt.Embedder,
		Synthesizer:        rt.Synthesizer,
		Tenant:             rt.ChromaTenant,
		Database:           rt.ChromaDatabase,
		Scopes:             []string{"backend"},
		Validator:          validator,
		MaxValidateRetries: 1,
	})
	if err != nil {
		t.Fatalf("validate.Run: %v", err)
	}
	if !res.JSONValid {
		t.Errorf("synth output not valid JSON:\n%s", res.RawJSON)
	}
	verdict, proceed := res.ExtractVerdict()
	if verdict == "" {
		t.Errorf("empty overall_verdict:\n%s", res.RawJSON)
	}
	t.Logf("verdict=%s proceed=%s retrieved=%d attempts=%d",
		verdict, proceed, res.RetrievedCount, res.ValidateAttempts)
}
