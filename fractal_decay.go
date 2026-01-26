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
	"os"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// CONFIGURATION
const (
	// Wenn Table 4 diese Größe überschreitet, wird in das Fraktal-Archiv ausgelagert
	TABLE4_SOFT_LIMIT = 50000

	// Zeit-Dilatation: Das Archiv zerfällt 10x langsamer als das Live-System
	ARCHIVE_LAMBDA = 0.0005
)

func main() {
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

	fmt.Println("❄️  YaFaD_ai FRACTAL ENGINE: Online.")
	fmt.Printf("🛡️  Guarding 'table4' (Threshold: %d records)\n", TABLE4_SOFT_LIMIT)

	var wg sync.WaitGroup
	wg.Add(2)

	// SYSTEM A: Das Überdruckventil (Table4 -> Archive0)
	go func() {
		defer wg.Done()
		monitorAndRelievePressure(pool)
	}()

	// SYSTEM B: Der Tiefen-Zyklus (Archive0 -> Archive4)
	go func() {
		defer wg.Done()
		var subWg sync.WaitGroup
		subWg.Add(4)
		for i := 0; i < 4; i++ {
			go func(tier int) {
				defer subWg.Done()
				runDeepDecay(pool, tier)
			}(i)
		}
		subWg.Wait()
	}()

	wg.Wait()
}

// --- SYSTEM A: Das Ventil (Repariert: conn busy Fix) ---
func monitorAndRelievePressure(pool *pgxpool.Pool) {
	ctx := context.Background()

	// Kleine Struktur, um die Daten kurz zwischenzuspeichern
	type Candidate struct {
		Id           string
		Payload      string
		Utility      float64
		LastActivity time.Time
	}

	for {
		var count int
		pool.QueryRow(ctx, "SELECT count(*) FROM table4").Scan(&count)

		if count > TABLE4_SOFT_LIMIT {
			overflow := count - TABLE4_SOFT_LIMIT
			batchSize := 1000
			if overflow < batchSize {
				batchSize = overflow
			}

			fmt.Printf("⚠️  Table4 Overload (%d/%d). Draining %d records...\n", count, TABLE4_SOFT_LIMIT, batchSize)

			tx, err := pool.Begin(ctx)
			if err != nil {
				fmt.Printf("❌ Transaktions-Fehler: %v\n", err)
				time.Sleep(1 * time.Second)
				continue
			}

			// SCHRITT 1: NUR LESEN (In den Speicher laden)
			rows, _ := tx.Query(ctx, fmt.Sprintf("SELECT id, payload, utility_index, last_activity FROM table4 ORDER BY utility_index ASC LIMIT %d", batchSize))

			var candidates []Candidate

			for rows.Next() {
				var c Candidate
				rows.Scan(&c.Id, &c.Payload, &c.Utility, &c.LastActivity)
				candidates = append(candidates, c)
			}
			rows.Close() // <--- WICHTIG: Hier geben wir die "Lese-Verbindung" frei!

			// SCHRITT 2: NUR SCHREIBEN (Jetzt ist die Leitung frei für Inserts)
			moved := 0
			var errCount int

			for _, c := range candidates {
				// Insert ins Archiv
				_, err := tx.Exec(ctx,
					"INSERT INTO archive0 (id, payload, utility_index, last_activity) VALUES ($1, $2, $3, $4) ON CONFLICT (id) DO NOTHING",
					c.Id, c.Payload, c.Utility, c.LastActivity)

				if err != nil {
					if errCount < 5 {
						fmt.Printf("❌ INSERT ERROR bei ID %s: %v\n", c.Id, err)
					}
					errCount++
				} else {
					// Löschen aus Table4
					tx.Exec(ctx, "DELETE FROM table4 WHERE id = $1", c.Id)
					moved++
				}
			}

			tx.Commit(ctx)
			fmt.Printf("❄️  Moved %d items to Archive0. (Errors: %d)\n", moved, errCount)
		}

		time.Sleep(5 * time.Second)
	}
}

// --- SYSTEM B: Der Tiefen-Zyklus ---
func runDeepDecay(pool *pgxpool.Pool, tier int) {
	ctx := context.Background()
	source := fmt.Sprintf("archive%d", tier)
	target := fmt.Sprintf("archive%d", tier+1)
	threshold := 0.1

	for {
		rows, err := pool.Query(ctx, fmt.Sprintf("SELECT id, utility_index, last_activity, payload FROM %s TABLESAMPLE SYSTEM(1) LIMIT 100", source))
		if err == nil {
			workDone := false
			for rows.Next() {
				var id, payload string
				var uLast float64
				var lastActivity time.Time
				rows.Scan(&id, &uLast, &lastActivity, &payload)

				deltaT := time.Since(lastActivity).Hours()
				uNow := float64(C.calculate_decay(C.double(uLast), C.double(ARCHIVE_LAMBDA), C.double(deltaT)))

				if uNow < threshold {
					tx, _ := pool.Begin(ctx)
					_, errDel := tx.Exec(ctx, fmt.Sprintf("DELETE FROM %s WHERE id = $1", source), id)
					if errDel == nil {
						tx.Exec(ctx, fmt.Sprintf("INSERT INTO %s (id, payload, utility_index, last_activity) VALUES ($1, $2, $3, $4)", target),
							id, payload, uNow, lastActivity)
						tx.Commit(ctx)
						workDone = true
					} else {
						tx.Rollback(ctx)
					}
				}
			}
			rows.Close()
			if workDone {
				fmt.Printf("📉 [Deep Decay] %s -> %s\n", source, target)
			}
		}
		time.Sleep(10 * time.Second)
	}
}
