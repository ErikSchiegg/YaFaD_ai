package monitoring

import (
	"context"
	"encoding/csv"
	"fmt"
	"math"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

// --- PROMETHEUS METRIKEN ---
var (
	StateValue = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{Name: "yafad_state_value", Help: "Current biomass count per tier"},
		[]string{"tier"},
	)
	LambdaValue       = prometheus.NewGauge(prometheus.GaugeOpts{Name: "yafad_lambda", Help: "Current decay rate"})
	PhiDiffValue      = prometheus.NewGauge(prometheus.GaugeOpts{Name: "yafad_phi_diff", Help: "Deviation from Golden Ratio"})
	TotalBiomassValue = prometheus.NewGauge(prometheus.GaugeOpts{Name: "yafad_total_biomass", Help: "Sum of all tiers"})
	InjectTargetValue = prometheus.NewGauge(prometheus.GaugeOpts{Name: "yafad_inject_target", Help: "Total records planned"})
	InjectDoneValue   = prometheus.NewGauge(prometheus.GaugeOpts{Name: "yafad_inject_done", Help: "Records injected so far"})
	SimActiveValue    = prometheus.NewGauge(prometheus.GaugeOpts{Name: "yafad_sim_active", Help: "1 if Injection is running"})

	// PID Werte für das Cockpit
	PidKpValue = prometheus.NewGauge(prometheus.GaugeOpts{Name: "yafad_pid_kp", Help: "Current Proportional Term"})
	PidKiValue = prometheus.NewGauge(prometheus.GaugeOpts{Name: "yafad_pid_ki", Help: "Current Integral Term"})
	PidKdValue = prometheus.NewGauge(prometheus.GaugeOpts{Name: "yafad_pid_kd", Help: "Current Derivative Term"})
)

func init() {
	prometheus.MustRegister(StateValue)
	prometheus.MustRegister(LambdaValue)
	prometheus.MustRegister(PhiDiffValue)
	prometheus.MustRegister(TotalBiomassValue)
	prometheus.MustRegister(InjectTargetValue)
	prometheus.MustRegister(InjectDoneValue)
	prometheus.MustRegister(SimActiveValue)
	prometheus.MustRegister(PidKpValue)
	prometheus.MustRegister(PidKiValue)
	prometheus.MustRegister(PidKdValue)
}

type MonitorConfig struct {
	Interval   time.Duration
	TargetPhi  float64
	CSVFile    string
	Capacities map[string]float64
}

// SystemStateFunc erweitert um PID Rückgabewerte (kp, ki, kd)
type SystemStateFunc func() (target int, done int, isRunning bool, kp, ki, kd float64)

func StartMonitor(pool *pgxpool.Pool, cfg MonitorConfig, getLambda func() float64, getSystemState SystemStateFunc) {
	ticker := time.NewTicker(cfg.Interval)
	startTime := time.Now()

	// 1. CSV Header anpassen (inkl. archive0 bis archive4)
	f, err := os.OpenFile(cfg.CSVFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err == nil {
		stat, _ := f.Stat()
		if stat.Size() == 0 {
			writer := csv.NewWriter(f)
			writer.Write([]string{
				"timestamp", "runtime_sec", "total_biomass",
				"t0", "t1", "t2", "t3", "t4", "deep_archive",
				"archive0", "archive1", "archive2", "archive3", "archive4", // <--- NEU
				"t0_pct", "t1_pct", "t2_pct", "t3_pct", "t4_pct",
				"lambda", "phi_diff",
			})
			writer.Flush()
		}
		f.Close()
	}

	for range ticker.C {
		// 2. DB Snapshot
		counts := make(map[string]int)
		var total int64

		// ---> NEU: Die komplette Liste inkl. Fractal Archives <---
		tiers := []string{
			"table0", "table1", "table2", "table3", "table4",
			"deep_archive",
			"archive0", "archive1", "archive2", "archive3", "archive4",
		}

		for _, t := range tiers {
			var c int
			// Fehlertolerante Abfrage (falls Archiv-Tabelle noch nicht da ist)
			err := pool.QueryRow(context.Background(), fmt.Sprintf("SELECT count(*) FROM %s", t)).Scan(&c)
			if err == nil {
				counts[t] = c
				total += int64(c)
				StateValue.WithLabelValues(t).Set(float64(c))
			} else {
				// Fallback auf 0, wenn Tabelle fehlt
				counts[t] = 0
				StateValue.WithLabelValues(t).Set(0)
			}
		}

		// 3. Prometheus Updates
		runtime := time.Since(startTime).Seconds()
		lambda := getLambda()

		injTarget, injDone, simRunning, kp, ki, kd := getSystemState()

		LambdaValue.Set(lambda)
		TotalBiomassValue.Set(float64(total))
		InjectTargetValue.Set(float64(injTarget))
		InjectDoneValue.Set(float64(injDone))

		PidKpValue.Set(kp)
		PidKiValue.Set(ki)
		PidKdValue.Set(kd)

		if simRunning {
			SimActiveValue.Set(1.0)
		} else {
			SimActiveValue.Set(0.0)
		}

		// Phi-Diff Berechnung
		phiDiff := 0.0
		if counts["table0"] > 0 {
			observedPhi := float64(counts["table1"]) / float64(counts["table0"])
			phiDiff = math.Abs(observedPhi - cfg.TargetPhi)
		}
		PhiDiffValue.Set(phiDiff)

		pcts := make(map[string]string)
		for _, t := range []string{"table0", "table1", "table2", "table3", "table4"} {
			cap := cfg.Capacities[t]
			val := 0.0
			if cap > 0 {
				val = (float64(counts[t]) / cap) * 100.0
			}
			pcts[t] = fmt.Sprintf("%.2f", val)
		}

		// 4. CSV Schreiben anpassen
		f, err := os.OpenFile(cfg.CSVFile, os.O_APPEND|os.O_WRONLY, 0644)
		if err == nil {
			writer := csv.NewWriter(f)
			record := []string{
				time.Now().Format(time.RFC3339), fmt.Sprintf("%.0f", runtime), fmt.Sprintf("%d", total),
				fmt.Sprintf("%d", counts["table0"]), fmt.Sprintf("%d", counts["table1"]),
				fmt.Sprintf("%d", counts["table2"]), fmt.Sprintf("%d", counts["table3"]),
				fmt.Sprintf("%d", counts["table4"]), fmt.Sprintf("%d", counts["deep_archive"]),
				fmt.Sprintf("%d", counts["archive0"]), fmt.Sprintf("%d", counts["archive1"]), // <--- NEU
				fmt.Sprintf("%d", counts["archive2"]), fmt.Sprintf("%d", counts["archive3"]), // <--- NEU
				fmt.Sprintf("%d", counts["archive4"]), // <--- NEU
				pcts["table0"], pcts["table1"], pcts["table2"], pcts["table3"], pcts["table4"],
				fmt.Sprintf("%f", lambda), fmt.Sprintf("%.4f", phiDiff),
			}
			writer.Write(record)
			writer.Flush()
			f.Close()
		}
	}
}
