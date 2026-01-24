package main

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

// --- 2. Logic: The Consolidator (Background Worker) ---
func StartConsolidator(conn *pgx.Conn, interval time.Duration) {
	ticker := time.NewTicker(interval)
	go func() {
		for range ticker.C {
			ctx := context.Background()
			fmt.Println("🧠 Consolidator: Syncing Synaptic Buffer to T0...")

			// Atomic Move: Move from buffer_tier to table0 and clean up
			mergeQuery := `
				INSERT INTO table0 (id, payload, utility_index, last_activity, created_at)
				SELECT id, payload, utility_index, last_activity, created_at FROM buffer_tier
				ON CONFLICT (id) DO UPDATE SET 
					utility_index = EXCLUDED.utility_index,
					last_activity = EXCLUDED.last_activity;
				DELETE FROM buffer_tier;`

			_, err := conn.Exec(ctx, mergeQuery)
			if err != nil {
				// We use a specific check here because the table might be locked
				fmt.Printf("❌ Consolidation Error: %v\n", err)
			} else {
				fmt.Println("✅ Consolidation successful. T0 is now up to date.")
			}
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

	// --- 3. Schema Provisioning (The missing part) ---

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
	// Consolidator runs every 10 seconds for testing
	StartConsolidator(conn, 10*time.Second)

	// Keep alive to watch the background worker
	fmt.Println("📡 System is idle. Waiting for background tasks (Ctrl+C to stop)...")
	select {}
}
