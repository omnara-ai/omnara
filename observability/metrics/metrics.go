package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const ScrapePath = "/metrics"

const DefaultNamespace = "omnara"

const (
	SubsystemAPI        = "api"
	SubsystemHTTPClient = "http_client"
)

type Set struct {
	registry  *prometheus.Registry
	handler   http.Handler
	namespace string
}

type Option func(*Set)

func WithNamespace(namespace string) Option {
	return func(set *Set) { set.namespace = namespace }
}

func New(opts ...Option) *Set {
	registry := prometheus.NewRegistry()
	registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	set := &Set{
		registry:  registry,
		handler:   promhttp.HandlerFor(registry, promhttp.HandlerOpts{}),
		namespace: DefaultNamespace,
	}
	for _, opt := range opts {
		opt(set)
	}
	return set
}

func (m *Set) Namespace() string {
	return m.namespace
}

func (m *Set) Handler() http.Handler {
	return m.handler
}

func (m *Set) MustRegister(collectors ...prometheus.Collector) {
	m.registry.MustRegister(collectors...)
}
