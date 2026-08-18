package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestNewMetricsCollectors(t *testing.T) {
	m := NewMetrics()
	m.ReportsGeneratedTotal.Inc()
	m.DeliveryTotal.WithLabelValues("discord", "success").Add(2)
	m.DBConnectionErrors.Inc()
	m.LastSentTimestamp.Set(123)
	m.ReporterRunning.Set(1)
	m.TemplateRenderDuration.Observe(0.25)

	checks := []struct {
		name string
		got  float64
		want float64
	}{
		{"reports", testutil.ToFloat64(m.ReportsGeneratedTotal), 1},
		{"delivery", testutil.ToFloat64(m.DeliveryTotal.WithLabelValues("discord", "success")), 2},
		{"db errors", testutil.ToFloat64(m.DBConnectionErrors), 1},
		{"last sent", testutil.ToFloat64(m.LastSentTimestamp), 123},
		{"running", testutil.ToFloat64(m.ReporterRunning), 1},
	}
	for _, check := range checks {
		if check.got != check.want {
			t.Errorf("%s = %v, want %v", check.name, check.got, check.want)
		}
	}
	if count := testutil.CollectAndCount(m.TemplateRenderDuration); count != 1 {
		t.Fatalf("histogram metric count = %d", count)
	}
}
