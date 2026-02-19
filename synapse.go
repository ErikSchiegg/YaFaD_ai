package main

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// --- KONFIGURATION & THEME ---
const (
	METRICS_FILE = "yafad_metrics.csv"
	CONFIG_FILE  = "yafad_config.json"
	REFRESH_RATE = 250 * time.Millisecond
)

const YAFAD_LOGO = ` __  __     ______     ______   ______     _____        ______     __    
/\ \_\ \   /\  __ \   /\  ___\ /\  __ \   /\  __-.     /\  __ \   /\ \   
\ \____ \  \ \  __ \  \ \  __\ \ \  __ \  \ \ \/\ \    \ \  __ \  \ \ \  
 \/\_____\  \ \_\ \_\  \ \_\    \ \_\ \_\  \ \____-     \ \_\ \_\  \ \_\ 
\/_____/   \/_/\/_/   \/_/     \/_/\/_/   \/____/      \/_/\/_/   \/_/
  `

var (
	// Palette
	colRam    = lipgloss.Color("#F72585")
	colDream  = lipgloss.Color("#7209B7")
	colCache  = lipgloss.Color("#4CC9F0")
	colCold   = lipgloss.Color("#4895EF")
	colText   = lipgloss.Color("#E0E0E0")
	colGray   = lipgloss.Color("#3A3A3A")
	colGreen  = lipgloss.Color("#43BF6D")
	colWarn   = lipgloss.Color("#F5A623")
	colDanger = lipgloss.Color("#D0021B")
	colInput  = lipgloss.Color("#FFFF00") // Gelb für Eingaben

	// Styles
	appStyle = lipgloss.NewStyle().Margin(1, 1)

	panelStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colGray).
			Padding(0, 1).
			Width(78)

	statLabelStyle = lipgloss.NewStyle().Foreground(colGray).Width(12)
	statValueStyle = lipgloss.NewStyle().Foreground(colText).Bold(true)
)

// --- STATES ---
type sessionState int

const (
	stateDashboard sessionState = iota
	stateInputRecords
	stateInputT0
	stateInputCPU
	stateInputRatio
	stateToggleFlush
	stateToggleReset
	stateConfirm
	stateAbortConfirm
)

// --- MODEL ---
type model struct {
	// System Data
	metrics MetricsData
	config  ConfigData
	err     error
	ticks   int

	// UI State
	state     sessionState
	textInput textinput.Model

	// Wizard Data (Temporary)
	wizRecords string
	wizT0      string
	wizCPU     string
	wizRatio   string
	wizFlush   bool
	wizReset   bool

	cmdStatus string
	cmdColor  lipgloss.Color
}

type MetricsData struct {
	Runtime string
	Biomass string
	T0_Raw  int64
	T0_Pct  float64
	T1, T2  string
	T3, T4  string
	Deep    string
}

type ConfigData struct {
	RunState    string  `json:"run_state"`
	InjectTotal int     `json:"inject_total"`
	T0Limit     int     `json:"t0_hard_limit"`
	CPU         int     `json:"max_cpu_percent"` // A bit hacky mapping, usually nested
	TargetRatio float64 `json:"target_ratio"`
	// Wir mappen die nested structs manuell beim Lesen
}

func initialModel() model {
	ti := textinput.New()
	ti.Cursor.Style = lipgloss.NewStyle().Foreground(colInput)
	ti.Prompt = " > "
	ti.CharLimit = 20
	ti.TextStyle = lipgloss.NewStyle().Foreground(colInput)
	ti.Focus()

	return model{
		state:     stateDashboard,
		textInput: ti,
	}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(tickCmd(), textinput.Blink)
}

// --- UPDATE ---
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd

	switch msg := msg.(type) {

	// 1. GLOBAL KEYS
	case tea.KeyMsg:
		key := msg.String()

		// Immer Quit erlauben (außer beim Tippen vielleicht, aber wir lassen es)
		if key == "ctrl+c" {
			return m, tea.Quit
		}

		// --- DASHBOARD MODE ---
		if m.state == stateDashboard {
			switch key {
			case "q", "ctrl+q":
				return m, tea.Quit
			case "r", "ctrl+r":
				return m, tickCmd()

			// STOP TRIGGER (Jetzt mit Bestätigung)
			case "x":
				m.state = stateAbortConfirm
				return m, nil

			case "s":
				_ = m.loadConfigDefaults()
				m.state = stateInputRecords
				m.textInput.SetValue(m.wizRecords)
				m.textInput.Focus()
				return m, nil
			}
		} else {
			// ABORT CONFIRMATION LOGIC (NEU)
			if m.state == stateAbortConfirm {
				switch key {
				case "y", "Y", "enter":
					// WIRKLICH STOPPEN
					_ = sendStopSignal()
					m.cmdStatus = "🛑 MISSION ABORTED"
					m.cmdColor = colDanger
					m.state = stateDashboard
				case "n", "N", "esc":
					// ZURÜCK
					m.state = stateDashboard
					m.cmdStatus = "🛡️ ABORT CANCELLED"
					m.cmdColor = colGreen
				}
				return m, nil
			}

			// --- WIZARD MODES ---
			switch key {
			case "esc":
				m.state = stateDashboard
				m.cmdStatus = "❌ CANCELLED"
				m.cmdColor = colWarn
				return m, nil
			}

			// Text Input Handling
			if m.state >= stateInputRecords && m.state <= stateInputRatio {
				switch key {
				case "enter":
					// Save & Next
					switch m.state {
					case stateInputRecords:
						m.wizRecords = m.textInput.Value()
						m.state = stateInputT0
						m.textInput.SetValue(m.wizT0)
					case stateInputT0:
						m.wizT0 = m.textInput.Value()
						m.state = stateInputCPU
						m.textInput.SetValue(m.wizCPU)
					case stateInputCPU:
						m.wizCPU = m.textInput.Value()
						m.state = stateInputRatio
						m.textInput.SetValue(m.wizRatio)
					case stateInputRatio:
						m.wizRatio = m.textInput.Value()
						m.state = stateToggleFlush // Next: Bool Toggle
					}
					return m, nil
				}
				// Normal typing
				m.textInput, cmd = m.textInput.Update(msg)
				return m, cmd
			}

			// Boolean Toggles (Flush / Reset)
			if m.state == stateToggleFlush || m.state == stateToggleReset {
				switch key {
				case "y", "Y":
					if m.state == stateToggleFlush {
						m.wizFlush = true
						m.state = stateToggleReset
					} else {
						m.wizReset = true
						m.state = stateConfirm
					}
				case "n", "N", "enter":
					if m.state == stateToggleFlush {
						m.wizFlush = false
						m.state = stateToggleReset
					} else {
						m.wizReset = false
						m.state = stateConfirm
					}
				}
				return m, nil
			}

			// Confirmation
			if m.state == stateConfirm {
				switch key {
				case "y", "Y", "enter":
					// EXECUTE
					err := sendStartSignal(m)
					if err != nil {
						m.cmdStatus = "❌ ERROR: " + err.Error()
						m.cmdColor = colDanger
					} else {
						m.cmdStatus = "🚀 MISSION STARTED"
						m.cmdColor = colGreen
					}
					m.state = stateDashboard
				case "n", "N":
					m.state = stateDashboard
					m.cmdStatus = "❌ ABORTED"
					m.cmdColor = colWarn
				}
				return m, nil
			}
		}

	// 2. TICKER (Data Refresh)
	case tickMsg:
		m.ticks++
		if m.ticks%20 == 0 {
			m.cmdStatus = ""
		}

		// Nur Metrics laden, Config nur bei Bedarf oder Status-Check
		metrics, err := readMetrics()
		if err == nil {
			m.metrics = metrics
		} else {
			m.err = err
		}

		// Wir lesen auch die Config, um den echten "RunState" zu sehen
		conf, _ := readConfigQuick()
		m.config = conf

		return m, tickCmd()
	}

	return m, nil
}

// --- HELPER: IO ---

func (m *model) loadConfigDefaults() error {
	c, err := readConfigQuick()
	if err != nil {
		return err
	}
	m.wizRecords = strconv.Itoa(c.InjectTotal)
	if m.wizRecords == "0" {
		m.wizRecords = "500000"
	}
	m.wizT0 = strconv.Itoa(c.T0Limit)
	if m.wizT0 == "0" {
		m.wizT0 = "100000"
	}
	m.wizCPU = strconv.Itoa(c.CPU) // CPU ist evtl im Nested Struct, wir vereinfachen hier
	if c.CPU == 0 {
		m.wizCPU = "50"
	}
	m.wizRatio = fmt.Sprintf("%.1f", c.TargetRatio)
	if c.TargetRatio == 0 {
		m.wizRatio = "1.0"
	}
	m.wizFlush = false
	m.wizReset = false
	return nil
}

func readConfigQuick() (ConfigData, error) {
	// Liest die Config Datei (vereinfachtes Mapping)
	f, err := os.ReadFile(CONFIG_FILE)
	if err != nil {
		return ConfigData{}, err
	}

	// Wir nutzen map[string]interface{}, um an die nested values zu kommen
	var raw map[string]interface{}
	json.Unmarshal(f, &raw)

	c := ConfigData{}
	if v, ok := raw["run_state"].(string); ok {
		c.RunState = v
	}
	if v, ok := raw["inject_total"].(float64); ok {
		c.InjectTotal = int(v)
	}
	if v, ok := raw["t0_hard_limit"].(float64); ok {
		c.T0Limit = int(v)
	}
	if v, ok := raw["target_ratio"].(float64); ok {
		c.TargetRatio = v
	}

	// Nested CPU
	if limits, ok := raw["limits"].(map[string]interface{}); ok {
		if cpu, ok := limits["max_cpu_percent"].(float64); ok {
			c.CPU = int(cpu)
		}
	}
	return c, nil
}

func sendStartSignal(m model) error {
	// Liest, patcht und speichert Config
	file, _ := os.ReadFile(CONFIG_FILE)
	var config map[string]interface{}
	json.Unmarshal(file, &config)

	// Updates
	config["run_state"] = "RUNNING"
	config["inject_total"], _ = strconv.Atoi(m.wizRecords)
	config["t0_hard_limit"], _ = strconv.Atoi(m.wizT0)
	config["target_ratio"], _ = strconv.ParseFloat(m.wizRatio, 64)

	cpu, _ := strconv.Atoi(m.wizCPU)
	config["limits"] = map[string]interface{}{"max_cpu_percent": cpu}

	if m.wizFlush {
		config["flush_on_start"] = true
	}
	if m.wizReset {
		config["inject_done"] = 0
	}

	data, _ := json.MarshalIndent(config, "", "  ")
	return os.WriteFile(CONFIG_FILE, data, 0644)
}

func sendStopSignal() error {
	file, _ := os.ReadFile(CONFIG_FILE)
	var config map[string]interface{}
	json.Unmarshal(file, &config)
	config["run_state"] = "STOPPED"
	data, _ := json.MarshalIndent(config, "", "  ")
	return os.WriteFile(CONFIG_FILE, data, 0644)
}

// --- VIEW ---
func (m model) View() string {

	// WIZARD OVERLAY
	if m.state != stateDashboard {
		return m.viewWizard()
	}

	// NORMAL DASHBOARD
	if m.err != nil {
		return fmt.Sprintf("\n ❌ SIGNAL LOST: %v\n Waiting for YaFaD Core...\n", m.err)
	}

	// 1. HEADER
	heartbeat := "●"
	if m.ticks%2 == 0 {
		heartbeat = "○"
	}

	// STATUS LOGIC
	statusText := "IDLE"
	statusColor := colGray
	if m.config.RunState == "RUNNING" {
		statusText = "INJECTING"
		statusColor = colGreen
	} else if m.config.RunState == "STOPPED" {
		statusText = "STOPPED"
		statusColor = colDanger
	}

	headerLeft := lipgloss.JoinVertical(lipgloss.Left,
		lipgloss.JoinHorizontal(lipgloss.Left, statLabelStyle.Render("RUNTIME"), statValueStyle.Render(m.metrics.Runtime)),
		lipgloss.JoinHorizontal(lipgloss.Left, statLabelStyle.Render("SYSTEM"), statValueStyle.Foreground(colGreen).Render(heartbeat+" ONLINE")),
	)

	headerRight := lipgloss.JoinVertical(lipgloss.Left,
		lipgloss.JoinHorizontal(lipgloss.Left, statLabelStyle.Render("BIOMASS"), statValueStyle.Render(m.metrics.Biomass)),
		lipgloss.JoinHorizontal(lipgloss.Left, statLabelStyle.Render("STATUS"), statValueStyle.Foreground(statusColor).Render(statusText)),
	)

	headerBox := panelStyle.Copy().BorderForeground(statusColor).Render(
		lipgloss.JoinHorizontal(lipgloss.Center,
			lipgloss.NewStyle().Width(37).Render(headerLeft),
			lipgloss.NewStyle().Width(37).Render(headerRight),
		),
	)

	// 2. CORTEX (T0) mit RAM in MB
	t0Color := colGreen
	if m.metrics.T0_Pct > 80 {
		t0Color = colWarn
	}
	if m.metrics.T0_Pct > 100 {
		t0Color = colDanger
	}

	t0Bar := progressBar(m.metrics.T0_Pct, 60, t0Color)

	// RAM CALCULATION (2KB per record estimate)
	ramBytes := float64(m.metrics.T0_Raw) * 2048.0
	ramMB := ramBytes / (1024.0 * 1024.0)

	t0Stats := fmt.Sprintf("RAM: %.1f MB | PRESSURE: %.1f%%", ramMB, m.metrics.T0_Pct)
	t0StatsStyled := lipgloss.NewStyle().Foreground(colGray).Render(t0Stats)

	cortexPanel := panelStyle.Copy().BorderForeground(t0Color).Render(
		lipgloss.JoinVertical(lipgloss.Left,
			lipgloss.NewStyle().Foreground(t0Color).Bold(true).Render("🧠 CORTEX (T0)"),
			t0Bar,
			t0StatsStyled,
		),
	)

	// 3. FRACTAL LAYERS
	l1 := renderMiniBlock("T1 (Dream)", m.metrics.T1, colDream)
	l2 := renderMiniBlock("T2 (Dream)", m.metrics.T2, colDream)
	l3 := renderMiniBlock("T3 (Fade)", m.metrics.T3, colCache)
	l4 := renderMiniBlock("T4 (Fade)", m.metrics.T4, colCache)

	row1 := lipgloss.JoinHorizontal(lipgloss.Top, l1, l2)
	row2 := lipgloss.JoinHorizontal(lipgloss.Top, l3, l4)

	fractalPanel := panelStyle.Copy().BorderForeground(colDream).Render(
		lipgloss.JoinVertical(lipgloss.Left,
			lipgloss.NewStyle().Foreground(colDream).Bold(true).Render("🕸️ FRACTAL DECAY LAYERS"),
			row1,
			row2,
		),
	)

	// 4. DEEP STORAGE
	deepPanel := panelStyle.Copy().BorderForeground(colCold).Render(
		lipgloss.JoinHorizontal(lipgloss.Left,
			lipgloss.NewStyle().Foreground(colCold).Bold(true).Width(20).Render("💾 DEEP ARCHIVE"),
			statValueStyle.Render(m.metrics.Deep+" records secured"),
		),
	)

	// 5. STATUS LINE
	keysText := "[S] Start Mission | [X] Stop | [Q] Quit"
	styledKeys := lipgloss.NewStyle().Foreground(colGray).Render(keysText)
	statusLine := styledKeys
	if m.cmdStatus != "" {
		statusLine = lipgloss.JoinHorizontal(lipgloss.Left, styledKeys, "   ", lipgloss.NewStyle().Foreground(m.cmdColor).Bold(true).Render(m.cmdStatus))
	}

	// ASSEMBLY
	ui := lipgloss.JoinVertical(lipgloss.Left,
		lipgloss.NewStyle().Width(78).Align(lipgloss.Center).Foreground(colGreen).Bold(true).MarginBottom(1).Render(YAFAD_LOGO),
		headerBox,
		cortexPanel,
		fractalPanel,
		deepPanel,
		lipgloss.NewStyle().MarginTop(1).Render(statusLine),
	)
	return appStyle.Render(ui)
}

func (m model) viewWizard() string {
	// Ein einfaches Overlay für den Wizard
	var title, prompt string

	switch m.state {
	case stateInputRecords:
		title = "SET TARGET RECORDS (Total Biomass)"
		prompt = "Records:"
	case stateInputT0:
		title = "SET T0 CAPACITY (Cortex Limit)"
		prompt = "T0 Size:"
	case stateInputCPU:
		title = "SET CPU THROTTLE (Percent)"
		prompt = "Max CPU %:"
	case stateInputRatio:
		title = "SET FRACTAL RATIO (Phi Target)"
		prompt = "Ratio:"
	case stateToggleFlush:
		title = "FLUSH TABLES? (Empty DB on start)"
		prompt = "[Y] Yes  /  [N] No"
	case stateToggleReset:
		title = "RESET COUNTER? (Start Progress at 0)"
		prompt = "[Y] Yes  /  [N] No"
	case stateConfirm:
		// SUMMARY SCREEN
		summary := fmt.Sprintf(
			"Please Confirm Launch Parameters:\n\n"+
				" • Target Records: %s\n"+
				" • T0 Capacity:    %s\n"+
				" • CPU Limit:      %s%%\n"+
				" • Fractal Ratio:  %s\n"+
				" • Flush Tables:   %v\n"+
				" • Reset Counter:  %v\n\n"+
				"PRESS [ENTER] TO IGNITE  or  [ESC] TO CANCEL",
			m.wizRecords, m.wizT0, m.wizCPU, m.wizRatio, m.wizFlush, m.wizReset,
		)
		box := panelStyle.Copy().BorderForeground(colInput).Padding(1, 2).Render(
			lipgloss.JoinVertical(lipgloss.Center,
				lipgloss.NewStyle().Foreground(colInput).Bold(true).Render("🚀 MISSION CONFIGURATION"),
				lipgloss.NewStyle().Foreground(colText).Margin(1, 0).Render(summary),
			),
		)
		return appStyle.Render(lipgloss.JoinVertical(lipgloss.Center, "\n\n", box))
	}

	// Input Box rendering for steps 1-4
	if m.state <= stateInputRatio {
		box := panelStyle.Copy().BorderForeground(colInput).Padding(1, 2).Render(
			lipgloss.JoinVertical(lipgloss.Left,
				lipgloss.NewStyle().Foreground(colInput).Bold(true).Render(title),
				lipgloss.NewStyle().Foreground(colText).MarginTop(1).Render(prompt),
				m.textInput.View(),
				lipgloss.NewStyle().Foreground(colGray).MarginTop(1).Render("Press [Enter] to Next, [Esc] to Cancel"),
			),
		)
		return appStyle.Render(lipgloss.JoinVertical(lipgloss.Center, "\n\n\n", box))
	}

	// Toggle Box rendering for steps 5-6
	box := panelStyle.Copy().BorderForeground(colInput).Padding(1, 2).Render(
		lipgloss.JoinVertical(lipgloss.Center,
			lipgloss.NewStyle().Foreground(colInput).Bold(true).Render(title),
			lipgloss.NewStyle().Foreground(colText).Margin(1, 0).Render(prompt),
		),
	)
	return appStyle.Render(lipgloss.JoinVertical(lipgloss.Center, "\n\n\n", box))
}

// --- HELPER METRICS ---
func progressBar(pct float64, width int, c lipgloss.Color) string {
	pct = pct / 100.0
	if pct > 1.0 {
		pct = 1.0
	}
	w := int(pct * float64(width))
	return lipgloss.NewStyle().Foreground(c).Render(strings.Repeat("█", w)) + lipgloss.NewStyle().Foreground(colGray).Render(strings.Repeat("░", width-w))
}

func renderMiniBlock(label, val string, c lipgloss.Color) string {
	return lipgloss.NewStyle().Width(37).Render(lipgloss.JoinHorizontal(lipgloss.Left, lipgloss.NewStyle().Foreground(colGray).Width(15).Render(label), lipgloss.NewStyle().Foreground(c).Bold(true).Render(val)))
}

func formatInt(n int64) string {
	in := strconv.FormatInt(n, 10)
	numOfDigits := len(in)
	if n < 0 {
		numOfDigits--
	}
	numOfCommas := (numOfDigits - 1) / 3
	out := make([]byte, len(in)+numOfCommas)
	if n < 0 {
		in, out[0] = in[1:], '-'
	}
	for i, j, k := len(in)-1, len(out)-1, 0; ; i, j = i-1, j-1 {
		out[j] = in[i]
		if i == 0 {
			return string(out)
		}
		if k++; k == 3 {
			j, k = j-1, 0
			out[j] = ','
		}
	}
}

func mustInt(s string) int64 { i, _ := strconv.ParseInt(s, 10, 64); return i }

type tickMsg time.Time

func tickCmd() tea.Cmd {
	return tea.Tick(REFRESH_RATE, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func readMetrics() (MetricsData, error) {
	f, err := os.Open(METRICS_FILE)
	if err != nil {
		return MetricsData{}, err
	}
	defer f.Close()
	reader := csv.NewReader(f)
	var last []string
	for {
		rec, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err == nil {
			last = rec
		}
	}
	if len(last) < 10 {
		return MetricsData{}, fmt.Errorf("syncing...")
	}
	rt_sec, _ := strconv.ParseInt(last[1], 10, 64)
	t0, _ := strconv.ParseInt(last[3], 10, 64)
	t0_pct, _ := strconv.ParseFloat(last[9], 64)
	return MetricsData{
		Runtime: fmt.Sprintf("%02d:%02d", rt_sec/60, rt_sec%60),
		Biomass: formatInt(mustInt(last[2])),
		T0_Raw:  t0,
		T0_Pct:  t0_pct,
		T1:      formatInt(mustInt(last[4])), T2: formatInt(mustInt(last[5])),
		T3: formatInt(mustInt(last[6])), T4: formatInt(mustInt(last[7])),
		Deep: formatInt(mustInt(last[8])),
	}, nil
}

func main() {
	p := tea.NewProgram(initialModel(), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Printf("Error: %v", err)
		os.Exit(1)
	}
}
