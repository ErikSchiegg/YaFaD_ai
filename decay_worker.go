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
	coldConnStr := hotConnStr // Simulation: Gleiche DB, aber logisch getrennt

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
	maxLambda := 0.05

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

		if sourceCount > 0 && targetCount > 0 {
			currentRatio := float64(targetCount) / float64(sourceCount)
			if currentRatio > PHI {
				currentLambda *= 0.95
			} else {
				currentLambda *= 1.05
			}
			if currentLambda < min {
				currentLambda = min
			}
			if currentLambda > max {
				currentLambda = max
			}
		}

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
					uNow := float64(C.calculate_decay(C.double(uLast), C.double(currentLambda), C.double(deltaT)))

					if uNow < threshold {
						success := migrateRecord(ctx, sourcePool, targetPool, sourceTable, targetTable, id, payload, uNow, lastActivity)
						if success {
							workFound = true
							time.Sleep(1 * time.Millisecond)
						}
					}
				}
				rows.Close()
			}
		}

		if workFound {
			if sourcePool != targetPool {
				fmt.Printf("🌉 [%s->%s] Migrated across Storage Zones | λ: %.5f\n", sourceTable, targetTable, currentLambda)
			} else {
				fmt.Printf("⚖️ [%s->%s] Ratio: %.2f | λ: %.5f\n", sourceTable, targetTable, float64(targetCount)/math.Max(1, float64(sourceCount)), currentLambda)
			}
			currentSleep /= 2
			if currentSleep < minSleep {
				currentSleep = minSleep
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
		time.Sleep(20 * time.Millisecond) // Latency Simulation
		_, err := targetPool.Exec(ctx, fmt.Sprintf("INSERT INTO %s (id, payload, utility_index, last_activity) VALUES ($1, $2, $3, $4) ON CONFLICT (id) DO NOTHING", targetTable), id, payload, uNow, lastActivity)
		if err != nil {
			return false
		}
		_, err = sourcePool.Exec(ctx, fmt.Sprintf("DELETE FROM %s WHERE id = $1", sourceTable), id)
		return err == nil
	}
}
