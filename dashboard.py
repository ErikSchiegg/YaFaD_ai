import gradio as gr
import psycopg2
import json
import time
import os
import subprocess

# --- GLOBAL SETTINGS ---
CONFIG_FILE = "yafad_config.json"
DASHBOARD_PORT = 7888
DEFAULT_GRAFANA = "http://localhost:3000/d/yafad-main"
PHI_SUM_FACTOR = 16.326
PHI = 1.61803398875

# Prozess-Handle für den Simulator
PROXY_PROCESS = None

# --- HELPER: CONFIG & IO ---
def get_current_config():
    default_conf = {
        "run_state": "IDLE",
        "inject_total": 500000,
        "inject_done": 0,
        "t0_hard_limit": 100000,
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
                if "watermarks" in data: default_conf["watermarks"].update(data["watermarks"])
                if "pid_settings" in data: default_conf["pid_settings"].update(data["pid_settings"])
                if "limits" in data: default_conf["limits"].update(data["limits"])
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

# --- ACTION: RELOAD UI VALUES ---
def reload_ui_values():
    """Liest die Config und gibt ALLE Werte für die UI-Komponenten zurück"""
    c = get_current_config()
    
    # Mission Tab Values
    total_recs = c.get("inject_total", 500000)
    t0 = c.get("t0_hard_limit", 100000)
    cpu = c.get("limits", {}).get("max_cpu_percent", 50)
    ratio = c.get("target_ratio", 1.0)
    flush = c.get("flush_on_start", False)
    
    # Tuning Tab Values
    w_high = c.get("watermarks", {}).get("high", 150.0)
    w_low = c.get("watermarks", {}).get("low", 120.0)
    buoy = c.get("buoyancy_factor", 0.7)
    kp = c.get("pid_settings", {}).get("kp", 1.5)
    ki = c.get("pid_settings", {}).get("ki", 0.05)
    kd = c.get("pid_settings", {}).get("kd", 0.2)
    
    # Settings Tab
    grafana = c.get("grafana_url", DEFAULT_GRAFANA)
    vanish = c.get("vanish_threshold", "10m")

    timestamp = time.strftime("%H:%M:%S")
    msg = f"🔄 UI Synced from System at {timestamp}"

    return (
        total_recs, t0, cpu, ratio, flush,      # Mission
        t0, w_high, w_low, buoy, kp, ki, kd,    # Tuning
        grafana, vanish,                        # Settings
        msg
    )

# --- RAM CALC ---
def calculate_ram_impact(t0_limit, high_mark):
    try:
        limit = int(t0_limit)
        pct = float(high_mark)
        if limit <= 0: return "🤖 Auto-Scale Mode"
        
        peak_records = limit * (pct / 100.0)
        bytes_total = peak_records * 2048 
        mb = bytes_total / (1024 * 1024)
        gb = mb / 1024
        
        if gb >= 1.0:
            return f"⚠️ Est. RAM: **{gb:.2f} GB** ({int(peak_records):,} Recs)"
        return f"ℹ️ Est. RAM: **{mb:.1f} MB** ({int(peak_records):,} Recs)"
    except:
        return "..."

# --- ACTION: TUNING UPDATE WITH SAFETY CLAMP ---
def update_tuning(t0_req, kp, ki, kd, buoyancy, w_high, w_low):
    conf = get_current_config()
    
    current_t0 = conf.get("t0_hard_limit", 100000)
    requested_t0 = int(t0_req)
    
    # Safety Clamp Logic (+/- 25%)
    status_prefix = "✅"
    final_t0 = requested_t0

    if current_t0 > 0 and requested_t0 > 0:
        delta = current_t0 * 0.25 
        min_allowed = int(current_t0 - delta)
        max_allowed = int(current_t0 + delta)
        
        if requested_t0 < min_allowed:
            final_t0 = min_allowed
            status_prefix = f"⚠️ Safety Clamp (-25%): Limited to {final_t0}"
        elif requested_t0 > max_allowed:
            final_t0 = max_allowed
            status_prefix = f"⚠️ Safety Clamp (+25%): Limited to {final_t0}"
    
    conf["t0_hard_limit"] = final_t0
    conf["pid_settings"] = {"kp": float(kp), "ki": float(ki), "kd": float(kd)}
    conf["buoyancy_factor"] = float(buoyancy)
    conf["watermarks"] = {"high": float(w_high), "low": float(w_low)}
    
    save_config(conf)
    return f"{status_prefix} | Physics Updated", final_t0

def start_mission(total_recs, t0_limit, cpu_percent, target_ratio, flush_tables, reset_counter):
    conf = get_current_config()
    
    if reset_counter:
        conf["inject_done"] = 0
        msg_prefix = "🔄 RESET & IGNITION!"
    else:
        msg_prefix = "🚀 RESUMING IGNITION!"

    conf["inject_total"] = int(total_recs)
    conf["t0_hard_limit"] = int(t0_limit)
    conf["limits"] = {"max_cpu_percent": int(cpu_percent)}
    conf["target_ratio"] = float(target_ratio)
    conf["flush_on_start"] = flush_tables
    conf["run_state"] = "RUNNING"
    
    save_config(conf)
    return f"{msg_prefix} Target: {int(total_recs)}"

def stop_mission():
    conf = get_current_config()
    conf["run_state"] = "STOPPED"
    save_config(conf)
    return "🛑 ABORT SIGNAL SENT."

def update_grafana_url(new_url):
    conf = get_current_config()
    conf["grafana_url"] = new_url
    save_config(conf)
    return get_grafana_iframe(new_url), "✅ URL Updated"

def update_general(vanish):
    conf = get_current_config()
    conf["vanish_threshold"] = vanish
    save_config(conf)
    return "✅ Settings Saved"

# --- HELPER: GRAFANA ---
def get_grafana_iframe(url):
    if "&kiosk" not in url and "?" in url: url += "&kiosk&theme=dark"
    elif "&kiosk" not in url: url += "?kiosk&theme=dark"
    return f'<div style="width:100%; height:95vh; border:1px solid #444; border-radius:8px; overflow:hidden;"><iframe src="{url}" width="100%" height="100%" frameborder="0"></iframe></div>'

# --- HELPER: MIGRATION ---
def scan_database(host, port, user, password, dbname):
    try:
        conn = psycopg2.connect(host=host, port=port, user=user, password=password, dbname=dbname)
        cur = conn.cursor()
        cur.execute("SELECT table_name FROM information_schema.tables WHERE table_schema='public'")
        tables = [row[0] for row in cur.fetchall()]
        conn.close()
        return gr.CheckboxGroup(choices=tables, value=[], visible=True, label="Detected Tables"), "✅ Connection OK"
    except Exception as e:
        return gr.update(visible=False), f"❌ Connection failed: {e}"

def start_strangler_migration(target_tables, host, port, user, pw, db):
    global PROXY_PROCESS
    config = {
        "mode": "strangler_fig", 
        "legacy_db": {"host": host, "port": port, "user": user, "password": pw, "dbname": db},
        "yafad_whitelist": target_tables
    }
    with open("migration_policy.json", "w") as f: json.dump(config, f, indent=4)
    
    if PROXY_PROCESS: return "⚠️ Proxy already running."
    try:
        cmd = ["go", "run", "user_simulator.go", "yafad_proxy.go"]
        PROXY_PROCESS = subprocess.Popen(cmd, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, cwd=os.getcwd())
        return f"🚀 Proxy & Simulator STARTED (PID: {PROXY_PROCESS.pid})"
    except Exception as e:
        return f"❌ Start failed: {e}"

def stop_strangler_migration():
    global PROXY_PROCESS
    if not PROXY_PROCESS: return "⚠️ No proxy running."
    try:
        PROXY_PROCESS.terminate()
        PROXY_PROCESS.wait(timeout=2)
        PROXY_PROCESS = None
        return "🛑 Proxy stopped."
    except:
        PROXY_PROCESS.kill()
        PROXY_PROCESS = None
        return "💀 Proxy killed."

# --- STATUS TICKER ---
def get_status_text():
    conf = get_current_config()
    state = conf.get("run_state", "UNKNOWN")
    done = conf.get("inject_done", 0)
    total = conf.get("inject_total", 1)
    t0 = conf.get("t0_hard_limit", 0)
    
    icon = "🔴"
    if state == "RUNNING": icon = "🟢"
    if state == "IDLE": icon = "🟡"
    
    return f"**State:** {icon} {state} | **Progress:** {(done/total)*100:.1f}% | **T0 Cap:** {t0}"

# --- UI LAYOUT ---
with gr.Blocks(title="YaFaD v0.9.3 Mission Control", theme=gr.themes.Monochrome()) as app:
    
    init_conf = get_current_config()
    
    with gr.Row():
        gr.Markdown("## 🦁 YaFaD v0.9.3 Mission Control")
        btn_refresh_all = gr.Button("🔄 Sync UI from System", variant="secondary", size="sm")

    with gr.Row():
        # LEFT
        with gr.Column(scale=4):
            html_grafana = gr.HTML(value=get_grafana_iframe(init_conf.get("grafana_url", DEFAULT_GRAFANA)))
            
        # RIGHT
        with gr.Column(scale=1):
            status_display = gr.Markdown(get_status_text)
            timer = gr.Timer(2)
            timer.tick(get_status_text, outputs=status_display)

            with gr.Tabs():
                # --- TAB 1: MISSION ---
                with gr.TabItem("🚀 Mission"):
                    n_recs = gr.Number(value=init_conf.get("inject_total"), label="Total Records")
                    n_t0_mission = gr.Number(value=init_conf.get("t0_hard_limit"), label="T0 Capacity (Start)")
                    lbl_ram = gr.Markdown("Calc RAM...")
                    
                    s_cpu = gr.Slider(10, 100, value=init_conf.get("limits", {}).get("max_cpu_percent"), label="CPU Limit %")
                    s_ratio = gr.Slider(0.5, 2.0, value=init_conf.get("target_ratio"), label="Target Ratio")
                    
                    with gr.Row():
                        chk_flush = gr.Checkbox(label="Flush Tables (Empty DB)", value=init_conf.get("flush_on_start"))
                        chk_reset = gr.Checkbox(label="Reset Counter (Start at 0)", value=False)
                    
                    with gr.Row():
                        btn_start = gr.Button("🔥 IGNITION", variant="primary")
                        btn_stop = gr.Button("🛑 ABORT", variant="stop")
                    out_mission = gr.Label(label="Status")
                    
                    btn_start.click(
                        start_mission, 
                        inputs=[n_recs, n_t0_mission, s_cpu, s_ratio, chk_flush, chk_reset], 
                        outputs=out_mission
                    )
                    btn_stop.click(stop_mission, outputs=out_mission)

                # --- TAB 2: TUNING ---
                with gr.TabItem("🎛️ Tuning"):
                    gr.Markdown("### Dynamic Architecture")
                    n_t0_tune = gr.Number(
                        value=init_conf.get("t0_hard_limit"), 
                        label="T0 Capacity (Live Update)",
                        info="🛡️ Safety Lock Active: Changes are limited to ±25% per update."
                    )
                    
                    gr.Markdown("### Pulse Control")
                    w_high = gr.Slider(100, 250, value=init_conf.get("watermarks", {}).get("high"), label="High Watermark (Stop)")
                    w_low = gr.Slider(50, 200, value=init_conf.get("watermarks", {}).get("low"), label="Low Watermark (Resume)")
                    s_buoy = gr.Slider(0, 1, value=init_conf.get("buoyancy_factor"), label="Buoyancy")
                    
                    gr.Markdown("### PID Controller")
                    pid = init_conf.get("pid_settings", {})
                    s_kp = gr.Slider(0, 5, value=pid.get("kp"), label="Kp")
                    s_ki = gr.Slider(0, 1, value=pid.get("ki"), label="Ki")
                    s_kd = gr.Slider(0, 2, value=pid.get("kd"), label="Kd")
                    
                    btn_tune = gr.Button("Update Live Physics")
                    out_tune = gr.Label()
                    
                    btn_tune.click(
                        update_tuning, 
                        inputs=[n_t0_tune, s_kp, s_ki, s_kd, s_buoy, w_high, w_low], 
                        outputs=[out_tune, n_t0_tune]
                    )

                # --- TAB 3: SETTINGS ---
                with gr.TabItem("⚙️ Settings"):
                    txt_grafana = gr.Textbox(label="Grafana URL", value=init_conf.get("grafana_url"))
                    t_vanish = gr.Textbox(value=init_conf.get("vanish_threshold"), label="Vanish Threshold")
                    
                    btn_graf = gr.Button("Reload View")
                    btn_set = gr.Button("Save Settings")
                    lbl_set = gr.Label()
                    
                    btn_graf.click(update_grafana_url, inputs=txt_grafana, outputs=[html_grafana, lbl_set])
                    btn_set.click(update_general, inputs=[t_vanish], outputs=lbl_set)

                # --- TAB 4: MIGRATION ---
                with gr.TabItem("🏗️ Migration"):
                    db_h = gr.Textbox(label="Host", value="localhost")
                    db_p = gr.Textbox(label="Port", value="5432")
                    db_u = gr.Textbox(label="User", value="postgres")
                    db_pw = gr.Textbox(label="Pass", type="password")
                    db_n = gr.Textbox(label="DB Name")
                    
                    btn_scan = gr.Button("Scan DB")
                    sel_tabs = gr.CheckboxGroup(visible=False)
                    stat_mig = gr.Textbox(interactive=False, label="Status")
                    
                    with gr.Row():
                        btn_run_mig = gr.Button("🚀 Start Proxy", variant="primary")
                        btn_kill_mig = gr.Button("🛑 Stop Proxy", variant="stop")
                    
                    btn_scan.click(scan_database, inputs=[db_h, db_p, db_u, db_pw, db_n], outputs=[sel_tabs, stat_mig])
                    btn_run_mig.click(start_strangler_migration, inputs=[sel_tabs, db_h, db_p, db_u, db_pw, db_n], outputs=stat_mig)
                    btn_kill_mig.click(stop_strangler_migration, outputs=stat_mig)

    # --- WIRING: SYNC BUTTON ---
    btn_refresh_all.click(
        reload_ui_values,
        inputs=None,
        outputs=[
            n_recs, n_t0_mission, s_cpu, s_ratio, chk_flush,
            n_t0_tune, w_high, w_low, s_buoy, s_kp, s_ki, s_kd,
            txt_grafana, t_vanish,
            out_mission
        ]
    )
    
    n_t0_mission.change(calculate_ram_impact, inputs=[n_t0_mission, w_high], outputs=lbl_ram)
    n_t0_tune.change(calculate_ram_impact, inputs=[n_t0_tune, w_high], outputs=lbl_ram)
    w_high.change(calculate_ram_impact, inputs=[n_t0_tune, w_high], outputs=lbl_ram)

if __name__ == "__main__":
    app.launch(server_name="0.0.0.0", server_port=DASHBOARD_PORT)