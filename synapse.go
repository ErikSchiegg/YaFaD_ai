package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// --- CONFIG & URLS ---
const (
	METRICS_URL  = "http://localhost:2112/metrics"
	CONFIG_FILE  = "yafad_config.json"
	REFRESH_RATE = 500 * time.Millisecond
)

const YAFAD_LOGO = ` __  __     ______     ______   ______     _____        ______     __    
/\ \_\ \   /\  __ \   /\  ___\ /\  __ \   /\  __-.     /\  __ \   /\ \   
\ \____ \  \ \  __ \  \ \  __\ \ \  __ \  \ \ \/\ \    \ \  __ \  \ \ \  
 \/\_____\  \ \_\ \_\  \ \_\    \ \_\ \_\  \ \____-     \ \_\ \_\  \ \_\ 
\/_____/   \/_/\/_/   \/_/     \/_/\/_/   \/____/      \/_/\/_/   \/_/`

var (
	colRam    = lipgloss.Color("#F72585")
	colDream  = lipgloss.Color("#BD93F9")
	colCache  = lipgloss.Color("#4CC9F0")
	colCold   = lipgloss.Color("#4895EF")
	colText   = lipgloss.Color("#E0E0E0")
	colGray   = lipgloss.Color("#A0A0A0")
	colGreen  = lipgloss.Color("#43BF6D")
	colWarn   = lipgloss.Color("#F5A623")
	colDanger = lipgloss.Color("#D0021B")
	colInput  = lipgloss.Color("#FFFF00")

	appStyle   = lipgloss.NewStyle().Margin(1, 1)
	panelStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colGray).
			Padding(0, 1).
			Width(78)

	statLabelStyle = lipgloss.NewStyle().Foreground(colGray).Width(12)
	statValueStyle = lipgloss.NewStyle().Foreground(colText).Bold(true)
)

type sessionState int

const (
	stateDashboard sessionState = iota
	stateInputRecords
	stateInputT0
	stateInputCPU
	stateChooseMode
	stateConfirm
	stateAbortConfirm
	stateTuneT0
	stateTuneKp
	stateTuneKi
	stateTuneKd
	stateTuneBuoy
	stateTuneWHigh
	stateTuneWLow
	stateTuneConfirm
)

type MetricsData struct {
	Runtime     string
	Biomass     string
	Biomass_Raw int64
	T0_Raw      int64
	T0_Pct      float64
	T1, T2      string
	T3, T4      string
	Deep        string
	PhiDiff     float64
}

type ConfigData struct {
	RunState    string
	InjectTotal int
	InjectDone  int
	T0Limit     int
	CPU         int
	Kp          float64
	Ki          float64
	Kd          float64
	Buoyancy    float64
	WHigh       float64
	WLow        float64
}

type model struct {
	metrics   MetricsData
	config    ConfigData
	err       error
	ticks     int
	state     sessionState
	textInput textinput.Model

	wizRecords string
	wizT0      string
	wizCPU     string
	wizMode    int

	wizTuneT0    string
	wizTuneKp    string
	wizTuneKi    string
	wizTuneKd    string
	wizTuneBuoy  string
	wizTuneWHigh string
	wizTuneWLow  string

	cmdStatus string
	cmdColor  lipgloss.Color
}

type tickMsg time.Time

func initialModel() model {
	ti := textinput.New()
	ti.Cursor.Style = lipgloss.NewStyle().Foreground(colInput)
	ti.Prompt = " > "
	ti.Focus()

	return model{
		state:     stateDashboard,
		textInput: ti,
	}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(tickCmd(), textinput.Blink)
}

func tickCmd() tea.Cmd {
	return tea.Tick(REFRESH_RATE, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// --- CORE LOGIC: METRICS FROM PROMETHEUS ---
func readMetricsFromPrometheus() (MetricsData, error) {
	resp, err := http.Get(METRICS_URL)
	if err != nil {
		return MetricsData{}, err
	}
	defer resp.Body.Close()

	m := MetricsData{}
	tiers := make(map[string]int64)
	scanner := bufio.NewScanner(resp.Body)

	var rtSec int64

	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		val, _ := strconv.ParseFloat(parts[1], 64)

		key := parts[0]
		switch {
		case strings.HasPrefix(key, "process_cpu_seconds_total"):
			rtSec = int64(val)
		case strings.HasPrefix(key, "yafad_total_biomass"):
			m.Biomass_Raw = int64(val)
		case strings.HasPrefix(key, "yafad_phi_diff"):
			m.PhiDiff = val
		case strings.HasPrefix(key, "yafad_state_value"):
			if strings.Contains(key, `tier="table0"`) {
				m.T0_Raw = int64(val)
			}
			if strings.Contains(key, `tier="table1"`) {
				tiers["t1"] = int64(val)
			}
			if strings.Contains(key, `tier="table2"`) {
				tiers["t2"] = int64(val)
			}
			if strings.Contains(key, `tier="table3"`) {
				tiers["t3"] = int64(val)
			}
			if strings.Contains(key, `tier="table4"`) {
				tiers["t4"] = int64(val)
			}
			if strings.Contains(key, `tier="deep_archive"`) {
				tiers["deep"] = int64(val)
			}
		}
	}

	m.Runtime = fmt.Sprintf("%02d:%02d", rtSec/60, rtSec%60)
	m.Biomass = formatInt(m.Biomass_Raw)
	m.T1 = formatInt(tiers["t1"])
	m.T2 = formatInt(tiers["t2"])
	m.T3 = formatInt(tiers["t3"])
	m.T4 = formatInt(tiers["t4"])
	m.Deep = formatInt(tiers["deep"])

	return m, nil
}

func readConfigQuick() (ConfigData, error) {
	f, err := os.ReadFile(CONFIG_FILE)
	if err != nil {
		return ConfigData{}, err
	}
	var raw map[string]interface{}
	json.Unmarshal(f, &raw)
	c := ConfigData{Kp: 1.5, Ki: 0.05, Kd: 0.2}
	if v, ok := raw["run_state"].(string); ok {
		c.RunState = v
	}
	if v, ok := raw["inject_total"].(float64); ok {
		c.InjectTotal = int(v)
	}
	if v, ok := raw["inject_done"].(float64); ok {
		c.InjectDone = int(v)
	}
	if v, ok := raw["t0_hard_limit"].(float64); ok {
		c.T0Limit = int(v)
	}
	// ... restliches Parsing wie gehabt
	return c, nil
}

func (m *model) loadConfigDefaults() error {
	c, _ := readConfigQuick()
	m.wizRecords = strconv.Itoa(c.InjectTotal)
	m.wizT0 = strconv.Itoa(c.T0Limit)
	m.wizCPU = "50"
	return nil
}

func (m *model) loadTuneDefaults() error {
	c, _ := readConfigQuick()
	m.wizTuneT0 = strconv.Itoa(c.T0Limit)
	m.wizTuneKp = fmt.Sprintf("%.2f", c.Kp)
	m.wizTuneKi = fmt.Sprintf("%.3f", c.Ki)
	m.wizTuneKd = fmt.Sprintf("%.2f", c.Kd)
	return nil
}

// --- UPDATE ---
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch msg := msg.(type) {
	case tea.KeyMsg:
		key := msg.String()
		if key == "ctrl+c" {
			return m, tea.Quit
		}

		if m.state == stateDashboard {
			switch key {
			case "q":
				return m, tea.Quit
			case "s":
				_ = m.loadConfigDefaults()
				m.state = stateInputRecords
				m.textInput.SetValue(m.wizRecords)
				return m, nil
			case "x":
				m.state = stateAbortConfirm
				return m, nil
			case "t":
				_ = m.loadTuneDefaults()
				m.state = stateTuneT0
				m.textInput.SetValue(m.wizTuneT0)
				return m, nil
			}
		} else {
			// Wizard Logik
			if key == "esc" {
				m.state = stateDashboard
				return m, nil
			}
			if key == "enter" {
				switch m.state {
				case stateInputRecords:
					m.wizRecords = m.textInput.Value()
					m.state = stateInputT0
				case stateInputT0:
					m.wizT0 = m.textInput.Value()
					m.state = stateInputCPU
				case stateInputCPU:
					m.wizCPU = m.textInput.Value()
					m.state = stateChooseMode
				case stateChooseMode:
					// hier Modus Logik einbauen
				case stateConfirm:
					// sendStartSignal...
					m.state = stateDashboard
				}
			}
			m.textInput, cmd = m.textInput.Update(msg)
			return m, cmd
		}

	case tickMsg:
		m.ticks++
		metrics, err := readMetricsFromPrometheus()
		if err == nil {
			m.metrics = metrics
			if m.config.T0Limit > 0 {
				m.metrics.T0_Pct = (float64(m.metrics.T0_Raw) / float64(m.config.T0Limit)) * 100
			}
			m.err = nil
		} else {
			m.err = err
		}
		m.config, _ = readConfigQuick()
		return m, tickCmd()
	}
	return m, nil
}

// --- VIEW (DEIN ORIGINAL DESIGN) ---
func (m model) View() string {
	if m.state != stateDashboard {
		return m.viewWizard()
	}
	if m.err != nil {
		return fmt.Sprintf("\n ❌ SIGNAL LOST: %v\n", m.err)
	}

	heartbeat := "○"
	if m.ticks%2 == 0 {
		heartbeat = "●"
	}
	statusText := "IDLE"
	statusColor := colGray
	if m.config.RunState == "RUNNING" {
		statusText = "INJECTING"
		statusColor = colGreen
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
		lipgloss.JoinHorizontal(lipgloss.Center, lipgloss.NewStyle().Width(37).Render(headerLeft), lipgloss.NewStyle().Width(37).Render(headerRight)),
	)

	t0Bar := progressBar(m.metrics.T0_Pct, 60, colGreen)
	cortexPanel := panelStyle.Render(lipgloss.JoinVertical(lipgloss.Left, "🧠 CORTEX (T0)", t0Bar))

	// FRACTAL LAYERS (T1-T4)
	l1 := renderMiniBlock("T1 (Dream)", m.metrics.T1, colDream)
	l2 := renderMiniBlock("T2 (Dream)", m.metrics.T2, colDream)
	fractalPanel := panelStyle.Render(lipgloss.JoinVertical(lipgloss.Left, "🕸️ FRACTAL DECAY", lipgloss.JoinHorizontal(lipgloss.Top, l1, l2)))

	ui := lipgloss.JoinVertical(lipgloss.Left,
		lipgloss.NewStyle().Width(78).Align(lipgloss.Center).Foreground(colGreen).Render(YAFAD_LOGO),
		headerBox, cortexPanel, fractalPanel,
		lipgloss.NewStyle().MarginTop(1).Render("[S] Start | [T] Tune | [Q] Quit"),
	)
	return appStyle.Render(ui)
}

// Wizard View (Platzhalter für deine Wizard Logik)
func (m model) viewWizard() string {
	return "Wizard State: " + strconv.Itoa(int(m.state)) + "\nPress Esc to return"
}

// Hilfs-Renderer
func progressBar(pct float64, width int, c lipgloss.Color) string {
	w := int(pct / 100.0 * float64(width))
	if w > width {
		w = width
	}
	return lipgloss.NewStyle().Foreground(c).Render(strings.Repeat("█", w)) + lipgloss.NewStyle().Foreground(colGray).Render(strings.Repeat("░", width-w))
}
func renderMiniBlock(label, val string, c lipgloss.Color) string {
	return lipgloss.NewStyle().Width(37).Render(lipgloss.JoinHorizontal(lipgloss.Left, statLabelStyle.Render(label), lipgloss.NewStyle().Foreground(c).Bold(true).Render(val)))
}
func formatInt(n int64) string {
	in := strconv.FormatInt(n, 10)
	out := make([]byte, len(in)+(len(in)-1)/3)
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

func main() {
	p := tea.NewProgram(initialModel(), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Printf("Error: %v", err)
		os.Exit(1)
	}
}
