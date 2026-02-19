package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"math/rand"
	"os"
	"time"

	_ "github.com/lib/pq"
)

// --- CONFIGURATION ---
const (
	// Simulations-Geschwindigkeit:
	// 5 echte Sekunden = 1 virtuelle Stunde.
	// Ein voller "Tag" dauert also 24 * 5 = 120 Sekunden (2 Minuten).
	RealSecondsPerHour = 5 * time.Second
)

func main() {
	// 1. DB Connection
	dbUser := os.Getenv("DB_USER")
	if dbUser == "" {
		dbUser = "eriks"
	}
	dbPass := os.Getenv("DB_PASSWORD")
	if dbPass == "" {
		dbPass = "test"
	}
	connStr := fmt.Sprintf("postgres://%s:%s@localhost:5432/yafad_test?sslmode=disable", dbUser, dbPass)

	db, err := sql.Open("postgres", connStr)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	// 2. Setup
	rand.Seed(time.Now().UnixNano())
	log.Println("🤖 Bio-Rhythm User Simulator v0.5.0 (Cyclic Pattern) starting...")
	log.Printf("⏱️  Time Scale: 1 Virtual Hour = %v Real Time", RealSecondsPerHour)

	// 3. The Daily Cycle Loop
	virtualHour := 6 // Wir starten morgens um 06:00
	hourTicker := time.NewTicker(RealSecondsPerHour)
	defer hourTicker.Stop()

	log.Printf("\n🌅 GOOD MORNING! Virtual Time: %02d:00", virtualHour)

	for {
		select {
		case <-hourTicker.C:
			// Eine Stunde ist vergangen
			virtualHour = (virtualHour + 1) % 24

			// Visueller Separator für den neuen Tag/Stunde
			icon := getHourIcon(virtualHour)
			log.Printf("\n%s Virtual Time: %02d:00 | Load: %s", icon, virtualHour, getLoadDescription(virtualHour))

			if virtualHour == 0 {
				log.Println("✨ A NEW DAY BEGINS...")
			}

		default:
			// Arbeite basierend auf der aktuellen virtuellen Stunde
			performHourlyAction(db, virtualHour)
		}
	}
}

// getHourIcon gibt ein passendes Emoji für die Tageszeit zurück
func getHourIcon(hour int) string {
	switch {
	case hour >= 0 && hour < 6:
		return "🌙"
	case hour >= 6 && hour < 9:
		return "🌅"
	case hour >= 9 && hour < 18:
		return "💼" // Working
	case hour >= 18 && hour < 23:
		return "🔥" // Prime Time
	default:
		return "🛌"
	}
}

func getLoadDescription(hour int) string {
	if hour >= 0 && hour < 6 {
		return "Deep Sleep (Low)"
	}
	if hour >= 6 && hour < 9 {
		return "Ramp Up (Medium)"
	}
	if hour >= 9 && hour < 18 {
		return "Business Steady (High)"
	}
	if hour >= 18 && hour < 23 {
		return "Viral Spike (Extreme)"
	}
	return "Cool Down"
}

// performHourlyAction generiert Traffic basierend auf der Uhrzeit
func performHourlyAction(db *sql.DB, hour int) {
	ctx := context.Background()

	var baseSleepMs int
	var variance int
	var batchSize int

	// --- DER TAGESRHYTHMUS ---
	switch {
	case hour >= 0 && hour < 6:
		// NACHT: Fast nichts los. Ideal für Hibernation Tests.
		baseSleepMs = 1500
		variance = 1000 // 1.5s - 2.5s Pause
		batchSize = 0   // Oft gar keine neuen Daten, nur Background Noise

	case hour >= 6 && hour < 9:
		// MORGEN: Langsames Aufwachen.
		baseSleepMs = 100
		variance = 100
		batchSize = 1

	case hour >= 9 && hour < 18:
		// ARBEITSTAG: Hohe, gleichmäßige Last.
		// Hier sollte die KI stabilisieren.
		baseSleepMs = 10
		variance = 20 // 10ms - 30ms Pause
		batchSize = 2

	case hour >= 18 && hour < 23:
		// ABEND / PRIME TIME: Streaming/Gaming Spitzen.
		// Hier ist der größte Stress für die DB.
		baseSleepMs = 2
		variance = 10  // 2ms - 12ms Pause (sehr schnell)
		batchSize = 15 // Bulk Inserts!

	default: // 23:00 - 00:00
		baseSleepMs = 500
		variance = 200
		batchSize = 1
	}

	// Berechne reale Pause mit Varianz ("Jitter")
	actualSleep := time.Duration(baseSleepMs+rand.Intn(variance+1)) * time.Millisecond
	time.Sleep(actualSleep)

	// Manchmal (in der Nacht) machen wir gar nichts
	if batchSize == 0 && rand.Float32() > 0.1 {
		return
	}
	if batchSize == 0 {
		batchSize = 1
	} // Mindestens ein Ping

	// Zufällige Entscheidung: Schreiben oder Lesen?
	// Abends wird mehr konsumiert (gelesen), tagsüber mehr gearbeitet (geschrieben)
	writeProbability := 0.3
	if hour >= 18 {
		writeProbability = 0.1
	} // Abends mehr Reads (Netflix-Effekt)

	if rand.Float64() < writeProbability {
		createRecords(ctx, db, batchSize)
	} else {
		simulateAccess(ctx, db)
	}
}

func createRecords(ctx context.Context, db *sql.DB, count int) {
	for i := 0; i < count; i++ {
		// ID generieren
		id := fmt.Sprintf("rec_%d_%d", time.Now().UnixNano(), rand.Intn(100000))

		// Insert in Table0 (Hot Tier)
		_, err := db.ExecContext(ctx,
			"INSERT INTO table0 (id, payload, utility_index, last_activity) VALUES ($1, 'user_data', 1.0, NOW()) ON CONFLICT DO NOTHING",
			id)
		if err != nil {
			// Ignore constraint errors during stress test
		}
	}
	// Kleiner Indikator im Terminal (Schreibmaschine)
	if count > 5 {
		fmt.Print("#")
	} else {
		fmt.Print(".")
	}
}

func simulateAccess(ctx context.Context, db *sql.DB) {
	// Wir greifen zufällig auf Daten zu, meistens in den oberen Tiers
	tier := 0
	r := rand.Float32()
	if r > 0.6 {
		tier = 1
	}
	if r > 0.85 {
		tier = 2
	} // Selten auf Cold Data zugreifen

	tableName := fmt.Sprintf("table%d", tier)

	// "Viral Hit": Wir lesen einen Record und setzen seinen Utility Index wieder auf 1.0 (Verjüngung)
	query := fmt.Sprintf("UPDATE %s SET utility_index = 1.0, last_activity = NOW() WHERE id IN (SELECT id FROM %s LIMIT 1)", tableName, tableName)
	db.ExecContext(ctx, query)
}

// Simuliert Traffic einer alten Applikation, der durch den Proxy läuft
func SimulateLegacyAppTraffic() {
	fmt.Println("\n🤖 --- STARTING USER SIMULATION (Legacy App Traffic) ---")

	// 1. Proxy initialisieren (Liest migration_policy.json)
	// HINWEIS: NewProxy() kommt aus yafad_proxy.go.
	// Damit das funktioniert, müssen beide Dateien zusammen gestartet werden.
	proxy := NewProxy()

	// Simulierte Tabellen einer typischen App
	tables := []string{"users", "orders", "audit_logs", "sensor_data", "inventory"}
	ops := []string{"INSERT", "UPDATE", "SELECT"}

	fmt.Println("🚦 Traffic Generator active. Press Ctrl+C to stop.")

	// Endlosschleife
	for {
		// Zufallsauswahl
		targetTable := tables[rand.Intn(len(tables))]
		operation := ops[rand.Intn(len(ops))]

		// Simulierter Daten-Payload
		payload := fmt.Sprintf("User-%d data payload", rand.Intn(1000))

		// Der entscheidende Aufruf: Die App spricht mit dem Proxy!
		// Der Proxy entscheidet dann: Legacy DB oder YaFaD?
		proxy.HandleRequest(targetTable, operation, payload)

		// Random Delay (50ms - 500ms) für Realismus
		time.Sleep(time.Duration(rand.Intn(450)+50) * time.Millisecond)
	}
}
