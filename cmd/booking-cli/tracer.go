package main

import (
	"context"
	"fmt"
	stdlog "log"
	"os"
	"strconv"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.17.0"
)

// otelServiceNameDefault is the OTel service.name when OTEL_SERVICE_NAME
// isn't set in the environment. OTel convention: prefer the env var;
// the const is purely a fallback for local runs / smoke tests.
const otelServiceNameDefault = "booking-service"

// initTracer constructs the OTLP tracer provider. Errors are fatal: a
// nil exporter would make the first span-export call panic.
func initTracer() (*trace.TracerProvider, error) {
	ctx := context.Background()

	svcName := os.Getenv("OTEL_SERVICE_NAME")
	if svcName == "" {
		svcName = otelServiceNameDefault
	}

	res, err := resource.New(ctx, resource.WithAttributes(semconv.ServiceName(svcName)))
	if err != nil {
		return nil, fmt.Errorf("initTracer: resource.New: %w", err)
	}

	// WithInsecure is fine for the in-cluster Jaeger collector; gate on
	// OTEL_EXPORTER_OTLP_INSECURE once we talk to a remote collector.
	traceExporter, err := otlptracegrpc.New(ctx, otlptracegrpc.WithInsecure())
	if err != nil {
		return nil, fmt.Errorf("initTracer: otlptracegrpc.New: %w", err)
	}

	tp := trace.NewTracerProvider(
		trace.WithSampler(resolveSampler()),
		trace.WithResource(res),
		trace.WithBatcher(traceExporter),
	)
	otel.SetTracerProvider(tp)
	return tp, nil
}

// resolveSampler picks the trace sampler based on OTEL_TRACES_SAMPLER_RATIO.
// Accepted values:
//   - unset/empty: AlwaysSample (backwards-compat default)
//   - "0":         NeverSample
//   - "1"/"1.0":   AlwaysSample
//   - 0<r<1:       TraceIDRatioBased(r)
//   - else:        warn + fallback to AlwaysSample
func resolveSampler() trace.Sampler {
	raw := strings.TrimSpace(os.Getenv("OTEL_TRACES_SAMPLER_RATIO"))
	if raw == "" {
		return trace.AlwaysSample()
	}
	r, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		stdlog.Printf("initTracer: invalid OTEL_TRACES_SAMPLER_RATIO=%q, falling back to AlwaysSample: %v", raw, err)
		return trace.AlwaysSample()
	}
	switch {
	case r <= 0:
		return trace.NeverSample()
	case r >= 1:
		return trace.AlwaysSample()
	default:
		return trace.TraceIDRatioBased(r)
	}
}
