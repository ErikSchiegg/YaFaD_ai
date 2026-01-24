package main

/*
#cgo LDFLAGS: -L./core/target/release -lyafad_core -lm -ldl
extern double calculate_decay(double u_last, double lambda, double delta_t);
*/
import "C"
import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

func main() {
	// 1. Verbindung zur DB (Passwort 'test' & sslmode deaktiviert)
	connStr := "postgres://eriks:test@localhost:5432/yafad_test?sslmode=disable"
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, connStr)
	if err != nil {
		fmt.Printf("❌ DB-Fehler: %v\n", err)
		return
	}
	defer conn.Close(ctx)

	fmt.Println("--- 🧬 YaFaD Migration Engine: Live Test ---")

	// 2. Wir legen einen "heißen" Datensatz in Table0 an
	recordID := "ai_model_weights_v1"
	initialUtility := 1.0 // 100% Relevanz beim Start
	payload := `{"layer": "attention", "params": 512, "status": "active"}`

	_, err = conn.Exec(ctx, "INSERT INTO table0 (id, payload, utility_index) VALUES ($1, $2, $3) ON CONFLICT (id) DO UPDATE SET utility_index = $3",
		recordID, payload, initialUtility)
	if err != nil {
		fmt.Printf("❌ Fehler beim Insert: %v\n", err)
		return
	}
	fmt.Printf("✅ Datensatz '%s' in table0 (Hot) angelegt.\n", recordID)

	// 3. Simulation: Zeit vergeht (48 Stunden)
	deltaT := 48.0
	lambda := 0.05 // Unser Administrator-Faktor

	// Rust berechnet den neuen Index
	uNow := float64(C.calculate_decay(C.double(initialUtility), C.double(lambda), C.double(deltaT)))
	fmt.Printf("🕒 48 Stunden später... Neuer Utility-Index: %.4f\n", uNow)

	// 4. MIGRATION: Das Herzstück der Bio-Middleware
	if uNow < 0.5 {
		fmt.Println("📉 Relevanz unter Schwellenwert (0.5). Starte Kaskadierung...")

		// Wir verschieben die Daten atomar (Transaktion)
		tx, _ := conn.Begin(ctx)

		// Aus Table0 löschen
		tx.Exec(ctx, "DELETE FROM table0 WHERE id = $1", recordID)

		// In Table1 (nächste Ebene) einfügen
		tx.Exec(ctx, "INSERT INTO table1 (id, payload, utility_index) VALUES ($1, $2, $3)",
			recordID, payload, uNow)

		err = tx.Commit(ctx)
		if err != nil {
			fmt.Printf("❌ Migration fehlgeschlagen: %v\n", err)
		} else {
			fmt.Println("🚚 Datensatz erfolgreich von table0 -> table1 verschoben.")
		}
	} else {
		fmt.Println("🔥 Daten sind noch relevant. Bleiben in table0.")
	}
}
