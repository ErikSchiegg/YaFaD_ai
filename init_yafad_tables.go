package main

import (
	"context"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	// 1. Connection
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

	fmt.Println("🏗️  Initializing YaFaD Core Tables...")

	// 2. Definition der Tabellen
	tables := []string{"table0", "table1", "table2", "table3", "table4", "deep_archive"}

	for _, table := range tables {
		fmt.Printf("   Checking %s... ", table)

		// Schema: ID (Text/UUID), Payload (JSON), Utility (Float), LastActivity (Time)
		query := fmt.Sprintf(`
			CREATE TABLE IF NOT EXISTS %s (
				id TEXT PRIMARY KEY,
				payload JSONB,
				utility_index DOUBLE PRECISION,
				last_activity TIMESTAMP
			);
		`, table)

		_, err := pool.Exec(ctx, query)
		if err != nil {
			fmt.Printf("❌ Error: %v\n", err)
		} else {
			fmt.Printf("✅ OK.\n")
		}
	}

	fmt.Println("\n✨ All systems ready. Restart main.go now!")
}
