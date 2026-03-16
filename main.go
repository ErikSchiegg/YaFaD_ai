package main

/*
#cgo LDFLAGS: -L${SRCDIR}/core/target/release -lyafad_core -Wl,-rpath,${SRCDIR}/core/target/release -lm -ldl
#cgo CPPFLAGS: -I${SRCDIR}/core
extern double calculate_decay(double u_last, double lambda, double delta_t);
*/
import "C"
import (
	"YaFaD_ai/internal/cortex"
	"YaFaD_ai/internal/monitoring"
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const PHI = 1.61803398875

// ---> DOCKER ANPASSUNGEN: Pfade in den shared/ Ordner umgeleitet <---
const CONFIG_FILE = "shared/yafad_config.json"
const BRAIN_FILE = "shared/brain_weights.json"
const METRICS_FILE = "shared/yafad_metrics.csv"
const KEY_FILE = "shared/yafad_secret.key"

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
	ActiveTiers     []int           `json:"active_tiers"`
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

// --- GOSSIP & OSMOSE STRUCTURES ---
var (
	localNodeID = fmt.Sprintf("Node-%d", time.Now().UnixNano()%10000)
	peerTable   = make(map[string]NodeHeartbeat)
	peerMu      sync.RWMutex
)

type NodeHeartbeat struct {
	NodeID    string    `json:"node_id"`
	IP        string    `json:"ip"`
	Tiers     []int     `json:"tiers"`
	Pressure  float64   `json:"pressure"`
	Timestamp time.Time `json:"timestamp"`
}

type TransferPayload struct {
	Tier         int       `json:"tier"`
	ID           string    `json:"id"`
	Payload      string    `json:"payload"`
	UtilityIndex float64   `json:"utility_index"`
	LastActivity time.Time `json:"last_activity"`
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

// --- IMMUNSYSTEM (AES-256-GCM Verschlüsselung) ---
var globalSymmetricKey []byte

func initCrypto() {
	// Sicherstellen, dass das Shared-Verzeichnis existiert
	os.MkdirAll("shared", os.ModePerm)

	data, err := os.ReadFile(KEY_FILE)
	if err == nil && len(data) == 32 {
		globalSymmetricKey = data
		fmt.Println("🛡️  Immune System Online: AES-256 Key loaded.")
		return
	}

	fmt.Println("⚠️  No valid key found. Generating new AES-256 Secret Key...")
	globalSymmetricKey = make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, globalSymmetricKey); err != nil {
		panic("Failed to generate secure random key: " + err.Error())
	}
	_ = os.WriteFile(KEY_FILE, globalSymmetricKey, 0600)
	fmt.Println("🛡️  Immune System Online: New Key generated and saved.")
}

func encryptPayload(plaintext string) string {
	if len(globalSymmetricKey) != 32 {
		return plaintext
	}

	block, err := aes.NewCipher(globalSymmetricKey)
	if err != nil {
		return plaintext
	}

	aesGCM, err := cipher.NewGCM(block)
	if err != nil {
		return plaintext
	}

	nonce := make([]byte, aesGCM.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return plaintext
	}

	ciphertext := aesGCM.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(ciphertext)
}

func decryptPayload(encoded string) string {
	if len(globalSymmetricKey) != 32 {
		return encoded
	}

	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return encoded
	}

	block, err := aes.NewCipher(globalSymmetricKey)
	if err != nil {
		return encoded
	}

	aesGCM, err := cipher.NewGCM(block)
	if err != nil {
		return encoded
	}

	nonceSize := aesGCM.NonceSize()
	if len(data) < nonceSize {
		return encoded
	}

	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	plaintext, err := aesGCM.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return encoded
	}

	return string(plaintext)
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

// --- DYNAMIC BIOMASS AND WATERMARK LOGIC ---
func getEstimatedBiomass(ctx context.Context, pool *pgxpool.Pool) int64 {
	var total float64
	query := `SELECT COALESCE(sum(reltuples), 0) FROM pg_class WHERE relname IN ('table0', 'table1', 'table2', 'table3', 'table4', 'deep_archive')`
	err := pool.QueryRow(ctx, query).Scan(&total)
	if err != nil {
		return 0
	}
	return int64(total)
}

func adaptPhysics(currentHigh, currentLow, currentBuoy float64, isRunning bool, totalBiomass int64, tickIntervalSec float64) (float64, float64, float64, bool) {
	targetHighIdle, targetLowIdle := 100.0, 95.0
	targetHighRun, targetLowRun := 150.0, 110.0

	targetBuoyIdle := 0.64
	targetBuoyRun := 0.85

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

	if math.Abs(newHigh-targetHigh) < 0.0001 {
		newHigh = targetHigh
	}
	if math.Abs(newLow-targetLow) < 0.0001 {
		newLow = targetLow
	}

	stretchFactor := (newHigh - targetHighIdle) / (targetHighRun - targetHighIdle)
	if stretchFactor < 0 {
		stretchFactor = 0
	}
	if stretchFactor > 1 {
		stretchFactor = 1
	}

	newBuoy := targetBuoyIdle + ((targetBuoyRun - targetBuoyIdle) * stretchFactor)
	newBuoy = math.Round(newBuoy*1000) / 1000

	if currentHigh != newHigh || currentLow != newLow || math.Abs(currentBuoy-newBuoy) > 0.001 {
		changed = true
	}

	return newHigh, newLow, newBuoy, changed
}

// --- MAIN ---
func main() {
	// Sicherstellen, dass das Shared-Verzeichnis existiert
	os.MkdirAll("shared", os.ModePerm)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Logger initialisieren
	logPath := "shared/yafad_debug.log" // Auch ins Shared-Dir für Docker
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

	// ---> DOCKER ANPASSUNG: DB_HOST auslesen <---
	dbUser := os.Getenv("DB_USER")
	if dbUser == "" {
		dbUser = "eriks"
	}
	dbPass := os.Getenv("DB_PASSWORD")
	if dbPass == "" {
		dbPass = "test"
	}
	dbHost := os.Getenv("DB_HOST")
	if dbHost == "" {
		dbHost = "localhost"
	}

	connStr := fmt.Sprintf("postgres://%s:%s@%s:5432/yafad_test?sslmode=disable", dbUser, dbPass, dbHost)

	var hotPool, coldPool *pgxpool.Pool
	var err error

	fmt.Printf("⏳ Connecting to Database at %s (User: %s)...\n", dbHost, dbUser)

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
		fmt.Printf("\n\n❌ FATAL: Could not connect to PostgreSQL Database at %s.\n", dbHost)
		os.Exit(1)
	}
	fmt.Println(" ✅ Connected!")
	defer hotPool.Close()

	// ---> RICHTIG: Wir übergeben hotPool statt pool <---
	ensureTablesExist(ctx, hotPool)

	coldPool, _ = pgxpool.New(ctx, connStr)
	defer coldPool.Close()

	router := &StorageRouter{HotPool: hotPool, ColdPool: coldPool}

	loadBrain()
	initConfig()
	initCrypto()

	//START WEBSERVER
	go func() {
		http.ListenAndServe(":6060", nil)
	}()
	// GOSSIP PROTOKOLL STARTEN!
	startGossipProtocol(ctx, hotPool)

	go configWatcher(ctx)
	go brainWatcher(ctx)
	go func() {
		fmt.Println("📈 Starting Prometheus Metrics Server on :2112/metrics")
		http.Handle("/metrics", promhttp.Handler())

		// Osmose Empfänger
		http.HandleFunc("/osmosis", func(w http.ResponseWriter, r *http.Request) {
			var tp TransferPayload
			if err := json.NewDecoder(r.Body).Decode(&tp); err != nil {
				http.Error(w, "Bad Request", 400)
				return
			}

			targetTable := fmt.Sprintf("table%d", tp.Tier)
			targetPool := router.GetPool(tp.Tier)

			_, err := targetPool.Exec(ctx, fmt.Sprintf("INSERT INTO %s (id, payload, utility_index, last_activity) VALUES ($1, $2, $3, $4) ON CONFLICT (id) DO NOTHING", targetTable), tp.ID, tp.Payload, tp.UtilityIndex, tp.LastActivity)

			if err != nil {
				http.Error(w, "DB Error", 500)
				return
			}
			w.WriteHeader(200)
		})
		if err := http.ListenAndServe(":2112", nil); err != nil && err != http.ErrServerClosed {
			slog.Error("Prometheus Server failed", "error", err)
		}
	}()

	// ---> RUST CORE: Fallback-Pfade für Docker <---
	// Versucht erst den Standardpfad, dann einen Root-Pfad falls kopiert
	rustLibPath := "./core/target/release/libyafad_core.so"
	if _, err := os.Stat(rustLibPath); os.IsNotExist(err) {
		rustLibPath = "./libyafad_core.so"
	}
	rustCore := &cortex.RustCoreFFI{LibraryPath: rustLibPath}
	brain := cortex.NewCortex(BRAIN_FILE, rustCore)

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

	fmt.Println("🦁 YaFaD v0.9.3 Online. Waiting for Mission Command via Dashboard...")

	startMonitoringService(hotPool)
	// Die Dashboard-Start-Funktion wird in Docker nicht mehr benötigt (Gradio läuft in eigenem Container)
	// Wir lassen den Aufruf aber drin für Non-Docker-Umgebungen
	if os.Getenv("DISABLE_INTERNAL_DASHBOARD") != "true" {
		go launchDashboard()
	}

	go runEquilibriumSmoother(ctx, router)

	ticker := time.NewTicker(1 * time.Second)
	workersStarted := false
	var wg sync.WaitGroup

	prevState := "UNKNOWN"

	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			return
		case <-ticker.C:

			biomass := getEstimatedBiomass(ctx, hotPool)

			configMu.RLock()
			wHigh := globalConfig.Watermarks.High
			wLow := globalConfig.Watermarks.Low
			cBuoy := globalConfig.BuoyancyFactor
			currentState := globalConfig.RunState
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

			configMu.RLock()
			cpu := globalConfig.Limits.MaxCpuPercent
			totalRecords := globalConfig.InjectTotal
			flush := globalConfig.FlushOnStart
			configMu.RUnlock()

			if currentState == "RUNNING" && prevState != "RUNNING" {
				fmt.Printf("🚀 Command received: START MISSION (Target: %d)\n", totalRecords)

				if flush {
					fmt.Println("🧹 FLUSHING TABLES...")
					if _, err := hotPool.Exec(ctx, "TRUNCATE table0, table1, table2, table3, table4, deep_archive, archive0, archive1, archive3, archive4"); err != nil {
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

			prevState = currentState
		}
	}
}

// --- NEUROPLASTIZITÄT: PID SELBSTOPTIMIERUNG ---
func optimizePIDParams() {
	fmt.Println("\n🧠 KONSOLIDIERUNG: Analysiere Zell-Metriken für PID-Tuning...")

	data, err := os.ReadFile(METRICS_FILE)
	if err != nil {
		fmt.Println("⚠️ Konnte Metriken nicht lesen, überspringe Tuning.")
		return
	}

	lines := strings.Split(string(data), "\n")
	var phiDiffs []float64
	var t0Pcts []float64

	// Letzte 60 Messpunkte auswerten (ca. die letzten 5 Minuten unter Last)
	limit := len(lines) - 60
	if limit < 1 {
		limit = 1
	}

	for i := limit; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "" {
			continue
		}
		cols := strings.Split(lines[i], ",")
		if len(cols) >= 16 && cols[0] != "timestamp" {
			t0Pct, _ := strconv.ParseFloat(cols[9], 64)
			phiDiff, _ := strconv.ParseFloat(cols[15], 64)
			t0Pcts = append(t0Pcts, t0Pct)
			phiDiffs = append(phiDiffs, phiDiff)
		}
	}

	if len(phiDiffs) == 0 {
		return
	}

	// Statistik berechnen
	var sumPhi, maxT0, minT0 float64
	minT0 = 999.0
	for i, val := range phiDiffs {
		sumPhi += val
		t0 := t0Pcts[i]
		if t0 > maxT0 {
			maxT0 = t0
		}
		if t0 < minT0 {
			minT0 = t0
		}
	}
	avgPhi := sumPhi / float64(len(phiDiffs))
	oscillation := maxT0 - minT0

	configMu.Lock()
	kp := globalConfig.PID.Kp
	ki := globalConfig.PID.Ki
	kd := globalConfig.PID.Kd

	var changed bool

	// Regel 1: Oszillation (hektisches Atmen) bekämpfen
	if oscillation > 20.0 {
		kp *= 0.95 // 5% Weniger aggressiv
		kd *= 1.10 // 10% Mehr Dämpfung
		changed = true
		fmt.Printf("   📉 Hohe Oszillation erkannt (%.1f%%). Erhöhe Dämpfung...\n", oscillation)
	}

	// Regel 2: Trägheit bekämpfen (Verfehlen des Goldenen Schnitts)
	if avgPhi > 0.20 && oscillation <= 20.0 {
		kp *= 1.05 // 5% Aggressiver
		ki *= 1.02 // Leicht erhöhter Integral-Faktor
		changed = true
		fmt.Printf("   🐢 System zu träge (Avg PhiDiff=%.3f). Erhöhe Reaktionsfreudigkeit...\n", avgPhi)
	}

	// Regel 3: Perfektion
	if avgPhi <= 0.20 && oscillation <= 20.0 {
		fmt.Println("   ✨ System lief nah am biologischen Optimum. Keine Änderungen nötig.")
	}

	// Physikalische Sicherheitsgrenzen (damit das System nicht explodiert)
	if kp > 3.0 {
		kp = 3.0
	}
	if kp < 0.1 {
		kp = 0.1
	}
	if kd > 1.0 {
		kd = 1.0
	}
	if ki > 0.5 {
		ki = 0.5
	}

	if changed {
		globalConfig.PID.Kp = math.Round(kp*1000) / 1000
		globalConfig.PID.Ki = math.Round(ki*1000) / 1000
		globalConfig.PID.Kd = math.Round(kd*1000) / 1000
		saveConfigToJSON(globalConfig)
		fmt.Printf("   ✅ Neue PID-Werte gespeichert -> Kp: %.3f | Ki: %.3f | Kd: %.3f\n", globalConfig.PID.Kp, globalConfig.PID.Ki, globalConfig.PID.Kd)
	}
	configMu.Unlock()
}

// -- wenn nötig, Tabellen erzeugen
func ensureTablesExist(ctx context.Context, pool *pgxpool.Pool) {
	// ---> NEU: Vektor-Erweiterung in PostgreSQL aktivieren
	_, err := pool.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS vector;")
	if err != nil {
		fmt.Println("⚠️ Konnte pgvector nicht aktivieren (läuft das pgvector-Image?):", err)
	}

	tables := []string{"table0", "table1", "table2", "table3", "table4", "deep_archive", "archive0", "archive1", "archive2", "archive3", "archive4"}

	for _, table := range tables {
		// Tabelle mit neuer Vector-Spalte erstellen
		query := fmt.Sprintf(`
            CREATE TABLE IF NOT EXISTS %s (
                id TEXT PRIMARY KEY,
                payload TEXT,
                utility_index DOUBLE PRECISION,
                last_activity TIMESTAMP,
                embedding VECTOR(768)
            );`, table)

		_, err := pool.Exec(ctx, query)
		if err != nil {
			fmt.Printf("❌ Fehler beim Erstellen der Tabelle %s: %v\n", table, err)
		}

		// Index für "Coldest First" Logik erstellen (SEHR WICHTIG!)
		indexQuery := fmt.Sprintf("CREATE INDEX IF NOT EXISTS idx_%s_utility ON %s (utility_index ASC);", table, table)
		_, err = pool.Exec(ctx, indexQuery)
		if err != nil {
			fmt.Printf("❌ Fehler beim Erstellen des Utility-Index für %s: %v\n", table, err)
		}

		// ---> NEU: HNSW Index für blitzschnelle semantische Vektor-Suche erstellen
		// vector_cosine_ops ist die optimale Metrik für Text-Embeddings
		vectorIndexQuery := fmt.Sprintf("CREATE INDEX IF NOT EXISTS idx_%s_embedding ON %s USING hnsw (embedding vector_cosine_ops);", table, table)
		_, err = pool.Exec(ctx, vectorIndexQuery)
		if err != nil {
			fmt.Printf("⚠️ Fehler beim Erstellen des Vektor-Index für %s: %v\n", table, err)
		}
	}
	fmt.Println("🏗️  Database Schema verified (T0-T4, Deep Archive, Vector Embeddings & Indices).")
}

// --- WORKER LOGIC (Dynamic Buoyancy) ---
func runWorker(ctx context.Context, router *StorageRouter, tier int, pid *PIDController) {
	sourceTable := fmt.Sprintf("table%d", tier)
	nextTable := fmt.Sprintf("table%d", tier+1)
	if tier == 4 {
		nextTable = "archive0"
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
		t0Cap := globalConfig.Capacities["table0"]
		if t0Cap <= 0 {
			t0Cap = 100000
		}
		targetRatio := globalConfig.TargetRatio
		vanishStr := globalConfig.VanishThreshold
		runState := globalConfig.RunState
		userBuoyancy := globalConfig.BuoyancyFactor
		configMu.RUnlock()

		// ---> NEU: Dynamische Fibonacci-Kapazität <---
		// Ignoriert alte Werte aus der Config und erzwingt den perfekten Goldenen Schnitt!
		capacity := int(float64(t0Cap) * math.Pow(PHI, float64(tier)))

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
			// ---> NEUE LOGIK (Coldest First Präzision) <---
			// Wir ignorieren Zufall und holen IMMER die 1000 Datensätze mit dem niedrigsten Utility Index.
			query := fmt.Sprintf("SELECT id, utility_index, last_activity, payload FROM %s ORDER BY utility_index ASC LIMIT 1000", sourceTable)
			rows, err := sourcePool.Query(ctx, query)
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
						// ---> NEU: Nur noch das Überdruck-Ventil (Der Heavy Lifter) <---
						if pressure >= 0.99 {
							// Tabelle ist brechend voll. Ventil aufreißen!
							shouldMigrate = true
						} else {
							// Tabelle hat noch Platz.
							// Wir halten das Ventil zu und lassen den "Smoother" später die Feinarbeit machen.
							shouldMigrate = false
						}
					}

					if shouldMigrate {
						targetTier := tier + 1
						isLocal := false

						configMu.RLock()
						for _, t := range globalConfig.ActiveTiers {
							if t == targetTier {
								isLocal = true
								break
							}
						}
						configMu.RUnlock()

						if isLocal || tier == 4 {
							// 1. GANZ NORMAL LOKAL MIGRIEREN
							if migrateRecord(ctx, sourcePool, targetPool, sourceTable, nextTable, r.ID, r.Payload, uNew, r.LastActivity) {
								migratedCount++
							}
						} else {
							// 2. EPIDEMIC ROUTING
							bestPeer := findBestPeer(targetTier)
							if bestPeer != nil {
								success := sendToPeer(bestPeer.IP, targetTier, r.ID, r.Payload, uNew, r.LastActivity)
								if success {
									sourcePool.Exec(ctx, fmt.Sprintf("DELETE FROM %s WHERE id = $1", sourceTable), r.ID)
									migratedCount++
								}
							}
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
	fmt.Println("🔨 Preparing and Compiling Generator...")

	// Zombie-Prozesse killen und alte Datei löschen, damit 'go build' nicht einfriert!
	exec.Command("pkill", "-f", "yafad_sim").Run()
	os.Remove("yafad_sim")

	// Kompilieren mit Fehler-Check
	errBuild := exec.Command("go", "build", "-o", "yafad_sim", "generator.go").Run()
	if errBuild != nil {
		fmt.Printf("❌ Fehler beim Kompilieren des Generators: %v\n", errBuild)
		return
	}

	configMu.RLock()
	done := globalConfig.InjectDone
	configMu.RUnlock()

	batchSize := 10000
	remaining := total - done

	// Transparente Ausgabe der Zahlen
	fmt.Printf("   -> Check: Target=%d | Done=%d | Remaining=%d\n", total, done, remaining)

	if remaining <= 0 {
		fmt.Println("\n✅ Target already reached. Nothing to inject.")
		return
	}

	isDraining := false
	lastBreathTime := time.Now()
	lastOptTime := time.Now() // Cooldown für Optimierung

	fmt.Printf("🚀 PULSE MISSION STARTED: Target %d Records (Remaining: %d)\n", total, remaining)

	// ---> NEU: Generiere einen einzigartigen Basis-Offset aus der aktuellen Zeit!
	// Dadurch kollidieren IDs ("user_...") beim "Add Records" nie wieder.
	baseOffset := int(time.Now().Unix())

	for remaining > 0 {
		// Sofort-Stopp wenn RunState sich ändert oder Target ungültig wird
		configMu.RLock()
		currentStatus := globalConfig.RunState
		currentTarget := globalConfig.InjectTotal
		configMu.RUnlock()

		if currentStatus != "RUNNING" || currentTarget <= 0 {
			fmt.Println("\n🛑 Emergency Stop: Mission aborted or Target cleared.")
			return
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

				// ---> BIOLOGISCHES SAHNEHÄUBCHEN: HYPERVENTILATIONS-ERKENNUNG <---
				breathDuration := time.Since(lastBreathTime)
				lastBreathTime = time.Now() // Startzeit für den nächsten Atemzug setzen

				fmt.Printf("\n🌊 T0 High Water Mark (%.1f%% >= %.1f%%). Switching to DRAIN Mode.\n", fillPct, wHigh)

				// Wenn die Maschine zu schnell atmet (Oszillation unter 20 Sekunden)
				// UND wir ihr seit der letzten Optimierung mindestens 60 Sekunden Zeit gegeben haben:
				if breathDuration < 20*time.Second && time.Since(lastOptTime) > 60*time.Second {
					fmt.Printf("⚠️ HYPERVENTILATION ERKANNT (Atemzug dauerte nur %v). Leite Notfall-Optimierung ein...\n", breathDuration.Round(time.Second))
					optimizePIDParams()
					lastOptTime = time.Now() // Cooldown starten
				}
				// -------------------------------------------------------------------
				continue
			}

			currentBatch := batchSize
			if remaining < batchSize {
				currentBatch = remaining
			}

			// ---> NEU: Der Offset setzt sich jetzt aus dem Zeitstempel und dem Fortschritt zusammen
			offset := baseOffset + (total - remaining)

			fmt.Printf("\r🔥 Injecting... [%d left] T0: %.1f%% (Target %.0f%%)    ", remaining, fillPct, wHigh)

			// DOCKER ANPASSUNG: DB_HOST an den Simulator weitergeben
			dbHost := os.Getenv("DB_HOST")
			if dbHost == "" {
				dbHost = "localhost"
			}
			cmd := exec.CommandContext(ctx, "./yafad_sim", "-count", fmt.Sprintf("%d", currentBatch), "-mode", "scenario", "-offset", fmt.Sprintf("%d", offset))

			// Das Environment für den Sub-Prozess anpassen!
			cmd.Env = append(os.Environ(), fmt.Sprintf("DB_HOST=%s", dbHost))
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
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

	// Abschließende Optimierung für den "Rest"
	optimizePIDParams()

	time.Sleep(10 * time.Second)
	configMu.Lock()
	globalConfig.RunState = "IDLE"
	saveConfigToJSON(globalConfig)
	configMu.Unlock()
	fmt.Println("✅ MISSION ACCOMPLISHED. Organism is resting.")
}

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
			if globalConfig.BuoyancyFactor == 0 {
				globalConfig.BuoyancyFactor = 0.64
			}
			if globalConfig.Watermarks.High == 0 {
				globalConfig.Watermarks.High = 150.0
			}
			if globalConfig.Watermarks.Low == 0 {
				globalConfig.Watermarks.Low = 100.0
			}
			if len(globalConfig.ActiveTiers) == 0 {
				globalConfig.ActiveTiers = []int{0, 1, 2, 3, 4}
			}

			saveConfigToJSON(globalConfig)
			return
		}
	}

	globalConfig = SystemConfig{
		RunState:       "IDLE",
		ActiveTiers:    []int{0, 1, 2, 3, 4},
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

	if _, err := os.Stat(METRICS_FILE); os.IsNotExist(err) {
		fmt.Println("⚠️  Metrics file missing! Creating a fresh one...")
		f, _ := os.Create(METRICS_FILE)
		f.WriteString("timestamp,runtime_sec,total_biomass,t0,t1,t2,t3,t4,deep_archive,t0_pct,t1_pct,t2_pct,t3_pct,t4_pct,lambda,phi_diff\n")
		f.Close()
	}

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

	fmt.Printf("📊 Monitoring active. Writing to %s & Prometheus :2112\n", METRICS_FILE)

	go monitoring.StartMonitor(pool, monitoring.MonitorConfig{
		Interval: 5 * time.Second, TargetPhi: PHI, CSVFile: METRICS_FILE, Capacities: monCaps,
	}, getLambda, getSystemState)
}

// =====================================================================
// --- GOSSIP PROTOCOL & ZELL-OSMOSE (EPIDEMIC NETWORKING) ---
// =====================================================================

func startGossipProtocol(ctx context.Context, pool *pgxpool.Pool) {
	go func() {
		addr, err := net.ResolveUDPAddr("udp", ":7777")
		if err != nil {
			return
		}

		conn, err := net.ListenUDP("udp", addr)
		if err != nil {
			fmt.Println("⚠️  Gossip-Listener failed to start (Port 7777 blocked?):", err)
			return
		}
		defer conn.Close()

		buf := make([]byte, 1024)
		for {
			select {
			case <-ctx.Done():
				return
			default:
				conn.SetReadDeadline(time.Now().Add(1 * time.Second))
				n, remoteAddr, err := conn.ReadFromUDP(buf)
				if err != nil {
					continue
				}

				var hb NodeHeartbeat
				if err := json.Unmarshal(buf[:n], &hb); err == nil {
					if hb.NodeID != localNodeID {
						hb.IP = remoteAddr.IP.String()

						peerMu.Lock()
						peerTable[hb.NodeID] = hb
						peerMu.Unlock()
					}
				}
			}
		}
	}()

	go func() {
		conn, err := net.Dial("udp", "255.255.255.255:7777")
		if err != nil {
			conn, _ = net.Dial("udp", "224.0.0.1:7777")
		}
		if conn != nil {
			defer conn.Close()
		}

		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				configMu.RLock()
				activeTiers := globalConfig.ActiveTiers
				capT0 := globalConfig.Capacities["table0"]
				configMu.RUnlock()

				var count int
				_ = pool.QueryRow(ctx, "SELECT count(*) FROM table0").Scan(&count)

				pressure := 0.0
				if capT0 > 0 {
					pressure = float64(count) / float64(capT0)
				}

				hb := NodeHeartbeat{
					NodeID:    localNodeID,
					Tiers:     activeTiers,
					Pressure:  pressure,
					Timestamp: time.Now(),
				}

				data, _ := json.Marshal(hb)
				if conn != nil {
					conn.Write(data)
				}
			}
		}
	}()

	fmt.Printf("📡 Gossip Protocol Online. Local Identity: %s\n", localNodeID)
}

func findBestPeer(targetTier int) *NodeHeartbeat {
	peerMu.RLock()
	defer peerMu.RUnlock()

	var bestPeer *NodeHeartbeat
	minPressure := 999.0

	for _, p := range peerTable {
		hasTier := false
		for _, t := range p.Tiers {
			if t == targetTier {
				hasTier = true
				break
			}
		}
		if hasTier && p.Pressure < minPressure && time.Since(p.Timestamp) < 10*time.Second {
			minPressure = p.Pressure
			peerCopy := p
			bestPeer = &peerCopy
		}
	}
	return bestPeer
}

func sendToPeer(ip string, targetTier int, id string, payload string, u float64, la time.Time) bool {
	tp := TransferPayload{Tier: targetTier, ID: id, Payload: payload, UtilityIndex: u, LastActivity: la}
	data, _ := json.Marshal(tp)

	resp, err := http.Post(fmt.Sprintf("http://%s:2112/osmosis", ip), "application/json", bytes.NewBuffer(data))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == 200
}

// --- THE EQUILIBRIUM SMOOTHER (Golden Ratio Massager) ---
func runEquilibriumSmoother(ctx context.Context, router *StorageRouter) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			configMu.RLock()
			runState := globalConfig.RunState
			targetRatio := globalConfig.TargetRatio
			configMu.RUnlock()

			// Der Smoother arbeitet NUR, wenn nicht gerade ein wilder Inject/Migration läuft
			if runState == "RUNNING" {
				continue
			}

			// Wir glätten von oben nach unten (T0->T1, T1->T2, T2->T3, T3->T4)
			for tier := 0; tier < 4; tier++ {
				sourceTable := fmt.Sprintf("table%d", tier)
				nextTable := fmt.Sprintf("table%d", tier+1)

				sourcePool := router.GetPool(tier)
				targetPool := router.GetPool(tier + 1)

				var countCurrent, countNext int
				sourcePool.QueryRow(ctx, fmt.Sprintf("SELECT count(*) FROM %s", sourceTable)).Scan(&countCurrent)
				targetPool.QueryRow(ctx, fmt.Sprintf("SELECT count(*) FROM %s", nextTable)).Scan(&countNext)

				if countCurrent == 0 {
					continue // Nichts zu glätten
				}

				// Wie viele Daten SOLLTEN im nächsten Tier sein, basierend auf dem aktuellen Tier?
				// Ziel: countNext = countCurrent * TargetRatio (z.B. 1.618)
				targetNextCount := float64(countCurrent) * targetRatio

				// Wenn das nächste Tier "zu leer" ist (weniger als 90% vom Idealwert)
				if float64(countNext) < targetNextCount*0.90 {

					// Berechne, wie viel wir sanft rüberschieben müssen
					deficit := int(targetNextCount) - countNext
					if deficit > 5000 {
						deficit = 5000
					} // Chunking für flüssige UI

					// Wir verschieben die "ältesten" (geringster Utility Index) Daten, um Platz zu machen
					if deficit > 0 {
						fmt.Printf("🧘 Smoother: Massaging %d records from %s -> %s to reach Phi...\n", deficit, sourceTable, nextTable)

						// Schneller Batch-Move der ältesten Records (Sortiert nach utility_index)
						query := fmt.Sprintf(`
                            WITH moved AS (
                                DELETE FROM %s 
                                WHERE id IN (SELECT id FROM %s ORDER BY utility_index ASC LIMIT %d) 
                                RETURNING *
                            )
                            INSERT INTO %s SELECT * FROM moved ON CONFLICT DO NOTHING;
                        `, sourceTable, sourceTable, deficit, nextTable)

						sourcePool.Exec(ctx, query)
					}
				}
			}
		}
	}
}

func startWorkers(ctx context.Context, router *StorageRouter, wg *sync.WaitGroup) {
	configMu.RLock()
	tiers := globalConfig.ActiveTiers
	configMu.RUnlock()

	defaultPIDs := map[int]*PIDController{
		0: NewPID(1.5, 0.05, 0.2),
		1: NewPID(1.2, 0.05, 0.2),
		2: NewPID(0.8, 0.01, 0.1),
		3: NewPID(0.5, 0.01, 0.1),
		4: NewPID(0.2, 0.00, 0.0),
	}

	for _, tier := range tiers {
		wg.Add(1)

		pid, exists := defaultPIDs[tier]
		if !exists {
			pid = NewPID(0.5, 0.01, 0.1)
		}

		t := tier
		go func(workerTier int, workerPID *PIDController) {
			defer wg.Done()
			runWorker(ctx, router, workerTier, workerPID)
		}(t, pid)
	}
}
