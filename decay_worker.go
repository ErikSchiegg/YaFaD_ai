ppackage main

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

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const PHI = 1.61803398875

// --- 1. The Storage Router ---
// This struct acts as the traffic controller, deciding where data lives.
type StorageRouter struct {
	HotPool  *pgxpool.Pool // Fast, Expensive (NVMe) -> T0, T1, T2
	ColdPool *pgxpool.Pool // Slow, Cheap (HDD/S3)  -> T3, T4
}

// GetPool assigns a physical location based on the logical tier.
// T0-T2 stay Hot. T3-T4 go Cold.
func (r *StorageRouter) GetPool(tier int) *pgxpool.Pool {
	if tier >= 3 {
		return r.ColdPool
	}
	return r.HotPool
}

func main() {
	// Connection Setup
	// In a real prod setup, these would be two different URLs.
	// For testing, we use the same DB but treat them as separate connections.
	dbUser := os.Getenv("DB_USER")
	if dbUser == "" { dbUser = "eriks" }
	dbPass := os.Getenv("DB_PASSWORD")
	if dbPass == "" { dbPass = "test" }
	
	hotConnStr := fmt.Sprintf("postgres://%s:%s@localhost:5432/yafad_test?sslmode=disable", dbUser, dbPass)
	coldConnStr := hotConnStr // Currently same DB, but logically separated

	ctx := context.Background()
	
	// Initialize Hot Pool
	hotPool, err := pgxpool.New(ctx, hotConnStr)
	if err != nil { panic(err) }
	defer hotPool.Close()

	// Initialize Cold Pool
	coldPool, err := pgxpool.New(ctx, coldConnStr)
	if err != nil { panic(err) }
	defer coldPool.Close()

	router := &StorageRouter{
		HotPool:  hotPool,
		ColdPool: coldPool,
	}

	fmt.Println("📉 YaFaD_ai Federated Decay Engine: Active.")
	fmt.Println("🌐 Storage Topology: [T0,T1,T2] -> Hot Node | [T3,T4] -> Cold Node")

	var wg sync.WaitGroup
	wg.Add(4)

	// Admin Parameters
	baseLambda := 0.005
	minLambda := 0.001
	maxLambda := 0.05

	// --- 1. SPRINTER (T0 -> T1) [Hot to Hot] ---
	go func() {
		defer wg.Done()
		fmt.Println("🚀 [Regulator: SPRINTER] Balancing T0 -> T1")
		runHomeostaticWorker(router, 0, baseLambda, minLambda, maxLambda, 10*time.Millisecond, 500*time.Millisecond)
	}()

	// --- 2. JOGGER (T1 -> T2) [Hot to Hot] ---
	go func() {
		defer wg.Done()
		fmt.Println("🏃 [Regulator: JOGGER] Balancing T1 -> T2")
		runHomeostaticWorker(router, 1, baseLambda, minLambda, maxLambda, 500*time.Millisecond, 2*time.Second)
	}()

	// --- 3. THE RELAY (T2 -> T3) [CROSS-BORDER: Hot to Cold] ---
	// This worker is special: It moves data across the physical boundary.
	go func() {
		defer wg.Done()
		fmt.Println("🌉 [Regulator: RELAY] Bridging Hot -> Cold (T2 -> T3)")
		runHomeostaticWorker(router, 2, baseLambda, minLambda, maxLambda, 1*time.Second, 4*time.Second)
	}()

	// --- 4. SWEEPER (T3 -> T4) [Cold to Cold] ---
	go func() {
		defer wg.Done()
		fmt.Println("🧹 [Regulator: SWEEPER] Balancing Archive T3 -> T4")
		runHomeostaticWorker(router, 3, baseLambda, minLambda, maxLambda, 5*time.Second, 30*time.Second)
	}()

	wg.Wait()
}

// --- The Core Logic ---
func runHomeostaticWorker(router *StorageRouter, startTier int, baseLambda, min, max float64, minSleep, maxSleep time.Duration) {
	ctx := context.Background()
	currentLambda := baseLambda
	threshold := 0.4 // Decay Threshold
	currentSleep := maxSleep

	sourceTier := startTier
	targetTier := startTier + 1
	
	sourceTable := fmt.Sprintf("table%d", sourceTier)
	targetTable := fmt.Sprintf("table%d", targetTier)

	for {
		// 1. Identify Physical Locations
		sourcePool := router.GetPool(sourceTier)
		targetPool := router.GetPool(targetTier)

		// 2. Diagnosis (Golden Ratio Check)
		var sourceCount, targetCount int
		// Note: We must query the respective pools!
		sourcePool.QueryRow(ctx, fmt.Sprintf("SELECT count(*) FROM %s", sourceTable)).Scan(&sourceCount)
		targetPool.QueryRow(ctx, fmt.Sprintf("SELECT count(*) FROM %s", targetTable)).Scan(&targetCount)

		// 3. PID Control (Endocrine System)
		if sourceCount > 0 && targetCount > 0 {
			currentRatio := float64(targetCount) / float64(sourceCount)
			if currentRatio > PHI {
				currentLambda *= 0.95 // Target full -> slow down
			} else {
				currentLambda *= 1.05 // Target empty -> speed up
			}
			// Clamp
			if currentLambda < min { currentLambda = min }
			if currentLambda > max { currentLambda = max }
		}

		// 4. Execution (Decay & Move)
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
						// performMigration handles both Local and Cross-DB moves
						success := migrateRecord(ctx, sourcePool, targetPool, sourceTable, targetTable, id, payload, uNow, lastActivity)
						if success {
							workFound = true
							time.Sleep(1 * time.Millisecond) // Micro-Pause
						}
					}
				}
				rows.Close()
			}
		}

		// 5. Adapt Sleep
		if workFound {
			if sourcePool != targetPool {
				// Log the bridge crossing occasionally
				fmt.Printf("🌉 [%s->%s] Migrated across Storage Zones | λ: %.5f\n", sourceTable, targetTable, currentLambda)
			} else {
				fmt.Printf("⚖️ [%s->%s] Ratio: %.2f | λ: %.5f\n", sourceTable, targetTable, float64(targetCount)/math.Max(1, float64(sourceCount)), currentLambda)
			}
			currentSleep /= 2
			if currentSleep < minSleep { currentSleep = minSleep }
		} else {
			currentSleep *= 2
			if currentSleep > maxSleep { currentSleep = maxSleep }
		}
		
		time.Sleep(currentSleep)
	}
}

// migrateRecord intelligently handles data movement regardless of physical location
func migrateRecord(ctx context.Context, sourcePool, targetPool *pgxpool.Pool, sourceTable, targetTable, id, payload string, uNow float64, lastActivity time.Time) bool {
	
	// SCENARIO A: Same Physical DB (Fast Path)
	if sourcePool == targetPool {
		tx, err := sourcePool.Begin(ctx)
		if err != nil { return false }
		defer tx.Rollback(ctx)

		_, err = tx.Exec(ctx, fmt.Sprintf("DELETE FROM %s WHERE id = $1", sourceTable), id)
		if err != nil { return false }

		_, err = tx.Exec(ctx, fmt.Sprintf("INSERT INTO %s (id, payload, utility_index, last_activity) VALUES ($1, $2, $3, $4)", targetTable), 
			id, payload, uNow, lastActivity)
		
		if err != nil { return false }
		return tx.Commit(ctx) == nil
	}

	// SCENARIO B: Cross-Database Transplant (Distributed Path)
	// T2 (Hot) -> T3 (Cold)
	// We cannot use a single transaction across two pools without 2PC.
	// Safe Strategy: Copy -> Verify -> Delete
	
	// 1. Simulate Network Latency for the "Cold" link
	// (Simulating S3 or remote HDD lag)
	time.Sleep(20 * time.Millisecond) 

	// 2. Insert into Target (Cold)
	_, err := targetPool.Exec(ctx, fmt.Sprintf("INSERT INTO %s (id, payload, utility_index, last_activity) VALUES ($1, $2, $3, $4) ON CONFLICT (id) DO NOTHING", targetTable),
		id, payload, uNow, lastActivity)
	
	if err != nil {
		fmt.Printf("❌ Archive Upload Failed: %v\n", err)
		return false
	}

	// 3. Delete from Source (Hot) ONLY if Insert succeeded
	// Note: In a real distributed system, we might need an eventual consistency check here.
	_, err = sourcePool.Exec(ctx, fmt.Sprintf("DELETE FROM %s WHERE id = $1", sourceTable), id)
	
	if err != nil {
		fmt.Printf("⚠️ Failed to clean up Hot Tier: %v\n", err)
		// We leave the record to be picked up next time (at worst we have a duplicate for a moment)
		return false
	}

	return true
}