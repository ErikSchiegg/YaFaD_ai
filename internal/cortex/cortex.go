package cortex

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

// Memory speichert das durchschnittliche Lambda pro Stunde (0-23)
type Memory struct {
	HourlyLoad map[int]float64 `json:"hourly_load"` // 0-23 -> Avg Lambda
	Alpha      float64         `json:"alpha"`       // Lernrate (0.1 = langsam, 0.5 = schnell)
}

type Cortex struct {
	mu       sync.RWMutex
	memory   Memory
	FilePath string
}

func NewCortex(filePath string) *Cortex {
	c := &Cortex{
		FilePath: filePath,
		memory: Memory{
			HourlyLoad: make(map[int]float64),
			Alpha:      0.2, // Wir lernen moderat (20% neues Wissen, 80% altes)
		},
	}
	c.load()
	return c
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
		// Exponential Moving Average (EMA)
		// Wir glätten Ausreißer, lernen aber Trends über die Zeit
		newVal := (c.memory.Alpha * currentLambda) + ((1 - c.memory.Alpha) * oldVal)
		c.memory.HourlyLoad[currentHour] = newVal
	}

	// Wir speichern nicht bei jedem Observe (zu viel I/O), das macht der Ticker im Worker
}

// Predict: Was erwartet uns in der nahen Zukunft?
// lookAheadHours: z.B. 1 für "nächste Stunde"
func (c *Cortex) Predict(lookAheadHours int) float64 {
	c.mu.RLock()
	defer c.mu.RUnlock()

	futureTime := time.Now().Add(time.Duration(lookAheadHours) * time.Hour)
	futureHour := futureTime.Hour()

	val, exists := c.memory.HourlyLoad[futureHour]
	if !exists {
		return 0.0 // Keine Erfahrungswerte -> Keine Vorhersage
	}
	return val
}

// Persist: Speichert das Wissen auf die Festplatte
func (c *Cortex) Persist() {
	c.mu.RLock()
	defer c.mu.RUnlock()

	data, err := json.MarshalIndent(c.memory, "", "  ")
	if err == nil {
		os.WriteFile(c.FilePath, data, 0644)
	}
}

// Load: Lädt altes Wissen beim Start
func (c *Cortex) load() {
	data, err := os.ReadFile(c.FilePath)
	if err == nil {
		json.Unmarshal(data, &c.memory)
		if c.memory.HourlyLoad == nil {
			c.memory.HourlyLoad = make(map[int]float64)
		}
	}
}
