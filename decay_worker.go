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

	"github.com/jackc/pgx/v5/pgxpool"
)

const PHI = 1.61803398875

// --- ANSI Colors for Thermodynamics ---
const (
	ColorReset  = "\033[0m"
	ColorRed    = "\033[1;31m" // T0: Hot
	ColorYellow = "\033[1;33m" // T1: Warm
	ColorGreen  = "\033[1;32m" // T2: Stable
	ColorCyan   = "\033[1;36m" // T3: Cold
	ColorBlue   = "\033[1;34m" // T4: Glacier
)

// Helper to colorize table names based on Tier ID
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

// --- 1. The Storage Router ---
type StorageRouter struct {
	HotPool  *pgxpool.Pool // T0, T1, T2
	ColdPool *pgxpool.Pool // T3, T4
}

func (r *StorageRouter) GetPool(tier int) *pgxpool.Pool {
	if tier >= 3 {
		return r.ColdPool
	}
	return r.HotPool
}

func main() {
	dbUser := os.Getenv("DB_USER")
	if dbUser == "" {
		dbUser = "eriks"
	}
	dbPass := os.Getenv("DB_PASSWORD")
	if dbPass == "" {
		dbPass = "test"
	}

	hotConnStr := fmt.Sprintf("postgres://%s:%s@localhost:5432/yafad_test?sslmode=disable", dbUser, dbPass)
	coldConnStr := hotConnStr

	ctx := context.Background()
	hotPool, err := pgxpool.New(ctx, hotConnStr)
	if err != nil {
		panic(err)
	}
	defer hotPool.Close()

	coldPool, err := pgxpool.New(ctx, coldConnStr)
	if err != nil {
		panic(err)
	}
	defer coldPool.Close()

	router := &StorageRouter{HotPool: hotPool, ColdPool: coldPool}

	fmt.Println("📉 YaFaD_ai Federated Decay Engine: Active.")

	var wg sync.WaitGroup
	wg.Add(4) // Wir starten 4 Worker

	// T0 -> T1: DER HOCHOFEN (Extrem Aggressiv: Max Lambda 5.0)
	go func() {
		defer wg.Done()
		runHomeostaticWorker(router, 0, 0.01, 0.001, 5.0, 1*time.Millisecond, 100*time.Millisecond)
	}()

	// T1 -> T2: DER DURCHLAUFERHITZER (Aggressiv: Max Lambda 2.0)
	go func() {
		defer wg.Done()
		runHomeostaticWorker(router, 1, 0.01, 0.001, 2.0, 10*time.Millisecond, 500*time.Millisecond)
	}()

	// T2 -> T3: DIE BRÜCKE (Standard: Max Lambda 1.0)
	go func() {
		defer wg.Done()
		runHomeostaticWorker(router, 2, 0.005, 0.001, 1.0, 50*time.Millisecond, 1*time.Second)
	}()

	// T3 -> T4: DAS SEDIMENT (Langsamer: Max Lambda 0.05)
	// Dieser Block fehlte, weshalb wg.Wait() ewig gewartet hätte!
	go func() {
		defer wg.Done()
		runHomeostaticWorker(router, 3, 0.005, 0.001, 0.05, 1*time.Second, 10*time.Second)
	}()

	wg.Wait()
}

func runHomeostaticWorker(router *StorageRouter, startTier int, baseLambda, min, max float64, minSleep, maxSleep time.Duration) {
	ctx := context.Background()
	currentLambda := baseLambda
	threshold := 0.4
	currentSleep := maxSleep

	sourceTier := startTier
	targetTier := startTier + 1
	sourceTable := fmt.Sprintf("table%d", sourceTier)
	targetTable := fmt.Sprintf("table%d", targetTier)

	// Standard Batch Size
	baseBatchSize := 1000

	for {
		sourcePool := router.GetPool(sourceTier)
		targetPool := router.GetPool(targetTier)

		var sourceCount, targetCount int
		sourcePool.QueryRow(ctx, fmt.Sprintf("SELECT count(*) FROM %s", sourceTable)).Scan(&sourceCount)
		targetPool.QueryRow(ctx, fmt.Sprintf("SELECT count(*) FROM %s", targetTable)).Scan(&targetCount)

		// --- ADAPTIVE CONTROL ---
		currentBatchSize := baseBatchSize
		isEmergency := false

		if sourceCount > 0 && targetCount > 0 {
			currentRatio := float64(targetCount) / float64(sourceCount)
			diff := PHI - currentRatio

			// PID Logik
			if currentRatio > PHI {
				// Ziel ist voll -> Langsamer machen
				currentLambda *= 0.95
			} else {
				// Ziel ist leer / Quelle ist voll -> GAS GEBEN
				// Quadratischer Fehler für Lambda
				errorMagnitude := math.Abs(diff)
				aggression := math.Pow(errorMagnitude, 2.0)

				multiplier := 1.0 + 0.05 + aggression
				currentLambda *= multiplier

				// --- ADAPTIVE BATCH SIZE ---
				// Wenn die Hütte brennt (Aggression hoch), nimm eine größere Schaufel!
				if aggression > 1.0 {
					// Skaliere BatchSize mit der Panik.
					extraShovel := int(aggression * 2000)
					currentBatchSize += extraShovel

					// Deckeln bei vernünftigem Maximum
					if currentBatchSize > 20000 {
						currentBatchSize = 20000
					}

					isEmergency = true
					// Log nur bei extremen Werten um Spam zu vermeiden
					if aggression > 3.0 {
						fmt.Printf("🚜 [%s->%s] MASSIVE LOAD: BatchSize set to %d | Multiplier %.1fx\n",
							colorize(sourceTable), colorize(targetTable), currentBatchSize, multiplier)
					}
				}
			}

			// Clamping
			if currentLambda < min {
				currentLambda = min
			}
			if currentLambda > max {
				currentLambda = max
			}
		}

		// --- DECAY LOOP ---
		workFound := false
		if sourceCount > 0 {
			// Nutze die dynamische BatchSize
			query := fmt.Sprintf("SELECT id, utility_index, last_activity, payload FROM %s LIMIT %d", sourceTable, currentBatchSize)
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
							// KEIN Sleep im Loop bei Notfall
							if !isEmergency {
								time.Sleep(10 * time.Microsecond)
							}
						}
					}
				}
				rows.Close()
			}
		}

		// --- ADAPTIVE SLEEP ---
		if workFound {
			if isEmergency {
				currentSleep = 1 * time.Millisecond // Herzrasen
			} else {
				// Normales Logging (vereinfacht)
				ratio := float64(targetCount) / math.Max(1, float64(sourceCount))
				// Um den Log sauber zu halten, nur loggen wenn nicht Emergency, oder spezifische Trigger
				if !isEmergency {
					fmt.Printf("⚖️ [%s->%s] Ratio: %.2f | λ: %.5f\n",
						colorize(sourceTable), colorize(targetTable), ratio, currentLambda)
				}
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
		}

		time.Sleep(currentSleep)
	}
}

func migrateRecord(ctx context.Context, sourcePool, targetPool *pgxpool.Pool, sourceTable, targetTable, id, payload string, uNow float64, lastActivity time.Time) bool {
	// Transaction Logic
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
		// Cross-Pool Migration
		time.Sleep(20 * time.Millisecond)
		_, err := targetPool.Exec(ctx, fmt.Sprintf("INSERT INTO %s (id, payload, utility_index, last_activity) VALUES ($1, $2, $3, $4) ON CONFLICT (id) DO NOTHING", targetTable), id, payload, uNow, lastActivity)
		if err != nil {
			return false
		}
		_, err = sourcePool.Exec(ctx, fmt.Sprintf("DELETE FROM %s WHERE id = $1", sourceTable), id)
		return err == nil
	}
}
