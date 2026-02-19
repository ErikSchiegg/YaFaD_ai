package main

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

// Konfiguration für das Konsolen-Dashboard
const (
	HUD_REFRESH_RATE = 2 * time.Second
	METRICS_FILE     = "yafad_metrics.csv"
	BAR_WIDTH        = 20
)

// ANSI Colors für Colab
const (
	ColorReset  = "\033[0m"
	ColorRed    = "\033[31m"
	ColorGreen  = "\033[32m"
	ColorYellow = "\033[33m"
	ColorBlue   = "\033[34m"
	ColorPurple = "\033[35m"
	ColorCyan   = "\033[36m"
	ColorWhite  = "\033[37m"
	ColorBold   = "\033[1m"
)

// StartLegacyDashboard startet die Überwachung in einer Goroutine
func StartLegacyDashboard() {
	fmt.Println("🖥️  Tactical Dashboard active. Monitoring metrics...")

	// Wir warten kurz, bis die Engine CSV-Dateien angelegt hat
	time.Sleep(3 * time.Second)

	for {
		printHUD()
		time.Sleep(HUD_REFRESH_RATE)
	}
}

func printHUD() {
	// 1. Letzte Zeile aus CSV lesen
	record, err := readLastLine(METRICS_FILE)
	if err != nil {
		// Noch keine Daten, wir warten leise
		return
	}

	// CSV Struktur Annahme:
	// timestamp, runtime, biomass, t0, t1, t2, t3, t4, deep, t0_pct, ...
	if len(record) < 10 {
		return
	}

	// Daten parsen (Fehler ignorieren wir für den Speed, setzen auf 0)
	biomass, _ := strconv.ParseInt(record[2], 10, 64)
	t0, _ := strconv.ParseInt(record[3], 10, 64)
	t1, _ := strconv.ParseInt(record[4], 10, 64)
	deep, _ := strconv.ParseInt(record[8], 10, 64)
	t0_pct, _ := strconv.ParseFloat(record[9], 64)

	// Runtime formatieren
	runtimeSec, _ := strconv.ParseInt(record[1], 10, 64)
	uptime := fmt.Sprintf("%02d:%02d", runtimeSec/60, runtimeSec%60)

	// 2. RENDERING THE HUD
	// Wir nutzen fmt.Printf für einen sauberen Block.
	// In Colab können wir den Screen nicht gut clearen, also drucken wir kompakte Blöcke.

	statusColor := ColorGreen
	statusIcon := "🟢 ONLINE"
	if t0_pct > 100 {
		statusColor = ColorRed
		statusIcon = "🔥 OVERLOAD"
	} else if t0_pct > 80 {
		statusColor = ColorYellow
		statusIcon = "⚠️  PRESSURE"
	}

	fmt.Println("\n" + strings.Repeat("━", 50))
	fmt.Printf(" %s🦁 YaFaD ENGINE v0.9.0  |  UPTIME: %s  |  %s%s%s\n", ColorBold, uptime, statusColor, statusIcon, ColorReset)
	fmt.Println(strings.Repeat("━", 50))

	// Stats Row 1
	fmt.Printf(" %sTOTAL BIOMASS:%s    %s%s%s records\n", ColorCyan, ColorReset, ColorBold, formatInt(biomass), ColorReset)

	// Stats Row 2 (T0 Bar)
	fmt.Printf(" %sCORTEX (T0):%s      %s %s (%s)\n", ColorBlue, ColorReset, drawBar(t0_pct, 120), fmt.Sprintf("%.1f%%", t0_pct), formatInt(t0))

	// Stats Row 3 (Detail Layers)
	fmt.Printf(" %sL1 (Dream):%s       %s\n", ColorPurple, ColorReset, formatInt(t1))
	fmt.Printf(" %sDEEP ARCHIVE:%s     %s\n", ColorWhite, ColorReset, formatInt(deep))

	// Footer
	fmt.Println(strings.Repeat("─", 50))
}

// Hilfsfunktion: Zeichnet einen ASCII Ladebalken
func drawBar(pct float64, maxPct float64) string {
	normalized := pct / maxPct
	if normalized > 1.0 {
		normalized = 1.0
	}
	filledLen := int(normalized * float64(BAR_WIDTH))

	bar := "["
	color := ColorGreen

	if pct > 80 {
		color = ColorYellow
	}
	if pct > 100 {
		color = ColorRed
	}

	for i := 0; i < BAR_WIDTH; i++ {
		if i < filledLen {
			bar += color + "█" + ColorReset
		} else {
			bar += "░"
		}
	}
	bar += "]"
	return bar
}

// Hilfsfunktion: Zahlen formatieren (1.000.000)
func formatInt(n int64) string {
	in := strconv.FormatInt(n, 10)
	numOfDigits := len(in)
	if n < 0 {
		numOfDigits-- // Handle negative numbers
	}
	numOfCommas := (numOfDigits - 1) / 3

	out := make([]byte, len(in)+numOfCommas)
	if n < 0 {
		in, out[0] = in[1:], '-'
	}

	for i, j, k := len(in)-1, len(out)-1, 0; ; i, j = i-1, j-1 {
		out[j] = in[i]
		if i == 0 {
			return string(out)
		}
		if k++; k == 3 {
			j, k = j-1, 0
			out[j] = ','
		}
	}
}

// Liest die allerletzte Zeile einer Datei effizient
func readLastLine(filepath string) ([]string, error) {
	file, err := os.Open(filepath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	// Wir lesen einfach die Datei. Bei riesigen Files wäre Seek besser,
	// aber metrics.csv wird in Colab Sessions selten >10MB.
	reader := csv.NewReader(file)
	var lastRecord []string

	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			continue
		}
		lastRecord = record
	}

	if lastRecord == nil {
		return nil, io.EOF
	}
	return lastRecord, nil
}
