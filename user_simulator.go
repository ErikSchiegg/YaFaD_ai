package main

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	connStr := "postgres://eriks:test@localhost:5432/yafad_test?sslmode=disable"
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		fmt.Printf("❌ Pool failed: %v\n", err)
		return
	}
	defer pool.Close()

	fmt.Println("🚀 YaFaD_ai Viral Traffic Simulator: Activated.")
	fmt.Println("🌊 Generiere massive 'Upward Mobility' (T4 -> T0)...")

	// Wir starten 10 parallele User-Gruppen (Threads)
	var wg sync.WaitGroup
	concurrency := 10

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			simulateUserBehavior(pool, id)
		}(i)
	}

	wg.Wait()
}

func simulateUserBehavior(pool *pgxpool.Pool, workerID int) {
	ctx := context.Background()
	rng := rand.New(rand.NewSource(time.Now().UnixNano() + int64(workerID)))

	// Tiers, in denen wir nach Daten suchen (wir bevorzugen tiefe Tiers, um Aufstieg zu erzwingen)
	tiers := []string{"table4", "table3", "table2", "table1", "table0"}

	for {
		// 1. Zufälliges Tier auswählen (Zipf-artig: oft tief greifen, um alte Daten zu holen)
		// Wir zwingen den Simulator, oft in T3/T4 zu suchen
		targetTier := tiers[rng.Intn(len(tiers))]

		// 2. Einen zufälligen Datensatz lesen (Reinforcement)
		var id string
		var payload string

		// TABLESAMPLE SYSTEM(0.1) ist extrem schnell, um zufällige Zeilen zu holen
		query := fmt.Sprintf("SELECT id, payload FROM %s TABLESAMPLE SYSTEM(0.1) LIMIT 1", targetTier)
		err := pool.QueryRow(ctx, query).Scan(&id, &payload)

		if err == nil {
			// 3. Promotion: Datensatz wurde "benutzt" -> Ab in den Buffer!
			// Der Consolidator (setup_db.go) wird ihn dann nach T0 schieben.
			_, err := pool.Exec(ctx, `
				INSERT INTO buffer_tier (id, payload, utility_index, last_activity)
				VALUES ($1, $2, 2.0, CURRENT_TIMESTAMP)
				ON CONFLICT (id) DO UPDATE SET
					utility_index = buffer_tier.utility_index + 1.0,
					last_activity = CURRENT_TIMESTAMP;
			`, id, payload)

			if err == nil {
				// Kleines Log nur ab und zu, um die Konsole nicht zu fluten
				if rng.Intn(100) == 0 {
					fmt.Printf("🔥 [Worker %d] Viral Hit! Promoting %s from %s -> T0\n", workerID, id, targetTier)
				}
			}
		}

		// Kurze Pause für Realismus (10ms - 50ms)
		time.Sleep(time.Duration(rng.Intn(40)+10) * time.Millisecond)
	}
}
