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

const (
	METRICS_FILE = "yafad_metrics.csv"
	CONFIG_FILE  = "yafad_config.json"
	REFRESH_RATE = 250 * time.Millisecond
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
	colGray   = lipgloss.Color("#3A3A3A")
	colGreen  = lipgloss.Color("#43BF6D")
	colWarn   = lipgloss.Color("#F5A623")
	colDanger = lipgloss.Color("#D0021B")
	colInput  = lipgloss.Color("#FFFF00")

	appStyle = lipgloss.NewStyle().Margin(1, 1)

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
	stateInputRatio
	stateToggleFlush
	stateToggleReset
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
	wizRatio   string
	wizFlush   bool
	wizReset   bool

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

type MetricsData struct {
	Runtime     string
	Biomass     string
	Biomass_Raw int64
	T0_Raw      int64
	T0_Pct      float64
	T1, T2      string
	T3, T4      string
	Deep        string
}

type ConfigData struct {
	RunState    string
	InjectTotal int
	InjectDone  int
	T0Limit     int
	CPU         int
	TargetRatio float64
	Kp          float64
	Ki          float64
	Kd          float64
	Buoyancy    float64
	WHigh       float64
	WLow        float64
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
			case "q", "ctrl+q":
				return m, tea.Quit
			case "r", "ctrl+r":
				return m, tickCmd()
			case "x":
				m.state = stateAbortConfirm
				return m, nil
			case "t":
				_ = m.loadTuneDefaults()
				m.state = stateTuneT0
				m.textInput.SetValue(m.wizTuneT0)
				m.textInput.Focus()
				return m, nil
			case "s":
				_ = m.loadConfigDefaults()
				m.state = stateInputRecords
				m.textInput.SetValue(m.wizRecords)
				m.textInput.Focus()
				return m, nil
			}
		} else {
			if m.state == stateAbortConfirm {
				switch key {
				case "y", "Y", "enter":
					_ = sendStopSignal()
					m.cmdStatus = "🛑 MISSION ABORTED"
					m.cmdColor = colDanger
					m.state = stateDashboard
				case "n", "N", "esc":
					m.state = stateDashboard
					m.cmdStatus = "🛡️ ABORT CANCELLED"
					m.cmdColor = colGreen
				}
				return m, nil
			}
			if key == "esc" {
				m.state = stateDashboard
				m.cmdStatus = "❌ CANCELLED"
				m.cmdColor = colWarn
				return m, nil
			}

			isTextInputState := m.state >= stateInputRecords && m.state <= stateInputRatio || m.state >= stateTuneT0 && m.state <= stateTuneWLow
			if isTextInputState {
				if key == "enter" {
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
						m.state = stateToggleFlush
					case stateTuneT0:
						m.wizTuneT0 = m.textInput.Value()
						m.state = stateTuneKp
						m.textInput.SetValue(m.wizTuneKp)
					case stateTuneKp:
						m.wizTuneKp = m.textInput.Value()
						m.state = stateTuneKi
						m.textInput.SetValue(m.wizTuneKi)
					case stateTuneKi:
						m.wizTuneKi = m.textInput.Value()
						m.state = stateTuneKd
						m.textInput.SetValue(m.wizTuneKd)
					case stateTuneKd:
						m.wizTuneKd = m.textInput.Value()
						m.state = stateTuneBuoy
						m.textInput.SetValue(m.wizTuneBuoy)
					case stateTuneBuoy:
						m.wizTuneBuoy = m.textInput.Value()
						m.state = stateTuneWHigh
						m.textInput.SetValue(m.wizTuneWHigh)
					case stateTuneWHigh:
						m.wizTuneWHigh = m.textInput.Value()
						m.state = stateTuneWLow
						m.textInput.SetValue(m.wizTuneWLow)
					case stateTuneWLow:
						m.wizTuneWLow = m.textInput.Value()
						m.state = stateTuneConfirm
					}
					return m, nil
				}
				m.textInput, cmd = m.textInput.Update(msg)
				return m, cmd
			}

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

			if m.state == stateConfirm || m.state == stateTuneConfirm {
				if key == "y" || key == "Y" || key == "enter" {
					var err error
					if m.state == stateConfirm {
						err = sendStartSignal(m)
						m.cmdStatus = "🚀 MISSION STARTED"
					} else {
						err = sendTuneSignal(m)
						m.cmdStatus = "🎛️ PHYSICS UPDATED"
					}

					if err != nil {
						m.cmdStatus = "❌ ERROR: " + err.Error()
						m.cmdColor = colDanger
					} else {
						m.cmdColor = colGreen
					}
					m.state = stateDashboard
				} else if key == "n" || key == "N" {
					m.state = stateDashboard
					m.cmdStatus = "❌ ABORTED"
					m.cmdColor = colWarn
				}
				return m, nil
			}
		}

	case tickMsg:
		m.ticks++
		if m.ticks%20 == 0 {
			m.cmdStatus = ""
		}

		metrics, err := readMetrics()
		if err == nil {
			m.metrics = metrics
			m.err = nil // <--- DER WICHTIGE BUGFIX: Löst den Lock
		} else {
			m.err = err
		}

		conf, _ := readConfigQuick()
		m.config = conf
		return m, tickCmd()
	}

	return m, nil
}

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
	m.wizCPU = strconv.Itoa(c.CPU)
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

func (m *model) loadTuneDefaults() error {
	c, err := readConfigQuick()
	if err != nil {
		return err
	}
	m.wizTuneT0 = strconv.Itoa(c.T0Limit)
	m.wizTuneKp = fmt.Sprintf("%.2f", c.Kp)
	m.wizTuneKi = fmt.Sprintf("%.3f", c.Ki)
	m.wizTuneKd = fmt.Sprintf("%.2f", c.Kd)
	m.wizTuneBuoy = fmt.Sprintf("%.2f", c.Buoyancy)
	m.wizTuneWHigh = fmt.Sprintf("%.1f", c.WHigh)
	m.wizTuneWLow = fmt.Sprintf("%.1f", c.WLow)
	return nil
}

func readConfigQuick() (ConfigData, error) {
	f, err := os.ReadFile(CONFIG_FILE)
	if err != nil {
		return ConfigData{}, err
	}
	var raw map[string]interface{}
	json.Unmarshal(f, &raw)

	c := ConfigData{Kp: 1.5, Ki: 0.05, Kd: 0.2, Buoyancy: 0.7, WHigh: 150.0, WLow: 120.0}

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
	if v, ok := raw["target_ratio"].(float64); ok {
		c.TargetRatio = v
	}

	if limits, ok := raw["limits"].(map[string]interface{}); ok {
		if cpu, ok := limits["max_cpu_percent"].(float64); ok {
			c.CPU = int(cpu)
		}
	}
	if pidSettings, ok := raw["pid_settings"].(map[string]interface{}); ok {
		if kp, ok := pidSettings["kp"].(float64); ok {
			c.Kp = kp
		}
		if ki, ok := pidSettings["ki"].(float64); ok {
			c.Ki = ki
		}
		if kd, ok := pidSettings["kd"].(float64); ok {
			c.Kd = kd
		}
	}
	if b, ok := raw["buoyancy_factor"].(float64); ok {
		c.Buoyancy = b
	}
	if watermarks, ok := raw["watermarks"].(map[string]interface{}); ok {
		if wh, ok := watermarks["high"].(float64); ok {
			c.WHigh = wh
		}
		if wl, ok := watermarks["low"].(float64); ok {
			c.WLow = wl
		}
	}
	return c, nil
}

func sendStartSignal(m model) error {
	file, _ := os.ReadFile(CONFIG_FILE)
	var config map[string]interface{}
	json.Unmarshal(file, &config)
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

func sendTuneSignal(m model) error {
	file, _ := os.ReadFile(CONFIG_FILE)
	var config map[string]interface{}
	json.Unmarshal(file, &config)
	t0, _ := strconv.Atoi(m.wizTuneT0)
	config["t0_hard_limit"] = t0
	kp, _ := strconv.ParseFloat(m.wizTuneKp, 64)
	ki, _ := strconv.ParseFloat(m.wizTuneKi, 64)
	kd, _ := strconv.ParseFloat(m.wizTuneKd, 64)

	if _, ok := config["pid_settings"]; !ok {
		config["pid_settings"] = map[string]interface{}{}
	}
	pid := config["pid_settings"].(map[string]interface{})
	pid["kp"], pid["ki"], pid["kd"] = kp, ki, kd
	b, _ := strconv.ParseFloat(m.wizTuneBuoy, 64)
	config["buoyancy_factor"] = b

	if _, ok := config["watermarks"]; !ok {
		config["watermarks"] = map[string]interface{}{}
	}
	wm := config["watermarks"].(map[string]interface{})
	wh, _ := strconv.ParseFloat(m.wizTuneWHigh, 64)
	wl, _ := strconv.ParseFloat(m.wizTuneWLow, 64)
	wm["high"], wm["low"] = wh, wl
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

func (m model) View() string {
	if m.state != stateDashboard {
		return m.viewWizard()
	}
	if m.err != nil {
		return fmt.Sprintf("\n ❌ SIGNAL LOST: %v\n Waiting for YaFaD Core...\n", m.err)
	}

	heartbeat := "●"
	if m.ticks%2 == 0 {
		heartbeat = "○"
	}
	statusText := "IDLE"
	statusColor := colGray
	if m.config.RunState == "RUNNING" {
		statusText = "INJECTING"
		statusColor = colGreen
	} else if m.config.RunState == "STOPPED" {
		statusText = "STOPPED"
		statusColor = colDanger
	}

	targetAbs := m.metrics.Biomass_Raw + int64(m.config.InjectTotal) - int64(m.config.InjectDone)

	headerLeft := lipgloss.JoinVertical(lipgloss.Left,
		lipgloss.JoinHorizontal(lipgloss.Left, statLabelStyle.Render("RUNTIME"), statValueStyle.Render(m.metrics.Runtime)),
		lipgloss.JoinHorizontal(lipgloss.Left, statLabelStyle.Render("SYSTEM"), statValueStyle.Foreground(colGreen).Render(heartbeat+" ONLINE")),
		lipgloss.JoinHorizontal(lipgloss.Left, statLabelStyle.Render("PID (Kp)"), statValueStyle.Render(fmt.Sprintf("%.2f", m.config.Kp))),
	)

	headerRight := lipgloss.JoinVertical(lipgloss.Left,
		lipgloss.JoinHorizontal(lipgloss.Left, statLabelStyle.Render("BIOMASS"), statValueStyle.Render(m.metrics.Biomass)),
		lipgloss.JoinHorizontal(lipgloss.Left, statLabelStyle.Render("STATUS"), statValueStyle.Foreground(statusColor).Render(statusText)),
		lipgloss.JoinHorizontal(lipgloss.Left, statLabelStyle.Render("TARGET"), statValueStyle.Render(formatInt(targetAbs))),
	)

	headerBox := panelStyle.Copy().BorderForeground(statusColor).Render(
		lipgloss.JoinHorizontal(lipgloss.Center, lipgloss.NewStyle().Width(37).Render(headerLeft), lipgloss.NewStyle().Width(37).Render(headerRight)),
	)

	t0Color := colGreen
	if m.metrics.T0_Pct > 80 {
		t0Color = colWarn
	}
	if m.metrics.T0_Pct > 100 {
		t0Color = colDanger
	}
	t0Bar := progressBar(m.metrics.T0_Pct, 60, t0Color)
	ramMB := (float64(m.metrics.T0_Raw) * 2048.0) / (1024.0 * 1024.0)
	maxRamMB := (float64(m.config.T0Limit) * 2048.0) / (1024.0 * 1024.0)
	t0Stats := fmt.Sprintf("RAM: %.1f MB / %.1f MB | PRESSURE: %.1f%%", ramMB, maxRamMB, m.metrics.T0_Pct)

	t0Values := fmt.Sprintf("%s / %s", formatInt(m.metrics.T0_Raw), formatInt(int64(m.config.T0Limit)))
	t0Header := lipgloss.JoinHorizontal(lipgloss.Left,
		lipgloss.NewStyle().Foreground(t0Color).Bold(true).Render("🧠 CORTEX (T0)"),
		lipgloss.NewStyle().Foreground(t0Color).Bold(true).MarginLeft(2).Render(t0Values),
	)

	cortexPanel := panelStyle.Copy().BorderForeground(t0Color).Render(lipgloss.JoinVertical(lipgloss.Left, t0Header, t0Bar, lipgloss.NewStyle().Foreground(colGray).Render(t0Stats)))

	l1 := renderMiniBlock("T1 (Dream)", m.metrics.T1, colDream)
	l2 := renderMiniBlock("T2 (Dream)", m.metrics.T2, colDream)
	l3 := renderMiniBlock("T3 (Fade)", m.metrics.T3, colCache)
	l4 := renderMiniBlock("T4 (Fade)", m.metrics.T4, colCache)
	fractalPanel := panelStyle.Copy().BorderForeground(colDream).Render(lipgloss.JoinVertical(lipgloss.Left, lipgloss.NewStyle().Foreground(colDream).Bold(true).Render("🕸️ FRACTAL DECAY LAYERS"), lipgloss.JoinHorizontal(lipgloss.Top, l1, l2), lipgloss.JoinHorizontal(lipgloss.Top, l3, l4)))

	deepPanel := panelStyle.Copy().BorderForeground(colCold).Render(lipgloss.JoinHorizontal(lipgloss.Left, lipgloss.NewStyle().Foreground(colCold).Bold(true).Width(20).Render("💾 DEEP ARCHIVE"), statValueStyle.Render(m.metrics.Deep+" records secured")))

	styledKeys := lipgloss.NewStyle().Foreground(colGray).Render("[S] Start | [T] Tune | [X] Stop | [Q] Quit")
	statusLine := styledKeys
	if m.cmdStatus != "" {
		statusLine = lipgloss.JoinHorizontal(lipgloss.Left, styledKeys, "   ", lipgloss.NewStyle().Foreground(m.cmdColor).Bold(true).Render(m.cmdStatus))
	}

	ui := lipgloss.JoinVertical(lipgloss.Left, lipgloss.NewStyle().Width(78).Align(lipgloss.Center).Foreground(colGreen).Bold(true).MarginBottom(1).Render(YAFAD_LOGO), headerBox, cortexPanel, fractalPanel, deepPanel, lipgloss.NewStyle().MarginTop(1).Render(statusLine))
	return appStyle.Render(ui)
}

func (m model) viewWizard() string {
	if m.state == stateAbortConfirm {
		return appStyle.Render(lipgloss.JoinVertical(lipgloss.Center, "\n\n\n", panelStyle.Copy().BorderForeground(colDanger).Padding(1, 2).Render(lipgloss.JoinVertical(lipgloss.Center, lipgloss.NewStyle().Foreground(colDanger).Bold(true).Blink(true).Render("⚠️  EMERGENCY STOP  ⚠️"), lipgloss.NewStyle().Foreground(colText).MarginTop(1).Render("ABORT MISSION?   [Y] Yes   /   [N] No")))))
	}

	var title, prompt string
	switch m.state {
	case stateInputRecords:
		title, prompt = "SET INJECTION AMOUNT (New Records)", "Records:"
	case stateInputT0:
		title, prompt = "SET T0 CAPACITY (Cortex Limit)", "T0 Size:"
	case stateInputCPU:
		title, prompt = "SET CPU THROTTLE (Percent)", "Max CPU %:"
	case stateInputRatio:
		title, prompt = "SET FRACTAL RATIO (Phi Target)", "Ratio:"
	case stateToggleFlush:
		title, prompt = "FLUSH TABLES? (Empty DB on start)", "[Y] Yes  /  [N] No"
	case stateToggleReset:
		title, prompt = "RESET COUNTER? (Start Progress at 0)", "[Y] Yes  /  [N] No"
	case stateTuneT0:
		title, prompt = "🎛️ TUNE: T0 Capacity (Hard Limit)", "T0 Limit:"
	case stateTuneKp:
		title, prompt = "🎛️ TUNE: PID Kp (Proportional)", "Kp Value:"
	case stateTuneKi:
		title, prompt = "🎛️ TUNE: PID Ki (Integral)", "Ki Value:"
	case stateTuneKd:
		title, prompt = "🎛️ TUNE: PID Kd (Derivative)", "Kd Value:"
	case stateTuneBuoy:
		title, prompt = "🎛️ TUNE: Buoyancy Factor", "Buoyancy:"
	case stateTuneWHigh:
		title, prompt = "🎛️ TUNE: High Watermark (%)", "High Mark:"
	case stateTuneWLow:
		title, prompt = "🎛️ TUNE: Low Watermark (%)", "Low Mark:"
	case stateConfirm:
		summary := fmt.Sprintf("Please Confirm Launch Parameters:\n\n • Inject Amount:  %s\n • T0 Capacity:    %s\n • CPU Limit:      %s%%\n • Fractal Ratio:  %s\n • Flush Tables:   %v\n • Reset Counter:  %v\n\nPRESS [ENTER] TO IGNITE  or  [ESC] TO CANCEL", m.wizRecords, m.wizT0, m.wizCPU, m.wizRatio, m.wizFlush, m.wizReset)
		return appStyle.Render(lipgloss.JoinVertical(lipgloss.Center, "\n\n", panelStyle.Copy().BorderForeground(colInput).Padding(1, 2).Render(lipgloss.JoinVertical(lipgloss.Center, lipgloss.NewStyle().Foreground(colInput).Bold(true).Render("🚀 MISSION CONFIGURATION"), lipgloss.NewStyle().Foreground(colText).Margin(1, 0).Render(summary)))))
	case stateTuneConfirm:
		summary := fmt.Sprintf("Confirm Physics Update:\n\n • T0 Limit:  %s\n • PID (Kp):  %s\n • PID (Ki):  %s\n • PID (Kd):  %s\n • Buoyancy:  %s\n • High Mark: %s%%\n • Low Mark:  %s%%\n\nPRESS [ENTER] TO APPLY  or  [ESC] TO CANCEL", m.wizTuneT0, m.wizTuneKp, m.wizTuneKi, m.wizTuneKd, m.wizTuneBuoy, m.wizTuneWHigh, m.wizTuneWLow)
		return appStyle.Render(lipgloss.JoinVertical(lipgloss.Center, "\n\n", panelStyle.Copy().BorderForeground(colInput).Padding(1, 2).Render(lipgloss.JoinVertical(lipgloss.Center, lipgloss.NewStyle().Foreground(colInput).Bold(true).Render("⚙️ APPLY TUNING"), lipgloss.NewStyle().Foreground(colText).Margin(1, 0).Render(summary)))))
	}

	isTextInputState := m.state >= stateInputRecords && m.state <= stateInputRatio || m.state >= stateTuneT0 && m.state <= stateTuneWLow
	if isTextInputState {
		box := panelStyle.Copy().BorderForeground(colInput).Padding(1, 2).Render(lipgloss.JoinVertical(lipgloss.Left, lipgloss.NewStyle().Foreground(colInput).Bold(true).Render(title), lipgloss.NewStyle().Foreground(colText).MarginTop(1).Render(prompt), m.textInput.View(), lipgloss.NewStyle().Foreground(colGray).MarginTop(1).Render("Press [Enter] to Next, [Esc] to Cancel")))
		return appStyle.Render(lipgloss.JoinVertical(lipgloss.Center, "\n\n\n", box))
	}
	box := panelStyle.Copy().BorderForeground(colInput).Padding(1, 2).Render(lipgloss.JoinVertical(lipgloss.Center, lipgloss.NewStyle().Foreground(colInput).Bold(true).Render(title), lipgloss.NewStyle().Foreground(colText).Margin(1, 0).Render(prompt)))
	return appStyle.Render(lipgloss.JoinVertical(lipgloss.Center, "\n\n\n", box))
}

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
	reader.FieldsPerRecord = -1 // <--- FIX für unvollständige Zeilen während des Schreibens
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
	bm_raw := mustInt(last[2])
	return MetricsData{
		Runtime: fmt.Sprintf("%02d:%02d", rt_sec/60, rt_sec%60), Biomass: formatInt(bm_raw), Biomass_Raw: bm_raw, T0_Raw: t0, T0_Pct: t0_pct,
		T1: formatInt(mustInt(last[4])), T2: formatInt(mustInt(last[5])), T3: formatInt(mustInt(last[6])), T4: formatInt(mustInt(last[7])), Deep: formatInt(mustInt(last[8])),
	}, nil
}
func main() {
	p := tea.NewProgram(initialModel(), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Printf("Error: %v", err)
		os.Exit(1)
	}
}
