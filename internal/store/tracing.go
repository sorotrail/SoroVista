package store

import (
	"context"

	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/sorotrail/sorovista/internal/tracing"
)

// queryTracer implements pgx.QueryTracer, which is the hook pgx provides for
// wrapping every query without hand-instrumenting each store method.
type queryTracer struct {
	tracer *tracing.Tracer
}

func (q queryTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	ctx, _ = q.tracer.Start(ctx, "postgres.query",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("db.system", "postgresql"),
			// The statement carries placeholders, not parameter values, so it
			// does not leak event data or grow cardinality with arguments.
			attribute.String("db.statement", data.SQL),
		),
	)
	return ctx
}

func (q queryTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	span := trace.SpanFromContext(ctx)
	if data.Err != nil {
		span.RecordError(data.Err)
		span.SetStatus(codes.Error, data.Err.Error())
	}
	span.End()
}
