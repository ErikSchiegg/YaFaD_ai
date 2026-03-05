package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type MigrationPolicy struct {
	Mode                string                 `json:"mode"`
	LegacyDB            map[string]interface{} `json:"legacy_db"`
	YafadWhitelist      []string               `json:"yafad_whitelist"`
	T0Cap               int                    `json:"t0_cap"`
	FlushOnStart        bool                   `json:"flush_on_start"`
	TruncateLegacyAfter bool                   `json:"truncate_legacy_after"`
}

// ---> DOCKER ANPASSUNG: Pfade in den shared/ Ordner umgeleitet <---
const CONFIG_FILE = "shared/yafad_config.json"
const POLICY_FILE = "shared/migration_policy.json"

// Checkt dynamisch yafad_config.json auf Pause-Zustand
func waitIfPaused() {
	for {
		data, err := os.ReadFile(CONFIG_FILE) // Angepasst
		if err == nil {
			var conf map[string]interface{}
			if err := json.Unmarshal(data, &conf); err == nil {
				if state, ok := conf["run_state"].(string); ok {
					if state == "PAUSED" {
						fmt.Println("⏸️ Proxy paused by Mission Control... Holding position.")
						time.Sleep(3 * time.Second)
						continue
					}
				}
			}
		}
		break
	}
}

func main() {
	policyFile, err := os.ReadFile(POLICY_FILE) // Angepasst
	if err != nil {
		fmt.Println("❌ Error reading migration_policy.json:", err)
		return
	}
	var policy MigrationPolicy
	json.Unmarshal(policyFile, &policy)

	t0Cap := policy.T0Cap
	if t0Cap <= 0 {
		t0Cap = 100000
	}

	targetPeak := int(float64(t0Cap) * 1.5)

	legDB := policy.LegacyDB
	legConnStr := fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable",
		legDB["user"], legDB["password"], legDB["host"], legDB["port"], legDB["dbname"])

	ctx := context.Background()
	legPool, err := pgxpool.New(ctx, legConnStr)
	if err != nil {
		fmt.Printf("❌ Legacy DB connection failed at %s:%s - %v\n", legDB["host"], legDB["port"], err)
		return
	}
	defer legPool.Close()

	// ---> DOCKER ANPASSUNG: DB_HOST dynamisch auslesen für YaFaD DB <---
	yafadUser := os.Getenv("DB_USER")
	if yafadUser == "" {
		yafadUser = "eriks"
	}
	yafadPass := os.Getenv("DB_PASSWORD")
	if yafadPass == "" {
		yafadPass = "test"
	}
	yafadHost := os.Getenv("DB_HOST")
	if yafadHost == "" {
		yafadHost = "localhost"
	}

	yafadConnStr := fmt.Sprintf("postgres://%s:%s@%s:5432/yafad_test?sslmode=disable", yafadUser, yafadPass, yafadHost) // Angepasst

	yafadPool, err := pgxpool.New(ctx, yafadConnStr)
	if err != nil {
		fmt.Printf("❌ YaFaD DB connection failed at %s - %v\n", yafadHost, err)
		return
	}
	defer yafadPool.Close()

	fmt.Println("==================================================")
	fmt.Printf("🌿 STRANGLER FIG PROXY ACTIVATED (SAFE READ MODE)\n")
	fmt.Printf("🎯 Target T0 Cap:  %d Records\n", t0Cap)
	fmt.Printf("⛰️  Target Peak:   %d Records (Strict 150%% Sawtooth)\n", targetPeak)
	if policy.TruncateLegacyAfter {
		fmt.Println("🔥 DESTRUCTIVE MODE ENABLED: Legacy tables will be TRUNCATED after migration!")
	}
	fmt.Println("==================================================")

	if policy.FlushOnStart {
		fmt.Println("🧹 User requested flush. Wiping YaFaD database for a clean start...")
		flushYaFaDTables(ctx, yafadPool)
	} else {
		fmt.Println("⏩ Skipping database flush (Resume/Append mode).")
	}

	for _, tableName := range policy.YafadWhitelist {
		fmt.Printf("\n🔍 Starting safe Osmosis for table: %s\n", tableName)

		var originalRecords int
		err = legPool.QueryRow(ctx, fmt.Sprintf("SELECT count(*) FROM %s", tableName)).Scan(&originalRecords)
		if err != nil {
			fmt.Printf("❌ Failed to count rows in table %s: %v\n", tableName, err)
			continue
		}

		fmt.Printf("📊 Table '%s' has %d records. Initiating dynamic extraction...\n", tableName, originalRecords)

		offset := 0
		totalMigrated := 0

		for offset < originalRecords {
			waitIfPaused()

			// 1. Warten, bis T0 verdaut hat
			currentT0 := waitForDigestion(ctx, yafadPool, t0Cap)

			// 2. Kalkulieren, wie viel für exakt 150% noch fehlt
			recordsNeeded := targetPeak - currentT0
			if recordsNeeded <= 0 {
				recordsNeeded = 5000
			}

			// Sicherstellen, dass wir am Ende nicht über das Ziel hinausschießen
			if offset+recordsNeeded > originalRecords {
				recordsNeeded = originalRecords - offset
			}

			fmt.Printf("📐 Dynamics: T0 at %d. Fetching %d records to hit %d peak...\n", currentT0, recordsNeeded, targetPeak)
			waitIfPaused()

			// 3. SICHERER READ
			query := fmt.Sprintf("SELECT row_to_json(t) FROM %s t LIMIT %d OFFSET %d", tableName, recordsNeeded, offset)

			rows, err := legPool.Query(ctx, query)
			if err != nil {
				fmt.Printf("❌ Chunk fetch error for table %s: %v\n", tableName, err)
				break
			}

			var batchData [][]any
			chunkCount := 0

			for rows.Next() {
				var rowJSON string
				if err := rows.Scan(&rowJSON); err == nil {
					payload := strings.TrimSuffix(rowJSON, "}") + `,"_legacy_source":"` + tableName + `"}`
					batchData = append(batchData, []any{
						uuid.New().String(),
						payload,
						1.0,
						time.Now(),
					})
					chunkCount++
					totalMigrated++
				}

				if len(batchData) >= 5000 {
					flushBatchToYaFaD(ctx, yafadPool, batchData)
					batchData = nil
				}
			}
			rows.Close()

			if len(batchData) > 0 {
				flushBatchToYaFaD(ctx, yafadPool, batchData)
			}

			offset += recordsNeeded

			fmt.Printf("💉 Pulse injected! (%d records). Migrated %s: %d / %d\n", chunkCount, tableName, totalMigrated, originalRecords)
		}

		fmt.Printf("✅ Table '%s' completely migrated! (Total extracted: %d)\n", tableName, totalMigrated)

		// 4. NEU: Die Legacy Tabelle NACH erfolgreicher Migration vernichten (falls Checkbox aktiv)
		if policy.TruncateLegacyAfter {
			fmt.Printf("🔥 Nuking legacy table '%s' to free up disk space...\n", tableName)
			_, err := legPool.Exec(ctx, fmt.Sprintf("TRUNCATE TABLE %s CASCADE;", tableName))
			if err != nil {
				fmt.Printf("⚠️ Warning: Failed to truncate legacy table %s: %v\n", tableName, err)
			} else {
				fmt.Printf("💥 Legacy table '%s' successfully destroyed!\n", tableName)
			}
		}
	}

	fmt.Println("\n🎉 ALL TABLES MIGRATED SUCCESSFULLY. STRANGLER FIG DISENGAGED.")
}

func flushYaFaDTables(ctx context.Context, pool *pgxpool.Pool) {
	tables := []string{"table0", "table1", "table2", "table3", "table4", "deep_archive"}
	for _, t := range tables {
		_, err := pool.Exec(ctx, fmt.Sprintf("TRUNCATE TABLE %s RESTART IDENTITY CASCADE;", t))
		if err != nil {
			fmt.Printf("⚠️ Could not flush table %s: %v\n", t, err)
		}
	}
	fmt.Println("✅ YaFaD Database is pristine.")
}

func flushBatchToYaFaD(ctx context.Context, pool *pgxpool.Pool, data [][]any) {
	if len(data) == 0 {
		return
	}
	_, err := pool.CopyFrom(
		ctx,
		pgx.Identifier{"table0"},
		[]string{"id", "payload", "utility_index", "last_activity"},
		pgx.CopyFromRows(data),
	)
	if err != nil {
		fmt.Printf("⚠️ Batch insert error: %v\n", err)
	}
}

func waitForDigestion(ctx context.Context, pool *pgxpool.Pool, t0Cap int) int {
	for {
		waitIfPaused()

		var currentT0 int
		err := pool.QueryRow(ctx, "SELECT count(*) FROM table0").Scan(&currentT0)
		if err != nil {
			time.Sleep(1 * time.Second)
			continue
		}

		if currentT0 <= t0Cap {
			fmt.Printf("🟢 Digestion Complete (T0 at %d / %d). Ready for next bite!\n", currentT0, t0Cap)
			return currentT0
		}

		fmt.Printf("   ⏳ Digesting... T0 Pressure: %d (Waiting to drop to <= %d)\n", currentT0, t0Cap)
		time.Sleep(2 * time.Second)
	}
}
