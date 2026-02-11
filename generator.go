package main

import (
	"context"
	"encoding/json"
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

// Config Struct
type SystemConfig struct {
	Capacities map[string]int `json:"capacities"`
}

func main() {
	countPtr := flag.Int("count", 100000, "Total records to inject")
	modePtr := flag.String("mode", "simple", "Mode: simple | scenario")
	workersPtr := flag.Int("workers", 4, "Number of workers")
	flag.Parse()

	totalRows := *countPtr
	workers := *workersPtr
	batchSize := 500

	// Config Load
	baseCap := 20000.0
	data, err := os.ReadFile("yafad_config.json")
	if err == nil {
		var conf SystemConfig
		if json.Unmarshal(data, &conf) == nil {
			if val, ok := conf.Capacities["table0"]; ok && val > 0 {
				baseCap = float64(val)
			}
		}
	}

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

	estimatedDuration := time.Duration(totalRows/1500) * time.Second
	if estimatedDuration < 10*time.Second {
		estimatedDuration = 10 * time.Second
	}

	fmt.Printf("\n🌊 HYBRID GENERATOR v0.8.1 (Mode: %s)\n", *modePtr)
	fmt.Printf("   Target: %d | Cap: %.0f\n", totalRows, baseCap)

	startTime := time.Now()

	// --- PACEMAKER ---
	go func() {
		for {
			elapsed := time.Since(startTime)

			// SCENARIO LOGIC
			progress := float64(elapsed) / float64(estimatedDuration)
			isLull := false

			if *modePtr == "scenario" {
				// Wenn alle Daten drin sind, gehen wir in den "Aging Mode" (Die Dürre)
				if progress > 1.2 {
					isLull = true
				}
			}

			if isLull {
				// LULL PHASE: 0 Injection
				atomic.StoreInt64(&currentSleepMs, 5000) // Sleep long
				fmt.Printf("\r💤 LULL PHASE (Aging...) | Time: %.0fs      ", elapsed.Seconds())
				time.Sleep(1 * time.Second)
				continue
			}

			// NORMAL PHASE (Sine/Sigmoid)
			if progress > 1.0 {
				progress = 1.0
			}

			sineFactor := 0.5 * (1.0 + math.Cos(progress*math.Pi))

			var t0Count int
			pool.QueryRow(ctx, "SELECT count(*) FROM table0").Scan(&t0Count)
			pressure := float64(t0Count) / baseCap

			k := 15.0
			target := 0.95
			sigmoidFactor := 1.0 / (1.0 + math.Exp(k*(pressure-target)))

			combinedFactor := sineFactor * sigmoidFactor
			baseDelay := 2000.0 * (1.0 - combinedFactor)
			newSleep := int64(baseDelay)

			if newSleep > 3000 {
				newSleep = 3000
			}
			if pressure > 1.20 {
				newSleep = 5000
			}

			atomic.StoreInt64(&currentSleepMs, newSleep)

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
				// Check Pause
				for {
					delay := atomic.LoadInt64(&currentSleepMs)
					if delay >= 5000 {
						// Lull Mode -> Worker Pause
						time.Sleep(1 * time.Second)
						continue
					}
					if delay > 0 {
						time.Sleep(time.Duration(delay) * time.Millisecond)
					}
					break
				}

				currentRows := [][]interface{}{}
				for j := 0; j < batchSize; j++ {
					jsonPayload := fmt.Sprintf(`{"content": "v0.8.1-data", "w": %d}`, id)
					currentRows = append(currentRows, []interface{}{
						fmt.Sprintf("gen-%d-%d-%d", id, time.Now().UnixNano(), j),
						jsonPayload,
						1.0,
						time.Now(),
					})
				}

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
			}
		}(i)
	}
	wg.Wait()

	// Nach Abschluss der Injection: Wenn Scenario an ist, warten wir noch für Aging
	if *modePtr == "scenario" {
		fmt.Printf("\n\n🛑 INJECTION DONE. Entering AGING PHASE (Ctrl+C to stop)...\n")
		// Wir lassen den Generator laufen, damit das Terminal nicht sofort schließt,
		// während main.go im Hintergrund weiter aufräumt.
		for {
			time.Sleep(10 * time.Second)
		}
	}

	dur := time.Since(startTime)
	rps := float64(atomic.LoadInt64(&ops)) / dur.Seconds()
	fmt.Printf("\n\n🏁 DONE. Injected: %d | RPS: %.0f\n", atomic.LoadInt64(&ops), rps)
}
