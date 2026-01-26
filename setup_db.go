package main

/*
#cgo LDFLAGS: -L${SRCDIR}/core/target/release -lyafad_core -Wl,-rpath,${SRCDIR}/core/target/release -lm -ldl
#cgo CPPFLAGS: -I${SRCDIR}/core
extern double calculate_ideal_capacity(double total_records, int tier);
*/
import "C"

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
)

// --- 1. Helper Function: Environment Variables ---
func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}

// DisplaySystemStatus prints a high-level overview of the data distribution.
func DisplaySystemStatus(ctx context.Context, conn *pgx.Conn) {
	fmt.Print("\033[H\033[2J") // Clear screen

	// 1. Snapshot der aktuellen Verteilung holen
	counts := make([]int, 5)
	var totalRecords int
	tiers := []string{"table0", "table1", "table2", "table3", "table4"}

	for i, table := range tiers {
		conn.QueryRow(ctx, fmt.Sprintf("SELECT count(*) FROM %s", table)).Scan(&counts[i])
		totalRecords += counts[i]
	}

	fmt.Println("--- 📊 YaFaD_ai Bio-Organic Dashboard ---")
	fmt.Printf("Total Biomass: %d records (Active Load)\n", totalRecords)
	fmt.Println("-----------------------------------------------------------------------")
	fmt.Println("Tier Name | Current | Ideal Cap | Fill % | Health Status")
	fmt.Println("-----------------------------------------------------------------------")

	// 2. Analyse jedes Tiers
	for i, table := range tiers {
		// RUST: Berechne ideale Kapazität dynamisch
		idealCap := float64(C.calculate_ideal_capacity(C.double(float64(totalRecords)), C.int(i)))

		fillPercent := 0.0
		if idealCap > 0 {
			fillPercent = (float64(counts[i]) / idealCap) * 100
		}

		// 3. Diagnose
		status := "🟢 Optimal" // Standardwert

		if i == 4 {
			// SONDERREGEL FÜR DAS ARCHIV:
			// Das Archiv darf immer wachsen. Es ist kein "Overflow", sondern Sedimentierung.
			status = "🔵 Archive growing with unused records"
		} else {
			// REGELN FÜR AKTIVE TIERS (T0 - T3):
			if fillPercent > 100 {
				status = "🔴 OVERFLOW" // Zu voll -> Lambda muss steigen (Druck nach unten erhöhen)
			} else if fillPercent < 50 {
				status = "🟡 Starving" // Zu leer -> Lambda muss sinken (Druck verringern)
			}
		}

		fmt.Printf("%-9s | %-7d | %-9.0f | %5.1f%% | %s\n", table, counts[i], idealCap, fillPercent, status)
	}
	fmt.Println("-----------------------------------------------------------------------")
}

// --- 2. Logic: The Consolidator (Background Worker) ---
func StartConsolidator(conn *pgx.Conn, interval time.Duration) {
	ticker := time.NewTicker(interval)
	go func() {
		for range ticker.C {
			ctx := context.Background()

			// Atomic Move: Buffer -> T0
			mergeQuery := `
                INSERT INTO table0 (id, payload, utility_index, last_activity, created_at)
                SELECT id, payload, utility_index, last_activity, created_at FROM buffer_tier
                ON CONFLICT (id) DO UPDATE SET 
                    utility_index = EXCLUDED.utility_index,
                    last_activity = EXCLUDED.last_activity;
                DELETE FROM buffer_tier;`

			_, err := conn.Exec(ctx, mergeQuery)
			if err != nil {
				fmt.Printf("❌ Consolidation Error: %v\n", err)
			}

			// Hier rufen wir das Dashboard auf
			DisplaySystemStatus(ctx, conn)
		}
	}()
}

func main() {
	// Credentials
	dbUser := getEnv("DB_USER", "eriks")
	dbPass := getEnv("DB_PASSWORD", "test")
	dbHost := getEnv("DB_HOST", "localhost")
	dbName := getEnv("DB_NAME", "yafad_test")

	connStr := fmt.Sprintf("postgres://%s:%s@%s:5432/%s?sslmode=disable",
		dbUser, dbPass, dbHost, dbName)

	ctx := context.Background()
	conn, err := pgx.Connect(ctx, connStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Connection failed: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close(ctx)

	fmt.Println("🐘 Connected. Starting YaFaD_ai provisioning...")

	// --- 3. Schema Provisioning ---

	// Create Synaptic Buffer First
	fmt.Println("🛠️ Provisioning Synaptic Buffer...")
	_, err = conn.Exec(ctx, `
        CREATE TABLE IF NOT EXISTS buffer_tier (
            id VARCHAR(255) PRIMARY KEY,
            payload JSONB,
            utility_index DOUBLE PRECISION DEFAULT 1.0,
            last_activity TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
            created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
        );`)
	if err != nil {
		fmt.Printf("❌ Failed to create buffer: %v\n", err)
		return
	}

	// Create T0 - T4 Tiers
	for i := 0; i < 5; i++ {
		tableName := fmt.Sprintf("table%d", i)
		fmt.Printf("🛠️ Provisioning Tier %s...\n", tableName)

		query := fmt.Sprintf(`
            CREATE TABLE IF NOT EXISTS %s (
                id VARCHAR(255) PRIMARY KEY,
                payload JSONB,
                utility_index DOUBLE PRECISION DEFAULT 1.0,
                last_activity TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
                created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
            );
            CREATE INDEX IF NOT EXISTS idx_%s_utility ON %s (utility_index DESC);
        `, tableName, tableName, tableName)

		_, err = conn.Exec(ctx, query)
		if err != nil {
			fmt.Printf("❌ Failed to create %s: %v\n", tableName, err)
			return
		}
	}

	fmt.Println("\n🚀 YaFaD_ai infrastructure is fully operational!")

	// --- 4. Start Background Services ---
	// Consolidator runs every 2 seconds for smooth dashboard updates
	StartConsolidator(conn, 2*time.Second)

	// Keep alive to watch the background worker
	fmt.Println("📡 System is idle. Waiting for background tasks (Ctrl+C to stop)...")
	select {}
}
