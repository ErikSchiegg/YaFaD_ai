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

// --- Configuration ---
const (
	TotalDuration    = 30 * time.Minute // Wie lange der Test insgesamt läuft
	SwitchPhaseEvery = 30 * time.Second // Wie oft sich das Wetter ändert
)

// TrafficPhase definiert die Stimmung des Users
type TrafficPhase string

const (
	PhaseMorningRush TrafficPhase = "☀️ (Steady High Load)"
	PhaseCoffeeBreak TrafficPhase = "☕ (Low Activity)"
	PhaseViralSpike  TrafficPhase = "🔥 (Extreme Burst)"
	PhaseNightMode   TrafficPhase = "🌙 (Deep Decay Time)"
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
	log.Println("🤖 Bio-Rhythm User Simulator v0.3.0 starting...")
	log.Printf("⏱️  Duration: %v | Phase Switch: %v\n", TotalDuration, SwitchPhaseEvery)

	// 3. The Loop
	startTime := time.Now()
	phaseTicker := time.NewTicker(SwitchPhaseEvery)
	defer phaseTicker.Stop()

	// HIER WAR DER FEHLER: Nutze jetzt konsistent PhaseMorningRush
	currentPhase := PhaseMorningRush

	// Statistik
	requests := 0

	for time.Since(startTime) < TotalDuration {
		select {
		case <-phaseTicker.C:
			// Wähle eine neue Phase zufällig
			r := rand.Intn(4)
			switch r {
			case 0:
				currentPhase = PhaseMorningRush // Korrigiert
			case 1:
				currentPhase = PhaseCoffeeBreak
			case 2:
				currentPhase = PhaseViralSpike
			case 3:
				currentPhase = PhaseNightMode
			}
			log.Printf("\n🔁 PHASE SWITCH: %s\n", currentPhase)

		default:
			// Führe Aktionen basierend auf der Phase aus
			performUserAction(db, currentPhase)
			requests++

			// Visuelles Feedback alle 1000 Requests
			if requests%1000 == 0 {
				fmt.Print(".")
			}
		}
	}

	log.Println("\n✅ Simulation complete.")
}

func performUserAction(db *sql.DB, phase TrafficPhase) {
	ctx := context.Background()

	// 1. Wie lange schlafen wir zwischen Aktionen? (Die Frequenz)
	var sleepDuration time.Duration
	var batchSize int

	switch phase {
	case PhaseMorningRush: // Korrigiert
		// Stetiger Fluss: 5ms - 20ms Pause
		sleepDuration = time.Duration(rand.Intn(15)+5) * time.Millisecond
		batchSize = 1 // Einzelne User

	case PhaseCoffeeBreak:
		// Faul: 200ms - 1s Pause
		sleepDuration = time.Duration(rand.Intn(800)+200) * time.Millisecond
		batchSize = 1

	case PhaseViralSpike:
		// Panik: 0ms Pause (So schnell es geht), aber manchmal kurze Atempausen
		if rand.Float32() < 0.9 {
			sleepDuration = 0
		} else {
			sleepDuration = 10 * time.Millisecond
		}
		batchSize = 10 // Bulk Inserts simulieren

	case PhaseNightMode:
		// Fast tot: 1s - 3s Pause
		sleepDuration = time.Duration(rand.Intn(2000)+1000) * time.Millisecond
		batchSize = 0 // Manchmal gar nichts tun
	}

	time.Sleep(sleepDuration)

	if batchSize == 0 {
		return
	}

	// 2. Was tun wir? (Insert vs. Update/Read)
	actionRoll := rand.Float32()

	if actionRoll < 0.3 {
		// 30% Chance: NEUE DATEN (Ingest)
		createRecords(ctx, db, batchSize)
	} else {
		// 70% Chance: ALTE DATEN LESEN (Viral Hit / Refresh)
		simulateViralHit(ctx, db)
	}
}

func createRecords(ctx context.Context, db *sql.DB, count int) {
	for i := 0; i < count; i++ {
		id := fmt.Sprintf("rec_%d_%d", time.Now().UnixNano(), rand.Intn(100000))
		_, err := db.ExecContext(ctx,
			"INSERT INTO table0 (id, payload, utility_index, last_activity) VALUES ($1, 'user_data', 1.0, NOW()) ON CONFLICT DO NOTHING",
			id)
		if err != nil {
			// Fehler ignorieren
		}
	}
}

func simulateViralHit(ctx context.Context, db *sql.DB) {
	tier := rand.Intn(3) + 1
	tableName := fmt.Sprintf("table%d", tier)

	// Refresh eines zufälligen Records (erhöht Lebensdauer)
	query := fmt.Sprintf("UPDATE %s SET utility_index = 1.0, last_activity = NOW() WHERE id IN (SELECT id FROM %s LIMIT 1)", tableName, tableName)

	db.ExecContext(ctx, query)
}
