package metrics

import (
	"github.com/kedacore/http-add-on/interceptor/config"
)

var (
	collectors []Collector
)

const meterName = "keda-interceptor-proxy"

type Collector interface {
	RecordRequestCount(method string, path string, responseCode int, host string)
	RecordPendingRequestCount(host string, value int64)
	RecordChoreoRequestCount(source string, destination string, statusCode int)
	RecordChoreoRequestDuration(source string, destination string, duration float64)
}

func NewMetricsCollectors(metricsConfig *config.Metrics) {
	if metricsConfig.OtelPrometheusExporterEnabled {
		promometrics := NewPrometheusMetrics()
		collectors = append(collectors, promometrics)
	}

	if metricsConfig.OtelHTTPExporterEnabled {
		otelhttpmetrics := NewOtelMetrics(metricsConfig)
		collectors = append(collectors, otelhttpmetrics)
	}
}

func RecordRequestCount(method string, path string, responseCode int, host string) {
	for _, collector := range collectors {
		collector.RecordRequestCount(method, path, responseCode, host)
	}
}

func RecordPendingRequestCount(host string, value int64) {
	for _, collector := range collectors {
		collector.RecordPendingRequestCount(host, value)
	}
}

func RecordChoreoRequestCount(source string, destination string, statusCode int) {
	for _, collector := range collectors {
		collector.RecordChoreoRequestCount(source, destination, statusCode)
	}
}

func RecordChoreoRequestDuration(source string, destination string, duration float64) {
	for _, collector := range collectors {
		collector.RecordChoreoRequestDuration(source, destination, duration)
	}
}
