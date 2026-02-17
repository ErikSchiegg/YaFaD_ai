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

	// NEU: PID Werte für das Cockpit
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
	// NEU: Registrieren
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

	// 1. CSV Header (unverändert)
	f, err := os.OpenFile(cfg.CSVFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err == nil {
		stat, _ := f.Stat()
		if stat.Size() == 0 {
			writer := csv.NewWriter(f)
			writer.Write([]string{
				"timestamp", "runtime_sec", "total_biomass",
				"t0", "t1", "t2", "t3", "t4", "deep_archive",
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
		tiers := []string{"table0", "table1", "table2", "table3", "table4", "deep_archive"}

		for _, t := range tiers {
			var c int
			pool.QueryRow(context.Background(), fmt.Sprintf("SELECT count(*) FROM %s", t)).Scan(&c)
			counts[t] = c
			total += int64(c)
			StateValue.WithLabelValues(t).Set(float64(c))
		}

		// 3. Prometheus Updates
		runtime := time.Since(startTime).Seconds()
		lambda := getLambda()

		// NEU: PID Werte abholen
		injTarget, injDone, simRunning, kp, ki, kd := getSystemState()

		LambdaValue.Set(lambda)
		TotalBiomassValue.Set(float64(total))
		InjectTargetValue.Set(float64(injTarget))
		InjectDoneValue.Set(float64(injDone))

		// NEU: PID Setzen
		PidKpValue.Set(kp)
		PidKiValue.Set(ki)
		PidKdValue.Set(kd)

		if simRunning {
			SimActiveValue.Set(1.0)
		} else {
			SimActiveValue.Set(0.0)
		}

		// Phi-Diff & CSV Schreiben (unverändert, hier abgekürzt der Übersicht halber)
		// ... (Der Rest bleibt identisch wie vorher) ...

		// (Füge hier den Rest der Phi-Berechnung und CSV-Schreiben aus der vorherigen Datei ein
		// oder kopiere einfach die oberen Änderungen in deine bestehende Datei)
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

		f, err := os.OpenFile(cfg.CSVFile, os.O_APPEND|os.O_WRONLY, 0644)
		if err == nil {
			writer := csv.NewWriter(f)
			record := []string{
				time.Now().Format(time.RFC3339), fmt.Sprintf("%.0f", runtime), fmt.Sprintf("%d", total),
				fmt.Sprintf("%d", counts["table0"]), fmt.Sprintf("%d", counts["table1"]),
				fmt.Sprintf("%d", counts["table2"]), fmt.Sprintf("%d", counts["table3"]),
				fmt.Sprintf("%d", counts["table4"]), fmt.Sprintf("%d", counts["deep_archive"]),
				pcts["table0"], pcts["table1"], pcts["table2"], pcts["table3"], pcts["table4"],
				fmt.Sprintf("%f", lambda), fmt.Sprintf("%.4f", phiDiff),
			}
			writer.Write(record)
			writer.Flush()
			f.Close()
		}
	}
}
