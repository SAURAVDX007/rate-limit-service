package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/sauravdx007/rate-limit-service/internal/limiter"
	"github.com/sauravdx007/rate-limit-service/internal/rules"
)

type API struct {
	Limiter limiter.Limiter
	Rules   *rules.Store
	Logger  *slog.Logger

	// FailOpen decides what happens when the shared state layer is
	// unreachable. Open: allow everything, availability wins, the limiter
	// never becomes the reason the product is down. Closed: deny everything,
	// enforcement wins, correct for abuse-prevention or billing-adjacent use.
	// Default is open, which is what most production limiters do; every
	// fail-open decision is logged and counted so the gap is visible.
	FailOpen bool
}

type checkRequest struct {
	Identifier string  `json:"identifier"`
	Namespace  string  `json:"namespace"`
	Cost       float64 `json:"cost,omitempty"` // defaults to 1
}

type checkResponse struct {
	Allowed      bool    `json:"allowed"`
	Limit        float64 `json:"limit"`
	Remaining    float64 `json:"remaining"`
	RetryAfterMS int64   `json:"retry_after_ms"`
	ResetAt      int64   `json:"reset_at"` // unix seconds
	Rule         string  `json:"matched_rule,omitempty"`
}

func (a *API) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/check", a.handleCheck)
	mux.HandleFunc("GET /v1/quota", a.handleQuota)
	mux.HandleFunc("PUT /v1/rules", a.handlePutRule)
	mux.HandleFunc("GET /v1/rules", a.handleListRules)
	mux.HandleFunc("DELETE /v1/rules", a.handleDeleteRule)
	return mux
}

func (a *API) handleCheck(w http.ResponseWriter, r *http.Request) {
	var req checkRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Identifier == "" || req.Namespace == "" {
		writeError(w, http.StatusBadRequest, "identifier and namespace are required")
		return
	}
	if req.Cost == 0 {
		req.Cost = 1
	}
	if req.Cost < 0 {
		writeError(w, http.StatusBadRequest, "cost must be positive")
		return
	}

	rule, err := a.Rules.Resolve(r.Context(), req.Identifier, req.Namespace)
	if errors.Is(err, rules.ErrNotFound) {
		// No rule configured: nothing to enforce, so allow. The alternative
		// (deny by default) is safer for abuse prevention but makes every new
		// namespace a production incident until someone writes a rule.
		writeJSON(w, http.StatusOK, checkResponse{Allowed: true, Rule: "none"})
		return
	}
	if err != nil {
		a.degraded(w, "resolve rule", err)
		return
	}

	key := "ratelimit:" + req.Identifier + ":" + req.Namespace
	dec, err := a.Limiter.Allow(r.Context(), key, rule, req.Cost)
	if errors.Is(err, limiter.ErrCostExceedsCapacity) {
		writeError(w, http.StatusBadRequest, "cost exceeds the capacity of the matched rule")
		return
	}
	if err != nil {
		a.degraded(w, "evaluate", err)
		return
	}

	writeDecision(w, dec, rule)
}

func (a *API) handleQuota(w http.ResponseWriter, r *http.Request) {
	identifier := r.URL.Query().Get("identifier")
	namespace := r.URL.Query().Get("namespace")
	if identifier == "" || namespace == "" {
		writeError(w, http.StatusBadRequest, "identifier and namespace query params are required")
		return
	}

	rule, err := a.Rules.Resolve(r.Context(), identifier, namespace)
	if errors.Is(err, rules.ErrNotFound) {
		writeJSON(w, http.StatusOK, checkResponse{Allowed: true, Rule: "none"})
		return
	}
	if err != nil {
		a.degraded(w, "resolve rule", err)
		return
	}

	key := "ratelimit:" + identifier + ":" + namespace
	// Peek, not Allow: observing a quota must never consume it, or a
	// monitoring dashboard would rate-limit the customers it is watching.
	dec, err := a.Limiter.Peek(r.Context(), key, rule, 1)
	if err != nil {
		a.degraded(w, "peek", err)
		return
	}

	writeDecision(w, dec, rule)
}

func (a *API) handlePutRule(w http.ResponseWriter, r *http.Request) {
	var rule rules.Rule
	if err := json.NewDecoder(r.Body).Decode(&rule); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if err := a.Rules.Put(r.Context(), rule); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rule)
}

func (a *API) handleListRules(w http.ResponseWriter, r *http.Request) {
	all, err := a.Rules.List(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "rule store unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rules": all})
}

func (a *API) handleDeleteRule(w http.ResponseWriter, r *http.Request) {
	identifier := r.URL.Query().Get("identifier")
	namespace := r.URL.Query().Get("namespace")
	if identifier == "" || namespace == "" {
		writeError(w, http.StatusBadRequest, "identifier and namespace query params are required")
		return
	}
	err := a.Rules.Delete(r.Context(), identifier, namespace)
	if errors.Is(err, rules.ErrNotFound) {
		writeError(w, http.StatusNotFound, "no such rule")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "rule store unavailable")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// degraded handles the shared-state-unavailable path per the FailOpen policy.
func (a *API) degraded(w http.ResponseWriter, op string, err error) {
	a.Logger.Error("state layer unavailable", "op", op, "err", err, "fail_open", a.FailOpen)
	stateErrors.Inc()

	if a.FailOpen {
		writeJSON(w, http.StatusOK, checkResponse{Allowed: true, Rule: "fail-open"})
		return
	}
	writeError(w, http.StatusServiceUnavailable, "rate limiter state unavailable")
}

func writeDecision(w http.ResponseWriter, dec limiter.Decision, rule rules.Rule) {
	// Standard rate-limit headers, so a gateway can build a 429 without
	// parsing the body.
	w.Header().Set("X-RateLimit-Limit", strconv.FormatFloat(dec.Limit, 'f', -1, 64))
	w.Header().Set("X-RateLimit-Remaining", strconv.FormatFloat(dec.Remaining, 'f', -1, 64))
	w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(dec.ResetAt.Unix(), 10))
	if !dec.Allowed {
		w.Header().Set("Retry-After", strconv.Itoa(int(dec.RetryAfter.Round(time.Second).Seconds())))
	}

	decisions.WithLabelValues(string(rule.Algorithm), allowLabel(dec.Allowed)).Inc()

	// 200 with allowed:false, not 429. This service *reports* a decision to a
	// caller that will enforce it; the caller returns 429 to the end user. A
	// 429 here would mean "you, the gateway, are being rate limited".
	writeJSON(w, http.StatusOK, checkResponse{
		Allowed:      dec.Allowed,
		Limit:        dec.Limit,
		Remaining:    dec.Remaining,
		RetryAfterMS: dec.RetryAfter.Milliseconds(),
		ResetAt:      dec.ResetAt.Unix(),
		Rule:         rule.Identifier + ":" + rule.Namespace,
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func allowLabel(allowed bool) string {
	if allowed {
		return "allow"
	}
	return "deny"
}
