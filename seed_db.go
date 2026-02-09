package main

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	TotalRecords = 2000000
	BatchSize    = 2000

	T2IdealCapacity = 51000
	HighWaterMark   = T2IdealCapacity * 2.0
	LowWaterMark    = T2IdealCapacity * 1.1
)

func main() {
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
		log.Fatal(err)
	}
	defer pool.Close()

	log.Println("🌱 YaFaD Smart Seeder v0.4.2 (JSON Fixed) starting...")
	log.Printf("🎯 Goal: %d Records | T2 Limit: >%d (Slow) / <%d (Fast)", TotalRecords, int(HighWaterMark), int(LowWaterMark))

	isThrottled := false
	insertedTotal := 0

	for insertedTotal < TotalRecords {

		// --- BACKPRESSURE CHECK ---
		if insertedTotal%(BatchSize*5) == 0 {
			var t2Count int
			err := pool.QueryRow(ctx, "SELECT count(*) FROM table2").Scan(&t2Count)
			if err == nil {
				pressure := float64(t2Count) / float64(T2IdealCapacity) * 100.0

				if !isThrottled && t2Count > int(HighWaterMark) {
					isThrottled = true
					log.Printf("🛑 OVERFLOW DETECTED (T2: %.0f%%). Throttling injection...", pressure)
				} else if isThrottled && t2Count < int(LowWaterMark) {
					isThrottled = false
					log.Printf("🟢 PRESSURE RELEASED (T2: %.0f%%). Resuming full speed.", pressure)
				}
			}
		}

		if isThrottled {
			time.Sleep(500 * time.Millisecond)
			fmt.Print("zzz ")
		}

		// --- INSERTION ---
		batch := make([][]interface{}, 0, BatchSize)
		for i := 0; i < BatchSize; i++ {
			id := fmt.Sprintf("seed_%d_%d", time.Now().UnixNano(), rand.Intn(999999))

			// --- KORREKTUR: Valides JSON Format ---
			// Statt nur "xxxxx" bauen wir jetzt ein JSON Objekt: {"data": "xxxxx"}
			rawContent := strings.Repeat("x", 50)
			payload := fmt.Sprintf(`{"content": "%s", "timestamp": "%v"}`, rawContent, time.Now().Unix())

			batch = append(batch, []interface{}{id, payload, 1.0, time.Now()})
		}

		_, err = pool.CopyFrom(
			ctx,
			pgx.Identifier{"table0"},
			[]string{"id", "payload", "utility_index", "last_activity"},
			pgx.CopyFromRows(batch),
		)

		if err != nil {
			// Wir loggen den Fehler, brechen aber nicht ab (falls mal ein einzelner Batch fehlschlägt)
			log.Printf("❌ Insert Error: %v", err)
			time.Sleep(1 * time.Second) // Kurze Pause bei Fehler
		} else {
			insertedTotal += BatchSize
			if !isThrottled {
				fmt.Print(".")
			}
		}

		if insertedTotal%(BatchSize*20) == 0 {
			fmt.Printf(" %d\n", insertedTotal)
		}
	}

	log.Println("\n✅ Seeding complete.")
}
