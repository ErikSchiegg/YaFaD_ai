package main

import (
	"context"
	"encoding/json"
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
	ConfigFile  = "yafad_config.json"
)

// Standard-Werte (Falls keine Config da ist)
var Capacities = map[string]int{
	"table0": 20000,
	"table1": 32360,
	"table2": 52360,
	"table3": 84720,
	"table4": 137080,
}

func main() {
	// 1. Versuche Config zu laden
	loadConfig()

	// 2. DB Connection
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

	// 3. Loop
	for {
		// Wir laden die Config bei jedem Tick neu (Hot-Reload!)
		// So passt sich das Dashboard an, wenn du main.go neu startest.
		loadConfig()

		clearScreen()
		renderDashboard(ctx, pool)
		time.Sleep(RefreshRate)
	}
}

func loadConfig() {
	data, err := os.ReadFile(ConfigFile)
	if err == nil {
		var loadedCaps map[string]int
		if json.Unmarshal(data, &loadedCaps) == nil {
			Capacities = loadedCaps
		}
	}
}

func renderDashboard(ctx context.Context, pool *pgxpool.Pool) {
	var t0, t1, t2, t3, t4, archive int

	// Fehler ignorieren
	pool.QueryRow(ctx, "SELECT count(*) FROM table0").Scan(&t0)
	pool.QueryRow(ctx, "SELECT count(*) FROM table1").Scan(&t1)
	pool.QueryRow(ctx, "SELECT count(*) FROM table2").Scan(&t2)
	pool.QueryRow(ctx, "SELECT count(*) FROM table3").Scan(&t3)
	pool.QueryRow(ctx, "SELECT count(*) FROM table4").Scan(&t4)
	pool.QueryRow(ctx, "SELECT count(*) FROM deep_archive").Scan(&archive)

	total := t0 + t1 + t2 + t3 + t4

	// Basis-Kapazität für Info anzeigen
	baseCap := Capacities["table0"]

	fmt.Println(strings.Repeat("=", 65))
	fmt.Printf("📊 YaFaD Monitor (Target Base: %d)\n", baseCap)
	fmt.Printf("   Total Biomass: %d records\n", total)
	fmt.Println(strings.Repeat("=", 65))
	fmt.Printf("%-8s | %-9s | %-9s | %-7s | %s\n", "Tier", "Current", "Ideal Cap", "Fill %", "Status")
	fmt.Println(strings.Repeat("-", 65))

	printRow("table0", t0)
	printRow("table1", t1)
	printRow("table2", t2)
	printRow("table3", t3)
	printRow("table4", t4)

	fmt.Println(strings.Repeat("-", 65))
	fmt.Printf("📦 Deep Archive: %d records \033[1;36m(Infinite Cold Storage)\033[0m\n", archive)
	fmt.Println(strings.Repeat("=", 65))
}

func printRow(name string, count int) {
	cap := Capacities[name]
	if cap == 0 {
		cap = 1
	}

	pct := float64(count) / float64(cap) * 100.0

	status := ""
	color := "\033[0m" // Reset

	// Status Logik
	if pct > 120.0 {
		status = "🔴 OVERFLOW"
		color = "\033[1;31m"
	} else if pct > 105.0 {
		status = "🟠 High Load"
		color = "\033[1;33m"
	} else if pct < 1.0 && count == 0 {
		status = "⚪ EMPTY"
		color = "\033[1;30m"
	} else if pct < 90.0 {
		status = "🟢 Buoyancy Active"
		color = "\033[1;32m"
	} else {
		status = "🟢 OPTIMAL"
		color = "\033[1;32m"
	}

	// Bar
	barLen := 10
	filledLen := int(pct / 100.0 * float64(barLen))
	if filledLen > barLen {
		filledLen = barLen
	}
	bar := "[" + strings.Repeat("#", filledLen) + strings.Repeat(".", barLen-filledLen) + "]"

	fmt.Printf("%s%-8s | %-9d | %-9d | %6.1f%% | %s %s\033[0m\n",
		color, name, count, cap, pct, bar, status)
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
