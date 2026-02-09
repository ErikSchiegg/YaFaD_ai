package monitoring

import (
	"log"
	"os"
)

// Simple Logger Wrapper für den Anfang
type Monitor struct {
	Logger *log.Logger
}

func NewMonitor() *Monitor {
	return &Monitor{
		Logger: log.New(os.Stdout, "[MONITOR] ", log.LstdFlags),
	}
}

func (m *Monitor) Log(msg string) {
	m.Logger.Println(msg)
}

func (m *Monitor) RecordState(t0, t1, t2, t3, t4 int) {
	// Hier könnte man später in CSV schreiben
	// Für jetzt reicht ein Print, damit der Code läuft
	// m.Logger.Printf("State: %d | %d | %d | %d | %d", t0, t1, t2, t3, t4)
}
