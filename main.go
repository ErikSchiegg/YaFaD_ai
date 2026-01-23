package main

import (
	"fmt"
	"time"
	"yafad/internal/utility"
)

func main() {
	fmt.Println("--- YaFaD AI Prototype ---")

	// 1. Ein neuer Datensatz wird erstellt (100% Relevanz)
	record := utility.Record{
		ID:           "user_session_123",
		UtilityIndex: 1.0,
		LastActivity: time.Now().Add(-24 * time.Hour), // Wir tun so, als wäre es gestern gewesen
	}

	fmt.Printf("Start-Wert: %.4f\n", record.UtilityIndex)

	// 2. Wir simulieren das "Vergessen" (Decay)
	// Ein Faktor von 0.1 ist moderat.
	record.ApplyDecay(0.1)
	fmt.Printf("Nach 24h ohne Nutzung: %.4f\n", record.UtilityIndex)

	// 3. Der User kommt zurück -> Pheromon-Boost!
	record.SignalActivity(0.5)
	fmt.Printf("Nach erneutem Zugriff: %.4f\n", record.UtilityIndex)
}
