package metrics

import (
	"context"
	"log"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/prometheus"
	api "go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.4.0"

	"github.com/kedacore/http-add-on/pkg/build"
)

type PrometheusMetrics struct {
	meter                 api.Meter
	requestCounter        api.Int64Counter
	pendingRequestCounter api.Int64UpDownCounter
	choreoRequestCounter  api.Int64Counter
	choreoRequestDuration api.Float64Histogram
}

func NewPrometheusMetrics(options ...prometheus.Option) *PrometheusMetrics {
	var exporter *prometheus.Exporter
	var err error
	if options == nil {
		exporter, err = prometheus.New()
	} else {
		exporter, err = prometheus.New(options...)
	}
	if err != nil {
		log.Fatalf("could not create Prometheus exporter: %v", err)
	}

	res := resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceNameKey.String("interceptor-proxy"),
		semconv.ServiceVersionKey.String(build.Version()),
	)

	provider := metric.NewMeterProvider(
		metric.WithReader(exporter),
		metric.WithResource(res),
	)
	meter := provider.Meter(meterName)

	reqCounter, err := meter.Int64Counter("interceptor_request_count", api.WithDescription("a counter of requests processed by the interceptor proxy"))
	if err != nil {
		log.Fatalf("could not create new Prometheus request counter: %v", err)
	}

	pendingRequestCounter, err := meter.Int64UpDownCounter("interceptor_pending_request_count", api.WithDescription("a count of requests pending forwarding by the interceptor proxy"))
	if err != nil {
		log.Fatalf("could not create new Prometheus pending request counter: %v", err)
	}

	choreoRequestCounter, err := meter.Int64Counter("keda_metric_request_count", api.WithDescription("a counter of choreo requests processed by the interceptor proxy"))
	if err != nil {
		log.Fatalf("could not create new Prometheus choreo request counter: %v", err)
	}

	choreoRequestDuration, err := meter.Float64Histogram(
		"keda_metric_request_duration",
		api.WithDescription("a histogram of the duration of choreo requests processed by the interceptor proxy"),
		api.WithExplicitBucketBoundaries(0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0, 2.5, 5.0, 10.0),
	)
	if err != nil {
		log.Fatalf("could not create new Prometheus choreo request duration histogram: %v", err)
	}

	return &PrometheusMetrics{
		meter:                 meter,
		requestCounter:        reqCounter,
		pendingRequestCounter: pendingRequestCounter,
		choreoRequestCounter:  choreoRequestCounter,
		choreoRequestDuration: choreoRequestDuration,
	}
}

func (p *PrometheusMetrics) RecordRequestCount(method string, path string, responseCode int, host string) {
	ctx := context.Background()
	opt := api.WithAttributeSet(
		attribute.NewSet(
			attribute.Key("method").String(method),
			attribute.Key("path").String(path),
			attribute.Key("status").Int(responseCode),
			attribute.Key("host").String(host),
		),
	)
	p.requestCounter.Add(ctx, 1, opt)
}

func (p *PrometheusMetrics) RecordPendingRequestCount(host string, value int64) {
	ctx := context.Background()
	opt := api.WithAttributeSet(
		attribute.NewSet(
			attribute.Key("host").String(host),
		),
	)

	p.pendingRequestCounter.Add(ctx, value, opt)
}

// FORMAT: hubble_http_requests_total{destination="dp-development-adeepakubecosttest-4247-1806815479/adeepakubecosttestservice-4136524348-dc8655587-qwgqr",source="dev-choreo-apim/choreo-connect-deployment-external-p1-85847c78f4-696ds",status="200"} 857
func (p *PrometheusMetrics) RecordChoreoRequestCount(source string, destination string, statusCode int) {
	ctx := context.Background()
	opt := api.WithAttributeSet(
		attribute.NewSet(
			attribute.Key("source").String(source),
			attribute.Key("destination").String(destination),
			attribute.Key("status").Int(statusCode),
		),
	)
	p.choreoRequestCounter.Add(ctx, 1, opt)
}

// FORMAT: hubble_http_request_duration_seconds_bucket{destination="dp-development-adeepakubecosttest-4247-1806815479/adeepakubecosttestservice-4136524348-dc8655587-qwgqr",source="dev-choreo-apim/choreo-connect-deployment-external-p1-85847c78f4-696ds",le="0.05"} 855
func (p *PrometheusMetrics) RecordChoreoRequestDuration(source string, destination string, duration float64) {
	ctx := context.Background()
	opt := api.WithAttributeSet(
		attribute.NewSet(
			attribute.Key("source").String(source),
			attribute.Key("destination").String(destination),
		),
	)
	p.choreoRequestDuration.Record(ctx, duration, opt)
}
