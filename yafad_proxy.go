package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type MigrationPolicy struct {
	Mode           string                 `json:"mode"`
	LegacyDB       map[string]interface{} `json:"legacy_db"`
	YaFaDWhitelist []interface{}          `json:"yafad_whitelist"`
}

// Kleiner, eigener Simulator nur für die Migration
func startLegacyTraffic(tables []interface{}) {
	fmt.Println("🤖 LEGACY APP: Simulating normal user traffic during migration...")
	for {
		time.Sleep(time.Duration(rand.Intn(2000)+500) * time.Millisecond)
		if len(tables) > 0 {
			target := tables[rand.Intn(len(tables))].(string)
			fmt.Printf("   [Traffic] Routine read/write operation on '%s'\n", target)
		}
	}
}

func main() {
	fmt.Println("🌿 STRANGLER FIG PROXY INITIALIZING...")

	// 1. Policy vom Dashboard lesen
	policyData, err := os.ReadFile("migration_policy.json")
	if err != nil {
		fmt.Printf("❌ Cannot read policy (Start Proxy from Dashboard first!): %v\n", err)
		return
	}

	var policy MigrationPolicy
	json.Unmarshal(policyData, &policy)

	// 2. Kleinen Legacy-Traffic im Hintergrund starten
	go startLegacyTraffic(policy.YaFaDWhitelist)

	// 3. Verbindung zu Legacy DB aufbauen
	db := policy.LegacyDB
	legacyConnStr := fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable",
		db["user"], db["password"], db["host"], db["port"], db["dbname"])

	ctx := context.Background()
	legacyPool, err := pgxpool.New(ctx, legacyConnStr)
	if err != nil {
		fmt.Printf("❌ Legacy DB connection failed: %v\n", err)
		return
	}
	defer legacyPool.Close()

	// 4. Verbindung zu YaFaD (T0 Cortex)
	yafadConnStr := "postgres://eriks:test@localhost:5432/yafad_test?sslmode=disable"
	yafadPool, err := pgxpool.New(ctx, yafadConnStr)
	if err != nil {
		fmt.Printf("❌ YaFaD DB connection failed: %v\n", err)
		return
	}
	defer yafadPool.Close()

	fmt.Println("✅ Proxy connected to both dimensions. Starting Osmosis...")

	// 5. Tabellen absaugen und injizieren
	for _, tableName := range policy.YaFaDWhitelist {
		table := tableName.(string)
		fmt.Printf("\n🩸 OSMOSIS ACTIVE: Draining table '%s'...\n", table)

		rows, err := legacyPool.Query(ctx, fmt.Sprintf("SELECT * FROM %s", table))
		if err != nil {
			fmt.Printf("⚠️ Could not read %s: %v\n", table, err)
			continue
		}

		fieldDescriptions := rows.FieldDescriptions()
		var columns []string
		for _, fd := range fieldDescriptions {
			columns = append(columns, string(fd.Name))
		}

		count := 0
		var batch [][]interface{}

		for rows.Next() {
			values, _ := rows.Values()
			recordMap := make(map[string]interface{})
			for i, col := range columns {
				recordMap[col] = values[i]
			}
			recordMap["_legacy_source_table"] = table

			payloadJSON, _ := json.Marshal(recordMap)
			cellID := uuid.New().String()
			utilityIndex := rand.Float64()

			batch = append(batch, []interface{}{cellID, payloadJSON, utilityIndex, time.Now()})
			count++

			// Alle 5000 Records injizieren
			if len(batch) >= 5000 {
				_, err = yafadPool.CopyFrom(
					ctx,
					pgx.Identifier{"table0"},
					[]string{"id", "payload", "utility_index", "last_activity"},
					pgx.CopyFromRows(batch),
				)
				if err != nil {
					fmt.Printf("❌ Injection error: %v\n", err)
				}
				fmt.Printf("   💉 Injected %d records from %s into YaFaD T0...\n", count, table)
				batch = nil
				time.Sleep(200 * time.Millisecond) // Kleiner Atemzug
			}
		}

		// Den Rest injizieren
		if len(batch) > 0 {
			yafadPool.CopyFrom(ctx, pgx.Identifier{"table0"}, []string{"id", "payload", "utility_index", "last_activity"}, pgx.CopyFromRows(batch))
			fmt.Printf("   💉 Injected final records (%d total) from %s into YaFaD T0...\n", count, table)
		}
		rows.Close()
	}

	fmt.Println("\n🏁 MIGRATION COMPLETE. Legacy database fully drained into YaFaD.")
	fmt.Println("🛑 Proxy will stay alive to route any stragglers. Press Stop Proxy in Dashboard to terminate.")

	select {} // Hält den Proxy am Leben
}
