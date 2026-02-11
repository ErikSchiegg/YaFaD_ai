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
		Help: "Gesamtzahl der von Yafad geschriebenen Log-Nachrichten",
	})

	StateValues = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "yafad_state_value",
		Help: "Aktuelle Werte der Yafad States (t0 bis t4)",
	}, []string{"state_index"})

	// NEU: Lambda Wert
	LambdaValue = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "yafad_lambda_value",
		Help: "Aktueller T0 Lambda (Verfallsrate) Wert",
	})

	// NEU: Phi Diff (Maß für die "Harmony")
	PhiDiffValue = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "yafad_phi_diff_value",
		Help: "Abweichung vom perfekten Goldenen Schnitt (Phi)",
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

// NEU: Funktion um die System-Intelligenz an Prometheus zu senden
func (m *Monitor) RecordSystemIntel(lambda, phiDiff float64) {
	LambdaValue.Set(lambda)
	PhiDiffValue.Set(phiDiff)
}
