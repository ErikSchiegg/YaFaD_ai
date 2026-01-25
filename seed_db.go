package main

import (
	"context"
	"fmt"
	"math/rand"
	"os"

	"github.com/jackc/pgx/v5"
)

func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}

func main() {
	connStr := fmt.Sprintf("postgres://%s:%s@%s:5432/%s?sslmode=disable",
		getEnv("DB_USER", "eriks"), getEnv("DB_PASSWORD", "test"),
		getEnv("DB_HOST", "localhost"), getEnv("DB_NAME", "yafad_test"))

	ctx := context.Background()
	conn, err := pgx.Connect(ctx, connStr)
	if err != nil {
		fmt.Printf("❌ Connection failed: %v\n", err)
		return
	}
	defer conn.Close(ctx)

	fmt.Println("🏗️  Seeding database with ~50MB of metadata...")

	totalRecords := 50000
	for i := 0; i < totalRecords; i++ {
		tier := rand.Intn(5)
		tableName := fmt.Sprintf("table%d", tier)
		id := fmt.Sprintf("rec_%06d", i)

		// Create a realistic JSON payload
		payload := fmt.Sprintf(`{"metadata": "ai_weights_block", "vector_sum": %v, "status": "seeded"}`, rand.Float64())
		utility := rand.Float64()

		query := fmt.Sprintf("INSERT INTO %s (id, payload, utility_index) VALUES ($1, $2, $3)", tableName)
		_, err = conn.Exec(ctx, query, id, payload, utility)

		// Fix: Actually check the error to satisfy the Go compiler
		if err != nil {
			fmt.Printf("❌ Insert error at record %d: %v\n", i, err)
			return
		}

		if i%5000 == 0 {
			fmt.Printf("📡 Progress: %d/%d records seeded...\n", i, totalRecords)
		}
	}

	fmt.Println("✅ Seeding complete. The 'Memory' is now primed.")
}
