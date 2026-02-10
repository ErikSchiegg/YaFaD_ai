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

// Struktur muss exakt zum JSON passen, das main.go schreibt
type SystemConfig struct {
	Capacities    map[string]int `json:"capacities"`
	TargetRatio   float64        `json:"target_ratio"`
	SnifferActive bool           `json:"sniffer_active"`
	LastUpdated   time.Time      `json:"last_updated"`
}

// Fallback Defaults
var Capacities = map[string]int{
	"table0": 20000,
	"table1": 32360,
	"table2": 52360,
	"table3": 84720,
	"table4": 137080,
}
var TargetRatio = 1.0

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
		loadConfig() // Hot-Reload bei jedem Tick
		clearScreen()
		renderDashboard(ctx, pool)
		time.Sleep(RefreshRate)
	}
}

func loadConfig() {
	data, err := os.ReadFile(ConfigFile)
	if err == nil {
		var conf SystemConfig
		// Hier war der Fehler: Wir müssen in das Struct unmarshallen
		if json.Unmarshal(data, &conf) == nil {
			if len(conf.Capacities) > 0 {
				Capacities = conf.Capacities
			}
			if conf.TargetRatio > 0 {
				TargetRatio = conf.TargetRatio
			}
		}
	}
}

func renderDashboard(ctx context.Context, pool *pgxpool.Pool) {
	var t0, t1, t2, t3, t4, archive int

	pool.QueryRow(ctx, "SELECT count(*) FROM table0").Scan(&t0)
	pool.QueryRow(ctx, "SELECT count(*) FROM table1").Scan(&t1)
	pool.QueryRow(ctx, "SELECT count(*) FROM table2").Scan(&t2)
	pool.QueryRow(ctx, "SELECT count(*) FROM table3").Scan(&t3)
	pool.QueryRow(ctx, "SELECT count(*) FROM table4").Scan(&t4)
	pool.QueryRow(ctx, "SELECT count(*) FROM deep_archive").Scan(&archive)

	total := t0 + t1 + t2 + t3 + t4
	baseCap := Capacities["table0"]

	fmt.Println(strings.Repeat("=", 65))
	fmt.Printf("📊 YaFaD Monitor v0.6.7 (Real Cap: %d | Target: %.2f)\n", baseCap, TargetRatio)
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

	// Echte Füllstandsberechnung
	pct := float64(count) / float64(cap) * 100.0

	// Status-Logik: Berücksichtigt jetzt das TargetRatio!
	// Wenn Target 1.5 ist, ist 150% "Normal" (Grün).
	normalizedPct := pct / TargetRatio

	status := ""
	color := "\033[0m"

	if normalizedPct > 120.0 {
		status = "🔴 OVERFLOW"
		color = "\033[1;31m"
	} else if normalizedPct > 105.0 {
		status = "🟠 High Load"
		color = "\033[1;33m"
	} else if count == 0 {
		status = "⚪ EMPTY"
		color = "\033[1;30m"
	} else {
		status = "🟢 OPTIMAL"
		color = "\033[1;32m"
	}

	barLen := 10
	filledLen := int(normalizedPct / 100.0 * float64(barLen))
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
