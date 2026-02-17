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

// NEU: Konfiguration für den Pulse-Mode
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

// --- MAIN ---
func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 1. Logging Setup
	logPath := "/tmp/yafad_debug.log"
	logFile, _ := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0666)
	defer logFile.Close()
	logger := slog.New(slog.NewJSONHandler(logFile, &slog.HandlerOptions{Level: slog.LevelDebug}))
	slog.SetDefault(logger)

	// 2. Signal Handling
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigChan
		fmt.Println("\n⚠️  Termination signal received! Initiating graceful shutdown...")
		cancel()
	}()

	// 3. Database Connection
	dbUser := os.Getenv("DB_USER")
	if dbUser == "" {
		dbUser = "eriks" // Default fallback
	}
	dbPass := os.Getenv("DB_PASSWORD")
	if dbPass == "" {
		dbPass = "test" // Default fallback
	}
	// SSL Mode disable ist wichtig für lokale Dev-Umgebungen
	connStr := fmt.Sprintf("postgres://%s:%s@localhost:5432/yafad_test?sslmode=disable", dbUser, dbPass)

	var hotPool, coldPool *pgxpool.Pool
	var err error

	fmt.Printf("⏳ Connecting to Database (User: %s)...\n", dbUser)

	// Retry Loop
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

	// HELP MESSAGE ON FAILURE
	if err != nil {
		fmt.Printf("\n\n❌ FATAL: Could not connect to PostgreSQL Database.\n")
		fmt.Printf("   Error details: %v\n\n", err)

		fmt.Println("💡 TROUBLESHOOTING:")
		fmt.Println("   1. Is PostgreSQL running? (sudo systemctl status postgresql)")
		fmt.Println("   2. Does the database 'yafad_test' exist?")
		fmt.Println("   3. Are the credentials correct?")

		fmt.Println("\n🔑 HOW TO SET CUSTOM CREDENTIALS:")
		fmt.Println("   Linux/Mac (Bash):")
		fmt.Println("     export DB_USER=myuser")
		fmt.Println("     export DB_PASSWORD=mypassword")
		fmt.Println("     go run main.go")

		fmt.Println("\n   Windows (PowerShell):")
		fmt.Println("     $env:DB_USER=\"myuser\"")
		fmt.Println("     $env:DB_PASSWORD=\"mypassword\"")
		fmt.Println("     go run main.go")

		fmt.Println("\n   ...or edit the defaults in main.go directly.")
		os.Exit(1)
	}
	fmt.Println(" ✅ Connected!")
	defer hotPool.Close()

	coldPool, _ = pgxpool.New(ctx, connStr)
	defer coldPool.Close()

	router := &StorageRouter{HotPool: hotPool, ColdPool: coldPool}

	// 4. Load Brain & Config
	loadBrain()
	initConfig()

	// 5. Start Config Watcher (Hot Reload)
	go configWatcher(ctx)
	go brainWatcher(ctx)

	// 6. Start Metrics Server
	go func() {
		fmt.Println("📈 Starting Prometheus Metrics Server on :2112/metrics")
		http.Handle("/metrics", promhttp.Handler())
		if err := http.ListenAndServe(":2112", nil); err != nil && err != http.ErrServerClosed {
			slog.Error("Prometheus Server failed", "error", err)
		}
	}()

	// 7. Rust Core Init (for Persistence only)
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

	// --- MAIN EVENT LOOP ---
	fmt.Println("🦁 YaFaD v0.9.0 Online. Waiting for Mission Command via Dashboard...")

	startMonitoringService(hotPool)
	go launchDashboard()

	ticker := time.NewTicker(1 * time.Second)
	workersStarted := false
	var wg sync.WaitGroup

	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			return
		case <-ticker.C:
			configMu.RLock()
			state := globalConfig.RunState
			cpu := globalConfig.Limits.MaxCpuPercent
			totalRecords := globalConfig.InjectTotal
			flush := globalConfig.FlushOnStart
			configMu.RUnlock()

			if state == "RUNNING" && !workersStarted {
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

				workersStarted = true
				maxCores := int(math.Ceil(float64(runtime.NumCPU()) * (float64(cpu) / 100.0)))
				if maxCores < 1 {
					maxCores = 1
				}
				runtime.GOMAXPROCS(maxCores)

				wg.Add(5)
				startWorkers(ctx, router, &wg)

				if totalRecords > 0 {
					go runInjector(ctx, hotPool, totalRecords)
				}
			} else if state == "STOPPED" && workersStarted {
				fmt.Println("🛑 Command received: ABORT MISSION")
				cancel()
				return
			}
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

	// ECO-MODE
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

		// 1. ARCHIVE GATEKEEPER
		archiveGateClosed := false
		if tier == 4 && pressure < 0.90 {
			archiveGateClosed = true
		}

		lambda := 0.005

		// BRAIN INTEGRATION WITH CLAMP
		brainMu.RLock()
		w := brainWeights
		brainMu.RUnlock()

		if w.WPressure != 0 {
			mlLambda := (w.WPressure * pressure) + (w.WVelocity * velocity) + w.Intercept

			// CLAMP: Wenn wir im Leerlauf sind UND der User eine hohe Buoyancy will,
			// zwingen wir den Intercept in die Knie.
			if (runState == "IDLE" || runState == "SETTLING") && pressure < userBuoyancy {
				// Intercept fast ausblenden
				mlLambda = (w.WPressure * pressure) + (w.WVelocity * velocity) + (w.Intercept * 0.05)
			}
			lambda = mlLambda
		} else {
			pidOut := pid.Update(pressure, targetRatio)
			lambda = 0.005 + pidOut
		}

		// 2. USER CONTROLLED BUOYANCY
		if pressure > 1.05 {
			lambda = 0.5
		}

		// Wenn Druck kleiner als User-Setting -> Lambda AUS (Schwimmweste aktiv)
		// Das targetRatio ist meistens 1.0, also ist userBuoyancy der Prozentwert (0.7 = 70%)
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

		// 3. ECO-THROTTLE
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

		// MIGRATION
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
						// Migration nur wenn Lambda aktiv ist oder Druck kritisch
						if (uNew < 0.4 && lambda > 0.001) || pressure > 1.05 {
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

// --- INJECTOR (Sawtooth / Pulse Mode - Dynamic Watermarks) ---
func runInjector(ctx context.Context, pool *pgxpool.Pool, total int) {
	fmt.Println("🔨 Compiling Generator...")
	exec.Command("go", "build", "-o", "yafad_sim", "generator.go").Run()

	configMu.Lock()
	globalConfig.InjectDone = 0
	configMu.Unlock()

	batchSize := 10000
	remaining := total
	isDraining := false

	fmt.Printf("🚀 PULSE MISSION STARTED: Target %d Records\n", total)

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

		// DYNAMISCHE CONFIG: Watermarks hier live lesen!
		configMu.RLock()
		t0Cap := globalConfig.Capacities["table0"]
		globalConfig.PID.Kp = 2.0
		globalConfig.PID.Ki = 0.1

		// Watermarks holen
		wHigh := globalConfig.Watermarks.High
		wLow := globalConfig.Watermarks.Low

		// Fallback Safety (falls JSON leer)
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

		// Pulse Logic mit dynamischen Watermarks
		if !isDraining {
			// PHASE: FÜLLEN
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
			// PHASE: DRAIN
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
	globalConfig.PID.Kp = 1.0
	saveConfigToJSON(globalConfig)
	configMu.Unlock()
	time.Sleep(5 * time.Second)
	configMu.Lock()
	globalConfig.RunState = "IDLE"
	saveConfigToJSON(globalConfig)
	configMu.Unlock()
	fmt.Println("✅ MISSION ACCOMPLISHED.")
}

// --- HELPERS (Existing ones + Bulletproof Launcher) ---
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
			globalConfig.RunState = "IDLE"
			// Ensure defaults if keys missing
			if globalConfig.BuoyancyFactor == 0 {
				globalConfig.BuoyancyFactor = 0.7
			}
			if globalConfig.Watermarks.High == 0 {
				globalConfig.Watermarks.High = 150.0
			}
			if globalConfig.Watermarks.Low == 0 {
				globalConfig.Watermarks.Low = 120.0
			}

			saveConfigToJSON(globalConfig)
			return
		}
	}
	// FALLBACK: Wenn keine Config existiert
	globalConfig = SystemConfig{
		RunState:       "IDLE",
		PID:            PIDConfig{1.5, 0.05, 0.2},
		Limits:         ResourceLimits{MaxCpuPercent: 50},
		Capacities:     map[string]int{"table0": 100000},
		BuoyancyFactor: 0.7,
		Watermarks:     WatermarkConfig{150.0, 120.0},
		T0HardLimit:    100000, // Hier setzen wir den Default für frische Installationen
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
						// Alle Felder kopieren
						globalConfig.RunState = nc.RunState
						globalConfig.InjectTotal = nc.InjectTotal
						globalConfig.InjectDone = nc.InjectDone
						globalConfig.T0HardLimit = nc.T0HardLimit // NEU
						globalConfig.TargetRatio = nc.TargetRatio
						globalConfig.FlushOnStart = nc.FlushOnStart
						globalConfig.Capacities = nc.Capacities
						globalConfig.PID = nc.PID
						globalConfig.Limits = nc.Limits
						globalConfig.BuoyancyFactor = nc.BuoyancyFactor
						globalConfig.Watermarks = nc.Watermarks
						configMu.Unlock()
					}
				}
			}
		}
	}
}

// LAUNCHER HELPER
func launchDashboard() {
	// 1. ZOMBIE KILLER
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
	go func() { defer wg.Done(); runWorker(ctx, router, 0, NewPID(1.5, 0.05, 0.2)) }()
	go func() { defer wg.Done(); runWorker(ctx, router, 1, NewPID(1.2, 0.05, 0.2)) }()
	go func() { defer wg.Done(); runWorker(ctx, router, 2, NewPID(0.8, 0.01, 0.1)) }()
	go func() { defer wg.Done(); runWorker(ctx, router, 3, NewPID(0.5, 0.01, 0.1)) }()
	go func() { defer wg.Done(); runWorker(ctx, router, 4, NewPID(0.2, 0.00, 0.0)) }()
}
