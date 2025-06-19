package server

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	sloglogrus "github.com/samber/slog-logrus/v2"
	slogmulti "github.com/samber/slog-multi"
	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/contrib/exporters/autoexport"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/prometheus"
	logsdk "go.opentelemetry.io/otel/sdk/log"
	meticsdk "go.opentelemetry.io/otel/sdk/metric"
	tracesdk "go.opentelemetry.io/otel/sdk/trace"

	logglobal "go.opentelemetry.io/otel/log/global"
)

func setEnvIfNotSet(key, value string) {
	if _, ok := os.LookupEnv(key); !ok {
		os.Setenv(key, value)
	}
}

func setupTelemetry(ctx context.Context) error {
	// I have no idea why otel choose to have otlp exporter to localhost as default option, none have much more sense
	setEnvIfNotSet("OTEL_TRACES_EXPORTER", "none")
	setEnvIfNotSet("OTEL_LOGS_EXPORTER", "none")
	setEnvIfNotSet("OTEL_METRICS_EXPORTER", "none")

	promExporter, err := prometheus.New(prometheus.WithNamespace("rgeocache"))
	if err != nil {
		return fmt.Errorf("failed to initialize prometheus exporter: %w", err)
	}
	metricExporter, err := autoexport.NewMetricReader(ctx)
	if err != nil {
		return fmt.Errorf("failed to initialize metric exporter: %w", err)
	}
	metricProvider := meticsdk.NewMeterProvider(meticsdk.WithReader(promExporter), meticsdk.WithReader(metricExporter))
	otel.SetMeterProvider(metricProvider)

	spanExporter, err := autoexport.NewSpanExporter(ctx)
	if err != nil {
		return fmt.Errorf("failed to initialize trace exporter: %w", err)
	}
	traceProvider := tracesdk.NewTracerProvider(tracesdk.WithBatcher(spanExporter))
	otel.SetTracerProvider(traceProvider)

	logsExporter, err := autoexport.NewLogExporter(ctx)
	if err != nil {
		return fmt.Errorf("failed to initialize log exporter: %w", err)
	}

	logsProvider := logsdk.NewLoggerProvider(logsdk.WithProcessor(logsdk.NewBatchProcessor(logsExporter)))
	logglobal.SetLoggerProvider(logsProvider)

	handlers := []slog.Handler{
		sloglogrus.Option{Level: slog.LevelDebug, Logger: logrus.StandardLogger()}.NewLogrusHandler(),
		otelslog.NewHandler(""),
	}

	slog.SetDefault(slog.New(slogmulti.Fanout(handlers...)))

	return nil
}
