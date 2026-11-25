package enrich

import (
	"context"

	"github.com/AutumnsGrove/Harbor/server/internal/store"
)

// What the models cost. The adapter owns the usage (it is what the provider
// reports) and the pipeline owns the store, so the interface lives here next to
// Decider, Writer and Embedder and store.Store implements it (issue #52). The
// row type is store.Usage rather than a second copy: one shape, one place that
// knows the columns, no bridge to keep in step.

// UsageSink records one call. An error is a warning, never a failed enrichment:
// the answer is already in hand and the call has already been paid for.
type UsageSink interface {
	RecordUsage(ctx context.Context, usage store.Usage) error
}

// usageItemKey carries the item id from the pipeline to the adapter without
// threading it through every model signature.
type usageItemKey struct{}

// WithUsageItem marks the context as belonging to one item. The pipeline calls
// this once per Process; the adapter reads it back when it records a call.
func WithUsageItem(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, usageItemKey{}, id)
}

// UsageItem returns the item id set by WithUsageItem, or "".
func UsageItem(ctx context.Context) string {
	id, _ := ctx.Value(usageItemKey{}).(string)
	return id
}
