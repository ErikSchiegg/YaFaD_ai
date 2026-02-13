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
	"math"
	"math/rand"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
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
const PHI_SUM_FACTOR = 16.326
const ARCHITECTURE_HEADROOM = 1.20
const TARGET_RATIO_FIXED = 1.0

// --- CONFIG ---
type ResourceLimits struct {
	MaxCpuPercent int `json:"max_cpu_percent"`
}

type SystemConfig struct {
	Capacities      map[string]int `json:"capacities"`
	TargetRatio     float64        `json:"target_ratio"`
	SnifferActive   bool           `json:"sniffer_active"`
	VanishThreshold string         `json:"vanish_threshold"`
	Limits          ResourceLimits `json:"limits"`
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
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	logPath := "/tmp/yafad_debug.log"
	logFile, _ := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0666)
	defer logFile.Close()
	logger := slog.New(slog.NewJSONHandler(logFile, &slog.HandlerOptions{Level: slog.LevelDebug}))
	slog.SetDefault(logger)

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

	caps, injectCount, cpuPercent := runSetupWizard(ctx, hotPool)

	maxCores := int(math.Ceil(float64(runtime.NumCPU()) * (float64(cpuPercent) / 100.0)))
	if maxCores < 1 {
		maxCores = 1
	}
	runtime.GOMAXPROCS(maxCores)
	slog.Info("Hardware Throttling Active", "max_cores", maxCores, "cpu_percent", cpuPercent)

	configMu.Lock()
	globalConfig = SystemConfig{
		Capacities:      caps,
		TargetRatio:     TARGET_RATIO_FIXED,
		SnifferActive:   true,
		VanishThreshold: "10m",
		Limits:          ResourceLimits{MaxCpuPercent: cpuPercent},
		LastUpdated:     time.Now(),
	}
	saveConfigToJSON(globalConfig)
	configMu.Unlock()

	go configWatcher(ctx)

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

	// WORKERS
	go func() {
		defer wg.Done()
		// T0 Worker: Aggressive Start at 30% Pressure
		runHomeostaticWorker(ctx, router, brain, NewPID(1.5, 0.05, 0.2), 0, 0.005, 0.0001, 0.8, 1*time.Millisecond, 100*time.Millisecond, reportT0Lambda)
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

	monCaps := make(map[string]float64)
	for k, v := range caps {
		monCaps[k] = float64(v)
	}

	go func() {
		monitoring.StartMonitor(hotPool, monitoring.MonitorConfig{
			Interval: 5 * time.Second, TargetPhi: PHI, CSVFile: "yafad_metrics.csv", Capacities: monCaps,
		}, getT0Lambda)
	}()

	go func() {
		fmt.Println("📈 Starting Prometheus Metrics Server on :2112/metrics")
		http.Handle("/metrics", promhttp.Handler())
		if err := http.ListenAndServe(":2112", nil); err != nil && err != http.ErrServerClosed {
			slog.Error("Prometheus Server failed", "error", err)
		}
	}()

	// INJECTION
	if injectCount > 0 {
		go func() {
			time.Sleep(2 * time.Second)

			fmt.Println("🔨 Compiling Generator...")
			buildCmd := exec.Command("go", "build", "-o", "yafad_sim", "generator.go")
			if buildErr := buildCmd.Run(); buildErr != nil {
				slog.Error("❌ FATAL: Failed to compile generator", "error", buildErr)
				return
			}
			fmt.Println("✅ Generator compiled. Starting Robust Batch Injection...")

			batchSize := 250000
			remaining := injectCount
			batchNum := 1
			totalStart := time.Now()

			for remaining > 0 {
				currentBatch := batchSize
				if remaining < batchSize {
					currentBatch = remaining
				}

				fmt.Printf("\n🚀 [Batch %d] Injecting %d records... (Remaining: %d)\n", batchNum, currentBatch, remaining-currentBatch)

				currentOffset := injectCount - remaining
				batchCtx, batchCancel := context.WithTimeout(ctx, 15*time.Minute)

				cmd := exec.CommandContext(batchCtx, "./yafad_sim",
					"-count", fmt.Sprintf("%d", currentBatch),
					"-mode", "scenario",
					"-offset", fmt.Sprintf("%d", currentOffset))

				logFile, _ := os.Create(fmt.Sprintf("batch_%d.log", batchNum))
				cmd.Stdout = logFile
				cmd.Stderr = logFile

				err := cmd.Run()
				logFile.Close()
				batchCancel()

				if ctx.Err() == context.DeadlineExceeded {
					slog.Error("❌ BATCH TIMEOUT! Generator was hung and killed.", "batch", batchNum)
					break
				}

				if err != nil {
					slog.Error("❌ Batch failed (Crash/Error)!", "batch", batchNum, "error", err)
					fmt.Printf("   -> Check 'batch_%d.log' for details.\n", batchNum)
					break
				}

				remaining -= currentBatch
				batchNum++

				if remaining > 0 {
					var t0C int
					hotPool.QueryRow(ctx, "SELECT count(*) FROM table0").Scan(&t0C)
					t0Cap := float64(globalConfig.Capacities["table0"])
					fillLevel := 0.0
					if t0Cap > 0 {
						fillLevel = float64(t0C) / t0Cap
					}

					// WAIT LOGIC: Only wait if T0 is actually getting full (>80%)
					if fillLevel < 0.8 {
						fmt.Printf("🌊 T0 Level %.1f%% (<80%%) - Skipping Wait to build pressure...\n", fillLevel*100)
					} else {
						waitForStabilization(ctx, hotPool, 0.1)
					}
				}
			}

			totalDuration := time.Since(totalStart)
			realTotal := injectCount - remaining
			avgSpeed := 0.0
			if totalDuration.Seconds() > 0 {
				avgSpeed = float64(realTotal) / totalDuration.Seconds()
			}

			report := fmt.Sprintf("\nDONE. Injected: %d in %v (Avg: %.0f ops/sec)\n",
				realTotal, totalDuration, avgSpeed)
			fmt.Print(report)
			_ = os.WriteFile("time_taken.txt", []byte(report), 0644)

			os.Remove("yafad_sim")
		}()
	}

	wg.Wait()
	fmt.Println("👋 YaFaD has successfully and safely shut down.")
}

// HIER IST DIE FEHLENDE FUNKTION:
func saveConfigToJSON(config SystemConfig) {
	file, _ := json.MarshalIndent(config, "", "  ")
	_ = os.WriteFile(CONFIG_FILE, file, 0644)
}

func waitForStabilization(ctx context.Context, pool *pgxpool.Pool, targetDiff float64) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	timeoutCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()

	fmt.Printf("⚖️  Stabilizing System (Waiting for Phi-Diff < %.2f)...\n", targetDiff)

	for {
		select {
		case <-timeoutCtx.Done():
			fmt.Println("\n⚠️  Stabilization Timeout (30m). Forcing next batch...")
			return
		case <-ticker.C:
			diff := calculateCurrentPhiDiff(timeoutCtx, pool)
			statusIcon := "⏳"
			if diff < 0.2 {
				statusIcon = "🤞"
			}
			// FIX 2: Println instead of \r
			fmt.Printf("%s Waiting for homeostasis... Current Phi-Diff: %.4f\n", statusIcon, diff)
			if diff < targetDiff {
				fmt.Printf("✅ System Stabilized (Phi-Diff: %.4f). Proceeding...\n", diff)
				return
			}
		}
	}
}

func calculateCurrentPhiDiff(ctx context.Context, pool *pgxpool.Pool) float64 {
	var counts []int
	for i := 0; i < 5; i++ {
		var c int
		pool.QueryRow(ctx, fmt.Sprintf("SELECT count(*) FROM table%d", i)).Scan(&c)
		counts = append(counts, c)
	}

	var totalDiff float64
	var countRatios int

	t0Capacity := float64(globalConfig.Capacities["table0"])
	if t0Capacity > 0 {
		t0Fill := float64(counts[0]) / t0Capacity
		if t0Fill > 0.8 && counts[1] < 1000 {
			// Stuck input buffer -> High Error
			return 2.0
		}
	}

	for i := 0; i < 4; i++ {
		cCurrent := float64(counts[i])
		cNext := float64(counts[i+1])

		if cCurrent > 5000 {
			if cNext < 100 {
				totalDiff += 1.0
				countRatios++
				continue
			}
			capCurrent := float64(globalConfig.Capacities[fmt.Sprintf("table%d", i)])
			capNext := float64(globalConfig.Capacities[fmt.Sprintf("table%d", i+1)])

			if capCurrent > 0 && capNext > 0 {
				fillCurrent := cCurrent / capCurrent
				fillNext := cNext / capNext
				diff := math.Abs(fillCurrent - fillNext)
				totalDiff += diff
				countRatios++
			}
		}
	}

	if countRatios == 0 {
		if counts[0] == 0 {
			return 0.0
		}
		return 0.5
	}

	return totalDiff / float64(countRatios)
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

func runSetupWizard(ctx context.Context, pool *pgxpool.Pool) (map[string]int, int, int) {
	reader := bufio.NewReader(os.Stdin)
	fmt.Println("╔══════════════════════════════════════════════════╗")
	fmt.Println("║ 🛡️  YaFaD ARCHITECT & RESOURCE BROKER            ║")
	fmt.Println("╚══════════════════════════════════════════════════╝")

	fmt.Print("❓ Flush tables? [y/N]: ")
	input, _ := reader.ReadString('\n')
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(input)), "y") {
		pool.Exec(ctx, "TRUNCATE table0, table1, table2, table3, table4, deep_archive")
		fmt.Println("🧹 Tables flushed.")
	}

	totalRecords := 500000
	fmt.Printf("🌊 Inject/Migration Count [default %d]: ", totalRecords)
	inputInj, _ := reader.ReadString('\n')
	inputInj = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(inputInj, ".", ""), ",", ""))
	if val, err := strconv.Atoi(inputInj); err == nil && val > 0 {
		totalRecords = val
	}

	numCores := runtime.NumCPU()
	cpuPercent := 50

	for {
		fmt.Printf("\n💻 System Analysis: Detected %d CPU cores.\n", numCores)
		fmt.Printf("❓ Max CPU usage allowed for YaFaD (1-100%%) [default %d]: ", cpuPercent)
		inputCpu, _ := reader.ReadString('\n')
		inputCpu = strings.TrimSpace(inputCpu)

		if inputCpu != "" {
			if val, err := strconv.Atoi(inputCpu); err == nil && val > 0 && val <= 100 {
				cpuPercent = val
			} else {
				fmt.Println("❌ Invalid input. Please enter a percentage between 1 and 100.")
				continue
			}
		}

		effectiveCores := float64(numCores) * (float64(cpuPercent) / 100.0)
		if effectiveCores < 0.5 {
			effectiveCores = 0.5
		}

		realWorldSpeed := MeasureSystemPulse(ctx, pool)
		fmt.Print("❓ Enter target max biomass (e.g., 100000): ")
		var targetBiomass int
		fmt.Scanln(&targetBiomass)

		complexityFactor := 27.0
		sustainedSpeed := realWorldSpeed / complexityFactor
		estimatedSeconds := float64(targetBiomass) / sustainedSpeed
		estimatedDuration := time.Duration(estimatedSeconds) * time.Second

		fmt.Printf("\n📊 PREDICTION (Real-World Complexity x%.0f):\n", complexityFactor)
		fmt.Printf("   • Raw Burst Speed:     %.0f ops/sec (Sensor)\n", realWorldSpeed)
		fmt.Printf("   • Est. Sustained Flow: ~%.0f items/sec (Homeostasis Mode)\n", sustainedSpeed)
		fmt.Printf("   • Time to %d records: %s\n", targetBiomass, estimatedDuration)
		fmt.Println("--------------------------------")

		fmt.Print("\n❓ Accept this resource plan? (Y to accept / N to adjust): ")
		confirm, _ := reader.ReadString('\n')
		confirm = strings.ToLower(strings.TrimSpace(confirm))
		if confirm == "" || strings.HasPrefix(confirm, "y") {
			break
		}
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

	fmt.Printf("\n🏗️  Configured T0: %d | Total Headroom: %.0f\n", caps["table0"], targetVolume)
	fmt.Println("🚀 Initializing Organism...")
	time.Sleep(1 * time.Second)

	return caps, totalRecords, cpuPercent
}

func runHomeostaticWorker(ctx context.Context, router *StorageRouter, brain *cortex.Cortex, pid *PIDController, startTier int, baseLambda, min, max float64, minSleep, maxSleep time.Duration, reportLambda func(float64)) {
	currentLambda := baseLambda
	threshold := 0.4
	currentSleep := maxSleep
	errorBackoff := 1 * time.Second

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

		if err != nil {
			slog.Warn("DB Error, applying backoff...", "tier", sourceTable, "err", err, "sleep", errorBackoff)
			time.Sleep(errorBackoff)
			errorBackoff *= 2
			if errorBackoff > 60*time.Second {
				errorBackoff = 60 * time.Second
			}
			continue
		} else {
			errorBackoff = 1 * time.Second
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

		// FIX 3: Turbo Charge for overflowing Tiers
		// If pressure is > 100%, we override PID and force flow!
		if pressure > 1.05 {
			currentLambda = 0.5 // Force fast decay
		} else if startTier <= 1 && pressure < (targetRatio*0.30) {
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
				slog.Warn("DB Read Error in Batch", "tier", sourceTable, "err", err)
				time.Sleep(errorBackoff)
			}
		} else {
			currentSleep *= 2
			if currentSleep > maxSleep {
				currentSleep = maxSleep
			}
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(currentSleep):
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

// MeasureSystemPulse
func MeasureSystemPulse(ctx context.Context, pool *pgxpool.Pool) float64 {
	fmt.Print("   sensor: Calibrating storage I/O speed... ")

	testBatchSize := 50
	start := time.Now()

	tx, err := pool.Begin(ctx)
	if err != nil {
		fmt.Printf("Error starting tx: %v\n", err)
		return 1000.0
	}
	defer tx.Rollback(ctx)

	sql := "INSERT INTO table0 (id, payload, utility_index, last_activity) VALUES ($1, $2, $3, $4)"
	dummyJSON := `{"type": "sensor_probe", "data": "calibration"}`

	for i := 0; i < testBatchSize; i++ {
		id := fmt.Sprintf("sensor_probe_%d", i)
		_, err := tx.Exec(ctx, sql, id, dummyJSON, 1.0, time.Now())
		if err != nil {
			fmt.Printf("Write error: %v\n", err)
			return 1000.0
		}
	}

	duration := time.Since(start)
	opsPerSecond := float64(testBatchSize) / duration.Seconds()

	fmt.Printf("DONE. (%.0f ops/sec)\n", opsPerSecond)
	return opsPerSecond
}
