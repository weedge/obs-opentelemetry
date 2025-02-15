// Copyright 2022 CloudWeGo Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package provider

import (
	"context"

	"github.com/cloudwego/kitex/pkg/klog"
	runtimemetrics "go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/metric/global"
	controller "go.opentelemetry.io/otel/sdk/metric/controller/basic"
	"go.opentelemetry.io/otel/sdk/metric/export/aggregation"
	processor "go.opentelemetry.io/otel/sdk/metric/processor/basic"
	"go.opentelemetry.io/otel/sdk/metric/selector/simple"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

type OtelProvider interface {
	Shutdown(ctx context.Context) error
}

type otelProvider struct {
	traceExp      *otlptrace.Exporter
	metricsPusher *controller.Controller
}

func (p *otelProvider) Shutdown(ctx context.Context) error {
	var err error

	if p.traceExp != nil {
		if err = p.traceExp.Shutdown(ctx); err != nil {
			otel.Handle(err)
		}
	}

	if p.metricsPusher != nil {
		if err = p.metricsPusher.Stop(ctx); err != nil {
			otel.Handle(err)
		}
	}

	return err
}

// NewOpenTelemetryProvider Initializes an otlp trace and metrics provider
func NewOpenTelemetryProvider(opts ...Option) *otelProvider {
	var (
		err           error
		traceExp      *otlptrace.Exporter
		metricsPusher *controller.Controller
	)

	ctx := context.TODO()

	cfg := newConfig(opts)

	if !cfg.enableTracing && !cfg.enableMetrics {
		return nil
	}

	// resource
	res := newResource(cfg)

	// propagator
	otel.SetTextMapPropagator(cfg.textMapPropagator)

	// Tracing
	if cfg.enableTracing {
		// trace client
		var traceClientOpts []otlptracegrpc.Option
		if cfg.exportEndpoint != "" {
			traceClientOpts = append(traceClientOpts, otlptracegrpc.WithEndpoint(cfg.exportEndpoint))
		}
		if len(cfg.exportHeaders) > 0 {
			traceClientOpts = append(traceClientOpts, otlptracegrpc.WithHeaders(cfg.exportHeaders))
		}
		if cfg.exportInsecure {
			traceClientOpts = append(traceClientOpts, otlptracegrpc.WithInsecure())
		}

		traceClient := otlptracegrpc.NewClient(traceClientOpts...)

		// trace exporter
		traceExp, err = otlptrace.New(ctx, traceClient)
		if err != nil {
			klog.Fatalf("failed to create otlp trace exporter: %s", err)
			return nil
		}

		// trace processor
		bsp := sdktrace.NewBatchSpanProcessor(traceExp)

		// trace provider
		tracerProvider := cfg.sdkTracerProvider
		if tracerProvider == nil {
			tracerProvider = sdktrace.NewTracerProvider(
				sdktrace.WithSampler(cfg.sampler),
				sdktrace.WithResource(res),
				sdktrace.WithSpanProcessor(bsp),
			)
		}

		otel.SetTracerProvider(tracerProvider)
	}

	// Metrics
	if cfg.enableMetrics {
		// prometheus only supports CumulativeTemporalitySelector
		exportKindSelector := aggregation.CumulativeTemporalitySelector()

		var metricsClientOpts []otlpmetricgrpc.Option
		if cfg.exportEndpoint != "" {
			metricsClientOpts = append(metricsClientOpts, otlpmetricgrpc.WithEndpoint(cfg.exportEndpoint))
		}
		if len(cfg.exportHeaders) > 0 {
			metricsClientOpts = append(metricsClientOpts, otlpmetricgrpc.WithHeaders(cfg.exportHeaders))
		}
		if cfg.exportInsecure {
			metricsClientOpts = append(metricsClientOpts, otlpmetricgrpc.WithInsecure())
		}

		// metrics client
		metricClient := otlpmetricgrpc.NewClient(metricsClientOpts...)

		// metrics exporter
		metricExp, err := otlpmetric.New(ctx, metricClient, otlpmetric.WithMetricAggregationTemporalitySelector(exportKindSelector))
		handleInitErr(err, "Failed to create the collector metric exporter")

		// metrics pusher
		pusher := controller.New(
			processor.NewFactory(
				simple.NewWithHistogramDistribution(),
				metricExp,
			),
			controller.WithResource(res),
			controller.WithExporter(metricExp),
		)
		global.SetMeterProvider(pusher)

		err = pusher.Start(ctx)
		handleInitErr(err, "Failed to start metrics pusher")

		err = runtimemetrics.Start()
		handleInitErr(err, "Failed to start runtime metrics collector")
	}

	return &otelProvider{
		traceExp:      traceExp,
		metricsPusher: metricsPusher,
	}
}

func newResource(cfg *config) *resource.Resource {
	if cfg.resource != nil {
		return cfg.resource
	}

	res, err := resource.New(
		context.Background(),
		resource.WithHost(),
		resource.WithFromEnv(),
		resource.WithProcessPID(),
		resource.WithTelemetrySDK(),
		resource.WithDetectors(cfg.resourceDetectors...),
		resource.WithAttributes(cfg.resourceAttributes...),
	)
	if err != nil {
		return resource.Default()
	}
	return res
}

func handleInitErr(err error, message string) {
	if err != nil {
		klog.Fatalf("%s: %v", message, err)
	}
}
