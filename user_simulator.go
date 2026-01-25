package main

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"time"

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
		panic(err)
	}

	fmt.Println("🎮 User Simulator Started: Emulating real-time traffic...")

	for {
		action := rand.Intn(10)

		if action < 3 { // 30% chance: New Data Inflow
			newID := fmt.Sprintf("new_data_%d", rand.Int63())
			fmt.Printf("✍️  User created new data: %s\n", newID)
			conn.Exec(ctx, "INSERT INTO buffer_tier (id, payload, utility_index) VALUES ($1, $2, 1.0)",
				newID, `{"type": "user_input", "content": "fresh_ai_prompt"}`)
		} else { // 70% chance: Data Recall (Reinforcement)
			// Pick a random tier and random record to "access"
			tier := rand.Intn(5)
			var id string
			var payload string

			err := conn.QueryRow(ctx, fmt.Sprintf("SELECT id, payload FROM table%d LIMIT 1 OFFSET %d", tier, rand.Intn(1000))).Scan(&id, &payload)

			if err == nil {
				fmt.Printf("🔍 User accessed data: %s (Promoting to Buffer)\n", id)
				// Promote to buffer (This is the Reinforcement logic)
				conn.Exec(ctx, `INSERT INTO buffer_tier (id, payload, utility_index) 
								VALUES ($1, $2, 1.0) ON CONFLICT (id) DO UPDATE SET utility_index = 1.0`, id, payload)
				conn.Exec(ctx, fmt.Sprintf("DELETE FROM table%d WHERE id = $1", tier), id)
			}
		}

		time.Sleep(500 * time.Millisecond) // Simulate human-like pace
	}
}
