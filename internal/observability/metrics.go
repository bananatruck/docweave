package observability

import "github.com/prometheus/client_golang/prometheus"

type Metrics struct {
	Fetches    *prometheus.CounterVec
	Duration   *prometheus.HistogramVec
	Active     prometheus.Gauge
	Discovered prometheus.Counter
}

func New(registry prometheus.Registerer) *Metrics {
	m := &Metrics{
		Fetches: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "docweave", Name: "fetches_total", Help: "Fetch attempts by outcome.",
		}, []string{"outcome", "status"}),
		Duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "docweave", Name: "fetch_duration_seconds", Help: "End-to-end fetch duration.",
			Buckets: prometheus.DefBuckets,
		}, []string{"outcome"}),
		Active: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "docweave", Name: "active_workers", Help: "Workers currently processing a URL.",
		}),
		Discovered: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "docweave", Name: "links_discovered_total", Help: "In-scope links discovered.",
		}),
	}
	registry.MustRegister(m.Fetches, m.Duration, m.Active, m.Discovered)
	return m
}
