package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	// Configure connection (Connects to the "Cold" storage)
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
		fmt.Printf("❌ Connection failed: %v\n", err)
		return
	}
	defer pool.Close()

	fmt.Println("🚜 YaFaD_ai Archive Gardener: Online.")
	fmt.Println("🍂 Target: 'table4' (Cold Storage Optimization)")

	// Maintenance Loop
	for {
		performMaintenance(ctx, pool)

		// Run every 60 seconds (In production: every 24h)
		fmt.Println("💤 Resting for 60 seconds...")
		time.Sleep(60 * time.Second)
	}
}

func performMaintenance(ctx context.Context, pool *pgxpool.Pool) {
	startTime := time.Now()
	fmt.Println("------------------------------------------------")
	fmt.Println("🔍 Analyzing Archive Fragmentation...")

	// 1. Check current size
	var count int
	pool.QueryRow(ctx, "SELECT count(*) FROM table4").Scan(&count)
	fmt.Printf("📦 Current Archive Load: %d records\n", count)

	if count == 0 {
		fmt.Println("✨ Archive is empty. No gardening needed.")
		return
	}

	// 2. Physical Clustering (The Heavy Lifting)
	// This commands PostgreSQL to physically rewrite the table file
	// so that rows are sorted by their Utility Index.
	fmt.Println("⚙️  Executing Physical Cluster Sort (ORDER BY utility_index DESC)...")

	// Note: CLUSTER takes an exclusive lock.
	// In a real distributed system, we would do this on a replica or during low-traffic windows.
	_, err := pool.Exec(ctx, "CLUSTER table4 USING idx_table4_utility")

	if err != nil {
		fmt.Printf("❌ Optimization Failed: %v\n", err)
		return
	}

	// 3. Update Statistics for the Query Planner
	pool.Exec(ctx, "ANALYZE table4")

	duration := time.Since(startTime)
	fmt.Printf("✅ Archive Optimized in %s.\n", duration)
	fmt.Println("💎 Result: High-Utility records are now physically adjacent on disk.")
	fmt.Println("------------------------------------------------")
}
