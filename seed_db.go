package main

import (
	"bufio"
	"context"
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"time" // Required for Time-Warp logic

	"github.com/jackc/pgx/v5"
)

// getEnv retrieves environment variables or returns a fallback value.
func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}

func main() {
	// 1. User Interaction: Ask for evaluation database size
	reader := bufio.NewReader(os.Stdin)
	fmt.Print("📊 Enter desired evaluation database size in MB [Default 50]: ")
	input, _ := reader.ReadString('\n')
	input = strings.TrimSpace(input)

	mbSize := 50 // Default value
	if input != "" {
		if val, err := strconv.Atoi(input); err == nil {
			mbSize = val
		}
	}

	// Calculation: 1 MB is approx. 1000 records (assuming ~1KB per record payload)
	totalRecords := mbSize * 1000
	fmt.Printf("🏗️  Seeding roughly %d MB (~%d records) with 48h Time-Warp...\n", mbSize, totalRecords)

	// 2. Database Connection setup
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

	// 3. Maintenance: Optional data cleanup for a clean evaluation run
	fmt.Print("🧹 Clear existing data before seeding? [y/N]: ")
	cleanInput, _ := reader.ReadString('\n')
	if strings.ToLower(strings.TrimSpace(cleanInput)) == "y" {
		fmt.Println("♻️  Truncating all tiers...")
		conn.Exec(ctx, "TRUNCATE buffer_tier, table0, table1, table2, table3, table4;")
	}

	// 4. Seeding Process: Injecting aged data to trigger immediate decay evaluation
	for i := 0; i < totalRecords; i++ {
		tier := rand.Intn(5)
		tableName := fmt.Sprintf("table%d", tier)
		id := fmt.Sprintf("rec_%08d", i)

		// Create a realistic payload (padded to ~1KB for volume simulation)
		payload := fmt.Sprintf(`{"data": "%s", "metadata": "ai_block_%d", "vector": %v}`,
			strings.Repeat("x", 800), i, rand.Float64())
		utility := rand.Float64()

		// TIME-WARP: Set 'last_activity' 24 to 48 hours into the past
		hoursAgo := 24 + rand.Intn(24)
		pastTime := time.Now().Add(time.Duration(-hoursAgo) * time.Hour)

		query := fmt.Sprintf("INSERT INTO %s (id, payload, utility_index, last_activity) VALUES ($1, $2, $3, $4)", tableName)
		_, err = conn.Exec(ctx, query, id, payload, utility, pastTime)

		if err != nil {
			fmt.Printf("❌ Error at record %d: %v\n", i, err)
			return
		}

		if i%5000 == 0 && i > 0 {
			fmt.Printf("📡 Progress: %d/%d records injected...\n", i, totalRecords)
		}
	}

	fmt.Printf("✅ Success! %d MB seeded across all tiers. Data is 'aged' and ready for the Decay Worker.\n", mbSize)
}
