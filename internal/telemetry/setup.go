package telemetry

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"time"

	"github.com/google/uuid"
	sloglogrus "github.com/samber/slog-logrus/v2"
	slogmulti "github.com/samber/slog-multi"
	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.34.0"
	"golang.org/x/sync/errgroup"
)

type Client struct {
	log *slog.Logger

	tracerProvider *trace.TracerProvider
	metricProvider *metric.MeterProvider
	loggerProvider *log.LoggerProvider
}

func (client *Client) Flush(ctx context.Context) error {
	g, ctx := errgroup.WithContext(ctx)

	if client.metricProvider != nil {
		g.Go(func() error {
			return client.metricProvider.ForceFlush(ctx)
		})
	}
	if client.loggerProvider != nil {
		g.Go(func() error {
			return client.loggerProvider.ForceFlush(ctx)
		})
	}
	if client.tracerProvider != nil {
		g.Go(func() error {
			return client.tracerProvider.ForceFlush(ctx)
		})
	}

	return g.Wait()
}

func (client *Client) Shutdown(ctx context.Context) {
	if client.metricProvider == nil {
		err := client.metricProvider.Shutdown(ctx)
		if err != nil {
			client.log.ErrorContext(ctx, "error shutting down metric provider", "error", err.Error())
		}
	}
	if client.tracerProvider == nil {
		err := client.tracerProvider.Shutdown(ctx)
		if err != nil {
			client.log.ErrorContext(ctx, "error shutting down tracer provider", "error", err.Error())
		}
	}
	if client.loggerProvider == nil {
		err := client.loggerProvider.Shutdown(ctx)
		if err != nil {
			client.log.ErrorContext(ctx, "error shutting down logger provider", "error", err.Error())
		}
	}
}

func Setup(ctx context.Context, appName, endpoint string) (*Client, error) {
	if endpoint == "" {
		return nil, nil
	}

	client := &Client{
		log: slog.With("component", "telemetry"),
	}
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(cause error) {
		client.log.ErrorContext(ctx, "otel error", "error", cause.Error())
	}))

	hostName, _ := os.Hostname()

	r, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(appName),
			semconv.HostName(hostName),
			semconv.ServiceInstanceID(uuid.NewString()),
		),
	)
	if err != nil {
		return nil, err
	}

	meticExporter, err := otlpmetrichttp.New(ctx,
		otlpmetrichttp.WithEndpoint(endpoint),
		// otlpmetrichttp.WithInsecure(),
		otlpmetrichttp.WithRetry(otlpmetrichttp.RetryConfig{
			Enabled: false,
		}),
	)
	if err != nil {
		return nil, err
	}

	promExporter, err := prometheus.New()
	if err != nil {
		return nil, fmt.Errorf("failed to initialize prometheus exporter: %w", err)
	}
	client.metricProvider = metric.NewMeterProvider(
		metric.WithResource(r),
		metric.WithReader(metric.NewPeriodicReader(meticExporter)),
		metric.WithReader(promExporter),
	)
	otel.SetMeterProvider(client.metricProvider)

	var meter = otel.Meter(appName + "/telemetry")
	counter, err := meter.Int64Counter("up")
	if err != nil {
		return nil, err
	}
	counter.Add(ctx, 1)
	client.log.InfoContext(ctx, "metrics provider initialized")

	traceExporter, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpoint(endpoint),
		// otlptracehttp.WithInsecure(),
		otlptracehttp.WithRetry(otlptracehttp.RetryConfig{
			Enabled: false,
		}),
	)

	if err != nil {
		return nil, err
	}
	client.tracerProvider = trace.NewTracerProvider(
		trace.WithResource(r),
		trace.WithBatcher(traceExporter, trace.WithExportTimeout(time.Second)),
	)
	otel.SetTracerProvider(client.tracerProvider)
	client.log.InfoContext(ctx, "tracing provider initialized")

	logExporter, err := otlploghttp.New(ctx,
		otlploghttp.WithEndpoint(endpoint),
		// otlploghttp.WithInsecure(),
		otlploghttp.WithRetry(otlploghttp.RetryConfig{
			Enabled: false,
		}),
	)
	if err != nil {
		return nil, err
	}

	client.loggerProvider = log.NewLoggerProvider(
		log.WithResource(r),
		log.WithProcessor(log.NewBatchProcessor(logExporter, log.WithExportInterval(time.Second))),
	)

	// slog.SetDefault(slog.New(otelslog.NewHandler("", otelslog.WithLoggerProvider(client.loggerProvider))))

	slog.SetDefault(slog.New(slogmulti.Fanout(
		otelslog.NewHandler("", otelslog.WithLoggerProvider(client.loggerProvider)),
		sloglogrus.Option{Level: slog.LevelDebug, Logger: logrus.StandardLogger()}.NewLogrusHandler(),
		// slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}),
	)))

	client.log.InfoContext(ctx, "logger provider initialized")

	// recreate telemetry logger
	client.log = slog.With("component", "telemetry")

	runtime.SetMutexProfileFraction(5)
	runtime.SetBlockProfileRate(5)

	return client, nil
}

func functionName() string {
	var pcs [1]uintptr
	runtime.Callers(1, pcs[:])
	return runtime.FuncForPC(pcs[0]).Name()
}
