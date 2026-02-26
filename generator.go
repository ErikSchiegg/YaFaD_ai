package main

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Global sleep control for pacemaker
var currentSleepMs int64 = 0

// Config Struct
type SystemConfig struct {
	Capacities map[string]int `json:"capacities"`
}

// --- IMMUNSYSTEM (AES-256-GCM Verschlüsselung) ---
const KEY_FILE = "yafad_secret.key"

var globalSymmetricKey []byte

func initCrypto() {
	// Versuche, den Schlüssel zu laden (der im Idealfall schon von main.go erstellt wurde)
	data, err := os.ReadFile(KEY_FILE)
	if err == nil && len(data) == 32 {
		globalSymmetricKey = data
		return
	}

	// Fallback: Falls der Generator mal vor der Engine startet
	fmt.Println("⚠️  Generator: No valid key found. Generating new AES-256 Secret Key...")
	globalSymmetricKey = make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, globalSymmetricKey); err != nil {
		panic("Failed to generate secure random key: " + err.Error())
	}
	_ = os.WriteFile(KEY_FILE, globalSymmetricKey, 0600)
}

func encryptPayload(plaintext string) string {
	if len(globalSymmetricKey) != 32 {
		return plaintext
	} // Fallback falls Crypto offline

	block, err := aes.NewCipher(globalSymmetricKey)
	if err != nil {
		return plaintext
	}

	aesGCM, err := cipher.NewGCM(block)
	if err != nil {
		return plaintext
	}

	nonce := make([]byte, aesGCM.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return plaintext
	}

	ciphertext := aesGCM.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(ciphertext) // Base64 für sicheren Transport
}

// --------------------------------------------------

func main() {
	// 0. Kryptografie initialisieren
	initCrypto()

	// 1. Flags definieren (VOR Parse!)
	countPtr := flag.Int("count", 100000, "Total records to inject in this batch")
	modePtr := flag.String("mode", "simple", "Mode: simple | scenario")
	workersPtr := flag.Int("workers", 4, "Number of workers")
	offsetPtr := flag.Int("offset", 0, "Global ID Start Offset (for batching)")

	// 2. Parsen
	flag.Parse()

	totalRows := *countPtr
	workers := *workersPtr
	offset := *offsetPtr
	batchSize := 500 // Rows per INSERT/COPY

	// Config Load (Optional, for baseCap)
	baseCap := 20000.0
	data, err := os.ReadFile("yafad_config.json")
	if err == nil {
		var conf SystemConfig
		if json.Unmarshal(data, &conf) == nil {
			if val, ok := conf.Capacities["table0"]; ok && val > 0 {
				baseCap = float64(val)
			}
		}
	}

	// Work distribution
	rowsPerWorker := int(math.Ceil(float64(totalRows) / float64(workers)))
	batchesPerWorker := int(math.Ceil(float64(rowsPerWorker) / float64(batchSize)))

	// DB Connection
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
		panic(fmt.Sprintf("DB Connection failed: %v", err))
	}
	defer pool.Close()

	fmt.Printf("\n🌊 HYBRID GENERATOR v0.8.2 (Mode: %s)\n", *modePtr)
	fmt.Printf("   Target: %d | Offset: %d | Workers: %d\n", totalRows, offset, workers)
	fmt.Println("   🛡️  Payload Encryption: ACTIVE")

	startTime := time.Now()

	// --- PACEMAKER (Flow Control) ---
	// Nur im 'scenario' mode aktiv, sonst Vollgas
	if *modePtr == "scenario" {
		go func() {
			for {
				// Einfacher Pacemaker: Prüft Füllstand von T0
				var t0Count int
				err := pool.QueryRow(ctx, "SELECT count(*) FROM table0").Scan(&t0Count)
				if err != nil {
					time.Sleep(1 * time.Second)
					continue
				}

				pressure := float64(t0Count) / baseCap

				// Dynamische Drosselung
				newSleep := int64(0)
				if pressure > 0.9 {
					newSleep = 100 // Leicht bremsen
				}
				if pressure > 1.1 {
					newSleep = 1000 // Stark bremsen
				}
				if pressure > 1.5 {
					newSleep = 5000 // Notbremse
				}

				atomic.StoreInt64(&currentSleepMs, newSleep)

				statusIcon := "🟢"
				if pressure > 0.95 {
					statusIcon = "🟠"
				}
				if pressure > 1.1 {
					statusIcon = "🔴"
				}

				// Status Line Update (Overwrites line)
				fmt.Printf("\r%s Pressure: %3.0f%% | T0: %d | Throttle: %dms    ",
					statusIcon, pressure*100, t0Count, newSleep)

				time.Sleep(500 * time.Millisecond)
			}
		}()
	}

	// --- WORKERS ---
	var wg sync.WaitGroup
	wg.Add(workers)

	// Globaler Counter für den Abschlussbericht
	var ops int64 = 0

	for w := 0; w < workers; w++ {
		go func(workerID int) {
			defer wg.Done()

			// Jeder Worker berechnet seinen eigenen ID-Bereich
			workerOffset := offset + (workerID * rowsPerWorker)
			processedInWorker := 0

			for b := 0; b < batchesPerWorker; b++ {
				// 1. Pacemaker Check
				for {
					delay := atomic.LoadInt64(&currentSleepMs)
					if delay > 0 {
						time.Sleep(time.Duration(delay) * time.Millisecond)
					}
					if delay >= 5000 {
						time.Sleep(1 * time.Second)
						continue
					}
					break
				}

				// 2. Batch zusammenbauen
				batchStartID := workerOffset + (b * batchSize)

				// Sicherheit: Nicht mehr generieren als zugeteilt
				currentBatchSize := batchSize
				if processedInWorker+currentBatchSize > rowsPerWorker {
					currentBatchSize = rowsPerWorker - processedInWorker
				}
				if currentBatchSize <= 0 {
					break // Worker ist fertig
				}

				rows := [][]interface{}{}
				for j := 0; j < currentBatchSize; j++ {
					globalID := batchStartID + j
					idString := fmt.Sprintf("user_%d", globalID)

					// JSON Payload erstellen...
					jsonPayload := fmt.Sprintf(`{"type": "synthetic", "batch": %d, "worker": %d}`, b, workerID)

					// ...und DIREKT VERSCHLÜSSELN!
					encryptedPayload := encryptPayload(jsonPayload)

					// FIX: Ein Base64-String muss für jsonb in Anführungszeichen stehen!
					finalPayload := fmt.Sprintf(`"%s"`, encryptedPayload)

					rows = append(rows, []interface{}{
						idString,     // id
						finalPayload, // payload (geschützt & gültig!)
						1.0,          // utility_index
						time.Now(),   // last_activity
					})
				}

				// 3. COPY into DB
				_, errCopy := pool.CopyFrom(
					ctx,
					pgx.Identifier{"table0"},
					[]string{"id", "payload", "utility_index", "last_activity"},
					pgx.CopyFromRows(rows),
				)

				if errCopy == nil {
					atomic.AddInt64(&ops, int64(currentBatchSize))
					processedInWorker += currentBatchSize
				} else {
					time.Sleep(500 * time.Millisecond)
				}
			}
		}(w)
	}

	wg.Wait()

	// Abschluss
	dur := time.Since(startTime)
	rps := float64(atomic.LoadInt64(&ops)) / dur.Seconds()
	fmt.Printf("\n\n🏁 BATCH DONE. Injected: %d | RPS: %.0f\n", atomic.LoadInt64(&ops), rps)
}
