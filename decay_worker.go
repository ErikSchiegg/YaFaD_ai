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
	// Simulation: Gleiche DB, aber logisch getrennt für High/Low Performance Simulation
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
	wg.Add(4)

	baseLambda := 0.005
	minLambda := 0.001
	maxLambda := 0.5 //fast flushing

	// T0 -> T1
	go func() {
		defer wg.Done()
		runHomeostaticWorker(router, 0, baseLambda, minLambda, maxLambda, 10*time.Millisecond, 500*time.Millisecond)
	}()
	// T1 -> T2
	go func() {
		defer wg.Done()
		runHomeostaticWorker(router, 1, baseLambda, minLambda, maxLambda, 500*time.Millisecond, 2*time.Second)
	}()
	// T2 -> T3 (Brücke)
	go func() {
		defer wg.Done()
		runHomeostaticWorker(router, 2, baseLambda, minLambda, maxLambda, 1*time.Second, 4*time.Second)
	}()
	// T3 -> T4
	go func() {
		defer wg.Done()
		runHomeostaticWorker(router, 3, baseLambda, minLambda, maxLambda, 5*time.Second, 30*time.Second)
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

	for {
		sourcePool := router.GetPool(sourceTier)
		targetPool := router.GetPool(targetTier)

		var sourceCount, targetCount int
		sourcePool.QueryRow(ctx, fmt.Sprintf("SELECT count(*) FROM %s", sourceTable)).Scan(&sourceCount)
		targetPool.QueryRow(ctx, fmt.Sprintf("SELECT count(*) FROM %s", targetTable)).Scan(&targetCount)

		// --- BIO-FEEDBACK PID LOGIC (Non-Linear) ---
		if sourceCount > 0 && targetCount > 0 {
			currentRatio := float64(targetCount) / float64(sourceCount)

			// Wie weit sind wir vom Goldenen Schnitt weg?
			// Bei PHI (1.618) ist diff = 0.
			// Bei Verstopfung (Ratio 0.1) ist diff = ~1.5 (RIESIG!)
			diff := currentRatio - PHI

			// Basis-Anpassung (5%)
			baseAdjustment := 0.05

			if diff > 0 {
				// Zu viele Daten im Ziel (Stau unten) -> BREMSEN (Lambda senken)
				// Hier reicht linear, Bremsen ist unkritisch
				currentLambda *= (1.0 - baseAdjustment)
			} else {
				// Zu wenig Daten im Ziel (Stau HIER) -> GAS GEBEN (Lambda erhöhen)
				// Wir nutzen den Fehler im Quadrat als Turbo!
				// Je größer der Fehler (diff), desto gewaltiger der Sprung.

				errorMagnitude := math.Abs(diff)

				// Beispiel:
				// Fehler 0.1 -> Aggression = 0.01 (sanft)
				// Fehler 1.5 -> Aggression = 2.25 (BRUTAL!)
				aggression := math.Pow(errorMagnitude, 2.0)

				// Der neue Multiplikator: 1.0 + 5% + Der Turbo
				multiplier := 1.0 + baseAdjustment + aggression

				currentLambda *= multiplier

				// Optional: Logging, wenn der Turbo zündet
				if aggression > 0.5 {
					fmt.Printf("🚀 TURBO BOOST: Error %.2f triggers Multiplier %.2fx\n", errorMagnitude, multiplier)
				}
			}

			// Clamping (Sicherheitsgrenzen)
			if currentLambda < min {
				currentLambda = min
			}
			if currentLambda > max {
				currentLambda = max
			}
		}

		// --- Decay & Migration Logic ---
		workFound := false
		if sourceCount > 0 {
			rows, err := sourcePool.Query(ctx, fmt.Sprintf("SELECT id, utility_index, last_activity, payload FROM %s LIMIT 1000", sourceTable))
			if err == nil {
				for rows.Next() {
					var id, payload string
					var uLast float64
					var lastActivity time.Time
					rows.Scan(&id, &uLast, &lastActivity, &payload)

					deltaT := time.Since(lastActivity).Hours()
					// Call Rust
					uNow := float64(C.calculate_decay(C.double(uLast), C.double(currentLambda), C.double(deltaT)))

					if uNow < threshold {
						success := migrateRecord(ctx, sourcePool, targetPool, sourceTable, targetTable, id, payload, uNow, lastActivity)
						if success {
							workFound = true
							time.Sleep(1 * time.Millisecond) // CPU Yield
						}
					}
				}
				rows.Close()
			}
		}

		// --- Logging with Colors ---
		if workFound {
			if sourcePool != targetPool {
				// Brücken-Migration (Hot -> Cold)
				fmt.Printf("🌉 [%s->%s] Migrated across Storage Zones | λ: %.5f\n",
					colorize(sourceTable), colorize(targetTable), currentLambda)
			} else {
				// Interne Migration
				ratio := float64(targetCount) / math.Max(1, float64(sourceCount))
				fmt.Printf("⚖️ [%s->%s] Ratio: %.2f | λ: %.5f\n",
					colorize(sourceTable), colorize(targetTable), ratio, currentLambda)
			}

			// Adaptiver Sleep: Wenn Arbeit da ist, arbeite schneller
			currentSleep /= 2
			if currentSleep < minSleep {
				currentSleep = minSleep
			}
		} else {
			// Adaptiver Sleep: Wenn nichts zu tun ist, schlaf länger
			currentSleep *= 2
			if currentSleep > maxSleep {
				currentSleep = maxSleep
			}
		}
		time.Sleep(currentSleep)
	}
}

func migrateRecord(ctx context.Context, sourcePool, targetPool *pgxpool.Pool, sourceTable, targetTable, id, payload string, uNow float64, lastActivity time.Time) bool {
	// Transaction Logic (gleicher Code wie vorher, nur ausgeblendet der Übersicht halber)
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
		// Cross-Pool Migration (langsamer)
		time.Sleep(20 * time.Millisecond)
		_, err := targetPool.Exec(ctx, fmt.Sprintf("INSERT INTO %s (id, payload, utility_index, last_activity) VALUES ($1, $2, $3, $4) ON CONFLICT (id) DO NOTHING", targetTable), id, payload, uNow, lastActivity)
		if err != nil {
			return false
		}
		_, err = sourcePool.Exec(ctx, fmt.Sprintf("DELETE FROM %s WHERE id = $1", sourceTable), id)
		return err == nil
	}
}
