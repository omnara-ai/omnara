package metrics

import (
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

type dbPoolCollector struct {
	pool    *pgxpool.Pool
	metrics []dbPoolMetric
}

type dbPoolMetric struct {
	desc  *prometheus.Desc
	kind  prometheus.ValueType
	value func(*pgxpool.Stat) float64
}

func NewDBPoolCollector(pool *pgxpool.Pool) prometheus.Collector {
	desc := func(name, help string) *prometheus.Desc {
		return prometheus.NewDesc("omnara_db_pool_"+name, help, nil, nil)
	}
	return &dbPoolCollector{pool: pool, metrics: []dbPoolMetric{
		{desc("acquired_connections", "Connections currently acquired from the pool."), prometheus.GaugeValue,
			func(s *pgxpool.Stat) float64 { return float64(s.AcquiredConns()) }},
		{desc("idle_connections", "Idle connections in the pool."), prometheus.GaugeValue,
			func(s *pgxpool.Stat) float64 { return float64(s.IdleConns()) }},
		{desc("constructing_connections", "Pool connections being constructed."), prometheus.GaugeValue,
			func(s *pgxpool.Stat) float64 { return float64(s.ConstructingConns()) }},
		{desc("max_connections", "Maximum number of pool connections."), prometheus.GaugeValue,
			func(s *pgxpool.Stat) float64 { return float64(s.MaxConns()) }},
		{desc("acquires_total", "Successful pool connection acquisitions."), prometheus.CounterValue,
			func(s *pgxpool.Stat) float64 { return float64(s.AcquireCount()) }},
		{
			desc("acquire_duration_seconds_total", "Total time spent acquiring pool connections successfully."),
			prometheus.CounterValue,
			func(s *pgxpool.Stat) float64 { return s.AcquireDuration().Seconds() },
		},
		{
			desc("empty_acquires_total",
				"Successful acquisitions that waited for a connection to be released or constructed."),
			prometheus.CounterValue,
			func(s *pgxpool.Stat) float64 { return float64(s.EmptyAcquireCount()) },
		},
		{
			desc("empty_acquire_wait_seconds_total",
				"Time waiting for successful acquisitions when the pool was empty; excludes canceled waits."),
			prometheus.CounterValue,
			func(s *pgxpool.Stat) float64 { return s.EmptyAcquireWaitTime().Seconds() },
		},
		{desc("canceled_acquires_total", "Pool connection acquisitions canceled by a context."), prometheus.CounterValue,
			func(s *pgxpool.Stat) float64 { return float64(s.CanceledAcquireCount()) }},
	}}
}

func (c *dbPoolCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, metric := range c.metrics {
		ch <- metric.desc
	}
}

func (c *dbPoolCollector) Collect(ch chan<- prometheus.Metric) {
	stat := c.pool.Stat()
	for _, metric := range c.metrics {
		ch <- prometheus.MustNewConstMetric(metric.desc, metric.kind, metric.value(stat))
	}
}
