package main

import (
	"log/slog"
	"testing"

	"github.com/AutumnsGrove/Harbor/server/internal/build"
	"github.com/AutumnsGrove/Harbor/server/internal/enrich"
	enrichnova "github.com/AutumnsGrove/Harbor/server/internal/enrich/nova"
	"github.com/AutumnsGrove/Harbor/server/internal/store"
)

// The pipeline's stored versions must come from the adapter that makes the
// calls, not from enrich.New's defaults. A regression here would write an old
// model id against new vectors.
func TestEnrichmentProcessorUsesTheNovaConstants(t *testing.T) {
	library, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer library.Close()

	processor := enrichmentProcessor(build.Config{OpenRouterAPIKey: "test-key"}, library, slog.New(slog.DiscardHandler))
	pipeline, ok := processor.(*enrich.Enricher)
	if !ok {
		t.Fatalf("processor = %T, want *enrich.Enricher", processor)
	}
	if pipeline.EmbeddingModel != enrichnova.EmbedModel {
		t.Errorf("EmbeddingModel = %q, want nova's %q", pipeline.EmbeddingModel, enrichnova.EmbedModel)
	}
	if pipeline.PromptVersion != enrichnova.PromptVersion {
		t.Errorf("PromptVersion = %q, want nova's %q", pipeline.PromptVersion, enrichnova.PromptVersion)
	}
}

func TestEnrichmentProcessorWithoutAKeyIsTheShell(t *testing.T) {
	library, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer library.Close()

	if processor := enrichmentProcessor(build.Config{}, library, slog.New(slog.DiscardHandler)); processor != nil {
		t.Errorf("processor = %T, want nil (the phase-2 shell)", processor)
	}
}
