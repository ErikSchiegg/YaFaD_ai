package main

import (
	"context"
	"flag"
	"fmt"
	"math"
	"math/rand"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Global pacing variable (Thread-safe)
var currentSleepMs int64 = 0

func main() {
	// Flags
	countPtr := flag.Int("count", 100000, "Total records to inject")
	// Wir nehmen 20k als Default-Basis für die Druckberechnung,
	// idealerweise passt das zu deiner main.go Einstellung.
	capPtr := flag.Int("cap", 20000, "Base Capacity of T0 (for pressure calc)")
	workersPtr := flag.Int("workers", 4, "Number of concurrent workers")
	flag.Parse()

	totalRows := *countPtr
	baseCap := float64(*capPtr)
	workers := *workersPtr
	batchSize := 500

	// Batches berechnen
	totalBatches := int(math.Ceil(float64(totalRows) / float64(batchSize)))
	batchesPerWorker := int(math.Ceil(float64(totalBatches) / float64(workers)))

	// DB Connection
	dbUser := os.Getenv("DB_USER")
	if dbUser == "" {
		dbUser = "eriks"
	}
	dbPass := os.Getenv("DB_PASSWORD")
	if dbPass == "" {
		dbPass = "test"
	}
	connStr := fmt.Sprintf("postgres://%s:%s@localhost:5432/yafad_test?sslmode=disable", dbUser, dbPass)

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		panic(err)
	}
	defer pool.Close()

	fmt.Printf("\n🌊 SMART BIO-GENERATOR v0.5.0 STARTED\n")
	fmt.Printf("   Target: %d rows\n", totalRows)
	fmt.Printf("   T0 Cap: %.0f (Regulating for ~95%% Load)\n", baseCap)

	// =========================================================================
	// 1. THE PACEMAKER (Herzschrittmacher) 💓
	// Dieser Goroutine prüft alle 100ms den Füllstand von T0 und setzt das Tempo.
	// =========================================================================
	go func() {
		for {
			var t0Count int
			// Schneller Count (ist bei Postgres Index-Only Scan, also billig)
			err := pool.QueryRow(ctx, "SELECT count(*) FROM table0").Scan(&t0Count)
			if err != nil {
				time.Sleep(500 * time.Millisecond)
				continue
			}

			pressure := float64(t0Count) / baseCap

			// --- DIE MATHEMATISCHE FUNKTION (Inverted Exponential) ---
			var newSleep int64

			if pressure < 0.80 {
				// Unter 80%: VOLLE KRAFT (0ms Wartezeit)
				newSleep = 0
			} else if pressure > 1.10 {
				// Über 110%: NOTBREMSE (Lange Wartezeit)
				newSleep = 2000
			} else {
				// Dazwischen (80% - 110%): Exponentielles Bremsen
				// Wir wollen bei 0.95 (95%) etwa 50-100ms Pause haben, um den Fluss zu glätten.
				// Formel: Base * e^(Steilheit * (Druck - Offset))

				// Beispiel:
				// P=0.80 -> exp(0) = 1       -> 10ms
				// P=0.90 -> exp(1) = 2.7     -> 27ms
				// P=1.00 -> exp(2) = 7.3     -> 73ms
				// P=1.10 -> exp(3) = 20      -> 200ms

				exponent := 10.0 * (pressure - 0.80) // Steilheit
				val := 10.0 * math.Exp(exponent)
				newSleep = int64(val)
			}

			// Safety Cap
			if newSleep > 3000 {
				newSleep = 3000
			}

			// Atomares Update für die Worker
			atomic.StoreInt64(&currentSleepMs, newSleep)

			// Live Status Zeile (überschreibt sich selbst)
			statusIcon := "🟢"
			if pressure > 0.95 {
				statusIcon = "🟠"
			}
			if pressure > 1.05 {
				statusIcon = "🔴"
			}

			fmt.Printf("\r%s Pressure: %3.0f%% | Pace: %4dms Delay | Injecting... ", statusIcon, pressure*100, newSleep)

			time.Sleep(100 * time.Millisecond)
		}
	}()

	// =========================================================================
	// 2. THE WORKERS (Die Muskeln) 💪
	// =========================================================================
	var wg sync.WaitGroup
	wg.Add(workers)

	startTime := time.Now()

	for i := 0; i < workers; i++ {
		go func(id int) {
			defer wg.Done()
			for b := 0; b < batchesPerWorker; b++ {

				// 1. Lesen der aktuellen Geschwindigkeit vom Pacemaker
				delay := atomic.LoadInt64(&currentSleepMs)
				if delay > 0 {
					time.Sleep(time.Duration(delay) * time.Millisecond)
				}

				// 2. Daten generieren
				rows := [][]interface{}{}
				for j := 0; j < batchSize; j++ {
					// 20% "Altlasten" (Simulation von Cache-Misses)
					uIndex := 1.0
					age := time.Now()
					if rand.Float64() < 0.2 {
						uIndex = 0.5
						age = time.Now().Add(-12 * time.Hour)
					}

					jsonPayload := fmt.Sprintf(`{"content": "smart-data-%d", "worker": %d}`, rand.Int(), id)

					rows = append(rows, []interface{}{
						fmt.Sprintf("smart-%d-%d-%d", id, time.Now().UnixNano(), j),
						jsonPayload,
						uIndex,
						age,
					})
				}

				// 3. High-Speed Copy
				_, err := pool.CopyFrom(
					ctx,
					pgx.Identifier{"table0"},
					[]string{"id", "payload", "utility_index", "last_activity"},
					pgx.CopyFromRows(rows),
				)

				if err != nil {
					// Bei Fehler (z.B. DB overloaded) kurz warten
					time.Sleep(1 * time.Second)
				}
			}
		}(i)
	}

	wg.Wait()
	duration := time.Since(startTime)
	rps := float64(totalRows) / duration.Seconds()

	fmt.Printf("\n\n🏁 INJECTION COMPLETE\n")
	fmt.Printf("   Time: %v\n", duration)
	fmt.Printf("   Avg Speed: %.0f rows/sec\n", rps)
	fmt.Printf("   System State: Should be near equilibrium.\n")
}
