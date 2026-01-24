package main

import (
	"context"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5"
)

func main() {
	// Verbindungsinformationen (Pass dein Passwort 'deinpw' an)
	connStr := "postgres://eriks:deinpw@localhost:5432/yafad_test"
	ctx := context.Background()

	conn, err := pgx.Connect(ctx, connStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Verbindung fehlgeschlagen: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close(ctx)

	fmt.Println("🐘 Verbunden mit PostgreSQL. Erstelle Kaskaden-Tabellen...")

	// Wir erstellen 5 Tabellen (0 bis 4)
	for i := 0; i < 5; i++ {
		tableName := fmt.Sprintf("table%d", i)

		// SQL-Query für das bio-inspirierte Schema
		query := fmt.Sprintf(`
			CREATE TABLE IF NOT EXISTS %s (
				id VARCHAR(255) PRIMARY KEY,
				payload JSONB,
				utility_index DOUBLE PRECISION DEFAULT 1.0,
				last_activity TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
				created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
			);
			CREATE INDEX IF NOT EXISTS idx_%s_utility ON %s (utility_index DESC);
		`, tableName, tableName, tableName)

		_, err := conn.Exec(ctx, query)
		if err != nil {
			fmt.Printf("❌ Fehler beim Erstellen von %s: %v\n", tableName, err)
		} else {
			fmt.Printf("✅ %s erfolgreich angelegt (inkl. Utility-Index).\n", tableName)
		}
	}

	fmt.Println("\n🚀 Infrastruktur bereit für YaFaD_ai!")
}
