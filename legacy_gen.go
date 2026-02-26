package main

import (
	"context"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	firstNames = []string{"Emma", "Noah", "Liam", "Olivia", "William", "Ava", "James", "Isabella", "Oliver", "Sophia", "Lucas", "Mia", "Benjamin", "Amelia", "Elijah", "Harper"}
	lastNames  = []string{"Smith", "Johnson", "Williams", "Brown", "Jones", "Garcia", "Miller", "Davis", "Rodriguez", "Martinez", "Hernandez", "Lopez", "Gonzalez", "Wilson"}
	domains    = []string{"gmail.com", "yahoo.com", "hotmail.com", "company.org", "startup.io", "tech.net"}
	statuses   = []string{"PENDING", "SHIPPED", "DELIVERED", "CANCELLED", "REFUNDED"}
	events     = []string{"USER_LOGIN", "DATA_EXPORT", "PASSWORD_RESET", "PURCHASE_FAILED", "SYSTEM_REBOOT"}
)

func randomString(options []string) string {
	return options[rand.Intn(len(options))]
}

func main() {
	// Parameter
	adminUser := flag.String("admin", "eriks", "Postgres Admin User")
	adminPass := flag.String("adminpw", "test", "Postgres Admin Password")
	records := flag.Int("records", 100000, "Total records to generate (split across tables)")
	flag.Parse()

	rand.Seed(time.Now().UnixNano())
	ctx := context.Background()

	// Zufällige Legacy-Identität generieren
	suffix := rand.Intn(9000) + 1000
	dbName := fmt.Sprintf("legacy_crm_%d", suffix)
	dbUser := fmt.Sprintf("legacy_user_%d", suffix)
	dbPass := fmt.Sprintf("pass_%d_secure", rand.Intn(999999))

	fmt.Println("🏗️  GENERATING REALISTIC LEGACY DATABASE...")
	fmt.Printf("   -> Target Size: %d Records\n", *records)

	// 1. Verbinden als Admin (zur 'postgres' Standard-DB), um User und DB anzulegen
	adminConnStr := fmt.Sprintf("postgres://%s:%s@localhost:5432/postgres?sslmode=disable", *adminUser, *adminPass)
	adminConn, err := pgx.Connect(ctx, adminConnStr)
	if err != nil {
		fmt.Printf("❌ Failed to connect as admin: %v\n", err)
		os.Exit(1)
	}
	defer adminConn.Close(ctx)

	// ---> NEU: Vollautomatischer Self-Healing-Fix für Linux glibc Updates! <---
	fmt.Println("   -> Healing PostgreSQL Template Collations (if necessary)...")
	_, _ = adminConn.Exec(ctx, "ALTER DATABASE template1 REFRESH COLLATION VERSION;")
	_, _ = adminConn.Exec(ctx, "ALTER DATABASE postgres REFRESH COLLATION VERSION;")

	fmt.Printf("   -> Creating User: %s\n", dbUser)
	_, err = adminConn.Exec(ctx, fmt.Sprintf("CREATE ROLE %s WITH LOGIN PASSWORD '%s'", dbUser, dbPass))
	if err != nil {
		fmt.Printf("⚠️ Role might exist or error: %v\n", err)
	}

	fmt.Printf("   -> Creating Database: %s\n", dbName)
	// Wieder ganz normales CREATE DATABASE (ohne C-Collation Hack)
	_, err = adminConn.Exec(ctx, fmt.Sprintf("CREATE DATABASE %s OWNER %s", dbName, dbUser))
	if err != nil {
		fmt.Printf("❌ Failed to create DB: %v\n", err)
		os.Exit(1)
	}
	adminConn.Close(ctx)

	// 2. Verbinden mit der neuen Legacy DB als neuer User
	time.Sleep(1 * time.Second) // Kurz warten, bis DB bereit ist
	legacyConnStr := fmt.Sprintf("postgres://%s:%s@localhost:5432/%s?sslmode=disable", dbUser, dbPass, dbName)
	pool, err := pgxpool.New(ctx, legacyConnStr)
	if err != nil {
		fmt.Printf("❌ Failed to connect to new DB: %v\n", err)
		os.Exit(1)
	}
	defer pool.Close()

	// 3. Tabellen anlegen
	fmt.Println("   -> Building legacy schema (customers, orders, system_logs)...")
	schemas := []string{
		"CREATE TABLE customers (id SERIAL PRIMARY KEY, full_name VARCHAR(100), email VARCHAR(100), signup_date TIMESTAMP)",
		"CREATE TABLE orders (id SERIAL PRIMARY KEY, customer_id INT, amount DECIMAL(10,2), status VARCHAR(20), order_date TIMESTAMP)",
		"CREATE TABLE system_logs (id SERIAL PRIMARY KEY, event_type VARCHAR(50), description TEXT, log_time TIMESTAMP)",
	}
	for _, q := range schemas {
		_, err := pool.Exec(ctx, q)
		if err != nil {
			fmt.Printf("❌ Failed to create table: %v\n", err)
			os.Exit(1)
		}
	}

	// 4. Daten generieren (Drittel-Aufteilung)
	limitPerTable := *records / 3
	fmt.Printf("🔥 Injecting %d records into each table...\n", limitPerTable)

	var custData [][]interface{}
	var ordData [][]interface{}
	var logData [][]interface{}

	now := time.Now()
	for i := 0; i < limitPerTable; i++ {
		// Customer
		name := randomString(firstNames) + " " + randomString(lastNames)
		email := fmt.Sprintf("%s.%d@%s", randomString(firstNames), rand.Intn(999), randomString(domains))
		cTime := now.Add(-time.Duration(rand.Intn(10000)) * time.Hour)
		custData = append(custData, []interface{}{name, email, cTime})

		// Order
		cID := rand.Intn(10000) + 1
		amt := float64(rand.Intn(100000)) / 100.0
		stat := randomString(statuses)
		oTime := now.Add(-time.Duration(rand.Intn(5000)) * time.Hour)
		ordData = append(ordData, []interface{}{cID, amt, stat, oTime})

		// Log
		evt := randomString(events)
		desc := fmt.Sprintf("Legacy system generated event code %d during routine operation.", rand.Intn(9999))
		lTime := now.Add(-time.Duration(rand.Intn(1000)) * time.Hour)
		logData = append(logData, []interface{}{evt, desc, lTime})
	}

	// High-Speed CopyFrom in Postgres
	_, _ = pool.CopyFrom(ctx, pgx.Identifier{"customers"}, []string{"full_name", "email", "signup_date"}, pgx.CopyFromRows(custData))
	_, _ = pool.CopyFrom(ctx, pgx.Identifier{"orders"}, []string{"customer_id", "amount", "status", "order_date"}, pgx.CopyFromRows(ordData))
	_, _ = pool.CopyFrom(ctx, pgx.Identifier{"system_logs"}, []string{"event_type", "description", "log_time"}, pgx.CopyFromRows(logData))

	// ---> NEU: Credentials in eine Datei schreiben <---
	credText := fmt.Sprintf("Legacy Database Credentials\n===========================\nHost:  localhost\nPort:  5432\nUser:  %s\nPass:  %s\nDB:    %s\n===========================\nGenerated at: %s\n",
		dbUser, dbPass, dbName, time.Now().Format(time.RFC1123))

	err = os.WriteFile("legacy_credentials.txt", []byte(credText), 0644)

	fmt.Println("\n✅ DATABASE SUCCESSFULLY GENERATED!")
	fmt.Println("=====================================================")
	fmt.Println("📋 COPY THESE CREDENTIALS TO THE DASHBOARD (MIGRATION):")
	fmt.Println("=====================================================")
	fmt.Printf("   Host:  localhost\n")
	fmt.Printf("   Port:  5432\n")
	fmt.Printf("   User:  %s\n", dbUser)
	fmt.Printf("   Pass:  %s\n", dbPass)
	fmt.Printf("   DB:    %s\n", dbName)
	fmt.Println("=====================================================")

	if err == nil {
		fmt.Println("💾 Saved securely to 'legacy_credentials.txt'")
	} else {
		fmt.Printf("⚠️ Could not save to file: %v\n", err)
	}

	fmt.Println("💡 Tip: Click 'Scan DB' in the dashboard, select all tables, and start the Proxy!")
}
