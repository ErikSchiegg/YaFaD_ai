package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgproto3/v2"
	"github.com/jackc/pgx/v5/pgxpool"
)

// --- CONFIGURATION STRUCTURE ---
type ProxyConfig struct {
	ListenPort string `json:"listen_port"`
	TargetHost string `json:"target_host"`
	BioFilter  struct {
		ManagedTables []string `json:"managed_tables"`
		IdPattern     string   `json:"id_pattern"`
	} `json:"bio_filter"`
}

var (
	config        ProxyConfig
	pheromoneChan chan string
	idRegex       *regexp.Regexp
	managedTables []string // Optimierter Cache (Upper Case)
)

func main() {
	// 1. Load Config
	loadConfig("yafad_proxy.json")

	// 2. Setup Pheromone Pipeline
	pheromoneChan = make(chan string, 10000)
	go startPheromoneWorker()

	// 3. Start Listener
	listener, err := net.Listen("tcp", ":"+config.ListenPort)
	if err != nil {
		log.Fatalf("❌ Failed to listen on port %s: %v", config.ListenPort, err)
	}

	log.Printf("🛡️  YaFaD SQL Proxy active on port %s", config.ListenPort)
	log.Printf("🌿 Bio-Filter active for tables: %v", config.BioFilter.ManagedTables)
	log.Printf("👉 Redirecting system traffic to %s", config.TargetHost)

	for {
		clientConn, err := listener.Accept()
		if err != nil {
			log.Printf("⚠️ Connection error: %v", err)
			continue
		}
		go handleConnection(clientConn)
	}
}

func loadConfig(path string) {
	file, err := os.ReadFile(path)
	if err != nil {
		log.Fatalf("❌ Could not load config %s: %v", path, err)
	}
	err = json.Unmarshal(file, &config)
	if err != nil {
		log.Fatalf("❌ Invalid Config JSON: %v", err)
	}

	// Pre-compile Regex and Table Names for Performance
	idRegex = regexp.MustCompile(config.BioFilter.IdPattern)

	managedTables = make([]string, len(config.BioFilter.ManagedTables))
	for i, t := range config.BioFilter.ManagedTables {
		managedTables[i] = strings.ToUpper(t)
	}
}

func handleConnection(clientConn net.Conn) {
	defer clientConn.Close()

	// Connect to real DB
	pgConn, err := net.Dial("tcp", config.TargetHost)
	if err != nil {
		log.Printf("❌ Backend DB down (%s): %v", config.TargetHost, err)
		return
	}
	defer pgConn.Close()

	errChan := make(chan error, 2)

	// DB -> Client (Passthrough)
	go func() {
		_, err := io.Copy(clientConn, pgConn)
		errChan <- err
	}()

	// Client -> DB (Sniffing)
	go func() {
		frontend := pgproto3.NewFrontend(pgproto3.NewChunkReader(clientConn), clientConn)
		for {
			msg, err := frontend.Receive()
			if err != nil {
				errChan <- err
				return
			}

			// --- INTELLIGENT SNIFFER ---
			switch m := msg.(type) {
			case *pgproto3.Query:
				analyzeQuery(m.String)
			case *pgproto3.Parse:
				analyzeQuery(m.Query)
			}

			// Forward
			raw := msg.Encode(nil)
			if _, err = pgConn.Write(raw); err != nil {
				errChan <- err
				return
			}
		}
	}()

	<-errChan
}

// --- INTELLIGENCE LAYER ---

func analyzeQuery(sql string) {
	sqlUpper := strings.ToUpper(sql)

	// 1. SYSTEM FILTER: Ignoriere alles, was nicht unsere Tabellen betrifft
	isOrganic := false
	for _, table := range managedTables {
		if strings.Contains(sqlUpper, table) {
			isOrganic = true
			break
		}
	}

	// Wenn die Tabelle nicht "managed" ist, ignorieren wir sie komplett.
	// Systemtabellen (users, config, migrations) bleiben unberührt.
	if !isOrganic {
		return
	}

	// 2. ID EXTRACTION (Nur bei organischen Tabellen)
	matches := idRegex.FindAllString(sql, -1)
	for _, id := range matches {
		select {
		case pheromoneChan <- id:
		default:
			// Channel voll, drop packet
		}
	}
}

func startPheromoneWorker() {
	// DB Connection für den Worker
	dbUser := os.Getenv("DB_USER")
	if dbUser == "" {
		dbUser = "eriks"
	}
	dbPass := os.Getenv("DB_PASSWORD")
	if dbPass == "" {
		dbPass = "test"
	}

	// Wir verbinden uns direkt zur Backend DB (5432), nicht zum Proxy!
	connStr := fmt.Sprintf("postgres://%s:%s@%s/yafad_test?sslmode=disable",
		dbUser, dbPass, config.TargetHost)

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		log.Fatal("Worker cannot connect to DB")
	}
	defer pool.Close()

	buffer := make([]string, 0, 100)
	ticker := time.NewTicker(500 * time.Millisecond)

	log.Println("🧪 Pheromone Injector ready.")

	for {
		select {
		case id := <-pheromoneChan:
			buffer = append(buffer, id)
			if len(buffer) >= 50 {
				flushPheromones(ctx, pool, buffer)
				buffer = buffer[:0]
			}
		case <-ticker.C:
			if len(buffer) > 0 {
				flushPheromones(ctx, pool, buffer)
				buffer = buffer[:0]
			}
		}
	}
}

func flushPheromones(ctx context.Context, db *pgxpool.Pool, ids []string) {
	if len(ids) == 0 {
		return
	}

	uniqueIDs := make(map[string]bool)
	for _, id := range ids {
		uniqueIDs[id] = true
	}

	// Hier aktualisieren wir blind die Hot Tiers.
	// In einer vollen Implementation könnte man basierend auf der Query wissen,
	// welche Tabelle genau getroffen wurde, aber für den MVP reicht der Broadcast.
	count := 0
	for id := range uniqueIDs {
		// Wir feuern asynchron Updates ab
		go func(recID string) {
			// Wir boosten Table0 und Table1
			queries := []string{
				fmt.Sprintf("UPDATE table0 SET utility_index = 1.0, last_activity = NOW() WHERE id = '%s'", recID),
				fmt.Sprintf("UPDATE table1 SET utility_index = 1.0, last_activity = NOW() WHERE id = '%s'", recID),
			}
			for _, q := range queries {
				db.Exec(context.Background(), q)
			}
		}(id)
		count++
	}
}
