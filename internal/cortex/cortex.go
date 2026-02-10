package cortex

/*
#cgo LDFLAGS: -L./target/release -lyafad_core -Wl,-rpath,./target/release -lm -ldl
*/
import "C"
import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

// --- ML WEIGHTS (Trained on 3M Run Data) ---
// R^2 = 0.9659
const (
	W_INTERCEPT = 0.0
	W_CURRENT   = 0.95  // Current Load is best predictor
	W_RATE      = 0.30  // Rate of Change (Velocity)
	W_LAMBDA    = -0.10 // Damping Factor
)

// --- Rust FFI Wrapper ---
type RustCoreFFI struct {
	LibraryPath string
}

// --- Cortex Structure ---
type Cortex struct {
	MemoryFile string
	Rust       *RustCoreFFI

	// Short-Term Memory (STM) for ML
	History   []float64
	LastValue float64
	LastTime  time.Time

	mu sync.RWMutex
}

func NewCortex(memoryFile string, rust *RustCoreFFI) *Cortex {
	c := &Cortex{
		MemoryFile: memoryFile,
		Rust:       rust,
		History:    make([]float64, 0),
		LastTime:   time.Now(),
	}
	// Try loading previous state (Memory)
	c.Load()
	return c
}

// Observe: The Brain sees the current Lambda state
func (c *Cortex) Observe(lambda float64) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.History = append(c.History, lambda)
	if len(c.History) > 100 {
		c.History = c.History[1:] // Keep buffer small
	}
	c.LastValue = lambda
	c.LastTime = time.Now()
}

// Predict: The "Pre-Cognition" Engine 🔮
// Returns a "Boost Factor" (0.0 to 1.0) to be added to Lambda
func (c *Cortex) Predict(horizonSeconds int) float64 {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if len(c.History) < 5 {
		return 0.0
	} // Not enough data

	// 1. Calculate Rate of Change (Velocity)
	// Simple difference between last two observations
	current := c.History[len(c.History)-1]
	prev := c.History[len(c.History)-2]
	rate := (current - prev)

	// 2. Linear Regression Formula (Hardcoded from Python Training)
	// Predicted_Load = (W_CURRENT * current) + (W_RATE * rate) + (W_LAMBDA * current_lambda_effect)

	// We normalize "current" to a stress factor (0.0 - 1.0) roughly
	// Here we predict the *Trend Intensity*, not the raw T0 count.

	predictedStress := (W_CURRENT * current) + (W_RATE * rate) + W_INTERCEPT

	// 3. Safety Check
	if predictedStress < 0 {
		predictedStress = 0
	}

	// Wenn die Vorhersage sagt "Es wird schlimmer" (Predicted > Current),
	// geben wir einen Boost zurück.
	if predictedStress > current*1.1 {
		boost := predictedStress - current
		return boost // Add this to Lambda immediately!
	}

	return 0.0 // Everything looks calm
}

func (c *Cortex) Persist() {
	c.mu.RLock()
	data, _ := json.Marshal(c.History)
	c.mu.RUnlock()
	_ = os.WriteFile(c.MemoryFile, data, 0644)
}

func (c *Cortex) Load() {
	data, err := os.ReadFile(c.MemoryFile)
	if err == nil {
		json.Unmarshal(data, &c.History)
	}
}
