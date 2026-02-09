package main

import (
	"context"
	"flag" // NEU: Für Command Line Arguments
	"fmt"
	"math"
	"math/rand"
	"os"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	BatchSize = 500
	Workers   = 4
)

func main() {
	// 1. Flags einlesen (Hier kommen die Daten von main.go an)
	totalRowsPtr := flag.Int("count", 100000, "Total rows to inject")
	flag.Parse()

	totalRows := *totalRowsPtr
	// Berechne Batches basierend auf der gewünschten Gesamtmenge
	totalBatches := int(math.Ceil(float64(totalRows) / float64(BatchSize)))
	batchesPerWorker := int(math.Ceil(float64(totalBatches) / float64(Workers)))

	// 2. Verbindung
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

	fmt.Printf("\n🌊 TRAFFIC GENERATOR STARTED (Remote Control)\n")
	fmt.Printf("🎯 Target: %d Rows (%d Batches via %d Workers)\n", totalRows, totalBatches, Workers)

	var wg sync.WaitGroup
	wg.Add(Workers)

	for i := 0; i < Workers; i++ {
		go func(id int) {
			defer wg.Done()
			for b := 0; b < batchesPerWorker; b++ {
				// Batch vorbereiten
				rows := [][]interface{}{}
				for j := 0; j < BatchSize; j++ {
					// Mix: 30% Altlasten, 70% Frisch
					uIndex := 1.0
					age := time.Now()
					if rand.Float64() < 0.3 {
						uIndex = 0.1
						age = time.Now().Add(-24 * time.Hour)
					}

					jsonPayload := fmt.Sprintf(`{"content": "data-%d", "worker": %d}`, rand.Int(), id)

					rows = append(rows, []interface{}{
						fmt.Sprintf("gen-%d-%d-%d", id, time.Now().UnixNano(), j),
						jsonPayload,
						uIndex,
						age,
					})
				}

				_, err := pool.CopyFrom(
					ctx,
					pgx.Identifier{"table0"},
					[]string{"id", "payload", "utility_index", "last_activity"},
					pgx.CopyFromRows(rows),
				)

				if err != nil {
					fmt.Printf("Worker %d Err: %v\n", id, err)
				} else {
					// Minimaler Output, damit main.go nicht zugemüllt wird
					if b%10 == 0 {
						fmt.Printf(".")
					}
				}
				time.Sleep(50 * time.Millisecond)
			}
		}(i)
	}

	wg.Wait()
	fmt.Println("\n✅ Injection Complete.")
}
