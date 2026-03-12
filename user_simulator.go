package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"os"
	"time"

	_ "github.com/lib/pq"
)

// --- CONFIGURATION ---
const (
	// Simulations-Geschwindigkeit:
	// 5 echte Sekunden = 1 virtuelle Stunde.
	// Ein voller "Tag" dauert also 24 * 5 = 120 Sekunden (2 Minuten).
	RealSecondsPerHour = 5 * time.Second
)

func main() {
	// Parse command line arguments
	var count int
	flag.IntVar(&count, "count", 0, "Number of records to generate (0 for infinite loop)")
	flag.Parse()

	// 1. DB Connection
	dbUser := os.Getenv("DB_USER")
	if dbUser == "" {
		dbUser = "eriks"
	}
	dbPass := os.Getenv("DB_PASSWORD")
	if dbPass == "" {
		dbPass = "test"
	}
	dbHost := os.Getenv("DB_HOST")
	if dbHost == "" {
		dbHost = "localhost"
	}

	connStr := fmt.Sprintf("postgres://%s:%s@%s:5432/yafad_test?sslmode=disable", dbUser, dbPass, dbHost)

	db, err := sql.Open("postgres", connStr)
	if err != nil {
		log.Fatalf("❌ FATAL: Could not connect to database at %s: %v", dbHost, err)
	}
	defer db.Close()

	err = db.Ping()
	if err != nil {
		log.Fatalf("❌ FATAL: Could not ping database at %s: %v", dbHost, err)
	}

	rand.Seed(time.Now().UnixNano())

	// Wenn ein Count angegeben wurde (von der Engine beim "Start Mission")
	if count > 0 {
		log.Printf("🤖 Injecting %d records into table0...", count)
		ctx := context.Background()
		for i := 0; i < count; i++ {
			createRecord(ctx, db)
		}
		log.Println("✅ Injection complete.")
		return
	}

	// Ansonsten: Endlosschleife (Bio-Rhythm Simulator)
	log.Println("🤖 Bio-Rhythm User Simulator v0.5.0 (Cyclic Pattern) starting...")
	log.Printf("⏱️  Time Scale: 1 Virtual Hour = %v Real Time", RealSecondsPerHour)

	virtualHour := 6
	hourTicker := time.NewTicker(RealSecondsPerHour)
	defer hourTicker.Stop()

	log.Printf("\n🌅 GOOD MORNING! Virtual Time: %02d:00", virtualHour)

	for {
		select {
		case <-hourTicker.C:
			virtualHour = (virtualHour + 1) % 24
			icon := getHourIcon(virtualHour)
			log.Printf("\n%s Virtual Time: %02d:00 | Load: %s", icon, virtualHour, getLoadDescription(virtualHour))
			if virtualHour == 0 {
				log.Println("✨ A NEW DAY BEGINS...")
			}

		default:
			performHourlyAction(db, virtualHour)
		}
	}
}

func getHourIcon(hour int) string {
	switch {
	case hour >= 0 && hour < 6:
		return "🌙"
	case hour >= 6 && hour < 9:
		return "🌅"
	case hour >= 9 && hour < 18:
		return "💼"
	case hour >= 18 && hour < 23:
		return "🔥"
	default:
		return "🛌"
	}
}

func getLoadDescription(hour int) string {
	if hour >= 0 && hour < 6 {
		return "Deep Sleep (Low)"
	}
	if hour >= 6 && hour < 9 {
		return "Ramp Up (Medium)"
	}
	if hour >= 9 && hour < 18 {
		return "Business Steady (High)"
	}
	if hour >= 18 && hour < 23 {
		return "Viral Spike (Extreme)"
	}
	return "Cool Down"
}

func performHourlyAction(db *sql.DB, hour int) {
	ctx := context.Background()
	var baseSleepMs, variance, batchSize int

	switch {
	case hour >= 0 && hour < 6:
		baseSleepMs, variance, batchSize = 1500, 1000, 0
	case hour >= 6 && hour < 9:
		baseSleepMs, variance, batchSize = 100, 100, 1
	case hour >= 9 && hour < 18:
		baseSleepMs, variance, batchSize = 10, 20, 2
	case hour >= 18 && hour < 23:
		baseSleepMs, variance, batchSize = 2, 10, 15
	default:
		baseSleepMs, variance, batchSize = 500, 200, 1
	}

	actualSleep := time.Duration(baseSleepMs+rand.Intn(variance+1)) * time.Millisecond
	time.Sleep(actualSleep)

	if batchSize == 0 && rand.Float32() > 0.1 {
		return
	}
	if batchSize == 0 {
		batchSize = 1
	}

	writeProbability := 0.3
	if hour >= 18 {
		writeProbability = 0.1
	}

	if rand.Float64() < writeProbability {
		for i := 0; i < batchSize; i++ {
			createRecord(ctx, db)
		}
		if batchSize > 5 {
			fmt.Print("#")
		} else {
			fmt.Print(".")
		}
	} else {
		simulateAccess(ctx, db)
	}
}

func createRecord(ctx context.Context, db *sql.DB) {
	id := fmt.Sprintf("rec_%d_%d", time.Now().UnixNano(), rand.Intn(100000))
	_, err := db.ExecContext(ctx, "INSERT INTO table0 (id, payload, utility_index, last_activity) VALUES ($1, '{\"type\": \"user_data\", \"value\": 42}', 1.0, NOW()) ON CONFLICT DO NOTHING", id)
	if err != nil {
		// Ignore constraint errors
	}
}

func simulateAccess(ctx context.Context, db *sql.DB) {
	tier := 0
	r := rand.Float32()
	if r > 0.6 {
		tier = 1
	}
	if r > 0.85 {
		tier = 2
	}

	tableName := fmt.Sprintf("table%d", tier)
	query := fmt.Sprintf("UPDATE %s SET utility_index = 1.0, last_activity = NOW() WHERE id IN (SELECT id FROM %s LIMIT 1)", tableName, tableName)
	_, _ = db.ExecContext(ctx, query)
}
