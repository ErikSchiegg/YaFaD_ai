package main

import (
	"context"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5"
)

func main() {
	// Connection string (using password 'test' and disabled SSL for local dev)
	connStr := "postgres://eriks:test@localhost:5432/yafad_test?sslmode=disable"
	ctx := context.Background()

	conn, err := pgx.Connect(ctx, connStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Connection failed: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close(ctx)

	fmt.Println("🐘 Connected to PostgreSQL. Provisioning YaFaD_ai hierarchy...")

	// 1. Create the Synaptic Buffer (Collector Table)
	// This table handles reinforced records before they are merged into T0.
	bufferQuery := `
		CREATE TABLE IF NOT EXISTS buffer_tier (
			id VARCHAR(255) PRIMARY KEY,
			payload JSONB,
			utility_index DOUBLE PRECISION DEFAULT 1.0,
			last_activity TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		);`

	_, err = conn.Exec(ctx, bufferQuery)
	if err != nil {
		fmt.Printf("❌ Error creating Synaptic Buffer: %v\n", err)
	} else {
		fmt.Println("✅ Synaptic Buffer (Collector) successfully provisioned.")
	}

	// 2. Create the Cascade Tiers (T0 to T4)
	for i := 0; i < 5; i++ {
		tableName := fmt.Sprintf("table%d", i)

		// Professional bio-inspired schema
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

		_, err := conn.Exec(ctx, query)
		if err != nil {
			fmt.Printf("❌ Error creating %s: %v\n", tableName, err)
		} else {
			fmt.Printf("✅ Tier %s successfully provisioned (including Utility Index).\n", tableName)
		}
	}

	// PromoteToBuffer moves or refreshes a record within the Synaptic Buffer.
	// It uses an "Upsert" logic to ensure the record is at peak utility.
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
	fmt.Println("\n🚀 YaFaD_ai infrastructure is fully operational!")
}
