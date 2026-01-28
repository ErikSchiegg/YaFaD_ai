package internal
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
	Interval time.Duration
	TargetPhi float64 // Normalerweise 1.618...
}

// StartMonitor startet die Routine in einer Endlosschleife
// getLambda ist eine Funktion, die uns den aktuellen Lambda-Wert aus dem PID-Controller liefert
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
	// 1. Snapshot der aktuellen Verteilung holen
	// Wir gehen davon aus, dass deine Tabelle 'records' heißt und eine Spalte 'tier' hat.
	// Falls deine Struktur anders ist (z.B. separate Tabellen), muss das SQL angepasst werden.
	rows, err := db.Query("SELECT tier, COUNT(*) FROM records GROUP BY tier ORDER BY tier ASC")
	if err != nil {
		log.Printf("⚠️ Monitor Error: %v", err)
		return
	}
	defer rows.Close()

	counts := make(map[int]int)
	totalRecords := 0

	for rows.Next() {
		var tier int
		var count int
		if err := rows.Scan(&tier, &count); err == nil {
			counts[tier] = count
			totalRecords += count
		}
	}

	if totalRecords == 0 {
		return // Nichts zu berichten
	}

	// 2. String bauen
	runTime := time.Since(start).Seconds()
	
	// Berechne Tier 0 (Hot) vs Tier 1 (Warm) Verhältnis
	t0 := counts[0]
	t1 := counts[1]
	
	// Phi-Abweichung berechnen (Ideal: T1 sollte T0 * Phi sein, vereinfacht)
	var phiDev string
	if t0 > 0 && t1 > 0 {
		ratio := float64(t1) / float64(t0)
		diff := math.Abs(ratio - targetPhi)
		phiDev = fmt.Sprintf("Φ-Diff: %.2f", diff)
	} else {
		phiDev = "Φ-Diff: N/A"
	}

	// 3. Die "Money Line" für den Reviewer
	// [t=120s] λ=0.045 | Total: 5000 | T0: 1200 (24%) | T1: 1900 (38%) | ...
	log.Printf(
		"[t=%.0fs] λ=%.5f | Total: %d | T0: %d (%d%%) | T1: %d (%d%%) | Arc: %d | %s",
		runTime,
		currentLambda,
		totalRecords,
		t0, percentage(t0, totalRecords),
		t1, percentage(t1, totalRecords),
		counts[2], // Archive / Vaporize Candidate
		phiDev,
	)
}

func percentage(part, total int) int {
	if total == 0 {
		return 0
	}
	return int((float64(part) / float64(total)) * 100)
}