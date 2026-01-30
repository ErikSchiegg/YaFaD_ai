package main

/*
#cgo LDFLAGS: -L./core/target/release -lyafad_core
#include "core/src/libyafad_core.h"
*/
import "C"

import (
	"database/sql" // Wichtig für sql.DB
	"log"
	"sync"
	"time"

	// Falls dein Modul in go.mod "github.com/DeinName/YaFaD_ai" heißt,
	// musst du den Pfad hier anpassen!
	"YaFaD_ai/internal/monitoring"

	_ "github.com/lib/pq" // Postgres Treiber nicht vergessen (Blank Import)
)

// PolicyConfig mimics the yaml structure
type PolicyConfig struct {
	Enabled   bool
	Threshold float64
	Mode      string
	DumpPath  string
}

// Record mimics your DB struct
type Record struct {
	ID       string
	Utility  float64
	Lambda   float64
	LastSeen time.Time
}

func main() {
	log.Println("🚀 YaFaD_ai Worker v0.2.0 starting...")
	log.Println("🇺🇸 Mode: US-Optimized | Policy: Radical Efficiency")

	// --- 1. DB Verbindung herstellen (FEHLTE VORHER) ---
	// Hier musst du deinen echten Connection-String eintragen!
	connStr := "user=postgres password=secret dbname=yafad sslmode=disable"
	db, err := sql.Open("postgres", connStr)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	// Optional: Verbindung testen
	if err := db.Ping(); err != nil {
		log.Printf("⚠️ Warning: DB connection failed: %v", err)
		// Wir machen weiter für den Demo-Modus, aber in Echt wäre hier Schluss.
	}

	// --- 2. Config Setup ---
	policy := PolicyConfig{
		Enabled:   true,
		Threshold: 0.0001,
		Mode:      "DELETE",
		DumpPath:  "./mnt/tape_archive/fossils/",
	}

	var wg sync.WaitGroup
	wg.Add(4)

	// Worker Signature: (router, tier, IDEAL_CAPACITY, baseLambda, min, max, minSleep, maxSleep)

	// T0 -> T1: Ideal ~20.000
	go func() {
		defer wg.Done()
		runHomeostaticWorker(router, 0, 20000, 0.01, 0.001, 5.0, 1*time.Millisecond, 100*time.Millisecond)
	}()

	// T1 -> T2: Ideal ~32.000 (20k * 1.618)
	go func() {
		defer wg.Done()
		runHomeostaticWorker(router, 1, 32000, 0.01, 0.001, 2.0, 10*time.Millisecond, 500*time.Millisecond)
	}()

	// T2 -> T3: Ideal ~51.000 (32k * 1.618)
	go func() {
		defer wg.Done()
		runHomeostaticWorker(router, 2, 51000, 0.005, 0.001, 1.0, 50*time.Millisecond, 1*time.Second)
	}()

	// T3 -> T4: Ideal ~82.000
	go func() {
		defer wg.Done()
		runHomeostaticWorker(router, 3, 82000, 0.005, 0.001, 0.05, 1*time.Second, 10*time.Second)
	}()

	// --- 3. Monitoring Starten (Hintergrund) ---

	getCurrentLambda := func() float64 {
		return 0.0085 // Später dynamisch aus dem PID Controller holen
	}

	monitorConfig := monitoring.Config{
		Interval:  10 * time.Second,
		TargetPhi: 1.618,
	}

	// Hier übergeben wir das 'db' Objekt, das wir oben erstellt haben
	monitoring.StartMonitor(db, monitorConfig, getCurrentLambda)
	log.Println("📊 Monitoring background service started...")

	// --- 4. Main Loop (Vordergrund) ---

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	log.Println("⚙️ Worker Loop active. Press Ctrl+C to stop.")

	// WICHTIG: Das 'select {}' ist weg! Wir nutzen den Loop als Blocker.
	for range ticker.C {
		// Mock: Wir tun so, als hätten wir einen sterbenden Record gefunden
		mockRecord := Record{
			ID:       "record_xyz_123",
			Utility:  0.00005,
			Lambda:   0.5,
			LastSeen: time.Now().Add(-100 * time.Hour),
		}

		processRecord(mockRecord, policy)
	}
}

// ---------------------------------------------------------
// HELPER FUNCTIONS
// ---------------------------------------------------------

func processRecord(rec Record, policy PolicyConfig) {
	timeDelta := time.Since(rec.LastSeen).Hours()

	// Call Rust
	result := C.calculate_decay_with_horizon(
		C.double(rec.Utility),
		C.double(rec.Lambda),
		C.double(timeDelta),
		C.double(policy.Threshold),
	)

	// Execute Verdict
	switch result.action {
	case 2: // Vaporize
		if policy.Mode == "DELETE" {
			log.Printf("💀 Event Horizon reached for ID %s (U=%.6f). VAPORIZING immediately.",
				rec.ID, float64(result.new_utility))
			// db.Exec("DELETE FROM archive WHERE id=$1", rec.ID)
		} else if policy.Mode == "GLACIER" {
			log.Printf("❄️ Event Horizon reached for ID %s. Dumping to GLACIER.", rec.ID)
		}
	case 1: // Migrate
		log.Printf("📦 Utility dropped. Migrating ID %s.", rec.ID)
	default: // Keep
		// log.Printf("✅ ID %s remains active.", rec.ID)
	}
}
