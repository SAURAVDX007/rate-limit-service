package httpapi

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	decisions = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ratelimit_decisions_total",
		Help: "Rate limit decisions by algorithm and outcome.",
	}, []string{"algorithm", "outcome"})

	stateErrors = promauto.NewCounter(prometheus.CounterOpts{
		Name: "ratelimit_state_errors_total",
		Help: "Failures talking to the shared state layer. Non-zero means decisions are degraded.",
	})
)
