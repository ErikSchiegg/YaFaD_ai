package main

/*
#cgo LDFLAGS: -L./core/target/release -lyafad_core -lm -ldl
extern double calculate_decay(double u_last, double lambda, double delta_t);
*/
import "C"
import (
	"fmt"
	"time"
)

// Wir behalten die Struktur bei, aber die Logik wandert zu Rust
type YaFadRecord struct {
	ID           string
	UtilityIndex float64
	LastActivity time.Time
}

func main() {
	fmt.Println("--- 🚀 YaFaD AI Hybrid Engine (Go/Rust) ---")

	// 1. Neuer Datensatz (100% Relevanz)
	record := YaFadRecord{
		ID:           "ai_data_stream_01",
		UtilityIndex: 1.0,
		LastActivity: time.Now().Add(-24 * time.Hour), // 24 Stunden alt
	}

	fmt.Printf("Initial Utility Index: %.4f\n", record.UtilityIndex)

	// 2. Wir nutzen RUST für den Decay-Prozess
	// Wir berechnen Delta T (vergangene Zeit in Stunden)
	deltaT := time.Since(record.LastActivity).Hours()
	lambda := 0.1 // Moderater Zerfall

	// Hier rufen wir die Rust-Funktion auf
	uNow := C.calculate_decay(C.double(record.UtilityIndex), C.double(lambda), C.double(deltaT))

	record.UtilityIndex = float64(uNow)
	fmt.Printf("Utility after 24h (calculated in Rust): %.4f\n", record.UtilityIndex)

	// 3. Pheromon-Boost (wird in Go gemanagt)
	if record.UtilityIndex < 0.5 {
		fmt.Println("Status: Data is decaying. Triggering Reinforcement...")
		record.UtilityIndex += 0.2 // Kleiner Boost durch Zugriff
	}

	fmt.Printf("Final Utility after Boost: %.4f\n", record.UtilityIndex)
}
