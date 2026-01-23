package utility

import (
	"math"
	"time"
)

// Record bildet das Datenmodell aus dem YaFaD-Konzept ab
type Record struct {
	ID           string
	Data         string
	UtilityIndex float64   // Normalisierter Wert zwischen 0 und 1
	LastActivity time.Time // Zeitstempel des letzten Zugriffs
}

// ApplyDecay berechnet das "Vergessen" von Daten basierend auf vergangener Zeit.
// Die Formel: U_neu = U_alt * e^(-factor * zeit)
func (r *Record) ApplyDecay(factor float64) {
	// Zeitdifferenz seit der letzten Aktivität in Stunden
	duration := time.Since(r.LastActivity).Hours()

	// Der Kern deines Konzepts: Exponentieller Zerfall
	r.UtilityIndex = r.UtilityIndex * math.Exp(-factor*duration)

	// Sicherheitsnetz: Der Index sollte nie negativ werden
	if r.UtilityIndex < 0 {
		r.UtilityIndex = 0
	}
}

// SignalActivity simuliert einen Zugriff (Pheromon-Prinzip), der den Nutzen erhöht
func (r *Record) SignalActivity(boost float64) {
	r.UtilityIndex += boost

	// Index auf maximal 1.0 begrenzen (Normalisierung)
	if r.UtilityIndex > 1.0 {
		r.UtilityIndex = 1.0
	}
	// Zeitstempel aktualisieren ("Pheromonspur auffrischen")
	r.LastActivity = time.Now()
}
