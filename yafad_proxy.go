package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"

	_ "github.com/lib/pq"
)

// Config Struktur für die JSON Policy
type MigrationConfig struct {
	Mode      string   `json:"mode"`
	LegacyDB  DBConfig `json:"legacy_db"`
	Whitelist []string `json:"yafad_whitelist"` // Tabellen, die YaFaD gehören
}

type DBConfig struct {
	Host     string `json:"host"`
	Port     string `json:"port"`
	User     string `json:"user"`
	Password string `json:"password"`
	DBName   string `json:"dbname"`
}

type Proxy struct {
	Config     MigrationConfig
	LegacyConn *sql.DB
	YaFaDConn  *sql.DB // Verbindung zum internen YaFaD (oder direkt Funktionsaufruf)
}

func NewProxy() *Proxy {
	// 1. Lade Config
	file, err := os.ReadFile("migration_policy.json")
	if err != nil {
		log.Println("⚠️ No migration policy found. Defaulting to Standalone Mode.")
		return &Proxy{}
	}

	var cfg MigrationConfig
	json.Unmarshal(file, &cfg)

	// 2. Verbinde zur Legacy DB
	connStr := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=disable",
		cfg.LegacyDB.Host, cfg.LegacyDB.Port, cfg.LegacyDB.User, cfg.LegacyDB.Password, cfg.LegacyDB.DBName)

	legacyDb, err := sql.Open("postgres", connStr)
	if err != nil {
		log.Printf("❌ Failed to connect to Legacy DB: %v", err)
	} else {
		fmt.Println("🔌 Connected to Legacy System.")
	}

	return &Proxy{
		Config:     cfg,
		LegacyConn: legacyDb,
	}
}

// RouteQuery entscheidet: Wer bekommt die Anfrage?
// Dies ist eine vereinfachte Router-Logik.
func (p *Proxy) HandleRequest(table string, operation string, data string) {
	target := "LEGACY"

	// Check Whitelist (Strangler Pattern)
	for _, t := range p.Config.Whitelist {
		if t == table {
			target = "YAFAD"
			break
		}
	}

	if target == "YAFAD" {
		p.routeToYaFaD(table, operation, data)
	} else {
		p.routeToLegacy(table, operation, data)
	}
}

func (p *Proxy) routeToYaFaD(table string, op string, data string) {
	// Hier würde der Aufruf an den YaFaD Core (Cortex) gehen
	// Z.B. via Channel oder Funktionsaufruf in main.go
	fmt.Printf("🦁 [PROXY -> YAFAD] Handling '%s' on table '%s' (Optimized Storage)\n", op, table)
	// Code to inject into YaFaD ingest loop...
}

func (p *Proxy) routeToLegacy(table string, op string, data string) {
	if p.LegacyConn == nil {
		fmt.Println("❌ Error: Legacy DB not connected.")
		return
	}
	fmt.Printf("👵 [PROXY -> LEGACY] Passthrough '%s' on table '%s'\n", op, table)

	// Realer SQL Durchstich zur alten DB
	// _, err := p.LegacyConn.Exec("INSERT INTO ...", ...)
}
