package main

/*
#cgo LDFLAGS: -L${SRCDIR}/core/target/release -lyafad_core -Wl,-rpath,${SRCDIR}/core/target/release -lm -ldl
#cgo CPPFLAGS: -I${SRCDIR}/core
extern double calculate_ideal_capacity(double total_records, int tier);
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

func DisplaySystemStatus(ctx context.Context, conn *pgx.Conn) {
	fmt.Print("\033[H\033[2J")
	counts := make([]int, 5)
	var totalRecords int
	tiers := []string{"table0", "table1", "table2", "table3", "table4"}

	for i, table := range tiers {
		conn.QueryRow(ctx, fmt.Sprintf("SELECT count(*) FROM %s", table)).Scan(&counts[i])
		totalRecords += counts[i]
	}

	fmt.Println("--- 📊 YaFaD_ai Bio-Organic Dashboard ---")
	fmt.Printf("Total Biomass: %d records\n", totalRecords)
	fmt.Println("-----------------------------------------------------------------------")
	fmt.Println("Tier Name | Current | Ideal Cap | Fill % | Health Status")
	fmt.Println("-----------------------------------------------------------------------")

	for i, table := range tiers {
		idealCap := float64(C.calculate_ideal_capacity(C.double(float64(totalRecords)), C.int(i)))
		fillPercent := 0.0
		if idealCap > 0 {
			fillPercent = (float64(counts[i]) / idealCap) * 100
		}

		status := "🟢 Optimal"
		if i == 4 {
			status = "🔵 Archive growing with unused records"
		} else {
			if fillPercent > 100 {
				status = "🔴 OVERFLOW"
			} else if fillPercent < 50 {
				status = "🟡 Starving"
			}
		}
		fmt.Printf("%-9s | %-7d | %-9.0f | %5.1f%% | %s\n", table, counts[i], idealCap, fillPercent, status)
	}
	fmt.Println("-----------------------------------------------------------------------")
}

func StartConsolidator(conn *pgx.Conn, interval time.Duration) {
	ticker := time.NewTicker(interval)
	go func() {
		for range ticker.C {
			ctx := context.Background()
			mergeQuery := `
                INSERT INTO table0 (id, payload, utility_index, last_activity, created_at)
                SELECT id, payload, utility_index, last_activity, created_at FROM buffer_tier
                ON CONFLICT (id) DO UPDATE SET 
                    utility_index = EXCLUDED.utility_index,
                    last_activity = EXCLUDED.last_activity;
                DELETE FROM buffer_tier;`
			conn.Exec(ctx, mergeQuery)
			DisplaySystemStatus(ctx, conn)
		}
	}()
}

func ProvisionFractalArchive(ctx context.Context, conn *pgx.Conn) {
	fmt.Println("❄️  Provisioning Fractal Archive Layer...")
	for i := 0; i < 5; i++ {
		tableName := fmt.Sprintf("archive%d", i)
		query := fmt.Sprintf(`
            CREATE TABLE IF NOT EXISTS %s (
                id VARCHAR(255) PRIMARY KEY,
                payload JSONB,
                utility_index DOUBLE PRECISION,
                last_activity TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
                archived_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
            );
            CREATE INDEX IF NOT EXISTS idx_%s_utility ON %s (utility_index DESC);
            CREATE INDEX IF NOT EXISTS idx_%s_brin_date ON %s USING BRIN(archived_at);
        `, tableName, tableName, tableName, tableName, tableName)
		conn.Exec(ctx, query)
	}
	fmt.Println("✅ Fractal Archive Ready.")
}

func main() {
	dbUser := getEnv("DB_USER", "eriks")
	dbPass := getEnv("DB_PASSWORD", "test")
	connStr := fmt.Sprintf("postgres://%s:%s@localhost:5432/yafad_test?sslmode=disable", dbUser, dbPass)

	ctx := context.Background()
	conn, err := pgx.Connect(ctx, connStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Connection failed: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close(ctx)

	fmt.Println("🐘 Connected. Starting YaFaD_ai provisioning...")

	conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS buffer_tier (id VARCHAR(255) PRIMARY KEY, payload JSONB, utility_index DOUBLE PRECISION DEFAULT 1.0, last_activity TIMESTAMP DEFAULT CURRENT_TIMESTAMP, created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP);`)

	for i := 0; i < 5; i++ {
		tableName := fmt.Sprintf("table%d", i)
		conn.Exec(ctx, fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (id VARCHAR(255) PRIMARY KEY, payload JSONB, utility_index DOUBLE PRECISION DEFAULT 1.0, last_activity TIMESTAMP DEFAULT CURRENT_TIMESTAMP, created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP); CREATE INDEX IF NOT EXISTS idx_%s_utility ON %s (utility_index DESC);`, tableName, tableName, tableName))
	}

	// --- WICHTIG: Das Fraktale Archiv erstellen ---
	ProvisionFractalArchive(ctx, conn)

	fmt.Println("\n🚀 YaFaD_ai infrastructure is fully operational!")
	StartConsolidator(conn, 2*time.Second)
	fmt.Println("📡 System is idle. Waiting for background tasks (Ctrl+C to stop)...")
	select {}
}
