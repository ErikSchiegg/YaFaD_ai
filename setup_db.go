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

// --- 2. Logic: Promote to Synaptic Buffer ---
func PromoteToBuffer(ctx context.Context, conn *pgx.Conn, id string, payload string) error {
	fmt.Printf("⚡ Promoting ID '%s' to Synaptic Buffer (Reinforcement)...\n", id)
	query := `
		INSERT INTO buffer_tier (id, payload, utility_index, last_activity)
		VALUES ($1, $2, 1.0, CURRENT_TIMESTAMP)
		ON CONFLICT (id) DO UPDATE SET 
			utility_index = 1.0, 
			last_activity = CURRENT_TIMESTAMP;`
	_, err := conn.Exec(ctx, query, id, payload)
	return err
}

// --- 3. Logic: The Consolidator (Background Worker) ---
// This merges the Buffer into T0 and clears the Buffer.
func StartConsolidator(conn *pgx.Conn, interval time.Duration) {
	ticker := time.NewTicker(interval)
	go func() {
		for range ticker.C {
			ctx := context.Background()
			fmt.Println("🧠 Consolidator: Merging Synaptic Buffer into T0...")

			// Atomic Merge: Move from buffer_tier to table0
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
			} else {
				fmt.Println("✅ Consolidation successful. Buffer cleared.")
			}
		}
	}()
}

func main() {
	// Anonymized Credentials
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

	fmt.Println("🐘 Connected. Provisioning infrastructure...")

	// [Schema Creation Logic remains here...]
	// (Ensure the buffer_tier and table0-4 queries run once)

	// --- Start the Consolidator ---
	// In production, this might run every minute. For testing: 10 seconds.
	StartConsolidator(conn, 10*time.Second)

	fmt.Println("\n🚀 YaFaD_ai infrastructure is fully operational!")

	// Keep the main process alive for the background worker test
	select {}
}
