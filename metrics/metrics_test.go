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

	checks := []struct {
		name string
		got  float64
		want float64
	}{
		{"reports", testutil.ToFloat64(m.ReportsGeneratedTotal), 1},
		{"delivery", testutil.ToFloat64(m.DeliveryTotal.WithLabelValues("discord", "success")), 2},
		{"db errors", testutil.ToFloat64(m.DBConnectionErrors), 1},
	}
	for _, check := range checks {
		if check.got != check.want {
			t.Errorf("%s = %v, want %v", check.name, check.got, check.want)
		}
	}
}
