package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// --- CONFIG ---
const (
	PolicyFile = "config/policy.yaml" // Hier schreiben wir die Whitelist rein
)

// TableStatus Enum
type Status int

const (
	StatusPending Status = iota
	StatusWhitelisted
	StatusMigrating
	StatusDigested
	StatusSkipped
)

type TableJob struct {
	ID       int
	Name     string
	RowCount int
	Priority int // 1 = High, 100 = Low
	Status   Status
	Progress int // 0-100%
}

var Jobs []*TableJob

func main() {
	// 1. DB Connect
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

	// 2. Scan Tables
	scanDatabase(ctx, pool)

	// 3. Interactive Loop
	reader := bufio.NewReader(os.Stdin)
	for {
		clearScreen()
		printDashboard()

		fmt.Print("\n🎮 Command (e.g., 'w 1' to whitelist, 'run' to start, 'q' to quit): ")
		input, _ := reader.ReadString('\n')
		input = strings.TrimSpace(input)

		if input == "q" || input == "quit" {
			break
		}
		if input == "run" {
			startMigrationSequence(ctx, pool)
			fmt.Println("\nPress Enter to return to menu...")
			reader.ReadString('\n')
		} else {
			processCommand(input)
		}
	}
}

// --- CORE LOGIC ---

func scanDatabase(ctx context.Context, pool *pgxpool.Pool) {
	// Ignoriere YaFaD interne Tabellen
	ignore := map[string]bool{
		"table0": true, "table1": true, "table2": true, "table3": true, "table4": true,
		"deep_archive": true, "goose_db_version": true,
	}

	rows, err := pool.Query(ctx, "SELECT table_name FROM information_schema.tables WHERE table_schema = 'public'")
	if err != nil {
		fmt.Printf("Error scanning DB: %v", err)
		return
	}
	defer rows.Close()

	idCounter := 1
	for rows.Next() {
		var name string
		rows.Scan(&name)
		if ignore[name] {
			continue
		}

		// Count rows
		var count int
		pool.QueryRow(ctx, fmt.Sprintf("SELECT count(*) FROM %s", name)).Scan(&count)

		Jobs = append(Jobs, &TableJob{
			ID:       idCounter,
			Name:     name,
			RowCount: count,
			Priority: 10, // Default Priority
			Status:   StatusPending,
		})
		idCounter++
	}
}

func processCommand(input string) {
	parts := strings.Fields(input)
	if len(parts) < 2 {
		return
	}

	cmd := parts[0]
	id, err := strconv.Atoi(parts[1])
	if err != nil {
		return
	}

	var job *TableJob
	for _, j := range Jobs {
		if j.ID == id {
			job = j
			break
		}
	}
	if job == nil {
		return
	}

	switch cmd {
	case "w": // Whitelist Toggle
		if job.Status == StatusWhitelisted {
			job.Status = StatusPending
		} else {
			job.Status = StatusWhitelisted
		}
		saveWhitelist() // Sofort config update
	case "p": // Set Priority (p 1 99)
		if len(parts) >= 3 {
			p, _ := strconv.Atoi(parts[2])
			job.Priority = p
		}
	}
	// Sort by Priority
	sort.Slice(Jobs, func(i, j int) bool {
		return Jobs[i].Priority > Jobs[j].Priority // Higher number = Higher prio logic? Or lower? Let's say Higher is better visually
	})
}

func saveWhitelist() {
	// Hier würde man echtes YAML schreiben. Simulation:
	// fmt.Println("Saving policy.yaml...")
	// Echter Code würde die Datei öffnen und die Namen der StatusWhitelisted Jobs reinschreiben.
}

// --- REAL MIGRATION LOGIC (MOVE MODE) ---
func startMigrationSequence(ctx context.Context, pool *pgxpool.Pool) {
	clearScreen()
	fmt.Println("🚀 STARTING LIVE MIGRATION (MOVE MODE)...")
	fmt.Println("⚠️  Original records will be DELETED from source tables!")
	fmt.Println("-----------------------------------------------------")

	// Sortieren: Hohe Prio zuerst
	sort.Slice(Jobs, func(i, j int) bool {
		return Jobs[i].Priority > Jobs[j].Priority
	})

	for _, job := range Jobs {
		if job.Status == StatusWhitelisted || job.Status == StatusDigested || job.RowCount == 0 {
			continue
		}

		job.Status = StatusMigrating
		fmt.Printf("\n🍽️  Moving Table: %s (%d rows)\n", job.Name, job.RowCount)

		// BATCH LOOP
		batchSize := 50000
		totalMoved := 0

		for totalMoved < job.RowCount {
			// 1. Wait if YaFaD Belly is full
			waitForDigestion(ctx, pool)

			fmt.Printf("\r    📦 Moving batch %d...", totalMoved+batchSize)

			// 2. THE ATOMIC MOVE (CTE)
			// Wir nutzen 'ctid', um beliebige Zeilen zu greifen, egal wie der Primary Key heißt.
			// DELETE ... RETURNING -> INSERT
			query := fmt.Sprintf(`
				WITH moved_rows AS (
					DELETE FROM %s
					WHERE ctid IN (
						SELECT ctid FROM %s
						LIMIT %d
					)
					RETURNING *
				)
				INSERT INTO table0 (id, payload, utility_index, last_activity)
				SELECT 
					gen_random_uuid()::text, 
					row_to_json(moved_rows)::text, 
					1.0, 
					NOW() 
				FROM moved_rows;
			`, job.Name, job.Name, batchSize)

			tag, err := pool.Exec(ctx, query)
			if err != nil {
				fmt.Printf("\n❌ Error moving data: %v\n", err)
				break
			}

			// Prüfen, wie viele wirklich bewegt wurden
			rowsAffected := int(tag.RowsAffected())
			if rowsAffected == 0 {
				break // Tabelle leer
			}

			totalMoved += rowsAffected

			// Progress Update
			pct := 0
			if job.RowCount > 0 {
				pct = int((float64(totalMoved) / float64(job.RowCount)) * 100)
			}
			if pct > 100 {
				pct = 100
			}
			job.Progress = pct
		}

		job.Status = StatusDigested
		fmt.Printf("\n✅ %s fully migrated (Source is empty).\n", job.Name)
	}
	fmt.Println("\n✨ Migration Sequence Complete.")
}

func waitForDigestion(ctx context.Context, pool *pgxpool.Pool) {
	// Polling Loop: Wir warten, bis T2 (der Bauch) sich beruhigt hat
	for {
		var t2Count int
		pool.QueryRow(ctx, "SELECT count(*) FROM table2").Scan(&t2Count)

		// Sagen wir Kapazität T2 ist 51000. Wir warten bis unter 80% (40k)
		if t2Count < 40000 {
			return // Genug Platz für den nächsten Brocken
		}
		time.Sleep(2 * time.Second)
		fmt.Print(".")
	}
}

// --- UI HELPERS ---

func printDashboard() {
	fmt.Println("╔══════════════════════════════════════════════════════════════════╗")
	fmt.Println("║ ✈️  YaFaD MIGRATION PILOT v1.0                                   ║")
	fmt.Println("╠══════════════════════════════════════════════════════════════════╣")
	fmt.Println("║ ID | Table Name         | Rows      | Prio | Status              ║")
	fmt.Println("╠══════════════════════════════════════════════════════════════════╣")

	for _, j := range Jobs {
		statusIcon := "⚪ Pending"
		color := "\033[0m"
		if j.Status == StatusWhitelisted {
			statusIcon = "🛡️  WHITELIST"
			color = "\033[1;32m"
		} // Grün
		if j.Status == StatusMigrating {
			statusIcon = "🔄 Ingesting"
			color = "\033[1;33m"
		} // Gelb
		if j.Status == StatusDigested {
			statusIcon = "✅ Digested"
			color = "\033[1;34m"
		} // Blau

		fmt.Printf("║ %2d | %-18s | %9d |  %2d  | %s%-19s\033[0m ║\n",
			j.ID,
			truncate(j.Name, 18),
			j.RowCount,
			j.Priority,
			color,
			statusIcon,
		)
	}
	fmt.Println("╚══════════════════════════════════════════════════════════════════╝")
	fmt.Println(" [w <id>] Toggle Whitelist  |  [p <id> <val>] Set Priority  |  [run] Start")
}

func truncate(s string, l int) string {
	if len(s) > l {
		return s[:l-3] + "..."
	}
	return s
}

func clearScreen() {
	fmt.Print("\033[H\033[2J")
}
