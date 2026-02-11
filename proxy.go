package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// --- CONFIG ---
const PORT = ":8080"
const LEGACY_TABLE = "user_posts" // Die alte Tabelle in der Sandbox

type Record struct {
	ID           string    `json:"id"`
	Payload      string    `json:"payload"`
	UtilityIndex float64   `json:"utility_index"`
	LastActivity time.Time `json:"last_activity"`
	Source       string    `json:"source,omitempty"` // Zeigt uns, woher die Daten kamen
}

type ProxyServer struct {
	YafadPool  *pgxpool.Pool
	LegacyPool *pgxpool.Pool
}

func main() {
	// Logger Setup
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	ctx := context.Background()

	// 1. Verbindung zu YaFaD (Das neue System)
	yafadConnStr := "postgres://eriks:test@localhost:5432/yafad_test?sslmode=disable"
	yafadPool, err := pgxpool.New(ctx, yafadConnStr)
	if err != nil {
		slog.Error("Failed to connect to YaFaD DB", "error", err)
		os.Exit(1)
	}
	defer yafadPool.Close()

	// 2. Verbindung zur Legacy Sandbox (Das alte System)
	legacyConnStr := "postgres://eriks:test@localhost:5432/yafad_sandbox?sslmode=disable"
	legacyPool, err := pgxpool.New(ctx, legacyConnStr)
	if err != nil {
		slog.Error("Failed to connect to Legacy DB", "error", err)
		os.Exit(1)
	}
	defer legacyPool.Close()

	server := &ProxyServer{
		YafadPool:  yafadPool,
		LegacyPool: legacyPool,
	}

	// HTTP Routing
	http.HandleFunc("/api/record/", server.handleGetRecord)
	http.HandleFunc("/api/record", server.handlePostRecord)

	fmt.Println("╔══════════════════════════════════════════════════╗")
	fmt.Println("║ 🌳 YaFaD SMART PROXY (v0.8.5 Strangler Fig)      ║")
	fmt.Printf("║    Listening on http://localhost%s            ║\n", PORT)
	fmt.Println("╚══════════════════════════════════════════════════╝")

	if err := http.ListenAndServe(PORT, nil); err != nil {
		slog.Error("Server crashed", "error", err)
	}
}

// --- WRITE (Immer in YaFaD T0) ---
func (s *ProxyServer) handlePostRecord(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var rec Record
	if err := json.NewDecoder(r.Body).Decode(&rec); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	// Set defaults if empty
	if rec.UtilityIndex == 0 {
		rec.UtilityIndex = 1.0
	}
	if rec.LastActivity.IsZero() {
		rec.LastActivity = time.Now()
	}

	query := `INSERT INTO table0 (id, payload, utility_index, last_activity) VALUES ($1, $2, $3, $4)`
	_, err := s.YafadPool.Exec(context.Background(), query, rec.ID, rec.Payload, rec.UtilityIndex, rec.LastActivity)

	if err != nil {
		slog.Error("Failed to write to YaFaD", "error", err)
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{"status": "written to YaFaD T0", "id": rec.ID})
	slog.Info("Write Request", "id", rec.ID, "destination", "yafad_t0")
}

// --- READ (Suche in YaFaD -> Fallback auf Legacy) ---
func (s *ProxyServer) handleGetRecord(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extrahiere ID aus URL (z.B. /api/record/123)
	parts := strings.Split(r.URL.Path, "/")
	if len(parts) < 4 || parts[3] == "" {
		http.Error(w, "Missing ID", http.StatusBadRequest)
		return
	}
	id := parts[3]
	ctx := context.Background()

	// 1. VERSUCH: Suche in YaFaD (Wir nutzen einen UNION ALL über alle Tiers)
	yafadQuery := `
		SELECT id, payload, utility_index, last_activity, 'yafad_t0' as source FROM table0 WHERE id = $1
		UNION ALL SELECT id, payload, utility_index, last_activity, 'yafad_t1' FROM table1 WHERE id = $1
		UNION ALL SELECT id, payload, utility_index, last_activity, 'yafad_t2' FROM table2 WHERE id = $1
		UNION ALL SELECT id, payload, utility_index, last_activity, 'yafad_t3' FROM table3 WHERE id = $1
		UNION ALL SELECT id, payload, utility_index, last_activity, 'yafad_t4' FROM table4 WHERE id = $1
		UNION ALL SELECT id, payload, utility_index, last_activity, 'yafad_archive' FROM deep_archive WHERE id = $1
		LIMIT 1;
	`

	var rec Record
	err := s.YafadPool.QueryRow(ctx, yafadQuery, id).Scan(&rec.ID, &rec.Payload, &rec.UtilityIndex, &rec.LastActivity, &rec.Source)

	if err == nil {
		// IN YAFAD GEFUNDEN!
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(rec)
		slog.Info("Read Request", "id", id, "found_in", rec.Source)
		return
	}

	// 2. VERSUCH: Fallback auf Legacy DB
	legacyQuery := fmt.Sprintf(`SELECT id, payload, utility_index, last_activity FROM %s WHERE id = $1`, LEGACY_TABLE)
	err = s.LegacyPool.QueryRow(ctx, legacyQuery, id).Scan(&rec.ID, &rec.Payload, &rec.UtilityIndex, &rec.LastActivity)

	if err == nil {
		// IN LEGACY GEFUNDEN!
		rec.Source = "legacy_sandbox"
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(rec)
		slog.Info("Read Request (Fallback)", "id", id, "found_in", "legacy_sandbox")
		return
	}

	// 3. NICHT GEFUNDEN
	http.Error(w, "Record not found", http.StatusNotFound)
	slog.Warn("Read Request Failed", "id", id, "status", "not_found")
}
