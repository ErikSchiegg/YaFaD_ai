package main

/*
#cgo LDFLAGS: -L${SRCDIR}/core/target/release -lyafad_core -Wl,-rpath,${SRCDIR}/core/target/release -lm -ldl
#cgo CPPFLAGS: -I${SRCDIR}/core
extern double calculate_decay(double u_last, double lambda, double delta_t);
*/
import "C"
import (
	"context"
	"fmt"
	"math"
	"os"
	"sync"
	"time"

	// PFAD ANPASSEN FALLS NÖTIG (z.B. "yafad/internal")
	"yafad/internal"

	"github.com/jackc/pgx/v5/pgxpool"
)

const PHI = 1.61803398875

// --- ANSI Colors ---
const (
	ColorReset  = "\033[0m"
	ColorRed    = "\033[1;31m"
	ColorYellow = "\033[1;33m"
	ColorGreen  = "\033[1;32m"
	ColorCyan   = "\033[1;36m"
	ColorBlue   = "\033[1;34m"
)

func colorize(tableName string) string {
	switch tableName {
	case "table0":
		return ColorRed + tableName + ColorReset
	case "table1":
		return ColorYellow + tableName + ColorReset
	case "table2":
		return ColorGreen + tableName + ColorReset
	case "table3":
		return ColorCyan + tableName + ColorReset
	case "table4":
		return ColorBlue + tableName + ColorReset
	default:
		return tableName
	}
}

// --- Storage Router ---
type StorageRouter struct {
	HotPool  *pgxpool.Pool
	ColdPool *pgxpool.Pool
}

func (r *StorageRouter) GetPool(tier int) *pgxpool.Pool {
	if tier >= 3 {
		return r.ColdPool
	}
	return r.HotPool
}

func main() {
	// --- DB Connection Setup ---
	dbUser := os.Getenv("DB_USER")
	if dbUser == "" {
		dbUser = "eriks"
	}
	dbPass := os.Getenv("DB_PASSWORD")
	if dbPass == "" {
		dbPass = "test"
	}

	hotConnStr := fmt.Sprintf("postgres://%s:%s@localhost:5432/yafad_test?sslmode=disable", dbUser, dbPass)

	ctx := context.Background()
	hotPool, err := pgxpool.New(ctx, hotConnStr)
	if err != nil {
		panic(err)
	}
	defer hotPool.Close()

	coldPool, err := pgxpool.New(ctx, hotConnStr)
	if err != nil {
		panic(err)
	}
	defer coldPool.Close()

	router := &StorageRouter{HotPool: hotPool, ColdPool: coldPool}

	fmt.Println("📉 YaFaD_ai Federated Decay Engine v0.4.2 (Live Lambda) Active.")

	// --- LIVE LAMBDA BRIDGE ---
	// Eine thread-sichere Variable, um das T0-Lambda an den Monitor zu übergeben
	var (
		t0Lambda float64
		lambdaMu sync.RWMutex
	)

	// Callback: Worker ruft das auf, um Lambda zu melden
	reportT0Lambda := func(val float64) {
		lambdaMu.Lock()
		t0Lambda = val
		lambdaMu.Unlock()
	}

	// Getter: Monitor ruft das auf, um Lambda zu lesen
	getT0Lambda := func() float64 {
		lambdaMu.RLock()
		defer lambdaMu.RUnlock()
		return t0Lambda
	}

	var wg sync.WaitGroup
	wg.Add(4)

	// --- WORKER START ---

	// T0 -> T1 (Der Hochofen - REPORTET SEIN LAMBDA!)
	go func() {
		defer wg.Done()
		// Wir übergeben 'reportT0Lambda' an den ersten Worker
		runHomeostaticWorker(router, 0, 28743, 0.01, 0.001, 5.0, 1*time.Millisecond, 100*time.Millisecond, reportT0Lambda)
	}()

	// T1 -> T2 (REPORTET NICHTS - nil)
	go func() {
		defer wg.Done()
		runHomeostaticWorker(router, 1, 46507, 0.01, 0.001, 2.0, 10*time.Millisecond, 500*time.Millisecond, nil)
	}()

	// T2 -> T3
	go func() {
		defer wg.Done()
		runHomeostaticWorker(router, 2, 75250, 0.005, 0.001, 1.0, 50*time.Millisecond, 1*time.Second, nil)
	}()

	// T3 -> T4
	go func() {
		defer wg.Done()
		runHomeostaticWorker(router, 3, 121757, 0.005, 0.001, 0.05, 1*time.Second, 10*time.Second, nil)
	}()

	// --- MONITORING ---
	// Nutzt jetzt den echten Getter!
	go internal.StartMonitor(hotPool, internal.MonitorConfig{
		Interval:  5 * time.Second,
		TargetPhi: PHI,
		CSVFile:   "yafad_metrics.csv",
	}, getT0Lambda)

	wg.Wait()
}

// --- THE LOGIC CORE ---

// Neue Signatur: reportLambda func(float64) am Ende hinzugefügt
func runHomeostaticWorker(router *StorageRouter, startTier int, idealCapacity int, baseLambda, min, max float64, minSleep, maxSleep time.Duration, reportLambda func(float64)) {
	ctx := context.Background()
	currentLambda := baseLambda
	threshold := 0.4
	currentSleep := maxSleep

	sourceTier := startTier
	targetTier := startTier + 1
	sourceTable := fmt.Sprintf("table%d", sourceTier)
	targetTable := fmt.Sprintf("table%d", targetTier)

	baseBatchSize := 1000

	for {
		sourcePool := router.GetPool(sourceTier)
		targetPool := router.GetPool(targetTier)

		var sourceCount, targetCount int
		sourcePool.QueryRow(ctx, fmt.Sprintf("SELECT count(*) FROM %s", sourceTable)).Scan(&sourceCount)
		targetPool.QueryRow(ctx, fmt.Sprintf("SELECT count(*) FROM %s", targetTable)).Scan(&targetCount)

		currentBatchSize := baseBatchSize
		isEmergency := false
		isHibernating := false

		// --- PID & PRESSURE LOGIC ---
		if sourceCount > 0 && targetCount > 0 {
			currentRatio := float64(targetCount) / float64(sourceCount)
			diff := PHI - currentRatio
			pressure := float64(sourceCount) / float64(idealCapacity)

			if currentRatio > (PHI*1.5) && pressure < 0.8 {
				currentLambda = min
				isHibernating = true
			} else {
				if currentRatio > PHI {
					currentLambda *= 0.90
				} else {
					errorMagnitude := math.Abs(diff)
					aggression := math.Pow(errorMagnitude, 2.0)
					currentLambda *= (1.0 + 0.05 + aggression)
				}

				if pressure > 1.1 {
					pressureBoost := baseLambda * pressure * 0.5
					if pressureBoost > currentLambda {
						currentLambda = pressureBoost
						if pressure > 3.0 {
							currentBatchSize = int(float64(baseBatchSize) * math.Min(pressure, 10.0))
							if currentBatchSize > 20000 {
								currentBatchSize = 20000
							}
							isEmergency = true
						}
					}
				}
			}

			if currentLambda < min {
				currentLambda = min
			}
			if currentLambda > max {
				currentLambda = max
			}
		}

		// --- LIVE REPORTING ---
		// Wenn eine Callback-Funktion übergeben wurde (nur T0), melden wir den neuen Wert
		if reportLambda != nil {
			reportLambda(currentLambda)
		}

		// --- DECAY EXECUTION ---
		workFound := false
		effectiveBatchSize := currentBatchSize
		if isHibernating {
			effectiveBatchSize = 10
		}

		if sourceCount > 0 {
			query := fmt.Sprintf("SELECT id, utility_index, last_activity, payload FROM %s LIMIT %d", sourceTable, effectiveBatchSize)
			rows, err := sourcePool.Query(ctx, query)

			if err == nil {
				for rows.Next() {
					var id, payload string
					var uLast float64
					var lastActivity time.Time
					rows.Scan(&id, &uLast, &lastActivity, &payload)

					deltaT := time.Since(lastActivity).Hours()
					uNow := float64(C.calculate_decay(C.double(uLast), C.double(currentLambda), C.double(deltaT)))

					if uNow < threshold {
						success := migrateRecord(ctx, sourcePool, targetPool, sourceTable, targetTable, id, payload, uNow, lastActivity)
						if success {
							workFound = true
						}
					}
				}
				rows.Close()
			}
		}

		// --- LOGGING ---
		if workFound {
			if isEmergency || (float64(sourceCount)/float64(idealCapacity) > 2.0) {
				currentSleep = 10 * time.Millisecond
			} else if isHibernating {
				currentSleep = 2 * time.Second
			} else {
				ratio := float64(targetCount) / math.Max(1, float64(sourceCount))
				press := float64(sourceCount) / float64(idealCapacity)

				fmt.Printf("⚖️ [%s->%s] Rat: %.2f | Press: %.1fx | λ: %.5f\n",
					colorize(sourceTable), colorize(targetTable), ratio, press, currentLambda)

				currentSleep /= 2
				if currentSleep < minSleep {
					currentSleep = minSleep
				}
			}
		} else {
			currentSleep *= 2
			if currentSleep > maxSleep {
				currentSleep = maxSleep
			}

			if isHibernating {
				fmt.Printf("❄️ [%s->%s] HIBERNATING | λ: %.5f\n", colorize(sourceTable), colorize(targetTable), currentLambda)
			}
		}

		time.Sleep(currentSleep)
	}
}

func migrateRecord(ctx context.Context, sourcePool, targetPool *pgxpool.Pool, sourceTable, targetTable, id, payload string, uNow float64, lastActivity time.Time) bool {
	if sourcePool == targetPool {
		tx, err := sourcePool.Begin(ctx)
		if err != nil {
			return false
		}
		defer tx.Rollback(ctx)

		_, err = tx.Exec(ctx, fmt.Sprintf("DELETE FROM %s WHERE id = $1", sourceTable), id)
		if err != nil {
			return false
		}
		_, err = tx.Exec(ctx, fmt.Sprintf("INSERT INTO %s (id, payload, utility_index, last_activity) VALUES ($1, $2, $3, $4)", targetTable), id, payload, uNow, lastActivity)
		if err != nil {
			return false
		}
		return tx.Commit(ctx) == nil
	} else {
		time.Sleep(20 * time.Millisecond)
		_, err := targetPool.Exec(ctx, fmt.Sprintf("INSERT INTO %s (id, payload, utility_index, last_activity) VALUES ($1, $2, $3, $4) ON CONFLICT (id) DO NOTHING", targetTable), id, payload, uNow, lastActivity)
		if err != nil {
			return false
		}
		_, err = sourcePool.Exec(ctx, fmt.Sprintf("DELETE FROM %s WHERE id = $1", sourceTable), id)
		return err == nil
	}
}
