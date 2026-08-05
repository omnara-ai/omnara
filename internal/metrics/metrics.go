package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const ScrapePath = "/metrics"

const (
	SubsystemAPI        = "api"
	SubsystemHTTPClient = "http_client"
)

type Set struct {
	registry *prometheus.Registry
	handler  http.Handler
}

func New() *Set {
	registry := prometheus.NewRegistry()
	registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	return &Set{
		registry: registry,
		handler:  promhttp.HandlerFor(registry, promhttp.HandlerOpts{}),
	}
}

func (m *Set) Handler() http.Handler {
	return m.handler
}

func (m *Set) MustRegister(collectors ...prometheus.Collector) {
	m.registry.MustRegister(collectors...)
}
