package main

import (
	"context"
	"flag"
	"fmt"
	"math"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var currentSleepMs int64 = 0

func main() {
	countPtr := flag.Int("count", 100000, "Total records to inject")
	capPtr := flag.Int("cap", 20000, "Base Capacity of T0")
	workersPtr := flag.Int("workers", 4, "Number of workers")
	flag.Parse()

	totalRows := *countPtr
	baseCap := float64(*capPtr)
	workers := *workersPtr
	batchSize := 500

	totalBatches := int(math.Ceil(float64(totalRows) / float64(batchSize)))
	batchesPerWorker := int(math.Ceil(float64(totalBatches) / float64(workers)))

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

	// Estimate Duration
	estimatedDuration := time.Duration(totalRows/1500) * time.Second
	if estimatedDuration < 10*time.Second {
		estimatedDuration = 10 * time.Second
	}

	fmt.Printf("\n🌊 HYBRID BIO-GENERATOR v0.6.6 (Syntax Verified)\n")
	fmt.Printf("   Target: %d rows\n", totalRows)
	fmt.Printf("   Est. Time: %v\n", estimatedDuration)

	startTime := time.Now()

	// --- PACEMAKER (Hybrid) ---
	go func() {
		for {
			elapsed := time.Since(startTime)
			progress := float64(elapsed) / float64(estimatedDuration)
			if progress > 1.0 {
				progress = 1.0
			}

			// 1. SEASONAL FACTOR (Sine Wave)
			sineFactor := 0.5 * (1.0 + math.Cos(progress*math.Pi))

			// 2. PRESSURE FACTOR (Sigmoid Damping)
			var t0Count int
			pool.QueryRow(ctx, "SELECT count(*) FROM table0").Scan(&t0Count)
			pressure := float64(t0Count) / baseCap

			k := 15.0
			target := 0.95
			sigmoidFactor := 1.0 / (1.0 + math.Exp(k*(pressure-target)))

			// 3. HYBRID RATE
			combinedFactor := sineFactor * sigmoidFactor
			baseDelay := 2000.0 * (1.0 - combinedFactor)
			newSleep := int64(baseDelay)

			if newSleep > 3000 {
				newSleep = 3000
			}
			if pressure > 1.20 {
				newSleep = 5000
			} // Notbremse

			atomic.StoreInt64(&currentSleepMs, newSleep)

			// Viz Update
			statusIcon := "🟢"
			if pressure > 0.95 {
				statusIcon = "🟠"
			}
			if pressure > 1.05 {
				statusIcon = "🔴"
			}
			if pressure > 2.00 {
				statusIcon = "💥"
			}

			fmt.Printf("\r%s P: %3.0f%% | Sea: %.2f Damp: %.2f | Delay: %4dms | Row: %d    ",
				statusIcon, pressure*100, sineFactor, sigmoidFactor, newSleep, t0Count)

			time.Sleep(100 * time.Millisecond)
		}
	}()

	// --- WORKERS ---
	var wg sync.WaitGroup
	wg.Add(workers)
	var ops int64 = 0

	for i := 0; i < workers; i++ {
		go func(id int) {
			defer wg.Done()
			for b := 0; b < batchesPerWorker; b++ {
				delay := atomic.LoadInt64(&currentSleepMs)
				if delay > 0 {
					time.Sleep(time.Duration(delay) * time.Millisecond)
				}

				currentRows := [][]interface{}{}
				for j := 0; j < batchSize; j++ {
					jsonPayload := fmt.Sprintf(`{"content": "hybrid-data", "w": %d}`, id)
					currentRows = append(currentRows, []interface{}{
						fmt.Sprintf("hyb-%d-%d-%d", id, time.Now().UnixNano(), j),
						jsonPayload,
						1.0,
						time.Now(),
					})
				} // End for j

				_, errCopy := pool.CopyFrom(
					ctx,
					pgx.Identifier{"table0"},
					[]string{"id", "payload", "utility_index", "last_activity"},
					pgx.CopyFromRows(currentRows),
				)

				if errCopy == nil {
					atomic.AddInt64(&ops, int64(batchSize))
				} else {
					time.Sleep(1 * time.Second)
				}
			} // End for b
		}(i) // End go func
	} // End for i

	wg.Wait()
	dur := time.Since(startTime)
	rps := float64(atomic.LoadInt64(&ops)) / dur.Seconds()
	fmt.Printf("\n\n🏁 DONE. Injected: %d | RPS: %.0f\n", atomic.LoadInt64(&ops), rps)
} // End main
