import gradio as gr
import json
import time
import os

CONFIG_FILE = "yafad_config.json"
DASHBOARD_PORT = 7888
DEFAULT_GRAFANA = "http://localhost:3030/d/adf7cqm/yafad-ai-biomass-tiers?orgId=1&from=now-1h&to=now&timezone=browser&refresh=2s&showCategory=Graph%20styles"
PHI_SUM_FACTOR = 16.326
PHI = 1.61803398875

# --- HELPER: CONFIG & IO ---
def get_current_config():
    default_conf = {
        "run_state": "IDLE",
        "inject_total": 500000,
        "inject_done": 0,
        "t0_hard_limit": 100000, # Default safe limit
        "target_ratio": 1.0,
        "limits": {"max_cpu_percent": 50},
        "pid_settings": {"kp": 1.5, "ki": 0.05, "kd": 0.2},
        "vanish_threshold": "10m",
        "capacities": {},
        "flush_on_start": False,
        "grafana_url": DEFAULT_GRAFANA,
        "buoyancy_factor": 0.7,
        "watermarks": {"high": 150.0, "low": 120.0}
    }
    try:
        if os.path.exists(CONFIG_FILE):
            with open(CONFIG_FILE, 'r') as f:
                data = json.load(f)
                default_conf.update(data)
                if "t0_hard_limit" not in data: default_conf["t0_hard_limit"] = 100000
                if "watermarks" not in data: default_conf["watermarks"] = {"high": 150.0, "low": 120.0}
                return default_conf
    except Exception as e:
        print(f"Error reading config: {e}")
    return default_conf

def save_config(conf):
    conf["last_updated"] = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
    try:
        with open(CONFIG_FILE, 'w') as f:
            json.dump(conf, f, indent=2)
        return "✅ Config saved."
    except Exception as e:
        return f"❌ Error: {str(e)}"

# --- HELPER: RAM CALCULATION ---
def calculate_ram_impact(t0_limit, high_mark):
    """
    Berechnet den RAM-Verbrauch basierend auf Records * Watermark.
    Annahme: 1 Record (DB Index + Go Struct + JSON Payload) ~ 2.0 KB Overhead.
    """
    try:
        limit = int(t0_limit)
        pct = float(high_mark)
        
        if limit <= 0:
            return "🤖 Auto-Scale Mode: RAM depends on Total Records."
            
        # Peak Records = Limit * 150% (oder was eingestellt ist)
        peak_records = limit * (pct / 100.0)
        
        # Bytes berechnen (2 KB per Record ist ein konservativer Wert für DB+App)
        bytes_total = peak_records * 2048 
        
        mb = bytes_total / (1024 * 1024)
        gb = mb / 1024
        
        if gb >= 1.0:
            return f"⚠️ Estimated Peak RAM: **{gb:.2f} GB** ({int(peak_records):,} Records at {pct}%)"
        else:
            return f"ℹ️ Estimated Peak RAM: **{mb:.1f} MB** ({int(peak_records):,} Records at {pct}%)"
    except:
        return "..."

# --- GRAFANA HELPER ---
def get_grafana_iframe(url):
    if "&kiosk" not in url and "?" in url:
        url += "&kiosk&theme=dark"
    elif "&kiosk" not in url:
        url += "?kiosk&theme=dark"
    return f"""
    <div style="width: 100%; height: 95vh; border: 1px solid #444; border-radius: 8px; overflow: hidden; background-color: #0b0c0e;">
        <iframe src="{url}" width="100%" height="100%" frameborder="0"></iframe>
    </div>
    """

def update_grafana_url(new_url):
    conf = get_current_config()
    conf["grafana_url"] = new_url
    save_config(conf)
    return get_grafana_iframe(new_url), "✅ URL Updated"

# --- MISSION CONTROL ---
def start_mission(total_recs, t0_limit, cpu_percent, target_ratio, flush_tables):
    conf = get_current_config()
    
    t0_base = 0
    if int(t0_limit) > 0:
        t0_base = int(t0_limit)
        print(f"🔧 Hardware Mode: T0 fixed to {t0_base}")
    else:
        raw_target = float(total_recs)
        t0_base = int(raw_target / PHI_SUM_FACTOR)
        print(f"⚖️  Proportional Mode: T0 calc to {t0_base}")

    caps = {
        "table0": int(t0_base),
        "table1": int(t0_base * PHI),
        "table2": int(t0_base * PHI**2),
        "table3": int(t0_base * PHI**3),
        "table4": int(t0_base * PHI**4)
    }
    
    conf["capacities"] = caps
    conf["inject_total"] = int(total_recs)
    conf["t0_hard_limit"] = int(t0_limit)
    conf["inject_done"] = 0
    
    if "limits" not in conf: conf["limits"] = {}
    conf["limits"]["max_cpu_percent"] = int(cpu_percent)
    conf["target_ratio"] = float(target_ratio)
    conf["flush_on_start"] = flush_tables
    conf["run_state"] = "RUNNING"
    
    save_config(conf)
    
    mode_text = "Hardware Limit" if int(t0_limit) > 0 else "Auto-Scale"
    msg = f"🚀 IGNITION! {int(total_recs)} records (Mode: {mode_text})"
    if flush_tables: msg += " + Flush"
    return msg

def stop_mission():
    conf = get_current_config()
    conf["run_state"] = "STOPPED"
    save_config(conf)
    return "🛑 ABORT SIGNAL SENT."

def update_tuning(kp, ki, kd, buoyancy, w_high, w_low):
    conf = get_current_config()
    conf["pid_settings"] = {"kp": float(kp), "ki": float(ki), "kd": float(kd)}
    conf["buoyancy_factor"] = float(buoyancy)
    conf["watermarks"] = {"high": float(w_high), "low": float(w_low)}
    return save_config(conf)

def update_general(vanish):
    conf = get_current_config()
    conf["vanish_threshold"] = vanish
    return save_config(conf)

def get_status_text():
    conf = get_current_config()
    state = conf.get("run_state", "UNKNOWN")
    done = conf.get("inject_done", 0)
    total = conf.get("inject_total", 1)
    if total <= 0: total = 1
    pct = (done / total) * 100
    
    icon = "🔴"
    if state == "RUNNING": icon = "🟢"
    if state == "IDLE": icon = "🟡"
    if state == "SETTLING": icon = "🛬"
    
    t0_lim = conf.get("t0_hard_limit", 0)
    limit_text = f"Auto-Scale"
    if t0_lim > 0: limit_text = f"Fixed T0: {t0_lim}"
    
    return f"**State:** {icon} {state} | **Progress:** {pct:.1f}% | **Mode:** {limit_text}"

# --- UI LAYOUT ---
with gr.Blocks(title="YaFaD v0.9.1 Mission Control", theme=gr.themes.Monochrome()) as app:
    gr.Markdown("# 🦁 YaFaD v0.9.1 Mission Control")
    
    init_conf = get_current_config()
    current_grafana = init_conf.get("grafana_url", DEFAULT_GRAFANA)
    init_high_water = init_conf.get("watermarks", {}).get("high", 150.0)

    with gr.Row():
        # LEFT: Grafana
        with gr.Column(scale=4):
            html_grafana = gr.HTML(value=get_grafana_iframe(current_grafana))
            
        # RIGHT: Controls
        with gr.Column(scale=1):
            status_display = gr.Markdown(get_status_text)
            timer = gr.Timer(1)
            timer.tick(get_status_text, inputs=None, outputs=status_display)

            with gr.Tabs():
                # TAB 1: MISSION
                with gr.TabItem("🚀 Mission"):
                    gr.Markdown("### 1. Goal")
                    n_recs = gr.Number(value=init_conf.get("inject_total", 500000), label="Total Records to Inject")
                    
                    gr.Markdown("### 2. Hardware Constraints (RAM)")
                    n_t0_limit = gr.Number(value=init_conf.get("t0_hard_limit", 100000), label="T0 Capacity (Records)", info="Base capacity (100%). System may exceed this by High Watermark %.")
                    
                    # NEU: Live RAM Warning
                    lbl_ram_warn = gr.Markdown(value="Calculating RAM...")
                    
                    gr.Markdown("### 3. Parameters")
                    s_cpu = gr.Slider(10, 100, value=init_conf.get("limits", {}).get("max_cpu_percent", 50), label="CPU Limit (%)")
                    s_ratio = gr.Slider(0.5, 2.0, value=init_conf.get("target_ratio", 1.0), label="Target Pressure")
                    chk_flush = gr.Checkbox(label="⚠️ Flush Tables", value=init_conf.get("flush_on_start", False))
                    
                    btn_start = gr.Button("🔥 IGNITION", variant="primary")
                    btn_stop = gr.Button("🛑 ABORT", variant="stop")
                    out_mission = gr.Label(label="Last Command")
                    
                    btn_start.click(start_mission, inputs=[n_recs, n_t0_limit, s_cpu, s_ratio, chk_flush], outputs=out_mission)
                    btn_stop.click(stop_mission, outputs=out_mission)

                # TAB 2: TUNING
                with gr.TabItem("🎛️ Tuning"):
                    gr.Markdown("### Pulse Injection Control")
                    w_high = gr.Slider(100, 250, value=init_high_water, label="🌊 High Water Mark (%) - STOP")
                    w_low = gr.Slider(50, 200, value=init_conf.get("watermarks", {}).get("low", 120), label="⚡ Low Water Mark (%) - RESUME")
                    
                    gr.Markdown("### Physics")
                    s_buoyancy = gr.Slider(0.0, 1.0, value=init_conf.get("buoyancy_factor", 0.7), step=0.05, label="🛟 Buoyancy")
                    
                    gr.Markdown("---")
                    pid = init_conf.get("pid_settings", {"kp": 1.5, "ki": 0.05, "kd": 0.2})
                    s_kp = gr.Slider(0.0, 5.0, value=pid["kp"], label="Kp")
                    s_ki = gr.Slider(0.0, 1.0, value=pid["ki"], label="Ki")
                    s_kd = gr.Slider(0.0, 2.0, value=pid["kd"], label="Kd")
                    
                    btn_tune = gr.Button("Update Physics & Pulse")
                    out_tune = gr.Label()
                    btn_tune.click(update_tuning, inputs=[s_kp, s_ki, s_kd, s_buoyancy, w_high, w_low], outputs=out_tune)
                
                # TAB 3: SETTINGS
                with gr.TabItem("⚙️ Settings"):
                    gr.Markdown("### Dashboard View")
                    txt_grafana = gr.Textbox(label="Grafana URL", value=current_grafana)
                    # NEU: Grafana Hint
                    gr.Markdown("""
                    <br><div style="font-size: 0.8em; color: #888; margin-top: -10px;">
                    ℹ️ <b>Troubleshooting:</b> If the chart is blank, enable embedding in <code>/etc/grafana/grafana.ini</code>:<br>
                    <code>[security]</code><br>
                    <code>allow_embedding = true</code>
                    </div>
                    """)
                    
                    btn_grafana = gr.Button("Reload View")
                    lbl_url_status = gr.Label(visible=False)
                    
                    gr.Markdown("### System Config")
                    t_vanish = gr.Textbox(value=init_conf.get("vanish_threshold", "10m"), label="Vanish Threshold")
                    btn_gen = gr.Button("Save")
                    out_gen = gr.Label()
                    
                    btn_grafana.click(update_grafana_url, inputs=txt_grafana, outputs=[html_grafana, lbl_url_status])
                    btn_gen.click(update_general, inputs=[t_vanish], outputs=out_gen)
    
    # --- INTERACTIVITY WIRING ---
    # Wenn sich das Limit ODER der Watermark-Slider ändert, RAM neu berechnen
    n_t0_limit.change(calculate_ram_impact, inputs=[n_t0_limit, w_high], outputs=lbl_ram_warn)
    w_high.change(calculate_ram_impact, inputs=[n_t0_limit, w_high], outputs=lbl_ram_warn)
    
    # Initial Call beim Laden
    app.load(calculate_ram_impact, inputs=[n_t0_limit, w_high], outputs=lbl_ram_warn)

    gr.Markdown(f"Backend Port: {DASHBOARD_PORT}")

if __name__ == "__main__":
    app.launch(server_name="0.0.0.0", server_port=DASHBOARD_PORT)