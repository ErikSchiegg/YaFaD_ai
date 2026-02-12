package monitoring

import (
	"context"
	"encoding/csv"
	"fmt"
	"math"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Config definiert die Einstellungen für den Monitor
type MonitorConfig struct {
	Interval   time.Duration
	TargetPhi  float64
	CSVFile    string
	Capacities map[string]float64 // NEU: Dynamische Kapazitäten
}

// Alias für main.go
type Config = MonitorConfig

func StartMonitor(pool *pgxpool.Pool, cfg Config, getLambda func() float64) {
	file, err := os.OpenFile(cfg.CSVFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		fmt.Printf("⚠️ Monitor: Cannot open CSV: %v\n", err)
		return
	}
	defer file.Close()

	writer := csv.NewWriter(file)
	defer writer.Flush()

	// Header
	info, _ := file.Stat()
	if info.Size() == 0 {
		writer.Write([]string{"timestamp", "runtime_sec", "total_biomass", "t0", "t1", "t2", "t3", "t4", "deep_archive", "lambda", "phi_diff"})
	}

	ticker := time.NewTicker(cfg.Interval)
	startTime := time.Now()

	fmt.Println("📊 Monitoring active. Writing to", cfg.CSVFile)

	// 1. Monitor Instanz einmalig vor dem Loop erstellen
	mon := NewMonitor()

	// Das ist deine funktionierende Haupt-Schleife
	for range ticker.C {
		ctx := context.Background()

		var t0, t1, t2, t3, t4, archive int
		pool.QueryRow(ctx, "SELECT count(*) FROM table0").Scan(&t0)
		pool.QueryRow(ctx, "SELECT count(*) FROM table1").Scan(&t1)
		pool.QueryRow(ctx, "SELECT count(*) FROM table2").Scan(&t2)
		pool.QueryRow(ctx, "SELECT count(*) FROM table3").Scan(&t3)
		pool.QueryRow(ctx, "SELECT count(*) FROM table4").Scan(&t4)
		pool.QueryRow(ctx, "SELECT count(*) FROM deep_archive").Scan(&archive)

		// ---------------------------------------------------------
		// 2. DIES IST DIE MAGIE: Hier schicken wir die frisch
		// gelesenen Werte direkt an unsere Prometheus-Brücke!
		// ---------------------------------------------------------

		total := t0 + t1 + t2 + t3 + t4 + archive
		lambda := getLambda()

		ratio := 0.0
		if t0 > 0 {
			ratio = float64(t1) / float64(t0)
		}
		phiDiff := math.Abs(cfg.TargetPhi - ratio)
		runtime := int(time.Since(startTime).Seconds())

		// ---------------------------------------------------------
		// DIES IST DIE MAGIE: Hier schicken wir die frisch
		// gelesenen Werte direkt an unsere Prometheus-Brücke!
		mon.RecordSystemIntel(lambda, phiDiff, total)
		mon.RecordState(t0, t1, t2, t3, t4, archive) // Nur noch dieser EINE Aufruf mit 6 Parametern
		// ---------------------------------------------------------

		// CSV Write
		record := []string{
			time.Now().Format(time.RFC3339),
			fmt.Sprintf("%d", runtime),
			fmt.Sprintf("%d", total),
			fmt.Sprintf("%d", t0), // Hier war das "AC"
			fmt.Sprintf("%d", t1),
			fmt.Sprintf("%d", t2),
			fmt.Sprintf("%d", t3),
			fmt.Sprintf("%d", t4),
			fmt.Sprintf("%d", archive),
			fmt.Sprintf("%f", lambda),
			fmt.Sprintf("%.4f", phiDiff),
		}
		writer.Write(record)
		writer.Flush()

		// Console Output mit dynamischen Caps
		p0 := (float64(t0) / cfg.Capacities["table0"]) * 100.0
		p1 := (float64(t1) / cfg.Capacities["table1"]) * 100.0

		fmt.Printf("%s [t=%ds] λ=%.5f | Total: %d | T0: %d (%.0f%%) | T1: %d (%.0f%%) | T4: %d | Deep: %d | Φ-Diff: %.2f\n",
			time.Now().Format("15:04:05"),
			runtime,
			lambda,
			total,
			t0, p0,
			t1, p1,
			t4,
			archive,
			phiDiff,
		)
	}
}
