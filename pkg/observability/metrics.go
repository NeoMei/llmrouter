package observability

import "github.com/prometheus/client_golang/prometheus"

// Gateway request volume, labeled by upstream provider, model, protocol, and
// final HTTP status category.
var RequestsTotal = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "gateway_requests_total",
		Help: "Total number of gateway requests processed.",
	},
	[]string{"provider", "model", "protocol", "status"},
)

// Upstream request latency distribution.
var RequestDuration = prometheus.NewHistogramVec(
	prometheus.HistogramOpts{
		Name:    "gateway_request_duration_seconds",
		Help:    "Gateway request latency in seconds.",
		Buckets: prometheus.DefBuckets,
	},
	[]string{"provider", "model", "protocol"},
)

// Currently in-flight streaming responses.
var StreamActive = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "gateway_stream_active",
		Help: "Number of active streaming responses.",
	},
	[]string{"provider", "model"},
)

// Cache hit / miss counters, partitioned by cache type.
var (
	CacheHits = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "gateway_cache_hits_total",
			Help: "Total cache hits.",
		},
		[]string{"type"},
	)
	CacheMisses = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "gateway_cache_misses_total",
			Help: "Total cache misses.",
		},
		[]string{"type"},
	)
)

// Number of active keys per provider in the key pool.
var KeypoolActive = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "gateway_keypool_active",
		Help: "Active keys per provider in the key pool.",
	},
	[]string{"provider"},
)

// Requests rejected by rate limiting, by layer (e.g. "global", "per-key").
var RateLimited = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "gateway_ratelimited_total",
		Help: "Requests rejected by rate limiting.",
	},
	[]string{"layer"},
)

// Init registers all gateway metrics with the default Prometheus registry.
// Safe to call once at startup; panics (via MustRegister) on duplicate registration.
func Init() {
	prometheus.MustRegister(
		RequestsTotal, RequestDuration, StreamActive,
		CacheHits, CacheMisses, KeypoolActive, RateLimited,
	)
}
