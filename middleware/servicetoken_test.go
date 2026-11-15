package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestServiceTokenAuthRejectsMissingOrWrongToken(t *testing.T) {
	reached := false
	handler := ServiceTokenAuth("secret-token", IsProbeOrMetricsPath)(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			reached = true
			w.WriteHeader(http.StatusOK)
		},
	))

	cases := []struct {
		name   string
		header string
		want   int
	}{
		{"no token", "", http.StatusUnauthorized},
		{"wrong token", "not-the-token", http.StatusUnauthorized},
		// A prefix must not be accepted: the comparison is constant-time over
		// the whole value, not a byte-by-byte early exit.
		{"token prefix", "secret", http.StatusUnauthorized},
		{"correct token", "secret-token", http.StatusOK},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reached = false
			req := httptest.NewRequest(http.MethodPost, "/api/v1/alerts", nil)
			if tc.header != "" {
				req.Header.Set("X-Service-Token", tc.header)
			}
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
			if (tc.want == http.StatusOK) != reached {
				t.Fatalf("handler reached = %v for status %d", reached, tc.want)
			}
		})
	}
}

func TestServiceTokenAuthExemptsProbesAndMetrics(t *testing.T) {
	// Kubernetes probes and the Prometheus scrape cannot present a token, so
	// requiring one here would turn every probe into a false unhealthy cascade.
	handler := ServiceTokenAuth("secret-token", IsProbeOrMetricsPath)(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) },
	))

	for _, path := range []string{"/healthz", "/ready", "/metrics"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want 200 without a token", path, rec.Code)
		}
	}
}

func TestServiceTokenAuthFailsClosedWithoutConfiguration(t *testing.T) {
	// An empty configured token must reject everything rather than accept
	// everything: a missing secret is a misconfiguration, not an open door.
	reached := false
	handler := ServiceTokenAuth("", IsProbeOrMetricsPath)(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) { reached = true },
	))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/reports", nil)
	req.Header.Set("X-Service-Token", "")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized || reached {
		t.Fatalf("status = %d reached = %v, want 401 and unreached", rec.Code, reached)
	}
}

// TestEveryReporterAPIRouteRequiresTheToken verifies the exemption list cannot
// accidentally widen to cover a delivery-triggering route.
func TestEveryReporterAPIRouteRequiresTheToken(t *testing.T) {
	routes := []string{
		"/api/v1/reports",
		"/api/v1/status",
		"/api/v1/alerts",
		"/api/v1/alerts/resolved",
		"/api/v1/retry-stalled-deliveries",
	}
	handler := ServiceTokenAuth("secret-token", IsProbeOrMetricsPath)(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) },
	))
	for _, route := range routes {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, route, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401 without a token", route, rec.Code)
		}
	}
}
