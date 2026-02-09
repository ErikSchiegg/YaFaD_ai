package main

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	// 1. DB Verbindung
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

	fmt.Println("🚜 SEEDING LEGACY DATABASE...")
	fmt.Println("-----------------------------")

	// 2. Tabellen erstellen & Füllen
	createAndFill(ctx, pool, "legacy_users", 15000, `
		CREATE TABLE legacy_users (
			id SERIAL PRIMARY KEY,
			username TEXT,
			email TEXT,
			created_at TIMESTAMP,
			last_login TIMESTAMP,
			preferences JSONB
		);
	`)

	createAndFill(ctx, pool, "legacy_session_cache", 50000, `
		CREATE TABLE legacy_session_cache (
			session_id UUID DEFAULT gen_random_uuid(),
			user_id INT,
			blob_data TEXT,
			expires_at TIMESTAMP
		);
	`)

	// Die große Tabelle am Schluss
	createAndFill(ctx, pool, "legacy_audit_logs", 1000000, `
		CREATE TABLE legacy_audit_logs (
			id SERIAL PRIMARY KEY,
			action TEXT,
			details JSONB,
			timestamp TIMESTAMP DEFAULT NOW()
		);
	`)

	fmt.Println("\n✅ Legacy DB populated. Ready for migration testing!")
}

func createAndFill(ctx context.Context, pool *pgxpool.Pool, tableName string, rows int, schema string) {
	fmt.Printf("\n🔨 Creating table '%s'...", tableName)

	// Drop & Create
	_, err := pool.Exec(ctx, fmt.Sprintf("DROP TABLE IF EXISTS %s", tableName))
	if err != nil {
		panic(err)
	}
	_, err = pool.Exec(ctx, schema)
	if err != nil {
		panic(err)
	}

	fmt.Printf(" Filling with %d rows... ", rows)

	// Batch Insert (schneller als Row-by-Row)
	batchSize := 5000
	totalInserted := 0

	for totalInserted < rows {
		// Wir nutzen COPY für maximale Geschwindigkeit
		// Dies ist eine vereinfachte Simulation. In echt würden wir CopyFrom nutzen,
		// aber hier generieren wir SQL Inserts für Einfachheit.

		var values []string
		currentBatch := 0
		for i := 0; i < batchSize && totalInserted < rows; i++ {

			// Generiere Dummy Daten basierend auf Tabellen-Typ
			var rowVal string
			if tableName == "legacy_users" {
				rowVal = fmt.Sprintf("('user_%d', 'user_%d@example.com', NOW(), NOW(), '{\"theme\": \"dark\"}')", totalInserted, totalInserted)
			} else if tableName == "legacy_session_cache" {
				rowVal = fmt.Sprintf("(gen_random_uuid(), %d, 'junk_data_%d', NOW())", rand.Intn(1000), rand.Intn(9999))
			} else { // Logs
				rowVal = fmt.Sprintf("('LOGIN_ATTEMPT', '{\"ip\": \"192.168.1.%d\"}', NOW())", rand.Intn(255))
			}

			values = append(values, rowVal)
			currentBatch++
			totalInserted++
		}

		if len(values) > 0 {
			query := fmt.Sprintf("INSERT INTO %s VALUES %s", tableName, strings.Join(values, ","))
			// Bei INSERT INTO ... VALUES (...) müssen wir die Spaltennamen weglassen oder matchen.
			// Einfacher Trick: Wir haben das Schema oben so definiert, dass die Reihenfolge passt.
			// Aber SERIAL ID stört bei legacy_users/logs.

			// Korrektur für Tabellen mit SERIAL: Wir müssen Spalten angeben oder DEFAULT nutzen.
			// Da das hier kompliziert wird im generischen Code, nutzen wir pgx CopyFrom für Speed & Einfachheit?
			// Nein, wir fixen den Query String oben im Loop für spezifische Tabellen.

			if tableName == "legacy_users" {
				query = fmt.Sprintf("INSERT INTO %s (username, email, created_at, last_login, preferences) VALUES %s", tableName, strings.Join(values, ","))
			} else if tableName == "legacy_audit_logs" {
				query = fmt.Sprintf("INSERT INTO %s (action, details, timestamp) VALUES %s", tableName, strings.Join(values, ","))
			} else {
				query = fmt.Sprintf("INSERT INTO %s (session_id, user_id, blob_data, expires_at) VALUES %s", tableName, strings.Join(values, ","))
			}

			_, err := pool.Exec(ctx, query)
			if err != nil {
				fmt.Printf("Insert Error: %v\n", err)
				return
			}
			fmt.Print(".")
		}
	}
	fmt.Println(" Done.")
}
