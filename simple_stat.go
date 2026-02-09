package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	// DB Connection
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

	fmt.Println("🏥 YaFaD DIRECT DB PULSE CHECK")
	fmt.Println("-----------------------------")

	for {
		var t0, t1, t2, t3, t4, archive int
		pool.QueryRow(ctx, "SELECT count(*) FROM table0").Scan(&t0)
		pool.QueryRow(ctx, "SELECT count(*) FROM table1").Scan(&t1)
		pool.QueryRow(ctx, "SELECT count(*) FROM table2").Scan(&t2)
		pool.QueryRow(ctx, "SELECT count(*) FROM table3").Scan(&t3)
		pool.QueryRow(ctx, "SELECT count(*) FROM table4").Scan(&t4)
		pool.QueryRow(ctx, "SELECT count(*) FROM deep_archive").Scan(&archive)

		fmt.Printf("\r [Live] T0: %-6d | T1: %-6d | T2: %-6d | T3: %-6d | T4: %-6d | 🏛️ Deep: %d",
			t0, t1, t2, t3, t4, archive)

		time.Sleep(1 * time.Second)
	}
}
