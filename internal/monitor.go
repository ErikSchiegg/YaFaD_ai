package monitoring

import (
	"database/sql"
	"fmt"
	"log"
	"math"
	"time"
)

// Config steuert, wie oft geloggt wird
type Config struct {
	Interval  time.Duration
	TargetPhi float64 // Normalerweise 1.618...
}

// StartMonitor startet die Routine in einer Endlosschleife
func StartMonitor(db *sql.DB, cfg Config, getLambda func() float64) {
	ticker := time.NewTicker(cfg.Interval)
	startTime := time.Now()

	log.Printf("📊 Monitoring Service started. Interval: %v", cfg.Interval)

	go func() {
		for range ticker.C {
			logStats(db, startTime, getLambda(), cfg.TargetPhi)
		}
	}()
}

func logStats(db *sql.DB, start time.Time, currentLambda float64, targetPhi float64) {
	// --- 1. Zähle die aktiven Tiers (table0 bis table4) ---
	counts := make(map[int]int)
	activeTotal := 0

	for i := 0; i <= 4; i++ {
		var count int
		tableName := fmt.Sprintf("table%d", i)
		// Fehler ignorieren, falls Tabelle leer/nicht existent
		_ = db.QueryRow("SELECT count(*) FROM " + tableName).Scan(&count)
		counts[i] = count
		activeTotal += count
	}

	// --- 2. Zähle die Deep Archives (archive0 bis archive9) ---
	var deepArchiveTotal int
	for i := 0; i < 10; i++ {
		var count int
		tableName := fmt.Sprintf("archive%d", i)
		// Fehler ignorieren
		err := db.QueryRow("SELECT count(*) FROM " + tableName).Scan(&count)
		if err == nil {
			deepArchiveTotal += count
		}
	}

	// --- 3. Gesamtsumme berechnen ---
	totalBiomass := activeTotal + deepArchiveTotal

	if totalBiomass == 0 {
		return // Nichts zu berichten
	}

	// --- 4. Berechnungen für die Statistik ---
	runTime := time.Since(start).Seconds()

	// Berechne Tier 0 (Hot) vs Tier 1 (Warm) Verhältnis
	t0 := counts[0]
	t1 := counts[1]

	var phiDev string
	if t0 > 0 && t1 > 0 {
		ratio := float64(t1) / float64(t0)
		diff := math.Abs(ratio - targetPhi)
		phiDev = fmt.Sprintf("Φ-Diff: %.2f", diff)
	} else {
		phiDev = "Φ-Diff: N/A"
	}

	// --- 5. Log-Ausgabe ---
	// Hier wird jetzt alles korrekt angezeigt:
	// T4: Das Übergabetier zum Archiv
	// Deep: Die Summe aller Archive
	log.Printf(
		"[t=%.0fs] λ=%.5f | Total: %d | T0: %d (%d%%) | T1: %d (%d%%) | T4: %d | Deep: %d | %s",
		runTime,
		currentLambda,
		totalBiomass,
		t0, percentage(t0, totalBiomass),
		t1, percentage(t1, totalBiomass),
		counts[4],
		deepArchiveTotal,
		phiDev,
	)
}

// Hilfsfunktion (nur einmal!)
func percentage(part, total int) int {
	if total == 0 {
		return 0
	}
	return int((float64(part) / float64(total)) * 100)
}
