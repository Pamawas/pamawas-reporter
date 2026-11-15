package middleware

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/rs/zerolog/log"
)

// ServiceTokenAuth authenticates service-to-service calls with a shared token.
//
// Every internal caller (ingest, scheduler) presents PAMAWAS_SERVICE_TOKEN in
// the X-Service-Token header. Nothing else may reach the reporter's
// /api/v1/* routes: they trigger Discord/Telegram/email delivery and report
// generation, so an unauthenticated caller inside the cluster could page
// people or burn the delivery channel's rate budget.
//
// Probe and scrape paths are exempt. Kubernetes and Prometheus have no way to
// present a token, and requiring one would turn every probe into an outage.
func ServiceTokenAuth(token string, exempt func(string) bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if exempt(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}

			presented := r.Header.Get("X-Service-Token")
			// Constant-time comparison: a byte-by-byte early exit leaks the
			// token prefix to anyone who can time the responses.
			if token == "" || subtle.ConstantTimeCompare([]byte(presented), []byte(token)) != 1 {
				log.Warn().
					Str("path", r.URL.Path).
					Str("remote_addr", r.RemoteAddr).
					Bool("token_presented", presented != "").
					Msg("Rejected request with invalid service token")
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":{"code":"unauthorized","message":"Invalid or missing service token"}}`))
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// IsProbeOrMetricsPath reports whether a path must stay reachable without
// authentication: liveness/readiness probes and the metrics scrape.
func IsProbeOrMetricsPath(path string) bool {
	switch path {
	case "/healthz", "/ready", "/metrics":
		return true
	}
	return strings.HasPrefix(path, "/metrics/")
}
