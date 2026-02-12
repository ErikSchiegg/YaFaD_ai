package monitoring

import (
	"log"
	"os"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	LogMessagesTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "yafad_log_messages_total",
		Help: "Total number of log messages written by Yafad",
	})

	StateValues = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "yafad_state_value",
		Help: "Current values of Yafad states (t0 to t4 and deep_archive)",
	}, []string{"state_index"})

	// NEW: Lambda Value
	LambdaValue = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "yafad_lambda_value",
		Help: "Current T0 Lambda (decay rate) value",
	})

	// NEW: Phi Diff (Measure of "Harmony")
	PhiDiffValue = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "yafad_phi_diff_value",
		Help: "Deviation from the perfect Golden Ratio (Phi)",
	})

	// NEW: Total Biomass Gauge
	TotalBiomassValue = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "yafad_total_biomass",
		Help: "Total biomass (record count) in the system",
	})
)

type Monitor struct {
	Logger *log.Logger
}

func NewMonitor() *Monitor {
	return &Monitor{
		Logger: log.New(os.Stdout, "[MONITOR] ", log.LstdFlags),
	}
}

func (m *Monitor) Log(msg string) {
	m.Logger.Println(msg)
	LogMessagesTotal.Inc()
}

func (m *Monitor) RecordState(t0, t1, t2, t3, t4, archive int) {
	StateValues.WithLabelValues("t0").Set(float64(t0))
	StateValues.WithLabelValues("t1").Set(float64(t1))
	StateValues.WithLabelValues("t2").Set(float64(t2))
	StateValues.WithLabelValues("t3").Set(float64(t3))
	StateValues.WithLabelValues("t4").Set(float64(t4))
	StateValues.WithLabelValues("deep_archive").Set(float64(archive))
}

// NEW: Function to send system intelligence to Prometheus
// Now includes 'total' biomass to update all high-level metrics at once
func (m *Monitor) RecordSystemIntel(lambda, phiDiff float64, total int) {
	LambdaValue.Set(lambda)
	PhiDiffValue.Set(phiDiff)
	TotalBiomassValue.Set(float64(total))
}
