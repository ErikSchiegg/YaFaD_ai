package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strings"
	"sync"
)

// --- CONFIG ---
type ProxyConfig struct {
	ListenPort string `json:"listen_port"`
	TargetHost string `json:"target_host"`
	TargetPort string `json:"target_port"`
	BioFilter  struct {
		Inorganic []string `json:"inorganic_ignored"`
		Organic   []string `json:"organic_managed"`
	} `json:"bio_filter"`
}

var config ProxyConfig

func main() {
	// 1. Load Config
	data, err := os.ReadFile("yafad_proxy.json")
	if err != nil {
		log.Fatalf("❌ Could not load config: %v", err)
	}
	json.Unmarshal(data, &config)

	// 2. Start Listener
	listener, err := net.Listen("tcp", ":"+config.ListenPort)
	if err != nil {
		log.Fatalf("❌ Failed to listen on port %s: %v", config.ListenPort, err)
	}

	fmt.Println("╔══════════════════════════════════════════════════╗")
	fmt.Printf("║ 🛡️  YaFaD BIO-PROXY ACTIVE on Port %s          ║\n", config.ListenPort)
	fmt.Printf("║ 🎯 Target: %s:%s                     ║\n", config.TargetHost, config.TargetPort)
	fmt.Println("╚══════════════════════════════════════════════════╝")
	fmt.Printf("Running Bio-Filter on: %v\n", config.BioFilter.Organic)

	for {
		clientConn, err := listener.Accept()
		if err != nil {
			log.Printf("Connection error: %v", err)
			continue
		}
		go handleConnection(clientConn)
	}
}

func handleConnection(clientConn net.Conn) {
	// Connect to Real DB
	targetAddr := config.TargetHost + ":" + config.TargetPort
	dbConn, err := net.Dial("tcp", targetAddr)
	if err != nil {
		log.Printf("❌ Could not connect to DB: %v", err)
		clientConn.Close()
		return
	}

	// Bidirectional Pipes
	var wg sync.WaitGroup
	wg.Add(2)

	// Pipe 1: App -> DB (Hier sitzt der Sniffer!)
	go func() {
		defer wg.Done()
		sniffAndCopy(clientConn, dbConn, true)
	}()

	// Pipe 2: DB -> App (Antworten interessieren uns gerade nicht so sehr)
	go func() {
		defer wg.Done()
		sniffAndCopy(dbConn, clientConn, false)
	}()

	wg.Wait()
}

// Der Herzschlag des Proxies
func sniffAndCopy(src, dst net.Conn, isUpstream bool) {
	defer src.Close()
	defer dst.Close()

	buffer := make([]byte, 4096)
	for {
		n, err := src.Read(buffer)
		if err != nil {
			if err != io.EOF {
				// Connection reset etc.
			}
			return
		}

		// --- THE SNIFFER ---
		if isUpstream {
			// Wir kopieren die Daten für die Analyse, damit wir den Fluss nicht blockieren
			payload := make([]byte, n)
			copy(payload, buffer[:n])

			// Analyse läuft asynchron (Non-Blocking IO)
			go analyzeTraffic(payload)
		}
		// -------------------

		_, err = dst.Write(buffer[:n])
		if err != nil {
			return
		}
	}
}

func analyzeTraffic(data []byte) {
	// Postgres Wire Protocol ist binär, aber SQL Queries stehen oft im Klartext drin.
	// Für diesen Prototyp scannen wir einfach den String.
	// In Production würde man einen echten PG-Parser nehmen.

	content := string(data)
	contentLower := strings.ToLower(content)

	// Filter 1: Ist es ein SELECT?
	if !strings.Contains(contentLower, "select") {
		return
	}

	// Filter 2: Bio-Check
	// Wir suchen nach Tabellennamen
	detectedOrganic := false
	detectedTable := ""

	// Check Inorganic (Ignore)
	for _, table := range config.BioFilter.Inorganic {
		if strings.Contains(contentLower, table) {
			// Es ist eine statische Tabelle (z.B. User Login). Ignorieren.
			return
		}
	}

	// Check Organic (Signal)
	for _, table := range config.BioFilter.Organic {
		if strings.Contains(contentLower, table) {
			detectedOrganic = true
			detectedTable = table
			break
		}
	}

	if detectedOrganic {
		// --- PHEROMONE SIGNAL ---
		// Hier würde der Proxy normalerweise per UDP an den Core senden:
		// "RESET UTILITY FOR ID X IN TABLE Y"

		// Wir simulieren das Loggen und Extrahieren einer ID (Mock)
		fmt.Printf("\r⚡ \033[1;36mSNIFFER:\033[0m Detected access on organic tissue [\033[1;32m%s\033[0m] -> Injecting Pheromone (Reset U=1.0)   ", detectedTable)

		// Simuliere kurze Verzögerung für "Core Contact"
		// In Realität: Fire & Forget UDP Packet
	}
}
