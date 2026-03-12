package main

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	rand.Seed(time.Now().UnixNano())

	// 1. Parameter auslesen (Standard: 100.000)
	totalRecords := 100000
	if len(os.Args) > 1 {
		if parsed, err := strconv.Atoi(os.Args[1]); err == nil && parsed > 0 {
			totalRecords = parsed
		}
	}

	// 2. Dynamische Credentials generieren
	randID := rand.Intn(9000) + 1000 // z.B. 7967
	newDBName := fmt.Sprintf("legacy_crm_%d", randID)
	newDBUser := fmt.Sprintf("legacy_user_%d", randID)
	newDBPass := fmt.Sprintf("pass_%d_secure", randID)

	// 3. Mit der Haupt-Datenbank (postgres) verbinden, um den neuen User und die DB zu erstellen
	adminUser := os.Getenv("DB_USER")
	if adminUser == "" {
		adminUser = "eriks"
	}
	adminPass := os.Getenv("DB_PASSWORD")
	if adminPass == "" {
		adminPass = "test"
	}

	adminConnStr := fmt.Sprintf("postgres://%s:%s@localhost:5432/postgres?sslmode=disable", adminUser, adminPass)
	ctx := context.Background()
	adminPool, err := pgxpool.New(ctx, adminConnStr)
	if err != nil {
		panic(fmt.Sprintf("Konnte nicht als Admin verbinden: %v", err))
	}

	fmt.Printf("🏗️  GENERATING ISOLATED LEGACY ENVIRONMENT...\n")

	// User erstellen
	_, _ = adminPool.Exec(ctx, fmt.Sprintf("DROP ROLE IF EXISTS %s", newDBUser))
	_, err = adminPool.Exec(ctx, fmt.Sprintf("CREATE ROLE %s WITH LOGIN PASSWORD '%s'", newDBUser, newDBPass))
	if err != nil {
		fmt.Printf("Warnung bei User-Erstellung (evtl. existiert er schon): %v\n", err)
	}

	// DB erstellen
	_, _ = adminPool.Exec(ctx, fmt.Sprintf("DROP DATABASE IF EXISTS %s", newDBName))
	_, err = adminPool.Exec(ctx, fmt.Sprintf("CREATE DATABASE %s OWNER %s", newDBName, newDBUser))
	if err != nil {
		fmt.Printf("Warnung bei DB-Erstellung: %v\n", err)
	}

	adminPool.Close()

	// 4. Mit der NEUEN Legacy-Datenbank verbinden
	time.Sleep(1 * time.Second) // Kurz warten, bis Postgres die DB registriert hat
	legacyConnStr := fmt.Sprintf("postgres://%s:%s@localhost:5432/%s?sslmode=disable", newDBUser, newDBPass, newDBName)
	legacyPool, err := pgxpool.New(ctx, legacyConnStr)
	if err != nil {
		panic(fmt.Sprintf("Konnte nicht mit neuer Legacy-DB verbinden: %v", err))
	}
	defer legacyPool.Close()

	// 5. Verteilung berechnen
	usersCount := int(float64(totalRecords) * 0.02)
	sessionsCount := int(float64(totalRecords) * 0.08)
	logsCount := totalRecords - usersCount - sessionsCount

	fmt.Printf("🚜 SEEDING %d RECORDS INTO '%s'...\n", totalRecords, newDBName)
	start := time.Now()

	createAndFill(ctx, legacyPool, "customers", usersCount, `
        CREATE TABLE customers (
            id SERIAL PRIMARY KEY,
            username TEXT,
            email TEXT,
            created_at TIMESTAMP,
            last_login TIMESTAMP,
            preferences JSONB
        );
    `)

	createAndFill(ctx, legacyPool, "orders", sessionsCount, `
        CREATE TABLE orders (
            session_id UUID DEFAULT gen_random_uuid(),
            user_id INT,
            blob_data TEXT,
            expires_at TIMESTAMP
        );
    `)

	createAndFill(ctx, legacyPool, "system_logs", logsCount, `
        CREATE TABLE system_logs (
            id SERIAL PRIMARY KEY,
            action TEXT,
            details JSONB,
            timestamp TIMESTAMP DEFAULT NOW()
        );
    `)

	// 6. Credentials Summary erstellen
	summary := fmt.Sprintf(`Legacy Database Credentials
===========================
Host:  localhost
Port:  5432
User:  %s
Pass:  %s
DB:    %s
Records: %d
===========================
Generated at: %s
`, newDBUser, newDBPass, newDBName, totalRecords, time.Now().Format(time.RFC1123))

	// ---> FIX: Hier setzen wir "\n\n" davor, damit immer sauber getrennt wird! <---
	f, err := os.OpenFile("legacy_credentials.txt", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err == nil {
		defer f.Close()
		f.WriteString("\n\n" + summary)
	} else {
		fmt.Printf("Fehler beim Schreiben der Datei: %v\n", err)
	}

	fmt.Printf("\n✅ Legacy Environment successfully built!\n")
	fmt.Printf("⏱️  Time taken: %v\n\n", time.Since(start))
	fmt.Println(summary)
	fmt.Println("📝 Credentials appended to 'legacy_credentials.txt'.")
	fmt.Println("🚀 Ready to strangle it with YaFaD!")
}

func createAndFill(ctx context.Context, pool *pgxpool.Pool, tableName string, rows int, schema string) {
	if rows <= 0 {
		return
	}
	fmt.Printf("🔨 Creating table '%s'... ", tableName)

	// Tabelle zurücksetzen
	_, _ = pool.Exec(ctx, fmt.Sprintf("DROP TABLE IF EXISTS %s", tableName))
	_, err := pool.Exec(ctx, schema)
	if err != nil {
		panic(err)
	}

	fmt.Printf("Bulk inserting %d rows... ", rows)

	// Vorbereiten der Spalten und Daten-Generator
	var columns []string
	var rowData [][]any

	switch tableName {
	case "customers":
		columns = []string{"username", "email", "created_at", "last_login", "preferences"}
		for i := 0; i < rows; i++ {
			rowData = append(rowData, []any{
				fmt.Sprintf("user_%d", i),
				fmt.Sprintf("user_%d@example.com", i),
				time.Now(),
				time.Now(),
				map[string]string{"theme": "dark"},
			})
		}
	case "orders":
		columns = []string{"user_id", "blob_data", "expires_at"} // UUID wird durch DEFAULT generiert
		for i := 0; i < rows; i++ {
			rowData = append(rowData, []any{rand.Intn(1000), fmt.Sprintf("junk_%d", i), time.Now()})
		}
	case "system_logs":
		columns = []string{"action", "details", "timestamp"}
		for i := 0; i < rows; i++ {
			rowData = append(rowData, []any{"LOGIN_ATTEMPT", map[string]string{"ip": "127.0.0.1"}, time.Now()})
		}
	}

	// Der "CopyFrom" Zauber
	copyCount, err := pool.CopyFrom(
		ctx,
		pgx.Identifier{tableName},
		columns,
		pgx.CopyFromRows(rowData),
	)

	if err != nil {
		fmt.Printf("❌ Bulk Insert failed: %v\n", err)
	} else {
		fmt.Printf("Done (%d rows).\n", copyCount)
	}
}
