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
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const PHI = 1.61803398875

func main() {
	connStr := "postgres://eriks:test@localhost:5432/yafad_test?sslmode=disable"
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		fmt.Printf("❌ Pool failed: %v\n", err)
		return
	}
	defer pool.Close()

	fmt.Println("📉 YaFaD_ai Homeostatic Decay Engine: Active.")

	var wg sync.WaitGroup
	wg.Add(3)

	// Admin-Vorgaben für Lambda (Basis, Min, Max)
	baseLambda := 0.005
	minLambda := 0.001
	maxLambda := 0.05

	// --- 1. THE SPRINTER (T0 -> T1) ---
	go func() {
		defer wg.Done()
		fmt.Println("🚀 [Regulator: SPRINTER] Balancing T0 <-> T1")
		runHomeostaticWorker(pool, 0, baseLambda, minLambda, maxLambda, 10*time.Millisecond, 500*time.Millisecond)
	}()

	// --- 2. THE JOGGER (T1 -> T2, T2 -> T3) ---
	go func() {
		defer wg.Done()
		fmt.Println("🏃 [Regulator: JOGGER] Balancing Middle Tiers")
		runHomeostaticWorker(pool, 1, baseLambda, minLambda, maxLambda, 500*time.Millisecond, 2*time.Second)
	}()

	// --- 3. THE RELAY (T2 -> T3) - DAS FEHLTE VORHER! ---
	go func() {
		defer wg.Done()
		fmt.Println("🔄 [Regulator: RELAY] Balancing T2 -> T3")
		// Dieser Worker läuft etwas entspannter als der Jogger
		runHomeostaticWorker(pool, 2, baseLambda, minLambda, maxLambda, 1*time.Second, 4*time.Second)
	}()

	// --- 3. THE SWEEPER (T3 -> T4) ---
	go func() {
		defer wg.Done()
		fmt.Println("🧹 [Regulator: SWEEPER] Balancing Archive")
		runHomeostaticWorker(pool, 3, baseLambda, minLambda, maxLambda, 5*time.Second, 30*time.Second)
	}()

	wg.Wait()
}

// --- Die selbstregulierende Logik ---
func runHomeostaticWorker(pool *pgxpool.Pool, startTier int, baseLambda, min, max float64, minSleep, maxSleep time.Duration) {
	ctx := context.Background()
	currentLambda := baseLambda // Startwert
	threshold := 0.4
	currentSleep := maxSleep

	// Wir iterieren durch die zugewiesenen Tiers (meistens nur einer pro Worker für bessere Parallelität)
	// Hier vereinfacht: Ein Worker kümmert sich um den Fluss von startTier -> startTier+1
	sourceTable := fmt.Sprintf("table%d", startTier)
	targetTable := fmt.Sprintf("table%d", startTier+1)

	for {
		// 1. Diagnose: Wie ist das aktuelle Verhältnis (Golden Ratio Check)?
		var sourceCount, targetCount int
		pool.QueryRow(ctx, fmt.Sprintf("SELECT count(*) FROM %s", sourceTable)).Scan(&sourceCount)
		pool.QueryRow(ctx, fmt.Sprintf("SELECT count(*) FROM %s", targetTable)).Scan(&targetCount)

		// 2. Hormon-Regulierung (Lambda anpassen)
		if sourceCount > 0 && targetCount > 0 {
			currentRatio := float64(targetCount) / float64(sourceCount)

			// Regelkreis:
			// Ist Ratio > PHI (1.618)? Ziel ist zu voll -> Lambda senken (bremsen)
			// Ist Ratio < PHI? Ziel ist zu leer -> Lambda erhöhen (gas geben)

			if currentRatio > PHI {
				// Ziel ist zu groß: Zerfall bremsen (-5%)
				currentLambda *= 0.95
			} else {
				// Ziel ist zu klein: Zerfall beschleunigen (+5%)
				currentLambda *= 1.05
			}

			// Clamp (Bereich einhalten)
			if currentLambda < min {
				currentLambda = min
			}
			if currentLambda > max {
				currentLambda = max
			}
		}

		// 3. Ausführung (Decay mit dynamischem Lambda)
		workFound := false

		// Nur Scannen, wenn überhaupt was in der Quelle ist
		if sourceCount > 0 {
			rows, err := pool.Query(ctx, fmt.Sprintf("SELECT id, utility_index, last_activity, payload FROM %s LIMIT 1000", sourceTable))
			if err == nil {
				for rows.Next() {
					var id, payload string
					var uLast float64
					var lastActivity time.Time
					rows.Scan(&id, &uLast, &lastActivity, &payload)

					deltaT := time.Since(lastActivity).Hours()
					// HIER nutzen wir das dynamische 'currentLambda'
					uNow := float64(C.calculate_decay(C.double(uLast), C.double(currentLambda), C.double(deltaT)))

					if uNow < threshold {
						tx, _ := pool.Begin(ctx)
						_, errDel := tx.Exec(ctx, fmt.Sprintf("DELETE FROM %s WHERE id = $1", sourceTable), id)
						if errDel == nil {
							tx.Exec(ctx, fmt.Sprintf("INSERT INTO %s (id, payload, utility_index, last_activity) VALUES ($1, $2, $3, $4)", targetTable),
								id, payload, uNow, lastActivity)
							tx.Commit(ctx)
							workFound = true
							time.Sleep(1 * time.Millisecond) // Micro-Pause
						} else {
							tx.Rollback(ctx)
						}
					}
				}
				rows.Close()
			}
		}

		// 4. Logging & Schlaf-Adaption
		if workFound {
			// Wenn wir arbeiten, drucken wir den aktuellen Hormonspiegel aus
			fmt.Printf("⚖️ [%s->%s] Ratio: %.2f | λ-Adjusted: %.5f\n", sourceTable, targetTable, float64(targetCount)/math.Max(1, float64(sourceCount)), currentLambda)
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
