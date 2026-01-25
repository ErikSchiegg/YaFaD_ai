package main

/*
#cgo LDFLAGS: -L${SRCDIR}/core/target/release -lyafad_core -Wl,-rpath,${SRCDIR}/core/target/release -lm -ldl
#cgo CPPFLAGS: -I${SRCDIR}/core
extern double calculate_decay(double u_last, double lambda, double delta_t);
*/
import "C"
import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
)

func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}

func main() {
	connStr := fmt.Sprintf("postgres://%s:%s@%s:5432/%s?sslmode=disable",
		getEnv("DB_USER", "eriks"), getEnv("DB_PASSWORD", "test"),
		getEnv("DB_HOST", "localhost"), getEnv("DB_NAME", "yafad_test"))

	ctx := context.Background()
	conn, err := pgx.Connect(ctx, connStr)
	if err != nil {
		fmt.Printf("❌ Connection failed: %v\n", err)
		return
	}
	defer conn.Close(ctx)

	fmt.Println("📉 YaFaD_ai Decay-Worker: Gravity is now active.")

	lambda := 0.05
	threshold := 0.5

	for {
		fmt.Println("\n🔄 Scanning tiers for signal attenuation...")

		for i := 0; i < 4; i++ {
			sourceTable := fmt.Sprintf("table%d", i)
			targetTable := fmt.Sprintf("table%d", i+1)

			rows, err := conn.Query(ctx, fmt.Sprintf("SELECT id, utility_index, last_activity, payload FROM %s", sourceTable))
			if err != nil {
				fmt.Printf("❌ Error reading %s: %v\n", sourceTable, err)
				continue
			}

			for rows.Next() {
				var id, payload string
				var uLast float64
				var lastActivity time.Time

				if err := rows.Scan(&id, &uLast, &lastActivity, &payload); err != nil {
					fmt.Printf("⚠️ Scan error: %v\n", err)
					continue
				}

				deltaT := time.Since(lastActivity).Hours()
				uNow := float64(C.calculate_decay(C.double(uLast), C.double(lambda), C.double(deltaT)))

				if uNow < threshold {
					// --- TRANSACTION WITH ERROR CHECKING ---
					tx, err := conn.Begin(ctx)
					if err != nil {
						fmt.Printf("❌ Transaction failed to start for %s: %v\n", id, err)
						continue
					}

					// Atomic Move
					_, err = tx.Exec(ctx, fmt.Sprintf("DELETE FROM %s WHERE id = $1", sourceTable), id)
					if err == nil {
						_, err = tx.Exec(ctx, fmt.Sprintf("INSERT INTO %s (id, payload, utility_index, last_activity) VALUES ($1, $2, $3, $4)", targetTable),
							id, payload, uNow, lastActivity)
					}

					if err != nil {
						fmt.Printf("❌ Cascade failed for %s: %v\n", id, err)
						tx.Rollback(ctx)
					} else {
						tx.Commit(ctx)
						fmt.Printf("📉 Cascaded %s: %s -> %s (Utility: %.4f)\n", id, sourceTable, targetTable, uNow)
					}
				} else {
					conn.Exec(ctx, fmt.Sprintf("UPDATE %s SET utility_index = $1 WHERE id = $2", sourceTable), uNow, id)
				}
			}
			rows.Close()
		}

		fmt.Println("😴 Cycle complete. Watching the Golden Ratio take shape...")
		time.Sleep(15 * time.Second) // Faster cycle for testing
	}
}
