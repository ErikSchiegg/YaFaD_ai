package main

/*
#cgo LDFLAGS: -L${SRCDIR}/core/target/release -lyafad_core -Wl,-rpath,${SRCDIR}/core/target/release -lm -ldl
#cgo CPPFLAGS: -I${SRCDIR}/core
extern double calculate_decay(double u_last, double lambda, double delta_t);
*/
import "C"
import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// --- KONFIGURATION ---
const (
	PHI         = 1.61803398875
	BASE_LAMBDA = 0.005 // Standard-Zerfall

	MIN_DEEP_CAPACITY = 20000
	CONFIG_FILE       = "shared/yafad_config.json"
	LOG_FILE          = "shared/fractal.log"
)

var fractalLogFile *os.File

func initLogger() {
	os.MkdirAll("shared", os.ModePerm)
	var err error
	fractalLogFile, err = os.OpenFile(LOG_FILE, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		fmt.Println("⚠️ Could not open shared/fractal.log:", err)
	}
}

// Eigene Log-Funktion: Schreibt ins Terminal UND in die Datei für Gradio
func fractalLog(format string, v ...interface{}) {
	msg := fmt.Sprintf(format, v...)
	fmt.Print(msg) // Docker Console
	if fractalLogFile != nil {
		timestamp := time.Now().Format("15:04:05")
		fractalLogFile.WriteString(fmt.Sprintf("[%s] %s", timestamp, msg))
	}
}

// --- PID CONTROLLER LOGIK ---
type PIDController struct {
	Kp, Ki, Kd float64
	Integral   float64
	PrevError  float64
	LastTime   time.Time
}

func NewPID(kp, ki, kd float64) *PIDController {
	return &PIDController{Kp: kp, Ki: ki, Kd: kd, LastTime: time.Now()}
}

func (pid *PIDController) Update(currentVal, setPoint float64) float64 {
	now := time.Now()
	dt := now.Sub(pid.LastTime).Seconds()
	if dt <= 0 {
		return 0
	}
	pid.LastTime = now
	error := currentVal - setPoint
	pid.Integral += error * dt

	if pid.Integral > 10 {
		pid.Integral = 10
	}
	if pid.Integral < -10 {
		pid.Integral = -10
	}

	derivative := (error - pid.PrevError) / dt
	pid.PrevError = error
	return (pid.Kp * error) + (pid.Ki * pid.Integral) + (pid.Kd * derivative)
}

func getArchivePIDSettings() (float64, float64, float64) {
	data, err := os.ReadFile(CONFIG_FILE)
	if err == nil {
		var config struct {
			PID struct {
				Kp float64 `json:"kp"`
				Ki float64 `json:"ki"`
				Kd float64 `json:"kd"`
			} `json:"pid_settings"`
		}
		if json.Unmarshal(data, &config) == nil {
			return config.PID.Kp * 0.1, config.PID.Ki * 0.1, config.PID.Kd * 0.1
		}
	}
	return 0.2, 0.01, 0.05
}

// Liest das dynamische Epsilon (Hawking Radiation Threshold) aus der Config
func getEpsilon() float64 {
	defaultEpsilon := 0.001
	data, err := os.ReadFile(CONFIG_FILE)
	if err == nil {
		var config struct {
			Epsilon float64 `json:"epsilon"`
		}
		if json.Unmarshal(data, &config) == nil && config.Epsilon > 0 {
			return config.Epsilon
		}
	}
	return defaultEpsilon
}

func main() {
	initLogger()

	dbUser, dbPass, dbHost := os.Getenv("DB_USER"), os.Getenv("DB_PASSWORD"), os.Getenv("DB_HOST")
	if dbUser == "" {
		dbUser = "eriks"
	}
	if dbPass == "" {
		dbPass = "test"
	}
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

	ensureStatsTable(ctx, pool)
	for i := 0; i <= 4; i++ {
		ensureArchiveTableExists(ctx, pool, i)
	}

	fractalLog("🌌 YaFaD_ai FRACTAL ENGINE V3 (PID + Vectors): Online.\n")

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		monitorDeepArchive(pool)
	}()

	go func() {
		defer wg.Done()
		runFractalCrawler(pool)
	}()

	wg.Wait()
}

// --- SYSTEM A: Die PID-Pumpe ---
func monitorDeepArchive(pool *pgxpool.Pool) {
	ctx := context.Background()
	kp, ki, kd := getArchivePIDSettings()
	pid := NewPID(kp, ki, kd)

	for {
		pid.Kp, pid.Ki, pid.Kd = getArchivePIDSettings()

		var countT4, countDeep int
		pool.QueryRow(ctx, "SELECT count(*) FROM table4").Scan(&countT4)
		pool.QueryRow(ctx, "SELECT count(*) FROM deep_archive").Scan(&countDeep)

		threshold := int(float64(countT4) * PHI)
		if threshold < MIN_DEEP_CAPACITY {
			threshold = MIN_DEEP_CAPACITY
		}

		pressure := float64(countDeep) / float64(threshold)
		pidOut := pid.Update(pressure, 1.0)

		lambda := (BASE_LAMBDA / PHI) + pidOut
		if lambda < 0.0001 {
			lambda = 0.0001
		}
		if lambda > 0.5 {
			lambda = 0.5
		}

		if pressure > 0.95 {
			batchSize := int(2000 * pressure)
			if batchSize > 5000 {
				batchSize = 5000
			}

			tx, _ := pool.Begin(ctx)
			// NEU: Wir lesen die Vektoren mit aus (als Text, das ist am sichersten für den Go-Transfer)
			rows, _ := tx.Query(ctx, fmt.Sprintf("SELECT id, payload, utility_index, last_activity, embedding::text FROM deep_archive ORDER BY utility_index ASC LIMIT %d", batchSize))

			var ids, payloads []string
			var uLasts []float64
			var lastActs []time.Time
			var embeddings []*string // Pointer, da Vektoren NULL sein können, wenn Ollama noch nicht fertig war!

			for rows.Next() {
				var id, pl string
				var u float64
				var la time.Time
				var emb *string
				rows.Scan(&id, &pl, &u, &la, &emb)
				ids = append(ids, id)
				payloads = append(payloads, pl)
				uLasts = append(uLasts, u)
				lastActs = append(lastActs, la)
				embeddings = append(embeddings, emb)
			}
			rows.Close()

			moved := 0
			for i := 0; i < len(ids); i++ {
				dt := time.Since(lastActs[i]).Hours()
				uNow := float64(C.calculate_decay(C.double(uLasts[i]), C.double(lambda), C.double(dt)))

				if uNow < 0.4 {
					// NEU: Beim INSERT speichern wir den Vektor wieder in der neuen Tabelle (Casting nach ::vector)
					_, err := tx.Exec(ctx, "INSERT INTO archive0 (id, payload, utility_index, last_activity, embedding) VALUES ($1, $2, $3, $4, $5::vector) ON CONFLICT DO NOTHING", ids[i], payloads[i], uNow, lastActs[i], embeddings[i])
					if err == nil {
						tx.Exec(ctx, "DELETE FROM deep_archive WHERE id = $1", ids[i])
						moved++
					}
				}
			}
			tx.Commit(ctx)
			if moved > 0 {
				fractalLog("🌊 PID-Flow: Pressure %.1f%% | Lambda %.5f | Moved %d to Archive0\n", pressure*100, lambda, moved)
			}
			time.Sleep(500 * time.Millisecond)
		} else {
			time.Sleep(5 * time.Second)
		}
	}
}

// --- SYSTEM B: Rekursiver Crawler ---
func runFractalCrawler(pool *pgxpool.Pool) {
	ctx := context.Background()
	const BATCH_SIZE = 5000

	for {
		tier := 0
		currentEpsilon := getEpsilon()
		workDone := false
		for {
			source, target := fmt.Sprintf("archive%d", tier), fmt.Sprintf("archive%d", tier+1)
			prev := "table4"
			if tier > 0 {
				prev = fmt.Sprintf("archive%d", tier-1)
			}

			if !tableExists(ctx, pool, source) {
				break
			}

			var cPrev, cCurr int
			_ = pool.QueryRow(ctx, fmt.Sprintf("SELECT count(*) FROM %s", prev)).Scan(&cPrev)
			_ = pool.QueryRow(ctx, fmt.Sprintf("SELECT count(*) FROM %s", source)).Scan(&cCurr)

			capacity := int(float64(cPrev) * PHI)
			if capacity < 5000 {
				capacity = 5000
			}
			isOverloaded := cCurr > capacity
			lambda := BASE_LAMBDA / math.Pow(PHI, float64(tier+1))

			// NEU: Vektoren auch beim regulären Gravity Fall mitnehmen
			rows, err := pool.Query(ctx, fmt.Sprintf("SELECT id, utility_index, last_activity, payload, embedding::text FROM %s ORDER BY utility_index ASC LIMIT %d", source, BATCH_SIZE))
			if err == nil {
				var batch_ids []string
				var batch_u []float64
				var batch_la []time.Time
				var batch_pl []string
				var batch_emb []*string

				for rows.Next() {
					var id, pl string
					var u float64
					var la time.Time
					var emb *string
					// ACHTUNG: Die Reihenfolge des Scans muss zwingend zum SELECT passen!
					rows.Scan(&id, &u, &la, &pl, &emb)
					batch_ids = append(batch_ids, id)
					batch_u = append(batch_u, u)
					batch_la = append(batch_la, la)
					batch_pl = append(batch_pl, pl)
					batch_emb = append(batch_emb, emb)
				}
				rows.Close()

				evapCount := 0
				movedCount := 0
				bytesEvap := int64(0)

				for i := 0; i < len(batch_ids); i++ {
					uNow := float64(C.calculate_decay(C.double(batch_u[i]), C.double(lambda), C.double(time.Since(batch_la[i]).Hours())))

					if uNow < currentEpsilon {
						tx, _ := pool.Begin(ctx)
						tx.Exec(ctx, fmt.Sprintf("DELETE FROM %s WHERE id = $1", source), batch_ids[i])
						tx.Exec(ctx, "UPDATE yafad_stats SET value = value + $1 WHERE key = 'evaporated_bytes'", float64(len(batch_pl[i])))
						tx.Commit(ctx)
						evapCount++
						bytesEvap += int64(len(batch_pl[i]))
						workDone = true
					} else if isOverloaded {
						ensureArchiveTableExists(ctx, pool, tier+1)
						tx, _ := pool.Begin(ctx)
						// NEU: INSERT mit Parameter 5 (Vector)
						_, errI := tx.Exec(ctx, fmt.Sprintf("INSERT INTO %s (id, payload, utility_index, last_activity, embedding) VALUES ($1, $2, $3, $4, $5::vector) ON CONFLICT DO NOTHING", target), batch_ids[i], batch_pl[i], uNow, batch_la[i], batch_emb[i])
						if errI == nil {
							tx.Exec(ctx, fmt.Sprintf("DELETE FROM %s WHERE id = $1", source), batch_ids[i])
							movedCount++
							workDone = true
						}
						tx.Commit(ctx)
					}
				}

				if evapCount > 0 {
					fractalLog("💨 [%s] Hawking Radiation: Evaporated %d records (%d bytes reclaimed).\n", source, evapCount, bytesEvap)
				}
				if movedCount > 0 {
					fractalLog("📉 [%s] Gravity Fall: %d records fell into %s.\n", source, movedCount, target)
				}
			}
			tier++
		}
		if workDone {
			time.Sleep(100 * time.Millisecond)
		} else {
			time.Sleep(5 * time.Second)
		}
	}
}

// --- HELPERS ---
func tableExists(ctx context.Context, pool *pgxpool.Pool, name string) bool {
	var exists bool
	pool.QueryRow(ctx, "SELECT EXISTS (SELECT FROM pg_tables WHERE schemaname = 'public' AND tablename = $1)", name).Scan(&exists)
	return exists
}

func ensureStatsTable(ctx context.Context, pool *pgxpool.Pool) {
	// Sicherstellen, dass pgvector aktiviert wird (doppelt hält besser)
	pool.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS vector;")

	pool.Exec(ctx, "CREATE TABLE IF NOT EXISTS yafad_stats (key TEXT PRIMARY KEY, value FLOAT);")
	pool.Exec(ctx, "INSERT INTO yafad_stats (key, value) VALUES ('evaporated_bytes', 0) ON CONFLICT DO NOTHING;")
}

func ensureArchiveTableExists(ctx context.Context, pool *pgxpool.Pool, tier int) {
	name := fmt.Sprintf("archive%d", tier)
	if !tableExists(ctx, pool, name) {
		// NEU: Tabellen mit VECTOR Spalte erstellen
		pool.Exec(ctx, fmt.Sprintf("CREATE TABLE %s (id TEXT PRIMARY KEY, payload TEXT, utility_index DOUBLE PRECISION, last_activity TIMESTAMP, embedding VECTOR(768));", name))
		pool.Exec(ctx, fmt.Sprintf("CREATE INDEX idx_%s_utility ON %s (utility_index ASC);", name, name))
		// NEU: High-Speed Index für Ähnlichkeitssuche erstellen
		pool.Exec(ctx, fmt.Sprintf("CREATE INDEX IF NOT EXISTS idx_%s_embedding ON %s USING hnsw (embedding vector_cosine_ops);", name, name))

		if tier > 4 {
			fractalLog("🏗️  Fractal Expansion: Tier %d spawned with Vector capabilities.\n", tier)
		}
	}
}
