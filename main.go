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
	"fmt"
	"log/slog"
	"math/rand"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"YaFaD_ai/internal/cortex"
	"YaFaD_ai/internal/monitoring"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const PHI = 1.61803398875

// --- PID Controller ---
type PIDController struct {
	Kp, Ki, Kd float64
	SetPoint   float64 // Ziel-Füllstand (1.0 = 100%)
	Integral   float64
	PrevError  float64
	LastTime   time.Time
}

func NewPID(kp, ki, kd, setpoint float64) *PIDController {
	return &PIDController{
		Kp:       kp,
		Ki:       ki,
		Kd:       kd,
		SetPoint: setpoint,
		LastTime: time.Now(),
	}
}

func (pid *PIDController) Update(currentVal float64) float64 {
	now := time.Now()
	dt := now.Sub(pid.LastTime).Seconds()
	if dt <= 0 {
		return 0
	}
	pid.LastTime = now

	// Fehler: Positiv wenn zu voll, Negativ wenn zu leer
	error := currentVal - pid.SetPoint

	// Integral (mit Anti-Windup)
	pid.Integral += error * dt
	if pid.Integral > 5 {
		pid.Integral = 5
	}
	if pid.Integral < -5 {
		pid.Integral = -5
	}

	// Derivative
	derivative := (error - pid.PrevError) / dt
	pid.PrevError = error

	output := (pid.Kp * error) + (pid.Ki * pid.Integral) + (pid.Kd * derivative)
	return output
}

// --- Router ---
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

func main() {
	// Logger Setup
	logPath := "/tmp/yafad_debug.log"
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0666)
	if err != nil {
		panic(err)
	}
	defer logFile.Close()

	logger := slog.New(slog.NewJSONHandler(logFile, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))
	slog.SetDefault(logger)

	slog.Info("YaFaD Monitor starting", "version", "0.4.3-tuning", "log_path", logPath)

	// 1. Connection
	dbUser := os.Getenv("DB_USER")
	if dbUser == "" {
		dbUser = "eriks"
	}
	dbPass := os.Getenv("DB_PASSWORD")
	if dbPass == "" {
		dbPass = "test"
	}
	connStr := fmt.Sprintf("postgres://%s:%s@localhost:5432/yafad_test?sslmode=disable", dbUser, dbPass)

	ctx := context.Background()
	hotPool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		panic(fmt.Sprintf("Unable to connect to database: %v", err))
	}
	defer hotPool.Close()

	coldPool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		panic(err)
	}
	defer coldPool.Close()

	router := &StorageRouter{HotPool: hotPool, ColdPool: coldPool}

	// 2. SETUP WIZARD
	caps, injectCount := runSetupWizard(ctx, hotPool)

	// 3. CORTEX
	rustCore := &cortex.RustCoreFFI{
		LibraryPath: "./libyafd_core.so",
	}

	brain := cortex.NewCortex("brain_data.json", rustCore)
	go func() {
		saveTicker := time.NewTicker(1 * time.Minute)
		for range saveTicker.C {
			brain.Persist()
		}
	}()
	fmt.Println("🧠 YaFaD_ai Cortex Online.")

	// 4. LAMBDA BRIDGE
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

	// 5. WORKER START (Tuned Buoyancy)

	// T0: Ziel 100%. Starvation Guard ab 90% (weniger aggressiv).
	go func() {
		defer wg.Done()
		pid := NewPID(1.5, 0.05, 0.2, 1.0)
		runHomeostaticWorker(router, brain, pid, 0, caps["table0"], 0.005, 0.0001, 5.0, 1*time.Millisecond, 100*time.Millisecond, reportT0Lambda)
	}()

	// T1: Ziel 100%.
	go func() {
		defer wg.Done()
		pid := NewPID(1.2, 0.05, 0.2, 1.0)
		runHomeostaticWorker(router, nil, pid, 1, caps["table1"], 0.005, 0.0001, 3.0, 10*time.Millisecond, 500*time.Millisecond, nil)
	}()

	// T2 (Belly): Ziel 90%.
	go func() {
		defer wg.Done()
		pid := NewPID(0.8, 0.01, 0.1, 0.90)
		runHomeostaticWorker(router, nil, pid, 2, caps["table2"], 0.005, 0.0001, 1.0, 50*time.Millisecond, 1*time.Second, nil)
	}()

	// T3: Ziel 80%.
	go func() {
		defer wg.Done()
		pid := NewPID(0.5, 0.01, 0.1, 0.80)
		runHomeostaticWorker(router, nil, pid, 3, caps["table3"], 0.005, 0.0001, 0.5, 1*time.Second, 10*time.Second, nil)
	}()

	// T4: Ziel 50%.
	go func() {
		defer wg.Done()
		pid := NewPID(0.2, 0.0, 0.0, 0.50)
		runHomeostaticWorker(router, nil, pid, 4, caps["table4"], 0.001, 0.0001, 0.1, 1*time.Second, 30*time.Second, nil)
	}()

	// 6. MONITORING
	monCaps := make(map[string]float64)
	for k, v := range caps {
		monCaps[k] = float64(v)
	}

	go monitoring.StartMonitor(hotPool, monitoring.MonitorConfig{
		Interval:   5 * time.Second,
		TargetPhi:  PHI,
		CSVFile:    "yafad_metrics.csv",
		Capacities: monCaps,
	}, getT0Lambda)

	// 7. AUTO-INJECTION
	if injectCount > 0 {
		go func() {
			time.Sleep(2 * time.Second)
			fmt.Printf("\n🚀 Launching Generator with %d rows...\n", injectCount)

			cmd := exec.Command("go", "run", "generator.go", "-count", fmt.Sprintf("%d", injectCount))
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr

			if err := cmd.Run(); err != nil {
				slog.Error("Generator failed", "err", err)
			}
		}()
	}

	wg.Wait()
}

// --- WIZARD LOGIC ---
func runSetupWizard(ctx context.Context, pool *pgxpool.Pool) (map[string]int, int) {
	reader := bufio.NewReader(os.Stdin)
	fmt.Println("╔══════════════════════════════════════════════════╗")
	fmt.Println("║ 🔧 YaFaD ENGINE SETUP (v0.4.3 Tuned)             ║")
	fmt.Println("╚══════════════════════════════════════════════════╝")

	// Truncate
	fmt.Print("❓ Flush internal tables (Start fresh)? [y/N]: ")
	input, _ := reader.ReadString('\n')
	input = strings.TrimSpace(strings.ToLower(input))
	if input == "y" || input == "yes" {
		fmt.Print("🧹 Truncating tables... ")
		_, err := pool.Exec(ctx, "TRUNCATE table0, table1, table2, table3, table4, deep_archive")
		if err != nil {
			fmt.Printf("Error: %v\n", err)
		} else {
			fmt.Println("Done.")
		}
	} else {
		fmt.Println("⏩ Keeping existing data.")
	}

	// Capacity (MIT BUGFIX für Kommas/Punkte)
	baseCap := 20000
	fmt.Printf("❓ Enter BASE CAPACITY (T0 Size) [default %d]: ", baseCap)
	inputCap, _ := reader.ReadString('\n')
	inputCap = strings.TrimSpace(inputCap)
	// CLEANUP: Entferne Tausendertrennzeichen
	inputCap = strings.ReplaceAll(inputCap, ".", "")
	inputCap = strings.ReplaceAll(inputCap, ",", "")

	if inputCap != "" {
		if val, err := strconv.Atoi(inputCap); err == nil && val > 0 {
			baseCap = val
		} else {
			fmt.Printf("⚠️  Invalid number '%s', using default %d\n", inputCap, baseCap)
		}
	}

	// Injection
	injectAmount := 0
	fmt.Print("🌊 Inject Simulation Data? (Enter amount or 0 for None) [default 0]: ")
	inputInj, _ := reader.ReadString('\n')
	inputInj = strings.TrimSpace(inputInj)
	inputInj = strings.ReplaceAll(inputInj, ".", "")
	inputInj = strings.ReplaceAll(inputInj, ",", "")

	if inputInj != "" {
		if val, err := strconv.Atoi(inputInj); err == nil && val > 0 {
			injectAmount = val
		}
	}

	caps := make(map[string]int)
	caps["table0"] = baseCap
	caps["table1"] = int(float64(baseCap) * PHI)
	caps["table2"] = int(float64(caps["table1"]) * PHI)
	caps["table3"] = int(float64(caps["table2"]) * PHI)
	caps["table4"] = int(float64(caps["table3"]) * PHI)

	fmt.Printf("📉 Configuration:\n")
	fmt.Printf("   T0: %d\n", caps["table0"])
	if injectAmount > 0 {
		fmt.Printf("   🌊 Simulation: %d rows incoming\n", injectAmount)
	}
	fmt.Println("----------------------------------------------------")
	fmt.Println("🚀 Starting Engine...")
	time.Sleep(1 * time.Second)

	return caps, injectAmount
}

// --- WORKER LOGIC ---

func runHomeostaticWorker(router *StorageRouter, brain *cortex.Cortex, pid *PIDController, startTier int, idealCapacity int, baseLambda, min, max float64, minSleep, maxSleep time.Duration, reportLambda func(float64)) {
	ctx := context.Background()
	currentLambda := baseLambda
	threshold := 0.4
	currentSleep := maxSleep

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
		sourcePool := router.GetPool(sourceTier)
		targetPool := router.GetPool(targetTier)

		var sourceCount, targetCount int
		sourcePool.QueryRow(ctx, fmt.Sprintf("SELECT count(*) FROM %s", sourceTable)).Scan(&sourceCount)
		targetPool.QueryRow(ctx, fmt.Sprintf("SELECT count(*) FROM %s", targetTable)).Scan(&targetCount)

		// Belly Awareness Check
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

		currentBatchSize := baseBatchSize
		isEmergency := false
		isPredictiveBoost := false

		// --- PID & BUOYANCY ---
		pressure := float64(sourceCount) / float64(idealCapacity)

		// TUNING: Starvation Guard reduziert auf 90% (weniger aggressiver Auftrieb)
		if startTier <= 1 && pressure < 0.89 {
			currentLambda = min // Winterschlaf
		} else {
			pidOutput := pid.Update(pressure)
			currentLambda = baseLambda + pidOutput
		}

		currentLambda *= bellyFactor

		if brain != nil && !isEmergency {
			if time.Since(lastObservation) > 10*time.Second {
				brain.Observe(currentLambda)
				lastObservation = time.Now()
			}
			predictedStress := brain.Predict(1)
			if predictedStress > currentLambda*1.5 {
				currentLambda = (currentLambda + predictedStress) / 2
				isPredictiveBoost = true
			}
		}

		if currentLambda < min {
			currentLambda = min
		}
		if currentLambda > max {
			currentLambda = max
		}

		// EMERGENCY DUMP (> 200%)
		if pressure > 3.0 {
			isEmergency = true
			evacSize := 40000
			slog.Warn("EMERGENCY DUMP", "source", sourceTable, "pressure", pressure, "size", evacSize)
			err := emergencyEvacuate(ctx, sourcePool, targetPool, sourceTable, targetTable, evacSize)
			if err != nil {
				slog.Error("Bulk Move Failed", "err", err)
				time.Sleep(1 * time.Second)
			} else {
				time.Sleep(10 * time.Millisecond)
				continue
			}
		}

		if reportLambda != nil {
			reportLambda(currentLambda)
		}

		// --- MIGRATION ---
		workFound := false

		if sourceCount > 0 {
			query := fmt.Sprintf("SELECT id, utility_index, last_activity, payload FROM %s LIMIT %d", sourceTable, currentBatchSize)
			rows, err := sourcePool.Query(ctx, query)
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
					if err := rows.Scan(&r.ID, &r.U, &r.LA, &r.PL); err == nil {
						batch = append(batch, r)
					}
				}
				rows.Close()

				for _, r := range batch {
					deltaT := time.Since(r.LA).Hours()
					uNow := float64(C.calculate_decay(C.double(r.U), C.double(currentLambda), C.double(deltaT)))

					forceFlush := pressure > 1.25

					if uNow < threshold || forceFlush {
						if migrateRecord(ctx, sourcePool, targetPool, sourceTable, targetTable, r.ID, r.PL, uNow, r.LA) {
							workFound = true
						}
					}
				}
			}
		}

		// Sleep Logic
		if workFound {
			if isEmergency {
				currentSleep = 10 * time.Millisecond
			} else {
				if randInt(0, 100) == 0 {
					slog.Info("Worker Active",
						"source", sourceTable,
						"lambda", fmt.Sprintf("%.5f", currentLambda),
						"pressure", fmt.Sprintf("%.2f", pressure),
						"buoyancy_active", pressure < 0.90, // Updated Log
						"ai_boost", isPredictiveBoost,
						"belly_brake", bellyFactor < 1.0)
				}
				currentSleep /= 2
				if currentSleep < minSleep {
					currentSleep = minSleep
				}
			}
		} else {
			currentSleep *= 2
			if currentSleep > maxSleep {
				currentSleep = maxSleep
			}
		}
		time.Sleep(currentSleep)
	}
}

// --- SQL HELPERS ---
// (Unverändert: emergencyEvacuate, migrateRecord, randInt, colorize)
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
		if err := rows.Scan(&id, &pl, &u, &la); err == nil {
			ids = append(ids, id)
			data = append(data, []interface{}{id, pl, u, la})
		}
	}
	rows.Close()

	if len(ids) == 0 {
		return nil
	}

	_, err = targetPool.CopyFrom(
		ctx,
		pgx.Identifier{targetT},
		[]string{"id", "payload", "utility_index", "last_activity"},
		pgx.CopyFromRows(data),
	)
	if err != nil {
		return err
	}

	_, err = sourcePool.Exec(ctx, fmt.Sprintf("DELETE FROM %s WHERE id = ANY($1)", sourceT), ids)
	return err
}

func migrateRecord(ctx context.Context, sourcePool, targetPool *pgxpool.Pool, sourceTable, targetTable, id, payload string, uNow float64, lastActivity time.Time) bool {
	_, err := targetPool.Exec(ctx,
		fmt.Sprintf("INSERT INTO %s (id, payload, utility_index, last_activity) VALUES ($1, $2, $3, $4) ON CONFLICT (id) DO NOTHING", targetTable),
		id, payload, uNow, lastActivity)

	if err != nil {
		slog.Error("Migration Insert Failed", "id", id, "err", err)
		return false
	}

	_, err = sourcePool.Exec(ctx, fmt.Sprintf("DELETE FROM %s WHERE id = $1", sourceTable), id)
	if err != nil {
		slog.Error("Migration Delete Failed", "id", id, "err", err)
		return false
	}

	return true
}

func randInt(min, max int) int {
	return min + rand.Intn(max-min+1)
}

// --- Colors ---
const (
	ColorReset  = "\033[0m"
	ColorRed    = "\033[1;31m"
	ColorYellow = "\033[1;33m"
	ColorGreen  = "\033[1;32m"
	ColorCyan   = "\033[1;36m"
	ColorBlue   = "\033[1;34m"
	ColorPurple = "\033[1;35m"
	ColorWhite  = "\033[1;37m"
)

func colorize(tableName string) string {
	switch tableName {
	case "table0":
		return ColorRed + tableName + ColorReset
	case "table1":
		return ColorYellow + tableName + ColorReset
	case "table2":
		return ColorGreen + tableName + ColorReset
	case "table3":
		return ColorCyan + tableName + ColorReset
	case "table4":
		return ColorBlue + tableName + ColorReset
	case "deep_archive":
		return ColorWhite + "ARCHIVE" + ColorReset
	default:
		return tableName
	}
}
