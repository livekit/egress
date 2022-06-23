package tracer

import (
	"context"
)

type Tracer interface {
	Start(ctx context.Context, spanName string) (context.Context, Span)
}

type Span interface {
	End()
}

type NoOpTracer struct{}

func (t *NoOpTracer) Start(ctx context.Context, _ string) (context.Context, Span) {
	return ctx, &NoOpSpan{}
}

type NoOpSpan struct{}

func (s *NoOpSpan) End() {}

var tracer Tracer = &NoOpTracer{}

// Can be used for your own tracing (for example, with Lightstep)
func SetTracer(t Tracer) {
	tracer = t
}

func Start(ctx context.Context, spanName string) (context.Context, Span) {
	return tracer.Start(ctx, spanName)
}
