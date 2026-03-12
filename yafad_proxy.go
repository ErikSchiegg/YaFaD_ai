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

const CONFIG_FILE = "shared/yafad_config.json"
const POLICY_FILE = "shared/migration_policy.json"

// Hilfsfunktion zur Erstellung eines optimierten Pools
func createOptimizedPool(ctx context.Context, connStr string) (*pgxpool.Pool, error) {
	config, err := pgxpool.ParseConfig(connStr)
	if err != nil {
		return nil, err
	}

	// PERFORMANCE & PERSISTENZ FIX
	// Verhindert ständiges Neu-Verbinden und DNS-Lookups (pprof peak fix)
	config.MaxConns = 20                       // Maximale parallele Verbindungen
	config.MinConns = 5                        // Mindestens X Verbindungen "warm" halten
	config.MaxConnLifetime = time.Hour         // Maximale Lebensdauer einer Verbindung
	config.MaxConnIdleTime = 30 * time.Minute  // Wie lange darf eine Verbindung ungenutzt bleiben?
	config.HealthCheckPeriod = 1 * time.Minute // Prüft im Hintergrund, ob die Connection noch lebt

	// DNS Fix: Falls "localhost" angegeben wurde, auf IP zwingen
	if config.ConnConfig.Host == "localhost" {
		config.ConnConfig.Host = "127.0.0.1"
	}

	return pgxpool.NewWithConfig(ctx, config)
}

func waitIfPaused() {
	for {
		data, err := os.ReadFile(CONFIG_FILE)
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
	os.MkdirAll("shared", os.ModePerm)
	policyFile, err := os.ReadFile(POLICY_FILE)
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

	// Legacy DB Connection String
	legDB := policy.LegacyDB
	legacyHost := fmt.Sprintf("%v", legDB["host"])
	legConnStr := fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable",
		legDB["user"], legDB["password"], legacyHost, legDB["port"], legDB["dbname"])

	ctx := context.Background()
	legPool, err := createOptimizedPool(ctx, legConnStr)
	if err != nil {
		fmt.Printf("❌ Legacy DB pool initialization failed: %v\n", err)
		return
	}
	defer legPool.Close()

	// YaFaD DB Connection String
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

	yafadConnStr := fmt.Sprintf("postgres://%s:%s@%s:5432/yafad_test?sslmode=disable", yafadUser, yafadPass, yafadHost)
	yafadPool, err := createOptimizedPool(ctx, yafadConnStr)
	if err != nil {
		fmt.Printf("❌ YaFaD DB pool initialization failed: %v\n", err)
		return
	}
	defer yafadPool.Close()

	fmt.Println("==================================================")
	fmt.Printf("🌿 STRANGLER FIG PROXY: ATOMIC PERSISTENT MODE\n")
	fmt.Printf("🎯 Target T0 Cap:  %d | Peak: %d\n", t0Cap, targetPeak)
	fmt.Println("==================================================")

	if policy.FlushOnStart {
		fmt.Println("🧹 Flushing YaFaD tables...")
		flushYaFaDTables(ctx, yafadPool)
	}

	for _, tableName := range policy.YafadWhitelist {
		fmt.Printf("\n🔍 Processing Table: %s\n", tableName)

		for {
			waitIfPaused()

			// RELIABILITY FIX: Stand prüfen mit Context-Timeout (verhindert Hänger am Ende)
			var remainingInLegacy int
			ctxCheck, cancel := context.WithTimeout(ctx, 3*time.Second)
			err = legPool.QueryRow(ctxCheck, fmt.Sprintf("SELECT count(*) FROM %s", tableName)).Scan(&remainingInLegacy)
			cancel()

			if err != nil {
				fmt.Printf("⚠️  Check error (Retrying...): %v\n", err)
				time.Sleep(2 * time.Second)
				continue
			}

			if remainingInLegacy == 0 {
				fmt.Printf("🏁 Table '%s' is verified empty. Mission accomplished.\n", tableName)
				break
			}

			// Warten bis T0 Platz hat
			currentT0 := waitForDigestion(ctx, yafadPool, t0Cap)

			// Batch-Größe berechnen
			batchSize := targetPeak - currentT0
			if batchSize <= 0 {
				batchSize = 1000
			}
			if batchSize > remainingInLegacy {
				batchSize = remainingInLegacy
			}

			fmt.Printf("📐 Space in T0: %d. Fetching next %d records...\n", targetPeak-currentT0, batchSize)

			// ATOMARER MOVE (CTIDs)
			moveQuery := fmt.Sprintf(`
				WITH target_batch AS (
					SELECT ctid, row_to_json(t) as data 
					FROM %s t 
					LIMIT %d
				)
				SELECT ctid, data FROM target_batch;
			`, tableName, batchSize)

			rows, err := legPool.Query(ctx, moveQuery)
			if err != nil {
				fmt.Printf("❌ Read error: %v\n", err)
				break
			}

			var batchData [][]any
			var ctidsToDrop []any

			for rows.Next() {
				var ctid any
				var rowJSON string
				if err := rows.Scan(&ctid, &rowJSON); err == nil {
					payload := strings.TrimSuffix(rowJSON, "}") + `,"_migrated_at":"` + time.Now().Format(time.RFC3339) + `"}`
					batchData = append(batchData, []any{
						uuid.New().String(),
						payload,
						1.0,
						time.Now(),
					})
					ctidsToDrop = append(ctidsToDrop, ctid)
				}
			}
			rows.Close()

			if len(batchData) > 0 {
				if flushBatchToYaFaD(ctx, yafadPool, batchData) {
					_, err := legPool.Exec(ctx, fmt.Sprintf("DELETE FROM %s WHERE ctid = ANY($1)", tableName), ctidsToDrop)
					if err != nil {
						fmt.Printf("⚠️ Delete error: %v\n", err)
					} else {
						fmt.Printf("✅ Pulse: %d records moved.\n", len(batchData))
					}
				}
			}
		}
	}

	fmt.Println("\n🎉 MIGRATION FINISHED. STRANGLER FIG DISENGAGED.")
}

func flushBatchToYaFaD(ctx context.Context, pool *pgxpool.Pool, data [][]any) bool {
	_, err := pool.CopyFrom(
		ctx,
		pgx.Identifier{"table0"},
		[]string{"id", "payload", "utility_index", "last_activity"},
		pgx.CopyFromRows(data),
	)
	if err != nil {
		fmt.Printf("❌ YaFaD Insert Error: %v\n", err)
		return false
	}
	return true
}

func flushYaFaDTables(ctx context.Context, pool *pgxpool.Pool) {
	tables := []string{"table0", "table1", "table2", "table3", "table4", "deep_archive"}
	for _, t := range tables {
		pool.Exec(ctx, fmt.Sprintf("TRUNCATE TABLE %s RESTART IDENTITY CASCADE;", t))
	}
}

func waitForDigestion(ctx context.Context, pool *pgxpool.Pool, t0Cap int) int {
	for {
		var currentT0 int
		err := pool.QueryRow(ctx, "SELECT count(*) FROM table0").Scan(&currentT0)
		if err == nil && currentT0 < t0Cap {
			return currentT0
		}
		fmt.Printf("   ⏳ T0 Full (%d). Waiting for drain...\n", currentT0)
		time.Sleep(2 * time.Second)
	}
}
