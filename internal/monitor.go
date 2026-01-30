package internal

import (
	"context"
	"encoding/csv"
	"fmt"
	"log"
	"math"
	"os"
	"strconv"
	"time"

	// WICHTIG: Wir importieren pgxpool, damit die Typen passen
	"github.com/jackc/pgx/v5/pgxpool"
)

// MonitorConfig steuert, wie oft geloggt wird und wohin
type MonitorConfig struct {
	Interval  time.Duration
	TargetPhi float64
	CSVFile   string
}

// StartMonitor akzeptiert jetzt *pgxpool.Pool (Passend zu decay_worker.go)
func StartMonitor(db *pgxpool.Pool, cfg MonitorConfig, getLambda func() float64) {
	// --- CSV SETUP ---
	f, err := os.OpenFile(cfg.CSVFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Printf("⚠️ Konnte CSV-Log nicht öffnen: %v", err)
	}
	defer f.Close()

	writer := csv.NewWriter(f)
	defer writer.Flush()

	// Header schreiben, wenn Datei leer ist
	fi, err := f.Stat()
	if err == nil && fi.Size() == 0 {
		header := []string{"timestamp", "runtime_sec", "total_biomass", "t0", "t1", "t2", "t3", "t4", "deep_archive", "lambda", "phi_diff"}
		writer.Write(header)
		writer.Flush()
	}

	ticker := time.NewTicker(cfg.Interval)
	startTime := time.Now()

	log.Printf("📊 Monitoring Service & CSV Logger started (PGX Version). File: %s", cfg.CSVFile)

	for range ticker.C {
		logStats(db, writer, startTime, getLambda(), cfg.TargetPhi)
		writer.Flush()
	}
}

func logStats(db *pgxpool.Pool, csvWriter *csv.Writer, start time.Time, currentLambda float64, targetPhi float64) {
	ctx := context.Background() // pgx benötigt Context

	// --- 1. Zähle aktive Tiers ---
	counts := make(map[int]int)
	activeTotal := 0

	for i := 0; i <= 4; i++ {
		var count int
		tableName := fmt.Sprintf("table%d", i)
		// Nutzung von pgx Syntax (QueryRow mit Context)
		_ = db.QueryRow(ctx, "SELECT count(*) FROM "+tableName).Scan(&count)
		counts[i] = count
		activeTotal += count
	}

	// --- 2. Zähle Deep Archives ---
	var deepArchiveTotal int
	for i := 0; i < 10; i++ {
		var count int
		tableName := fmt.Sprintf("archive%d", i)
		if err := db.QueryRow(ctx, "SELECT count(*) FROM "+tableName).Scan(&count); err == nil {
			deepArchiveTotal += count
		}
	}

	totalBiomass := activeTotal + deepArchiveTotal
	if totalBiomass == 0 {
		return
	}

	// --- 3. Berechnungen ---
	runTime := time.Since(start).Seconds()

	// Phi Abweichung
	var phiDiff float64
	var phiDevStr string
	if counts[0] > 0 && counts[1] > 0 {
		ratio := float64(counts[1]) / float64(counts[0])
		phiDiff = math.Abs(ratio - targetPhi)
		phiDevStr = fmt.Sprintf("Φ-Diff: %.2f", phiDiff)
	} else {
		phiDevStr = "Φ-Diff: N/A"
	}

	// --- 4. Konsolen-Output ---
	log.Printf(
		"[t=%.0fs] λ=%.5f | Total: %d | T0: %d (%d%%) | T1: %d (%d%%) | T4: %d | Deep: %d | %s",
		runTime, currentLambda, totalBiomass,
		counts[0], percentage(counts[0], totalBiomass),
		counts[1], percentage(counts[1], totalBiomass),
		counts[4], deepArchiveTotal, phiDevStr,
	)

	// --- 5. CSV-Output ---
	record := []string{
		time.Now().Format(time.RFC3339),
		fmt.Sprintf("%.0f", runTime),
		strconv.Itoa(totalBiomass),
		strconv.Itoa(counts[0]),
		strconv.Itoa(counts[1]),
		strconv.Itoa(counts[2]),
		strconv.Itoa(counts[3]),
		strconv.Itoa(counts[4]),
		strconv.Itoa(deepArchiveTotal),
		fmt.Sprintf("%.6f", currentLambda),
		fmt.Sprintf("%.4f", phiDiff),
	}
	csvWriter.Write(record)
}

func percentage(part, total int) int {
	if total == 0 {
		return 0
	}
	return int((float64(part) / float64(total)) * 100)
}
