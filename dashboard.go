package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// --- CONFIG ---
const (
	RefreshRate = 1 * time.Second
)

// Ideale Kapazitäten (müssen mit main.go übereinstimmen)
var Capacities = map[string]int{
	"table0": 20000,
	"table1": 32000,
	"table2": 51000,
	"table3": 82000,
	"table4": 131000, // Schätzwert
}

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
		panic(fmt.Sprintf("❌ DB Connect failed: %v", err))
	}
	defer pool.Close()

	// Loop
	for {
		clearScreen()
		renderDashboard(ctx, pool)
		time.Sleep(RefreshRate)
	}
}

func renderDashboard(ctx context.Context, pool *pgxpool.Pool) {
	// Daten holen
	var t0, t1, t2, t3, t4, archive int
	pool.QueryRow(ctx, "SELECT count(*) FROM table0").Scan(&t0)
	pool.QueryRow(ctx, "SELECT count(*) FROM table1").Scan(&t1)
	pool.QueryRow(ctx, "SELECT count(*) FROM table2").Scan(&t2)
	pool.QueryRow(ctx, "SELECT count(*) FROM table3").Scan(&t3)
	pool.QueryRow(ctx, "SELECT count(*) FROM table4").Scan(&t4)

	// Archive ist optional (Fehler ignorieren)
	pool.QueryRow(ctx, "SELECT count(*) FROM deep_archive").Scan(&archive) // oder "buffer_tier" je nach Setup

	total := t0 + t1 + t2 + t3 + t4

	fmt.Println(strings.Repeat("-", 60))
	fmt.Printf("📊 YaFaD Bio-Organic Dashboard v1.0\n")
	fmt.Printf("   Total Biomass: %d records\n", total)
	fmt.Println(strings.Repeat("-", 60))
	fmt.Printf("%-8s | %-9s | %-9s | %-7s | %s\n", "Tier", "Current", "Ideal Cap", "Fill %", "Health Status")
	fmt.Println(strings.Repeat("-", 60))

	printRow("table0", t0)
	printRow("table1", t1)
	printRow("table2", t2)
	printRow("table3", t3)
	printRow("table4", t4)

	fmt.Println(strings.Repeat("-", 60))
	fmt.Printf("📦 Deep Archive: %d records (Cold Storage)\n", archive)
	fmt.Println(strings.Repeat("-", 60))
}

func printRow(name string, count int) {
	cap := Capacities[name]
	pct := float64(count) / float64(cap) * 100.0

	status := "🟢 Stable"
	color := "\033[0m" // Reset

	if pct > 120.0 {
		status = "🔴 OVERFLOW"
		color = "\033[1;31m" // Rot
	} else if pct > 100.0 {
		status = "🟠 High Load"
		color = "\033[1;33m" // Gelb
	} else if pct < 10.0 && count < 100 { // Nur warnen wenn wirklich leer
		status = "🔵 Starving"
		color = "\033[1;34m" // Blau
	} else {
		color = "\033[1;32m" // Grün
	}

	// Bar Visualization (Optional)
	// barLen := int(pct / 5)
	// bar := strings.Repeat("|", barLen)

	fmt.Printf("%s%-8s | %-9d | %-9d | %6.1f%% | %s\033[0m\n",
		color, name, count, cap, pct, status)
}

func clearScreen() {
	if runtime.GOOS == "windows" {
		cmd := exec.Command("cmd", "/c", "cls")
		cmd.Stdout = os.Stdout
		cmd.Run()
	} else {
		fmt.Print("\033[H\033[2J")
	}
}
