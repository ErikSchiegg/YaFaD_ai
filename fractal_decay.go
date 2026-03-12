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

// --- CONFIGURATION ---
const (
	PHI         = 1.61803398875
	BASE_LAMBDA = 0.005 // Basis-Zerfallsrate (Live-System)
	EPSILON     = 0.001 // Der Ereignishorizont (Verdampfung)

	// Sicherheitsnetz für den Boot-Vorgang
	MIN_DEEP_CAPACITY = 20000
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
	dbHost := os.Getenv("DB_HOST")
	if dbHost == "" {
		dbHost = "localhost"
	}
	connStr := fmt.Sprintf("postgres://%s:%s@%s:5432/yafad_test?sslmode=disable", dbUser, dbPass, dbHost)

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		panic(err)
	}
	defer pool.Close()

	// Initiales Setup: Stats-Tabelle und Basis-Archive (0-4) sicherstellen
	ensureStatsTable(ctx, pool)
	for i := 0; i <= 4; i++ {
		ensureArchiveTableExists(ctx, pool, i)
	}

	fmt.Println("🌌 YaFaD_ai FRACTAL ENGINE V2: Online.")
	fmt.Printf("🕳️  Event Horizon (Epsilon) set to: %.4f\n", EPSILON)

	var wg sync.WaitGroup
	wg.Add(2)

	// SYSTEM A: Das Überdruckventil (Deep Archive -> Archive0)
	// Dieses System behalten wir exakt so bei, wie es in deiner Version gut funktioniert hat!
	go func() {
		defer wg.Done()
		monitorDeepArchive(pool)
	}()

	// SYSTEM B: Der Unendliche Crawler (Archive0 -> ArchiveN -> Evaporation)
	go func() {
		defer wg.Done()
		runFractalCrawler(pool)
	}()

	wg.Wait()
}

// Prüft geräuschlos, ob eine Tabelle in Postgres existiert
func tableExists(ctx context.Context, pool *pgxpool.Pool, tableName string) bool {
	var exists bool
	query := "SELECT EXISTS (SELECT FROM pg_tables WHERE schemaname = 'public' AND tablename = $1)"
	err := pool.QueryRow(ctx, query, tableName).Scan(&exists)
	if err != nil {
		return false
	}
	return exists
}

// --- SYSTEM A: Das Überdruckventil (deep_archive -> archive0) ---
func monitorDeepArchive(pool *pgxpool.Pool) {
	ctx := context.Background()

	type Candidate struct {
		Id           string
		Payload      string
		Utility      float64
		LastActivity time.Time
	}

	for {
		var countT4, countDeep int

		pool.QueryRow(ctx, "SELECT count(*) FROM table4").Scan(&countT4)
		pool.QueryRow(ctx, "SELECT count(*) FROM deep_archive").Scan(&countDeep)

		deepThreshold := int(float64(countT4) * PHI)
		if deepThreshold < MIN_DEEP_CAPACITY {
			deepThreshold = MIN_DEEP_CAPACITY
		}

		if countDeep > deepThreshold {
			overflow := countDeep - deepThreshold
			batchSize := 2000
			if overflow < batchSize {
				batchSize = overflow
			}

			fmt.Printf("⚠️  Deep Archive Overload (%d/%d). Draining %d records to Archive0...\n", countDeep, deepThreshold, batchSize)

			tx, err := pool.Begin(ctx)
			if err != nil {
				time.Sleep(1 * time.Second)
				continue
			}

			rows, _ := tx.Query(ctx, fmt.Sprintf("SELECT id, payload, utility_index, last_activity FROM deep_archive ORDER BY utility_index ASC LIMIT %d", batchSize))

			var candidates []Candidate
			for rows.Next() {
				var c Candidate
				rows.Scan(&c.Id, &c.Payload, &c.Utility, &c.LastActivity)
				candidates = append(candidates, c)
			}
			rows.Close()

			moved := 0
			lambda := BASE_LAMBDA / PHI

			for _, c := range candidates {
				dt := time.Since(c.LastActivity).Hours()
				uNow := float64(C.calculate_decay(C.double(c.Utility), C.double(lambda), C.double(dt)))

				_, err := tx.Exec(ctx,
					"INSERT INTO archive0 (id, payload, utility_index, last_activity) VALUES ($1, $2, $3, $4) ON CONFLICT (id) DO NOTHING",
					c.Id, c.Payload, uNow, c.LastActivity)

				if err == nil {
					tx.Exec(ctx, "DELETE FROM deep_archive WHERE id = $1", c.Id)
					moved++
				}
			}

			tx.Commit(ctx)
			fmt.Printf("⬇️  Moved %d items from Deep Archive to Archive0.\n", moved)
		}

		time.Sleep(5 * time.Second)
	}
}

// --- SYSTEM B: Der unendliche Fraktal-Crawler ---
func runFractalCrawler(pool *pgxpool.Pool) {
	ctx := context.Background()

	for {
		tier := 0
		for {
			sourceTable := fmt.Sprintf("archive%d", tier)
			targetTable := fmt.Sprintf("archive%d", tier+1)

			prevTable := "deep_archive"
			if tier > 0 {
				prevTable = fmt.Sprintf("archive%d", tier-1)
			}

			// LOG-SPAM SCHUTZ: Prüfen, ob die Tabellen überhaupt existieren!
			if !tableExists(ctx, pool, prevTable) || !tableExists(ctx, pool, sourceTable) {
				break // Wir sind am unteren Ende des Fraktals angekommen
			}

			var countPrev, countCurrent int
			_ = pool.QueryRow(ctx, fmt.Sprintf("SELECT count(*) FROM %s", prevTable)).Scan(&countPrev)
			_ = pool.QueryRow(ctx, fmt.Sprintf("SELECT count(*) FROM %s", sourceTable)).Scan(&countCurrent)

			capacity := int(float64(countPrev) * PHI)
			if capacity < 1000 {
				capacity = 1000 // Kleiner Puffer
			}
			isOverloaded := countCurrent > capacity

			lambda := BASE_LAMBDA / math.Pow(PHI, float64(tier+2))

			rows, err := pool.Query(ctx, fmt.Sprintf("SELECT id, utility_index, last_activity, payload FROM %s ORDER BY utility_index ASC LIMIT 500", sourceTable))

			if err == nil {
				var ids, payloads []string
				var uLasts []float64
				var lastActs []time.Time

				for rows.Next() {
					var id, payload string
					var uLast float64
					var lastActivity time.Time
					rows.Scan(&id, &uLast, &lastActivity, &payload)

					ids = append(ids, id)
					payloads = append(payloads, payload)
					uLasts = append(uLasts, uLast)
					lastActs = append(lastActs, lastActivity)
				}
				rows.Close()

				evaporatedCount := 0
				movedCount := 0
				var bytesEvaporated int64 = 0

				for i := 0; i < len(ids); i++ {
					dt := time.Since(lastActs[i]).Hours()
					uNow := float64(C.calculate_decay(C.double(uLasts[i]), C.double(lambda), C.double(dt)))

					if uNow < EPSILON {
						// ☠️ EREIGNISHORIZONT ERREICHT: VERDAMPFEN
						tx, _ := pool.Begin(ctx)
						_, errDel := tx.Exec(ctx, fmt.Sprintf("DELETE FROM %s WHERE id = $1", sourceTable), ids[i])
						if errDel == nil {
							saved := int64(len(payloads[i]) + len(ids[i]))
							bytesEvaporated += saved
							tx.Exec(ctx, "UPDATE yafad_stats SET value = value + $1 WHERE key = 'evaporated_bytes'", float64(saved))
							tx.Commit(ctx)
							evaporatedCount++
						} else {
							tx.Rollback(ctx)
						}

					} else if isOverloaded {
						// ⬇️ ZU VIEL DRUCK: EINE EBENE TIEFER FALLEN
						// Hier erschaffen wir bei Bedarf organisch archive5, archive6 usw.
						ensureArchiveTableExists(ctx, pool, tier+1)

						tx, _ := pool.Begin(ctx)
						_, errDel := tx.Exec(ctx, fmt.Sprintf("DELETE FROM %s WHERE id = $1", sourceTable), ids[i])
						if errDel == nil {
							tx.Exec(ctx, fmt.Sprintf("INSERT INTO %s (id, payload, utility_index, last_activity) VALUES ($1, $2, $3, $4) ON CONFLICT (id) DO NOTHING", targetTable),
								ids[i], payloads[i], uNow, lastActs[i])
							tx.Commit(ctx)
							movedCount++
						} else {
							tx.Rollback(ctx)
						}
					}
				}

				if evaporatedCount > 0 {
					fmt.Printf("💨 [%s] Hawking Radiation: Evaporated %d records (%d bytes reclaimed).\n", sourceTable, evaporatedCount, bytesEvaporated)
				}
				if movedCount > 0 {
					fmt.Printf("📉 [%s] Gravity Fall: %d records fell into %s.\n", sourceTable, movedCount, targetTable)
				}
			}

			tier++ // Nächste Ebene prüfen
		}

		time.Sleep(10 * time.Second)
	}
}

// --- HILFSFUNKTIONEN FÜR SCHEMA & STATS ---

func ensureStatsTable(ctx context.Context, pool *pgxpool.Pool) {
	query := `CREATE TABLE IF NOT EXISTS yafad_stats (key TEXT PRIMARY KEY, value FLOAT);`
	pool.Exec(ctx, query)
	pool.Exec(ctx, `INSERT INTO yafad_stats (key, value) VALUES ('evaporated_bytes', 0) ON CONFLICT DO NOTHING;`)
}

func ensureArchiveTableExists(ctx context.Context, pool *pgxpool.Pool, tier int) {
	table := fmt.Sprintf("archive%d", tier)

	// Prüfen, ob die Tabelle schon existiert, bevor wir SQL-Fehler riskieren
	if !tableExists(ctx, pool, table) {
		query := fmt.Sprintf(`
			CREATE TABLE %s (
				id TEXT PRIMARY KEY,
				payload TEXT,
				utility_index DOUBLE PRECISION,
				last_activity TIMESTAMP
			);`, table)

		_, err := pool.Exec(ctx, query)
		if err == nil {
			indexQuery := fmt.Sprintf("CREATE INDEX idx_%s_utility ON %s (utility_index ASC);", table, table)
			pool.Exec(ctx, indexQuery)
			// Wenn es eine Ebene > 4 ist, loggen wir das organische Wachstum
			if tier > 4 {
				fmt.Printf("🏗️  Fractal Expansion: Spawned new tier '%s'!\n", table)
			}
		}
	}
}
