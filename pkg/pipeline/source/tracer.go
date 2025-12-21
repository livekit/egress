package source

import "go.opentelemetry.io/otel"

var (
	tracer = otel.Tracer("github.com/livekit/egress/pkg/pipeline/source")
)
