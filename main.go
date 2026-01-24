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
	// 1. Database connection (pwd: 'test' & sslmode disabled)
	connStr := "postgres://eriks:test@localhost:5432/yafad_test?sslmode=disable"
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, connStr)
	if err != nil {
		fmt.Printf("❌ DB connection error: %v\n", err)
		return
	}
	defer conn.Close(ctx)

	fmt.Println("--- 🧬 YaFaD Migration Engine: Live Test ---")

	// 2. Initializing record in T0 (Hot Tier)
	recordID := "ai_model_weights_v1"
	initialUtility := 1.0 // 100% relevance at startup
	payload := `{"layer": "attention", "params": 512, "status": "active"}`

	_, err = conn.Exec(ctx, "INSERT INTO table0 (id, payload, utility_index) VALUES ($1, $2, $3) ON CONFLICT (id) DO UPDATE SET utility_index = $3",
		recordID, payload, initialUtility)
	if err != nil {
		fmt.Printf("❌ INSERT error: %v\n", err)
		return
	}
	fmt.Printf("✅ Record '%s' successfully created in T0 (Hot Tier).\n", recordID)

	// 3. Simulation: 48h time interval
	deltaT := 48.0
	lambda := 0.05 // Administrator decay factor

	// Rust calculates the new Index
	uNow := float64(C.calculate_decay(C.double(initialUtility), C.double(lambda), C.double(deltaT)))
	fmt.Printf("🕒 48 hours later... New Utility Index: %.4f\n", uNow)

	// 4. MIGRATION: Bio-inspired middleware logic
	if uNow < 0.5 {
		fmt.Println("📉 Relevance below threshold (0.5). Starting cascade migration...")

		// Moving data atomically (transaction)
		tx, _ := conn.Begin(ctx)

		// Delete from T0
		tx.Exec(ctx, "DELETE FROM table0 WHERE id = $1", recordID)

		// Relocate to T1 (Warm Tier)
		tx.Exec(ctx, "INSERT INTO table1 (id, payload, utility_index) VALUES ($1, $2, $3)",
			recordID, payload, uNow)

		err = tx.Commit(ctx)
		if err != nil {
			fmt.Printf("❌ Migration failed: %v\n", err)
		} else {
			fmt.Println("🚚 Successfully moved record from T0 to T1.")
		}
	} else {
		fmt.Println("🔥 Data still relevant. Remaining in T0.")
	}
}
