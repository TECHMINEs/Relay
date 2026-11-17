package metrics

import "context"

type contextKey int

const ctxLabels contextKey = iota

// RequestLabels is a mutable bag of per-request label values. Middleware puts a
// pointer to it in the context so handlers can set provider and model on a
// request copy and the middleware still reads them back. Only touched by the
// goroutine serving the request.
type RequestLabels struct {
	Provider string
	Model    string
}

// labelsFromContext retrieves the *RequestLabels injected by Middleware.
// Returns nil if not set (e.g. in tests that bypass the middleware).
func labelsFromContext(ctx context.Context) *RequestLabels {
	v, _ := ctx.Value(ctxLabels).(*RequestLabels)
	return v
}

// contextWithLabels stores an empty *RequestLabels bag into the context.
// Called only by Middleware so every request has a writable bag.
func contextWithLabels(ctx context.Context) (context.Context, *RequestLabels) {
	lb := &RequestLabels{}
	return context.WithValue(ctx, ctxLabels, lb), lb
}

// ContextWithProvider writes the provider name into the per-request label bag.
// If no bag is present (middleware not active) the call is a no-op.
func ContextWithProvider(ctx context.Context, provider string) context.Context {
	if lb := labelsFromContext(ctx); lb != nil {
		lb.Provider = provider
	}
	return ctx
}

// ProviderFromContext reads the provider name from the per-request label bag.
// Returns "unknown" if not set.
func ProviderFromContext(ctx context.Context) string {
	if lb := labelsFromContext(ctx); lb != nil && lb.Provider != "" {
		return lb.Provider
	}
	return "unknown"
}

// ContextWithModel writes the model name into the per-request label bag.
// If no bag is present (middleware not active) the call is a no-op.
func ContextWithModel(ctx context.Context, model string) context.Context {
	if lb := labelsFromContext(ctx); lb != nil {
		lb.Model = model
	}
	return ctx
}

// ModelFromContext reads the model name from the per-request label bag.
// Returns "unknown" if not set.
func ModelFromContext(ctx context.Context) string {
	if lb := labelsFromContext(ctx); lb != nil && lb.Model != "" {
		return lb.Model
	}
	return "unknown"
}
