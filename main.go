package main

/*
#cgo LDFLAGS: -L${SRCDIR}/core/target/release -lyafad_core -Wl,-rpath,${SRCDIR}/core/target/release -lm -ldl
#cgo CPPFLAGS: -I${SRCDIR}/core
extern double calculate_decay(double u_last, double lambda, double delta_t);
*/
import "C"
import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"YaFaD_ai/internal/cortex"
	"YaFaD_ai/internal/monitoring"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const PHI = 1.61803398875
const CONFIG_FILE = "yafad_config.json"
const PHI_SUM_FACTOR = 16.326
const ARCHITECTURE_HEADROOM = 1.20
const TARGET_RATIO_FIXED = 1.0

// --- CONFIG ---
type SystemConfig struct {
	Capacities      map[string]int `json:"capacities"`
	TargetRatio     float64        `json:"target_ratio"`
	SnifferActive   bool           `json:"sniffer_active"`
	VanishThreshold string         `json:"vanish_threshold"`
	LastUpdated     time.Time      `json:"last_updated"`
}

var (
	globalConfig SystemConfig
	configMu     sync.RWMutex
)

// --- PID ---
type PIDController struct {
	Kp, Ki, Kd float64
	Integral   float64
	PrevError  float64
	LastTime   time.Time
}

func NewPID(kp, ki, kd float64) *PIDController {
	return &PIDController{Kp: kp, Ki: ki, Kd: kd, LastTime: time.Now()}
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
	// 1. Context & Graceful Shutdown Setup
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	// Logger
	logPath := "/tmp/yafad_debug.log"
	logFile, _ := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0666)
	defer logFile.Close()
	logger := slog.New(slog.NewJSONHandler(logFile, &slog.HandlerOptions{Level: slog.LevelDebug}))
	slog.SetDefault(logger)

	// Shutdown Goroutine
	go func() {
		<-sigChan
		fmt.Println("\n⚠️  Termination signal received! Initiating graceful shutdown...")
		slog.Info("Shutting down gracefully... waiting for active workers to finish.")
		cancel() // Signalisiert allen Workern den Abbruch
	}()

	// DB Connection (with Retry/Backoff instead of Panic)
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

	fmt.Print("⏳ Connecting to Database...")
	for attempts := 1; attempts <= 10; attempts++ {
		hotPool, err = pgxpool.New(ctx, connStr)
		if err == nil {
			err = hotPool.Ping(ctx)
		}
		if err == nil {
			break
		}
		fmt.Printf(" [Attempt %d failed, retrying in 2s...]", attempts)
		time.Sleep(2 * time.Second)
	}

	if err != nil {
		fmt.Printf("\n❌ FATAL: Could not connect to DB after 10 attempts: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(" ✅ Connected!")
	defer hotPool.Close()

	coldPool, _ = pgxpool.New(ctx, connStr)
	defer coldPool.Close()

	router := &StorageRouter{HotPool: hotPool, ColdPool: coldPool}

	// WIZARD
	caps, injectCount := runSetupWizard(ctx, hotPool)

	// Config Init
	configMu.Lock()
	globalConfig = SystemConfig{
		Capacities:      caps,
		TargetRatio:     TARGET_RATIO_FIXED,
		SnifferActive:   true,
		VanishThreshold: "10m",
		LastUpdated:     time.Now(),
	}
	saveConfigToJSON(globalConfig)
	configMu.Unlock()

	go configWatcher(ctx)

	// Cortex
	rustCore := &cortex.RustCoreFFI{LibraryPath: "./libyafd_core.so"}
	brain := cortex.NewCortex("brain_data.json", rustCore)
	go func() {
		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				brain.Persist() // Letztes Speichern vorm Tod
				return
			case <-ticker.C:
				brain.Persist()
			}
		}
	}()
	fmt.Println("🧠 YaFaD_ai Cortex Online.")

	var (
		t0Lambda float64
		lambdaMu sync.RWMutex
	)
	reportT0Lambda := func(val float64) {
		lambdaMu.Lock()
		t0Lambda = val
		lambdaMu.Unlock()
	}
	getT0Lambda := func() float64 {
		lambdaMu.RLock()
		defer lambdaMu.RUnlock()
		return t0Lambda
	}

	var wg sync.WaitGroup
	wg.Add(5)

	// Workers (Pass Context for Cancellation)
	go func() {
		defer wg.Done()
		runHomeostaticWorker(ctx, router, brain, NewPID(1.5, 0.05, 0.2), 0, 0.005, 0.0001, 5.0, 1*time.Millisecond, 100*time.Millisecond, reportT0Lambda)
	}()
	go func() {
		defer wg.Done()
		runHomeostaticWorker(ctx, router, nil, NewPID(1.2, 0.05, 0.2), 1, 0.005, 0.0001, 3.0, 10*time.Millisecond, 500*time.Millisecond, nil)
	}()
	go func() {
		defer wg.Done()
		runHomeostaticWorker(ctx, router, nil, NewPID(0.8, 0.01, 0.1), 2, 0.005, 0.0001, 1.0, 50*time.Millisecond, 1*time.Second, nil)
	}()
	go func() {
		defer wg.Done()
		runHomeostaticWorker(ctx, router, nil, NewPID(0.5, 0.01, 0.1), 3, 0.005, 0.0001, 0.5, 1*time.Second, 10*time.Second, nil)
	}()
	go func() {
		defer wg.Done()
		runHomeostaticWorker(ctx, router, nil, NewPID(0.2, 0.0, 0.0), 4, 0.001, 0.0001, 0.1, 1*time.Second, 30*time.Second, nil)
	}()

	// Monitoring (Background Task)
	monCaps := make(map[string]float64)
	for k, v := range caps {
		monCaps[k] = float64(v)
	}

	// Wir feuern den Monitor einfach ab. Wenn main.go sich beendet (wg.Wait() der Worker ist fertig),
	// wird dieser Background-Prozess automatisch vom OS gekillt. Keine WaitGroup hierfür!
	go func() {
		monitoring.StartMonitor(hotPool, monitoring.MonitorConfig{
			Interval: 5 * time.Second, TargetPhi: PHI, CSVFile: "yafad_metrics.csv", Capacities: monCaps,
		}, getT0Lambda)
	}()

	// Injection
	if injectCount > 0 {
		go func() {
			time.Sleep(2 * time.Second)
			cmd := exec.CommandContext(ctx, "go", "run", "generator.go", "-count", fmt.Sprintf("%d", injectCount), "-mode", "scenario")
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			cmd.Run()
		}()
	}

	// Wir warten NUR noch auf die 5 Worker.
	wg.Wait()
	fmt.Println("👋 YaFaD has successfully and safely shut down.")
}

func configWatcher(ctx context.Context) {
	lastModTime := time.Time{}
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			fileInfo, err := os.Stat(CONFIG_FILE)
			if err != nil {
				continue
			}
			if fileInfo.ModTime().After(lastModTime) {
				lastModTime = fileInfo.ModTime()
				data, err := os.ReadFile(CONFIG_FILE)
				if err == nil {
					var newConfig SystemConfig
					if json.Unmarshal(data, &newConfig) == nil {
						configMu.Lock()
						globalConfig.TargetRatio = newConfig.TargetRatio
						globalConfig.VanishThreshold = newConfig.VanishThreshold
						configMu.Unlock()
						slog.Info("Config Hot-Reloaded", "target", newConfig.TargetRatio, "vanish", newConfig.VanishThreshold)
					}
				}
			}
		}
	}
}

func runSetupWizard(ctx context.Context, pool *pgxpool.Pool) (map[string]int, int) {
	reader := bufio.NewReader(os.Stdin)
	fmt.Println("╔══════════════════════════════════════════════════╗")
	fmt.Println("║ 🛡️  YaFaD ARCHITECT (v0.8.1 Resilient)           ║")
	fmt.Println("╚══════════════════════════════════════════════════╝")

	fmt.Print("❓ Flush tables? [y/N]: ")
	input, _ := reader.ReadString('\n')
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(input)), "y") {
		pool.Exec(ctx, "TRUNCATE table0, table1, table2, table3, table4, deep_archive")
		fmt.Println("🧹 Tables flushed.")
	}

	totalRecords := 500000
	injectAmount := 500000

	fmt.Printf("🌊 Inject Count [default %d]: ", totalRecords)
	inputInj, _ := reader.ReadString('\n')
	inputInj = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(inputInj, ".", ""), ",", ""))
	if val, err := strconv.Atoi(inputInj); err == nil && val > 0 {
		totalRecords = val
		injectAmount = val
	}

	targetVolume := float64(totalRecords) * ARCHITECTURE_HEADROOM
	baseCap := int(targetVolume / PHI_SUM_FACTOR)
	if baseCap < 1000 {
		baseCap = 1000
	}

	caps := make(map[string]int)
	caps["table0"] = baseCap
	caps["table1"] = int(float64(baseCap) * PHI)
	caps["table2"] = int(float64(caps["table1"]) * PHI)
	caps["table3"] = int(float64(caps["table2"]) * PHI)
	caps["table4"] = int(float64(caps["table3"]) * PHI)

	saveConfigToJSON(SystemConfig{
		Capacities: caps, TargetRatio: TARGET_RATIO_FIXED, SnifferActive: true,
		VanishThreshold: "10m",
		LastUpdated:     time.Now(),
	})

	fmt.Printf("🏗️  Configured T0: %d | Total: %d\n", caps["table0"], totalRecords)
	fmt.Println("🚀 Starting...")
	time.Sleep(1 * time.Second)
	return caps, injectAmount
}

func saveConfigToJSON(config SystemConfig) {
	file, _ := json.MarshalIndent(config, "", "  ")
	_ = os.WriteFile(CONFIG_FILE, file, 0644)
}

// --- WORKER (With Context & Exponential Backoff) ---
func runHomeostaticWorker(ctx context.Context, router *StorageRouter, brain *cortex.Cortex, pid *PIDController, startTier int, baseLambda, min, max float64, minSleep, maxSleep time.Duration, reportLambda func(float64)) {
	currentLambda := baseLambda
	threshold := 0.4
	currentSleep := maxSleep
	errorBackoff := 1 * time.Second // Start Backoff für DB Fehler

	sourceTier := startTier
	targetTier := startTier + 1
	sourceTable := fmt.Sprintf("table%d", sourceTier)
	targetTable := fmt.Sprintf("table%d", targetTier)
	if startTier == 4 {
		targetTable = "deep_archive"
	}

	bellyTier := targetTier + 1
	bellyTable := fmt.Sprintf("table%d", bellyTier)
	if targetTier >= 4 {
		bellyTable = ""
	}

	baseBatchSize := 1000
	lastObservation := time.Now()

	for {
		// 1. Graceful Shutdown Check
		select {
		case <-ctx.Done():
			slog.Info("Worker stopped gracefully.", "tier", sourceTable)
			return
		default:
		}

		configMu.RLock()
		targetRatio := globalConfig.TargetRatio
		idealCapacity := globalConfig.Capacities[sourceTable]
		vanishStr := globalConfig.VanishThreshold
		configMu.RUnlock()

		vanishDur, _ := time.ParseDuration(vanishStr)
		if vanishDur == 0 {
			vanishDur = 1 * time.Hour
		}

		sourcePool := router.GetPool(sourceTier)
		targetPool := router.GetPool(targetTier)

		var sourceCount int
		err := sourcePool.QueryRow(ctx, fmt.Sprintf("SELECT count(*) FROM %s", sourceTable)).Scan(&sourceCount)

		// 2. Exponential Backoff bei DB Verbindungsverlust
		if err != nil {
			slog.Warn("DB Error, applying backoff...", "tier", sourceTable, "err", err, "sleep", errorBackoff)
			time.Sleep(errorBackoff)
			errorBackoff *= 2
			if errorBackoff > 60*time.Second {
				errorBackoff = 60 * time.Second
			}
			continue // Nächster Versuch
		} else {
			errorBackoff = 1 * time.Second // Reset bei Erfolg
		}

		bellyFactor := 1.0
		if bellyTable != "" {
			var bellyCount int
			if router.GetPool(bellyTier).QueryRow(ctx, fmt.Sprintf("SELECT count(*) FROM %s", bellyTable)).Scan(&bellyCount) == nil {
				bellyCap := float64(idealCapacity) * PHI * PHI
				if bellyCap > 0 {
					bellyRatio := float64(bellyCount) / bellyCap
					if bellyRatio > 1.05 {
						bellyFactor = 0.5
					}
				}
			}
		}

		pressure := float64(sourceCount) / float64(idealCapacity)

		buoyancyLimit := targetRatio * 0.90
		if startTier <= 1 && pressure < buoyancyLimit {
			currentLambda = min
		} else {
			pidOutput := pid.Update(pressure, targetRatio)
			currentLambda = baseLambda + pidOutput
		}
		currentLambda *= bellyFactor

		if brain != nil {
			if time.Since(lastObservation) > 10*time.Second {
				brain.Observe(currentLambda)
				lastObservation = time.Now()
			}
			pred := brain.Predict(1)
			if pred > currentLambda*1.5 {
				currentLambda = (currentLambda + pred) / 2
			}
		}

		if currentLambda < min {
			currentLambda = min
		}
		if currentLambda > max {
			currentLambda = max
		}

		if pressure > targetRatio*3.0 {
			emergencyEvacuate(ctx, sourcePool, targetPool, sourceTable, targetTable, 40000)
		}

		if reportLambda != nil {
			reportLambda(currentLambda)
		}

		if sourceCount > 0 {
			rows, err := sourcePool.Query(ctx, fmt.Sprintf("SELECT id, utility_index, last_activity, payload FROM %s LIMIT %d", sourceTable, baseBatchSize))
			if err == nil {
				type Record struct {
					ID string
					U  float64
					LA time.Time
					PL string
				}
				var batch []Record
				for rows.Next() {
					var r Record
					if rows.Scan(&r.ID, &r.U, &r.LA, &r.PL) == nil {
						batch = append(batch, r)
					}
				}
				rows.Close()

				for _, r := range batch {
					deltaT := time.Since(r.LA).Hours()
					uNow := float64(C.calculate_decay(C.double(r.U), C.double(currentLambda), C.double(deltaT)))

					if startTier == 4 {
						isOld := time.Since(r.LA) > vanishDur
						if isOld && uNow < 0.8 {
							uNow = 0.001
						}
					}

					forceFlush := pressure > targetRatio*1.2
					if uNow < threshold || forceFlush {
						migrateRecord(ctx, sourcePool, targetPool, sourceTable, targetTable, r.ID, r.PL, uNow, r.LA)
						currentSleep /= 2
						if currentSleep < minSleep {
							currentSleep = minSleep
						}
					}
				}
			} else {
				// DB Read Fehler beim Fetch
				slog.Warn("DB Read Error in Batch", "tier", sourceTable, "err", err)
				time.Sleep(errorBackoff)
			}
		} else {
			currentSleep *= 2
			if currentSleep > maxSleep {
				currentSleep = maxSleep
			}
		}

		// Letzter Check vorm Sleep (damit Shutdown schnell geht)
		select {
		case <-ctx.Done():
			return
		case <-time.After(currentSleep):
			// Weiter im Loop
		}
	}
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

func migrateRecord(ctx context.Context, sP, tP *pgxpool.Pool, sT, tT, id, pl string, u float64, la time.Time) bool {
	_, err := tP.Exec(ctx, fmt.Sprintf("INSERT INTO %s (id, payload, utility_index, last_activity) VALUES ($1, $2, $3, $4) ON CONFLICT (id) DO NOTHING", tT), id, pl, u, la)
	if err != nil {
		return false
	}
	sP.Exec(ctx, fmt.Sprintf("DELETE FROM %s WHERE id = $1", sT), id)
	return true
}

func randInt(min, max int) int { return min + rand.Intn(max-min+1) }
