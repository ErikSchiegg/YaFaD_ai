package cortex

import (
	"encoding/json"
	"log/slog"
	"os"
	"sync"
	"time"
)

// --- 1. Die fehlende SystemMetrics Definition ---
// SystemMetrics ist das Datenpaket, das der Pilot sammelt.
type SystemMetrics struct {
	CPU            float64 `json:"cpu_usage"`       // CPU in %
	Memory         float64 `json:"memory_usage"`    // RAM in %
	ActiveQueries  int     `json:"active_queries"`  // Laufende Queries
	IOWait         float64 `json:"io_wait"`         // Warten auf Disk (%)
	ReplicationLag float64 `json:"replication_lag"` // Lag in MB oder Sekunden
}

// --- 2. Der fehlende RustCoreFFI Stub ---
// Damit der Compiler Ruhe gibt, definieren wir die Struktur hier.
// Später wird das durch die echte CGO-Implementierung ersetzt.
type RustCoreFFI struct {
	LibraryPath string
}

// CalculateSigmoid ist die Methode, die Cortex aufruft.
// Aktuell ein Mock, damit es kompiliert.
func (r *RustCoreFFI) CalculateSigmoid(metrics SystemMetrics) float64 {
	// TODO: Hier kommt später der echte CGO Call hin!
	// return C.calculate_sigmoid(...)

	// Mock-Logik für Debugging:
	// Wenn IOWait hoch ist, bremsen wir massiv.
	if metrics.IOWait > 10.0 {
		return 500.0 // 500ms Pause (langsam)
	}
	return 50.0 // 50ms Standard-Pause (schnell)
}

// --- 3. Der Cortex (Das Gehirn) ---

// Memory speichert das durchschnittliche Lambda pro Stunde (0-23)
type Memory struct {
	HourlyLoad map[int]float64 `json:"hourly_load"` // 0-23 -> Avg Lambda
	Alpha      float64         `json:"alpha"`       // Lernrate
}

type Cortex struct {
	mu       sync.RWMutex
	memory   Memory
	FilePath string
	rustCore *RustCoreFFI // Jetzt kennt er den Typ!
}

// Konstruktor: Nimmt den Rust-Core entgegen
func NewCortex(filePath string, rustBridge *RustCoreFFI) *Cortex {
	c := &Cortex{
		FilePath: filePath,
		rustCore: rustBridge,
		memory: Memory{
			HourlyLoad: make(map[int]float64),
			Alpha:      0.2,
		},
	}
	c.load()
	return c
}

func (c *Cortex) AssessLoad(metrics SystemMetrics) time.Duration {
	// 1. Loggen, was wir reinstecken
	slog.Debug("Cortex: Assessing load",
		"io_wait", metrics.IOWait,
		"cpu", metrics.CPU,
		"lag", metrics.ReplicationLag)

	// Der eigentliche Rust-Aufruf
	startTime := time.Now()
	calculatedPace := c.rustCore.CalculateSigmoid(metrics)
	duration := time.Since(startTime)

	// 2. Loggen, was rauskommt
	slog.Info("Cortex: Decision computed",
		"calculated_pace_ms", calculatedPace,
		"computation_time_µs", duration.Microseconds())

	return time.Duration(calculatedPace) * time.Millisecond
}

// Observe: Das Gehirn lernt aus der aktuellen Situation
func (c *Cortex) Observe(currentLambda float64) {
	c.mu.Lock()
	defer c.mu.Unlock()

	currentHour := time.Now().Hour()
	oldVal, exists := c.memory.HourlyLoad[currentHour]

	if !exists {
		c.memory.HourlyLoad[currentHour] = currentLambda
	} else {
		newVal := (c.memory.Alpha * currentLambda) + ((1 - c.memory.Alpha) * oldVal)
		c.memory.HourlyLoad[currentHour] = newVal
	}
}

// Predict: Was erwartet uns?
func (c *Cortex) Predict(lookAheadHours int) float64 {
	c.mu.RLock()
	defer c.mu.RUnlock()

	futureTime := time.Now().Add(time.Duration(lookAheadHours) * time.Hour)
	futureHour := futureTime.Hour()

	val, exists := c.memory.HourlyLoad[futureHour]
	if !exists {
		return 0.0
	}
	return val
}

// Persist: Speichern
func (c *Cortex) Persist() {
	c.mu.RLock()
	defer c.mu.RUnlock()

	data, err := json.MarshalIndent(c.memory, "", "  ")
	if err == nil {
		os.WriteFile(c.FilePath, data, 0644)
	}
}

// Load: Laden
func (c *Cortex) load() {
	data, err := os.ReadFile(c.FilePath)
	if err == nil {
		json.Unmarshal(data, &c.memory)
		if c.memory.HourlyLoad == nil {
			c.memory.HourlyLoad = make(map[int]float64)
		}
	}
}
