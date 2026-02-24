package main

/*
#cgo LDFLAGS: -L${SRCDIR}/core/target/release -lyafad_core -Wl,-rpath,${SRCDIR}/core/target/release -lm -ldl
#cgo CPPFLAGS: -I${SRCDIR}/core
extern double calculate_decay(double u_last, double lambda, double delta_t);
*/
import "C"
import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"YaFaD_ai/internal/cortex"
	"YaFaD_ai/internal/monitoring"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const PHI = 1.61803398875
const CONFIG_FILE = "yafad_config.json"
const BRAIN_FILE = "brain_weights.json"

// --- CONFIG STRUCTURES ---

type PIDConfig struct {
	Kp float64 `json:"kp"`
	Ki float64 `json:"ki"`
	Kd float64 `json:"kd"`
}

type WatermarkConfig struct {
	High float64 `json:"high"`
	Low  float64 `json:"low"`
}

type ResourceLimits struct {
	MaxCpuPercent int `json:"max_cpu_percent"`
}

type SystemConfig struct {
	RunState        string          `json:"run_state"`
	InjectTotal     int             `json:"inject_total"`
	InjectDone      int             `json:"inject_done"`
	T0HardLimit     int             `json:"t0_hard_limit"`
	ActiveTiers     []int           `json:"active_tiers"` // <--- NEU: Welche Tabellen laufen lokal? (z.B. [0] oder [0,1,2,3,4])
	Capacities      map[string]int  `json:"capacities"`
	TargetRatio     float64         `json:"target_ratio"`
	FlushOnStart    bool            `json:"flush_on_start"`
	BuoyancyFactor  float64         `json:"buoyancy_factor"`
	Watermarks      WatermarkConfig `json:"watermarks"`
	SnifferActive   bool            `json:"sniffer_active"`
	VanishThreshold string          `json:"vanish_threshold"`
	PID             PIDConfig       `json:"pid_settings"`
	Limits          ResourceLimits  `json:"limits"`
	LastUpdated     time.Time       `json:"last_updated"`
}

type BrainWeights struct {
	WPressure float64 `json:"w_pressure"`
	WVelocity float64 `json:"w_velocity"`
	WAccel    float64 `json:"w_accel"`
	Intercept float64 `json:"intercept"`
}

type Record struct {
	ID           string
	Payload      string
	UtilityIndex float64
	LastActivity time.Time
}

var (
	globalConfig SystemConfig
	brainWeights BrainWeights
	t0Lambda     float64
	configMu     sync.RWMutex
	brainMu      sync.RWMutex
	lambdaMu     sync.RWMutex
)

// --- PID CONTROLLER ---
type PIDController struct {
	Kp, Ki, Kd float64
	Integral   float64
	PrevError  float64
	LastTime   time.Time
}

func NewPID(kp, ki, kd float64) *PIDController {
	return &PIDController{Kp: kp, Ki: ki, Kd: kd, LastTime: time.Now()}
}

func (pid *PIDController) UpdateParams(kp, ki, kd float64) {
	pid.Kp = kp
	pid.Ki = ki
	pid.Kd = kd
}

func (pid *PIDController) Update(currentVal, setPoint float64) float64 {
	now := time.Now()
	dt := now.Sub(pid.LastTime).Seconds()
	if dt <= 0 {
		return 0
	}
	pid.LastTime = now
	error := currentVal - setPoint
	pid.Integral += error * dt

	if pid.Integral > 10 {
		pid.Integral = 10
	}
	if pid.Integral < -10 {
		pid.Integral = -10
	}

	derivative := (error - pid.PrevError) / dt
	pid.PrevError = error
	return (pid.Kp * error) + (pid.Ki * pid.Integral) + (pid.Kd * derivative)
}

// --- ROUTER ---
type StorageRouter struct {
	HotPool  *pgxpool.Pool
	ColdPool *pgxpool.Pool
}

func (r *StorageRouter) GetPool(tier int) *pgxpool.Pool {
	if tier >= 3 {
		return r.ColdPool
	}
	return r.HotPool
}

// --- NEW: DYNAMIC BIOMASS AND WATERMARK LOGIC ---

// getEstimatedBiomass fragt in <1ms die interne Statistik von PostgreSQL ab, anstatt Millionen Zeilen zu zählen
func getEstimatedBiomass(ctx context.Context, pool *pgxpool.Pool) int64 {
	var total float64
	query := `SELECT COALESCE(sum(reltuples), 0) FROM pg_class WHERE relname IN ('table0', 'table1', 'table2', 'table3', 'table4', 'deep_archive')`
	err := pool.QueryRow(ctx, query).Scan(&total)
	if err != nil {
		return 0
	}
	return int64(total)
}

// adaptPhysics berechnet das Atmen für Cortex-Grenzen UND koppelt die Buoyancy (Auftrieb) daran
func adaptPhysics(currentHigh, currentLow, currentBuoy float64, isRunning bool, totalBiomass int64, tickIntervalSec float64) (float64, float64, float64, bool) {
	// 1. Definition der physikalischen Extreme
	targetHighIdle, targetLowIdle := 100.0, 95.0
	targetHighRun, targetLowRun := 150.0, 110.0

	// NEU: Buoyancy Sweet Spots (durch deine Tests ermittelt)
	targetBuoyIdle := 0.64 // Entspannung: Lässt T0 sauber auf 100% abtropfen
	targetBuoyRun := 0.85  // Stress: Hält Daten während der Injektion aggressiv in T0

	var targetHigh, targetLow float64
	var stepHigh, stepLow float64

	if isRunning {
		targetHigh = targetHighRun
		targetLow = targetLowRun
		openUpSeconds := 30.0
		stepHigh = ((targetHighRun - targetHighIdle) / openUpSeconds) * tickIntervalSec
		stepLow = ((targetLowRun - targetLowIdle) / openUpSeconds) * tickIntervalSec
	} else {
		targetHigh = targetHighIdle
		targetLow = targetLowIdle
		hoursToClose := (float64(totalBiomass) / 1000000.0) * 1.5
		if hoursToClose < 0.01 {
			hoursToClose = 0.01
		}
		secondsToClose := hoursToClose * 3600.0

		stepHigh = ((targetHighRun - targetHighIdle) / secondsToClose) * tickIntervalSec
		stepLow = ((targetLowRun - targetLowIdle) / secondsToClose) * tickIntervalSec
	}

	newHigh, newLow := currentHigh, currentLow
	changed := false

	// Step anwenden (High)
	if currentHigh < targetHigh {
		newHigh += stepHigh
		if newHigh > targetHigh {
			newHigh = targetHigh
		}
	} else if currentHigh > targetHigh {
		newHigh -= stepHigh
		if newHigh < targetHigh {
			newHigh = targetHigh
		}
	}

	// Step anwenden (Low)
	if currentLow < targetLow {
		newLow += stepLow
		if newLow > targetLow {
			newLow = targetLow
		}
	} else if currentLow > targetLow {
		newLow -= stepLow
		if newLow < targetLow {
			newLow = targetLow
		}
	}

	// Präzisions-Korrektur für Watermarks
	if math.Abs(newHigh-targetHigh) < 0.0001 {
		newHigh = targetHigh
	}
	if math.Abs(newLow-targetLow) < 0.0001 {
		newLow = targetLow
	}

	// ==========================================
	// 2. MATHEMATISCHE KOPPLUNG DER BUOYANCY
	// ==========================================

	// Berechnet, wie weit T0 aktuell aufgedehnt ist (0.0 = Idle, 1.0 = Voll aufgepumpt)
	stretchFactor := (newHigh - targetHighIdle) / (targetHighRun - targetHighIdle)
	if stretchFactor < 0 {
		stretchFactor = 0
	}
	if stretchFactor > 1 {
		stretchFactor = 1
	}

	// Lerp (Linear Interpolation) für die Buoyancy
	newBuoy := targetBuoyIdle + ((targetBuoyRun - targetBuoyIdle) * stretchFactor)
	newBuoy = math.Round(newBuoy*1000) / 1000 // Auf 3 Nachkommastellen runden

	if currentHigh != newHigh || currentLow != newLow || math.Abs(currentBuoy-newBuoy) > 0.001 {
		changed = true
	}

	return newHigh, newLow, newBuoy, changed
}

// --- MAIN ---
func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	logPath := "/tmp/yafad_debug.log"
	logFile, _ := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0666)
	defer logFile.Close()
	logger := slog.New(slog.NewJSONHandler(logFile, &slog.HandlerOptions{Level: slog.LevelDebug}))
	slog.SetDefault(logger)

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigChan
		fmt.Println("\n⚠️  Termination signal received! Initiating graceful shutdown...")
		cancel()
	}()

	dbUser := os.Getenv("DB_USER")
	if dbUser == "" {
		dbUser = "eriks"
	}
	dbPass := os.Getenv("DB_PASSWORD")
	if dbPass == "" {
		dbPass = "test"
	}

	connStr := fmt.Sprintf("postgres://%s:%s@localhost:5432/yafad_test?sslmode=disable", dbUser, dbPass)

	var hotPool, coldPool *pgxpool.Pool
	var err error

	fmt.Printf("⏳ Connecting to Database (User: %s)...\n", dbUser)

	for attempts := 1; attempts <= 10; attempts++ {
		hotPool, err = pgxpool.New(ctx, connStr)
		if err == nil {
			err = hotPool.Ping(ctx)
		}
		if err == nil {
			break
		}
		fmt.Printf("\r [Attempt %d/10 failed, retrying in 2s...] ", attempts)
		time.Sleep(2 * time.Second)
	}

	if err != nil {
		fmt.Printf("\n\n❌ FATAL: Could not connect to PostgreSQL Database.\n")
		os.Exit(1)
	}
	fmt.Println(" ✅ Connected!")
	defer hotPool.Close()

	coldPool, _ = pgxpool.New(ctx, connStr)
	defer coldPool.Close()

	router := &StorageRouter{HotPool: hotPool, ColdPool: coldPool}

	loadBrain()
	initConfig()

	go configWatcher(ctx)
	go brainWatcher(ctx)

	go func() {
		fmt.Println("📈 Starting Prometheus Metrics Server on :2112/metrics")
		http.Handle("/metrics", promhttp.Handler())
		if err := http.ListenAndServe(":2112", nil); err != nil && err != http.ErrServerClosed {
			slog.Error("Prometheus Server failed", "error", err)
		}
	}()

	rustCore := &cortex.RustCoreFFI{LibraryPath: "./libyafd_core.so"}
	brain := cortex.NewCortex("brain_data.json", rustCore)
	go func() {
		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				brain.Persist()
				return
			case <-ticker.C:
				brain.Persist()
			}
		}
	}()

	fmt.Println("🦁 YaFaD v0.9.0 Online. Waiting for Mission Command via Dashboard...")

	startMonitoringService(hotPool)
	go launchDashboard()

	// Hauptschleife (Tickt jede 1 Sekunde)
	ticker := time.NewTicker(1 * time.Second)
	workersStarted := false
	var wg sync.WaitGroup

	prevState := "UNKNOWN" // <--- NEU: Wir merken uns den vorherigen Zustand

	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			return
		case <-ticker.C:

			// === 1. DYNAMIC PHYSICS ("Breathing Architecture") ===
			biomass := getEstimatedBiomass(ctx, hotPool)

			configMu.RLock()
			wHigh := globalConfig.Watermarks.High
			wLow := globalConfig.Watermarks.Low
			cBuoy := globalConfig.BuoyancyFactor
			currentState := globalConfig.RunState // <--- Hier lesen wir den State richtig aus!
			configMu.RUnlock()

			newHigh, newLow, newBuoy, physicsChanged := adaptPhysics(wHigh, wLow, cBuoy, (currentState == "RUNNING"), biomass, 1.0)

			if physicsChanged {
				configMu.Lock()
				globalConfig.Watermarks.High = newHigh
				globalConfig.Watermarks.Low = newLow
				globalConfig.BuoyancyFactor = newBuoy
				saveConfigToJSON(globalConfig)
				configMu.Unlock()
			}
			// =======================================================

			// === 2. MISSION CONTROL LOGIC ===
			configMu.RLock()
			cpu := globalConfig.Limits.MaxCpuPercent
			totalRecords := globalConfig.InjectTotal
			flush := globalConfig.FlushOnStart
			configMu.RUnlock()

			// Reagiere auf JEDEN Übergang zu "RUNNING"
			if currentState == "RUNNING" && prevState != "RUNNING" {
				fmt.Printf("🚀 Command received: START MISSION (Target: %d)\n", totalRecords)

				if flush {
					fmt.Println("🧹 FLUSHING TABLES...")
					if _, err := hotPool.Exec(ctx, "TRUNCATE table0, table1, table2, table3, table4, deep_archive"); err != nil {
						slog.Error("Failed to flush tables", "error", err)
					} else {
						fmt.Println("✅ Tables flushed.")
					}
					configMu.Lock()
					globalConfig.FlushOnStart = false
					saveConfigToJSON(globalConfig)
					configMu.Unlock()
				}

				if !workersStarted {
					workersStarted = true
					maxCores := int(math.Ceil(float64(runtime.NumCPU()) * (float64(cpu) / 100.0)))
					if maxCores < 1 {
						maxCores = 1
					}
					runtime.GOMAXPROCS(maxCores)
					startWorkers(ctx, router, &wg)
				}

				if totalRecords > 0 {
					go runInjector(ctx, hotPool, totalRecords)
				}
			} else if currentState == "STOPPED" && workersStarted {
				fmt.Println("🛑 Command received: ABORT MISSION")
				cancel()
				return
			}

			prevState = currentState // <--- Jetzt merkt er sich den State sauber!
		}
	}
}

// --- WORKER LOGIC (Dynamic Buoyancy) ---
func runWorker(ctx context.Context, router *StorageRouter, tier int, pid *PIDController) {
	sourceTable := fmt.Sprintf("table%d", tier)
	nextTable := fmt.Sprintf("table%d", tier+1)
	if tier == 4 {
		nextTable = "deep_archive"
	}

	minSleep := 10 * time.Millisecond
	maxSleep := 2000 * time.Millisecond
	currentSleep := 100 * time.Millisecond
	errorBackoff := 1 * time.Second
	var prevCount int

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		configMu.RLock()
		pidParams := globalConfig.PID
		capacity := globalConfig.Capacities[sourceTable]
		targetRatio := globalConfig.TargetRatio
		vanishStr := globalConfig.VanishThreshold
		runState := globalConfig.RunState
		userBuoyancy := globalConfig.BuoyancyFactor
		configMu.RUnlock()

		vanishDur, _ := time.ParseDuration(vanishStr)
		if vanishDur == 0 {
			vanishDur = 1 * time.Hour
		}

		pid.UpdateParams(pidParams.Kp, pidParams.Ki, pidParams.Kd)

		sourcePool := router.GetPool(tier)
		targetPool := router.GetPool(tier + 1)

		var count int
		err := sourcePool.QueryRow(ctx, fmt.Sprintf("SELECT count(*) FROM %s", sourceTable)).Scan(&count)
		if err != nil {
			time.Sleep(errorBackoff)
			continue
		}

		velocity := float64(count - prevCount)
		prevCount = count
		pressure := float64(count) / float64(capacity)

		archiveGateClosed := false
		if tier == 4 && pressure < 0.90 {
			archiveGateClosed = true
		}

		lambda := 0.005

		brainMu.RLock()
		w := brainWeights
		brainMu.RUnlock()

		if w.WPressure != 0 {
			mlLambda := (w.WPressure * pressure) + (w.WVelocity * velocity) + w.Intercept
			if (runState == "IDLE" || runState == "SETTLING") && pressure < userBuoyancy {
				mlLambda = (w.WPressure * pressure) + (w.WVelocity * velocity) + (w.Intercept * 0.05)
			}
			lambda = mlLambda
		} else {
			pidOut := pid.Update(pressure, targetRatio)
			lambda = 0.005 + pidOut
		}

		// Dynamisches Überdruckventil (wHigh liegt z.B. bei 150.0 oder 100.0)
		if pressure > 1.00 {
			lambda = 0.5
		}

		if pressure < (targetRatio * userBuoyancy) {
			lambda = 0.0001
		}
		if archiveGateClosed {
			lambda = 0.00001
		}
		if lambda < 0.00001 {
			lambda = 0.00001
		}
		if lambda > 0.5 {
			lambda = 0.5
		}

		if tier == 0 {
			lambdaMu.Lock()
			t0Lambda = lambda
			lambdaMu.Unlock()
		}

		if count > 0 {
			throttleFactor := 1.0 - pressure
			if throttleFactor < 0 {
				throttleFactor = 0
			}
			adaptiveSleep := time.Duration(float64(minSleep) + (throttleFactor * float64(maxSleep-minSleep)))
			currentSleep = adaptiveSleep
			if archiveGateClosed {
				currentSleep = maxSleep
			}
		} else {
			currentSleep = maxSleep
		}

		if count > 0 {
			rows, err := sourcePool.Query(ctx, fmt.Sprintf("SELECT id, utility_index, last_activity, payload FROM %s LIMIT 1000", sourceTable))
			if err == nil {
				var batch []Record
				for rows.Next() {
					var r Record
					if err := rows.Scan(&r.ID, &r.UtilityIndex, &r.LastActivity, &r.Payload); err == nil {
						batch = append(batch, r)
					}
				}
				rows.Close()

				migratedCount := 0
				for _, r := range batch {
					dt := time.Since(r.LastActivity).Hours()
					uNew := float64(C.calculate_decay(C.double(r.UtilityIndex), C.double(lambda), C.double(dt)))

					shouldMigrate := false
					if tier == 4 {
						if !archiveGateClosed && (uNew < 0.4 || time.Since(r.LastActivity) > vanishDur) {
							shouldMigrate = true
						}
					} else {
						if (uNew < 0.4 && lambda > 0.001) || pressure > 1.00 {
							shouldMigrate = true
						}
					}

					if shouldMigrate {
						if migrateRecord(ctx, sourcePool, targetPool, sourceTable, nextTable, r.ID, r.Payload, uNew, r.LastActivity) {
							migratedCount++
						}
					}
				}

				if migratedCount > 500 {
					time.Sleep(10 * time.Millisecond)
				} else {
					time.Sleep(currentSleep)
				}
			} else {
				time.Sleep(errorBackoff)
			}
		} else {
			time.Sleep(maxSleep)
		}
	}
}

// --- INJECTOR ---
func runInjector(ctx context.Context, pool *pgxpool.Pool, total int) {
	fmt.Println("🔨 Compiling Generator...")
	exec.Command("go", "build", "-o", "yafad_sim", "generator.go").Run()

	configMu.RLock()
	done := globalConfig.InjectDone
	configMu.RUnlock()

	batchSize := 10000
	remaining := total - done
	if remaining <= 0 {
		fmt.Println("\n✅ Target already reached. Nothing to inject.")
		return
	}

	isDraining := false
	fmt.Printf("🚀 PULSE MISSION STARTED: Target %d Records (Remaining: %d)\n", total, remaining)

	for remaining > 0 {
		select {
		case <-ctx.Done():
			fmt.Println("\n🛑 Injection Aborted by User.")
			return
		default:
		}

		var t0Count int
		err := pool.QueryRow(ctx, "SELECT count(*) FROM table0").Scan(&t0Count)
		if err != nil {
			fmt.Println("⚠️ DB Error reading T0, retrying...")
			time.Sleep(1 * time.Second)
			continue
		}

		configMu.RLock()
		t0Cap := globalConfig.Capacities["table0"]
		wHigh := globalConfig.Watermarks.High
		wLow := globalConfig.Watermarks.Low
		if wHigh <= 0 {
			wHigh = 150.0
		}
		if wLow <= 0 {
			wLow = 120.0
		}
		configMu.RUnlock()

		if t0Cap == 0 {
			t0Cap = 100000
		}
		fillPct := (float64(t0Count) / float64(t0Cap)) * 100.0

		if !isDraining {
			if fillPct >= wHigh {
				isDraining = true
				fmt.Printf("\n🌊 T0 High Water Mark (%.1f%% >= %.1f%%). Switching to DRAIN Mode.\n", fillPct, wHigh)
				continue
			}

			currentBatch := batchSize
			if remaining < batchSize {
				currentBatch = remaining
			}
			offset := total - remaining

			fmt.Printf("\r🔥 Injecting... [%d left] T0: %.1f%% (Target %.0f%%)    ", remaining, fillPct, wHigh)

			cmd := exec.CommandContext(ctx, "./yafad_sim", "-count", fmt.Sprintf("%d", currentBatch), "-mode", "scenario", "-offset", fmt.Sprintf("%d", offset))
			if err := cmd.Run(); err != nil {
				fmt.Printf("❌ Sim Error: %v\n", err)
				time.Sleep(1 * time.Second)
			} else {
				remaining -= currentBatch
				configMu.Lock()
				globalConfig.InjectDone += currentBatch
				saveConfigToJSON(globalConfig)
				configMu.Unlock()
			}

		} else {
			if fillPct <= wLow {
				isDraining = false
				fmt.Printf("\n⚡ T0 Low Water Mark (%.1f%% <= %.1f%%). RESUMING INJECTION.\n", fillPct, wLow)
				continue
			}

			fmt.Printf("\r⏳ Draining... T0: %.1f%% (Target %.0f%%) -> Gravity active...    ", fillPct, wLow)
			time.Sleep(500 * time.Millisecond)
		}
	}

	os.Remove("yafad_sim")

	fmt.Println("\n🏁 INJECTION COMPLETE. Finalizing System...")
	configMu.Lock()
	globalConfig.RunState = "SETTLING"
	saveConfigToJSON(globalConfig)
	configMu.Unlock()
	time.Sleep(5 * time.Second)
	configMu.Lock()
	globalConfig.RunState = "IDLE"
	saveConfigToJSON(globalConfig)
	configMu.Unlock()
	fmt.Println("✅ MISSION ACCOMPLISHED.")
}

// --- HELPERS ---
func saveConfigToJSON(config SystemConfig) {
	data, _ := json.MarshalIndent(config, "", "  ")
	_ = os.WriteFile(CONFIG_FILE, data, 0644)
}

func migrateRecord(ctx context.Context, sP, tP *pgxpool.Pool, sT, tT, id, pl string, u float64, la time.Time) bool {
	_, err := tP.Exec(ctx, fmt.Sprintf("INSERT INTO %s (id, payload, utility_index, last_activity) VALUES ($1, $2, $3, $4) ON CONFLICT (id) DO NOTHING", tT), id, pl, u, la)
	if err != nil {
		return false
	}
	sP.Exec(ctx, fmt.Sprintf("DELETE FROM %s WHERE id = $1", sT), id)
	return true
}

func emergencyEvacuate(ctx context.Context, sourcePool, targetPool *pgxpool.Pool, sourceT, targetT string, limit int) error {
	rows, err := sourcePool.Query(ctx, fmt.Sprintf("SELECT id, payload, utility_index, last_activity FROM %s LIMIT %d", sourceT, limit))
	if err != nil {
		return err
	}
	var ids []string
	var data [][]interface{}
	for rows.Next() {
		var id, pl string
		var u float64
		var la time.Time
		if rows.Scan(&id, &pl, &u, &la) == nil {
			ids = append(ids, id)
			data = append(data, []interface{}{id, pl, u, la})
		}
	}
	rows.Close()
	if len(ids) == 0 {
		return nil
	}

	_, err = targetPool.CopyFrom(ctx, pgx.Identifier{targetT}, []string{"id", "payload", "utility_index", "last_activity"}, pgx.CopyFromRows(data))
	if err == nil {
		sourcePool.Exec(ctx, fmt.Sprintf("DELETE FROM %s WHERE id = ANY($1)", sourceT), ids)
	}
	return err
}

func loadBrain() {
	data, err := os.ReadFile(BRAIN_FILE)
	if err == nil {
		if json.Unmarshal(data, &brainWeights) == nil {
			fmt.Printf("🧠 Loaded Brain Weights: P:%.4f V:%.4f I:%.4f\n", brainWeights.WPressure, brainWeights.WVelocity, brainWeights.Intercept)
			return
		}
	}
	fmt.Println("⚠️  No Brain found (or invalid). Running in basic PID mode.")
}

func initConfig() {
	configMu.Lock()
	defer configMu.Unlock()
	data, err := os.ReadFile(CONFIG_FILE)
	if err == nil {
		if json.Unmarshal(data, &globalConfig) == nil {
			globalConfig.RunState = "IDLE" // Nach Neustart immer IDLE
			if globalConfig.BuoyancyFactor == 0 {
				globalConfig.BuoyancyFactor = 0.64
			}
			if globalConfig.Watermarks.High == 0 {
				globalConfig.Watermarks.High = 150.0
			}
			if globalConfig.Watermarks.Low == 0 {
				globalConfig.Watermarks.Low = 100.0
			}

			// FIX: Fallback, falls die JSON noch keine Tiers definiert hat!
			if len(globalConfig.ActiveTiers) == 0 {
				globalConfig.ActiveTiers = []int{0, 1, 2, 3, 4}
			}

			saveConfigToJSON(globalConfig)
			return
		}
	}

	// Fallback nur, wenn Datei komplett fehlt
	globalConfig = SystemConfig{
		RunState:       "IDLE",
		ActiveTiers:    []int{0, 1, 2, 3, 4}, // <--- NEU: Default Heavy Node
		PID:            PIDConfig{1.5, 0.05, 0.2},
		Limits:         ResourceLimits{MaxCpuPercent: 50},
		Capacities:     map[string]int{"table0": 100000},
		BuoyancyFactor: 0.64,
		Watermarks:     WatermarkConfig{150.0, 100.0},
		T0HardLimit:    100000,
	}
	saveConfigToJSON(globalConfig)
}

func configWatcher(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	lastMod := time.Time{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			stat, err := os.Stat(CONFIG_FILE)
			if err != nil {
				continue
			}
			if stat.ModTime().After(lastMod) {
				lastMod = stat.ModTime()
				data, err := os.ReadFile(CONFIG_FILE)
				if err == nil {
					var nc SystemConfig
					if json.Unmarshal(data, &nc) == nil {
						configMu.Lock()
						globalConfig.RunState = nc.RunState
						globalConfig.InjectTotal = nc.InjectTotal
						globalConfig.InjectDone = nc.InjectDone
						globalConfig.T0HardLimit = nc.T0HardLimit
						globalConfig.TargetRatio = nc.TargetRatio
						globalConfig.FlushOnStart = nc.FlushOnStart
						globalConfig.Capacities = nc.Capacities
						globalConfig.PID = nc.PID
						globalConfig.Limits = nc.Limits
						globalConfig.BuoyancyFactor = nc.BuoyancyFactor
						globalConfig.Watermarks = nc.Watermarks
						if len(nc.ActiveTiers) > 0 {
							globalConfig.ActiveTiers = nc.ActiveTiers
						} else {
							globalConfig.ActiveTiers = []int{0, 1, 2, 3, 4}
						}

						configMu.Unlock()
					}
				}
			}
		}
	}
}

func launchDashboard() {
	exec.Command("pkill", "-f", "dashboard.py").Run()
	time.Sleep(500 * time.Millisecond)

	pyBin := "python"
	if condaPrefix := os.Getenv("CONDA_PREFIX"); condaPrefix != "" {
		pyBin = condaPrefix + "/bin/python"
	} else {
		commonPaths := []string{
			"/home/eriks/anaconda3/envs/yafad_cockpit/bin/python",
			"/home/eriks/miniconda3/envs/yafad_cockpit/bin/python",
			"~/anaconda3/envs/yafad_cockpit/bin/python",
		}
		found := false
		for _, path := range commonPaths {
			if _, err := os.Stat(path); err == nil {
				pyBin = path
				found = true
				break
			}
		}
		if !found {
			if _, err := exec.LookPath("python3"); err == nil {
				pyBin = "python3"
			}
		}
	}
	fmt.Printf("🖥️  Launching Dashboard using: %s\n", pyBin)
	cmd := exec.Command(pyBin, "dashboard.py")
	cmd.Env = os.Environ()
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		fmt.Printf("⚠️  Could not start dashboard automatically: %v\n", err)
	} else {
		fmt.Println("✅ Dashboard started! Open http://localhost:7888")
	}
}

func brainWatcher(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	lastMod := time.Time{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			stat, err := os.Stat(BRAIN_FILE)
			if err != nil {
				continue
			}
			if stat.ModTime().After(lastMod) {
				lastMod = stat.ModTime()
				data, err := os.ReadFile(BRAIN_FILE)
				if err == nil {
					var newBrain BrainWeights
					if json.Unmarshal(data, &newBrain) == nil {
						brainMu.Lock()
						brainWeights = newBrain
						brainMu.Unlock()
						fmt.Printf("\n🧠 CORTEX UPGRADE DETECTED! New Weights loaded (P:%.4f V:%.4f)\n", newBrain.WPressure, newBrain.WVelocity)
					}
				}
			}
		}
	}
}

func startMonitoringService(pool *pgxpool.Pool) {
	configMu.RLock()
	caps := globalConfig.Capacities
	configMu.RUnlock()

	// --- BUGFIX: Schutz vor gelöschter CSV-Datei ---
	if _, err := os.Stat("yafad_metrics.csv"); os.IsNotExist(err) {
		fmt.Println("⚠️  yafad_metrics.csv fehlt! Erstelle eine neue Datei mit Headern...")
		f, _ := os.Create("yafad_metrics.csv")
		f.WriteString("timestamp,runtime_sec,total_biomass,t0,t1,t2,t3,t4,deep_archive,t0_pct,t1_pct,t2_pct,t3_pct,t4_pct,lambda,phi_diff\n")
		f.Close()
	}
	// -----------------------------------------------

	monCaps := make(map[string]float64)
	for k, v := range caps {
		monCaps[k] = float64(v)
	}

	getLambda := func() float64 {
		lambdaMu.RLock()
		defer lambdaMu.RUnlock()
		return t0Lambda
	}

	getSystemState := func() (int, int, bool, float64, float64, float64) {
		configMu.RLock()
		defer configMu.RUnlock()

		target := globalConfig.InjectTotal
		done := globalConfig.InjectDone
		isRunning := (globalConfig.RunState == "RUNNING")
		kp := globalConfig.PID.Kp
		ki := globalConfig.PID.Ki
		kd := globalConfig.PID.Kd

		return target, done, isRunning, kp, ki, kd
	}

	fmt.Println("📊 Monitoring active. Writing to yafad_metrics.csv & Prometheus :2112")

	go monitoring.StartMonitor(pool, monitoring.MonitorConfig{
		Interval: 5 * time.Second, TargetPhi: PHI, CSVFile: "yafad_metrics.csv", Capacities: monCaps,
	}, getLambda, getSystemState)
}

func startWorkers(ctx context.Context, router *StorageRouter, wg *sync.WaitGroup) {
	configMu.RLock()
	tiers := globalConfig.ActiveTiers
	configMu.RUnlock()

	// Die Standard-PID Werte für die verschiedenen Schichten (werden später dynamisch überschrieben)
	defaultPIDs := map[int]*PIDController{
		0: NewPID(1.5, 0.05, 0.2),
		1: NewPID(1.2, 0.05, 0.2),
		2: NewPID(0.8, 0.01, 0.1),
		3: NewPID(0.5, 0.01, 0.1),
		4: NewPID(0.2, 0.00, 0.0),
	}

	for _, tier := range tiers {
		wg.Add(1) // Dynamisch einen Worker zum WaitGroup hinzufügen

		pid, exists := defaultPIDs[tier]
		if !exists {
			pid = NewPID(0.5, 0.01, 0.1) // Fallback
		}

		t := tier // Shadowing für die Goroutine
		go func(workerTier int, workerPID *PIDController) {
			defer wg.Done()
			runWorker(ctx, router, workerTier, workerPID)
		}(t, pid)
	}
}
