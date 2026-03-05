import gradio as gr
import psycopg2
import json
import time
import os
import subprocess

# --- GLOBAL SETTINGS ---
CONFIG_FILE = "yafad_config.json"
METRICS_FILE = "yafad_metrics.csv"
DASHBOARD_PORT = 7888
DEFAULT_GRAFANA = "http://localhost:3030/d/adf7cqm/yafad-ai-biomass-tiers?orgId=1&from=now-90m&to=now&timezone=browser&refresh=5s&tab=queries"

PROXY_PROCESS = None
PROXY_LOG_FILE = None 

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
        pass
    return default_conf

def save_config(conf):
    try:
        with open(CONFIG_FILE, 'w') as f:
            json.dump(conf, f, indent=2)
        return "✅ Config saved."
    except Exception as e:
        return f"❌ Error: {str(e)}"

def get_current_biomass():
    try:
        if os.path.exists(METRICS_FILE):
            with open(METRICS_FILE, "r") as f:
                lines = f.readlines()
                if len(lines) > 1:
                    last_line = lines[-1].strip().split(",")
                    if len(last_line) >= 3:
                        return int(last_line[2])
    except:
        pass
    return 0

def reload_ui_values():
    c = get_current_config()
    total_recs = c.get("inject_total", 500000)
    t0 = c.get("t0_hard_limit", 100000)
    cpu = c.get("limits", {}).get("max_cpu_percent", 50)
    w_high = c.get("watermarks", {}).get("high", 150.0)
    w_low = c.get("watermarks", {}).get("low", 120.0)
    buoy = c.get("buoyancy_factor", 0.7)
    kp = c.get("pid_settings", {}).get("kp", 1.5)
    ki = c.get("pid_settings", {}).get("ki", 0.05)
    kd = c.get("pid_settings", {}).get("kd", 0.2)
    grafana = c.get("grafana_url", DEFAULT_GRAFANA)
    vanish = c.get("vanish_threshold", "10m")
    msg = f"🔄 UI Synced from System at {time.strftime('%H:%M:%S')}"
    return (total_recs, t0, cpu, t0, cpu, w_high, w_low, buoy, kp, ki, kd, grafana, vanish, msg)

def calc_sim_ram(t0_limit):
    try:
        limit = int(t0_limit)
        if limit <= 0: return "🤖 Auto-Scale Mode"
        peak_records = limit * 1.5
        mb = (peak_records * 2048) / (1024 * 1024)
        if mb >= 1024: return f"⚠️ Est. Peak RAM: **{mb/1024:.2f} GB**"
        return f"ℹ️ Est. Peak RAM: **{mb:.1f} MB**"
    except:
        return "..."

def calc_mig_ram(t0_limit):
    try:
        limit = int(t0_limit)
        if limit <= 0: return "🤖 Auto-Scale Mode"
        peak_records = limit * 1.5
        mb = (peak_records * 2048) / (1024 * 1024)
        if mb >= 1024: return f"⚠️ Est. Pulse RAM: **{mb/1024:.2f} GB**"
        return f"ℹ️ Est. Pulse RAM: **{mb:.1f} MB**"
    except:
        return "..."

def hard_flush_yafad_db():
    global PROXY_PROCESS, PROXY_LOG_FILE
    db_user = os.getenv("DB_USER", "eriks")
    db_pass = os.getenv("DB_PASSWORD", "test")
    try:
        conf = get_current_config()
        conf["run_state"] = "IDLE"
        conf["inject_done"] = 0
        save_config(conf)
        subprocess.run(["pkill", "-f", "yafad_proxy"], stderr=subprocess.DEVNULL)
        if PROXY_PROCESS:
            try: PROXY_PROCESS.kill()
            except: pass
            PROXY_PROCESS = None
        if PROXY_LOG_FILE:
            try: PROXY_LOG_FILE.close()
            except: pass
            PROXY_LOG_FILE = None
        time.sleep(2.5)
        
        conn = psycopg2.connect(host="localhost", port=5432, user=db_user, password=db_pass, dbname="yafad_test")
        conn.autocommit = True
        cur = conn.cursor()
        for t in ["table0", "table1", "table2", "table3", "table4", "deep_archive"]:
            cur.execute(f"TRUNCATE TABLE {t} RESTART IDENTITY CASCADE;")
        conn.close()
        return "🧹 SUCCESS: NUCLEAR FLUSH! All proxy zombies killed & DB is 100% pristine!\n⚠️ REMINDER: You MUST restart main.go now!"
    except Exception as e:
        return f"❌ FLUSH FAILED: {e}"

def update_tuning(t0_req, cpu_tune, kp, ki, kd, buoyancy, w_high, w_low):
    conf = get_current_config()
    current_t0 = conf.get("t0_hard_limit", 100000)
    requested_t0 = int(t0_req)
    if "capacities" not in conf: conf["capacities"] = {}
    conf["capacities"]["table0"] = requested_t0
    status_prefix = "✅"
    final_t0 = requested_t0

    if current_t0 > 0 and requested_t0 > 0:
        delta = current_t0 * 0.25 
        if requested_t0 < int(current_t0 - delta):
            final_t0 = int(current_t0 - delta)
            status_prefix = f"⚠️ Safety Clamp (-25%): Limited to {final_t0}"
        elif requested_t0 > int(current_t0 + delta):
            final_t0 = int(current_t0 + delta)
            status_prefix = f"⚠️ Safety Clamp (+25%): Limited to {final_t0}"
    
    if "limits" not in conf: conf["limits"] = {}
    conf["limits"]["max_cpu_percent"] = int(cpu_tune)
    
    conf["t0_hard_limit"] = final_t0
    conf["pid_settings"] = {"kp": float(kp), "ki": float(ki), "kd": float(kd)}
    conf["buoyancy_factor"] = float(buoyancy)
    conf["watermarks"] = {"high": float(w_high), "low": float(w_low)}
    save_config(conf)
    return f"{status_prefix} | Physics & CPU Output Updated!", final_t0

def start_mission(total_recs, t0_limit, cpu_percent, run_mode, host, port, user, pw, db, target_tables, mig_t0_cap, mig_flush_enabled, mig_nuke_legacy):
    global PROXY_PROCESS, PROXY_LOG_FILE
    conf = get_current_config()
    biomass = get_current_biomass()

    flush_tables = False
    reset_counter = False
    is_migration = False
    
    if run_mode == "🧪 New Test Run (Flush DB & Reset)":
        flush_tables = True; reset_counter = True
    elif run_mode == "➕ Add Records (Keep Data)":
        flush_tables = False; reset_counter = True
    elif run_mode == "🌿 Migrate Legacy DB (Strangler Fig)":
        flush_tables = False; reset_counter = False; is_migration = True

    if flush_tables: 
        hard_flush_yafad_db()
        biomass = 0

    if reset_counter:
        conf["inject_done"] = 0
        msg_prefix = "🔄 RESET & IGNITION!"
        done = 0
    else:
        msg_prefix = "🚀 RESUMING IGNITION!"
        done = conf.get("inject_done", 0)

    active_t0_limit = int(mig_t0_cap) if is_migration else int(t0_limit)

    conf["t0_hard_limit"] = active_t0_limit
    if "capacities" not in conf: conf["capacities"] = {}
    conf["capacities"]["table0"] = active_t0_limit
    conf["limits"] = {"max_cpu_percent": int(cpu_percent)}
    conf["flush_on_start"] = flush_tables
    
    if is_migration:
        if not target_tables: return "❌ Please scan and select at least one table to migrate."
        subprocess.run(["pkill", "-f", "yafad_proxy"], stderr=subprocess.DEVNULL)
        
        proxy_conf = {
            "mode": "strangler_fig", 
            "legacy_db": {"host": host, "port": port, "user": user, "password": pw, "dbname": db}, 
            "yafad_whitelist": target_tables,
            "t0_cap": active_t0_limit,
            "flush_on_start": mig_flush_enabled,
            "truncate_legacy_after": mig_nuke_legacy # NEU: Wird an den Go Proxy gesendet
        }
        with open("migration_policy.json", "w") as f: 
            json.dump(proxy_conf, f, indent=4)
        
        try:
            cmd = ["go", "run", "yafad_proxy.go"]
            PROXY_LOG_FILE = open("yafad_proxy.log", "w")
            PROXY_PROCESS = subprocess.Popen(cmd, stdout=PROXY_LOG_FILE, stderr=subprocess.STDOUT, text=True, cwd=os.getcwd())
        except Exception as e:
            return f"❌ Proxy Start failed: {e}"
        
        conf["inject_total"] = 0 
        conf["run_state"] = "RUNNING"
        save_config(conf)
        return f"🌿 STRANGLER FIG ACTIVE! Tracking live in 'yafad_proxy.log'"

    else:
        conf["inject_total"] = int(total_recs)
        conf["run_state"] = "RUNNING"
        save_config(conf)
        target_abs = biomass + (int(total_recs) - done)
        return f"{msg_prefix} Target Biomass: {target_abs:,} (Adding {int(total_recs):,} records)"

def pause_mission():
    conf = get_current_config()
    conf["run_state"] = "PAUSED"
    save_config(conf)
    return "⏸️ PAUSE SIGNAL SENT. Engine is holding..."

def resume_mission():
    conf = get_current_config()
    conf["run_state"] = "RUNNING"
    save_config(conf)
    return "▶️ RESUMING MISSION. Engines engaged."

def stop_mission():
    global PROXY_PROCESS, PROXY_LOG_FILE
    conf = get_current_config()
    conf["run_state"] = "STOPPED"
    save_config(conf)
    subprocess.run(["pkill", "-f", "yafad_proxy"], stderr=subprocess.DEVNULL)
    if PROXY_PROCESS:
        try: PROXY_PROCESS.kill()
        except: pass
        PROXY_PROCESS = None
        if PROXY_LOG_FILE:
            PROXY_LOG_FILE.close()
            PROXY_LOG_FILE = None
    return "🛑 ABORT SIGNAL SENT. Proxy eradicated."

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

def get_grafana_iframe(url):
    if "&kiosk" not in url and "?" in url: url += "&kiosk&theme=dark"
    elif "&kiosk" not in url: url += "?kiosk&theme=dark"
    return f'<div style="width:100%; height:95vh; border:1px solid #444; border-radius:8px; overflow:hidden;"><iframe src="{url}" width="100%" height="100%" frameborder="0"></iframe></div>'

def scan_database(host, port, user, password, dbname):
    try:
        conn = psycopg2.connect(host=host, port=port, user=user, password=password, dbname=dbname)
        cur = conn.cursor()
        cur.execute("SELECT table_name FROM information_schema.tables WHERE table_schema='public'")
        tables = [row[0] for row in cur.fetchall()]
        conn.close()
        return gr.update(choices=tables, value=[], visible=True, label="Detected Tables"), "✅ Connection OK"
    except Exception as e:
        return gr.update(visible=False), f"❌ Connection failed: {e}"

def get_live_db_sizes():
    db_user = os.getenv("DB_USER", "eriks")
    db_pass = os.getenv("DB_PASSWORD", "test")
    try:
        conn = psycopg2.connect(host="localhost", port=5432, user=db_user, password=db_pass, dbname="yafad_test")
        cur = conn.cursor()
        query = """
        SELECT 
            pg_size_pretty(pg_total_relation_size('table0')) AS t0,
            pg_size_pretty(pg_total_relation_size('table1')) AS t1,
            pg_size_pretty(pg_total_relation_size('table2')) AS t2,
            pg_size_pretty(pg_total_relation_size('table3')) AS t3,
            pg_size_pretty(pg_total_relation_size('table4')) AS t4,
            pg_size_pretty(pg_total_relation_size('deep_archive')) AS arc
        """
        cur.execute(query)
        sizes = cur.fetchone()
        conn.close()
        return f"💾 **Live Disk/RAM Size:** T0: `{sizes[0]}` | T1: `{sizes[1]}` | T2: `{sizes[2]}` | T3: `{sizes[3]}` | T4: `{sizes[4]}` | Archive: `{sizes[5]}`"
    except Exception as e:
        return "💾 **Live Disk/RAM Size:** `Offline or DB unreachable`"

def get_status_text():
    conf = get_current_config()
    state = conf.get("run_state", "UNKNOWN")
    done = conf.get("inject_done", 0)
    total = conf.get("inject_total", 0)
    t0 = conf.get("t0_hard_limit", 0)
    
    if state == "STOPPED": icon = "🔴"
    elif state == "PAUSED": icon = "⏸️"
    elif state == "RUNNING": icon = "🟢"
    else: icon = "🟡"
    
    biomass = get_current_biomass()
    
    if state == "RUNNING" and total == 0:
        return f"**State:** 🌿 MIGRATION | **Absorbed:** {biomass:,} | **T0 Cap:** {t0} | *Terminal>tail yafad_proxy.log for details*"
    
    target = biomass + (total - done)
    progress = (done / total) * 100 if total > 0 else 0.0
    return f"**State:** {icon} {state} | **Progress:** {progress:.1f}% | **Target:** {target:,} | **T0 Cap:** {t0}"

with gr.Blocks(title="YaFaD v0.9.3 Mission Control") as app:
    init_conf = get_current_config()
    
    with gr.Row(equal_height=True):
        gr.Image("assets/Mission_control_logo.png", show_label=False, container=False, interactive=False, height=110)
        btn_refresh_all = gr.Button("🔄 Sync UI from System", variant="secondary", size="sm")

    with gr.Row():
        with gr.Column(scale=4):
            html_grafana = gr.HTML(value=get_grafana_iframe(init_conf.get("grafana_url", DEFAULT_GRAFANA)))
            
        with gr.Column(scale=1):
            status_display = gr.Markdown(get_status_text)
            live_sizes_display = gr.Markdown(get_live_db_sizes)
            
            timer = gr.Timer(2)
            timer.tick(get_status_text, outputs=status_display)
            timer.tick(get_live_db_sizes, outputs=live_sizes_display)

            with gr.Tabs():
                with gr.TabItem("🚀 Mission"):
                    with gr.Row():
                        n_recs = gr.Number(value=init_conf.get("inject_total"), label="Injection Amount (New Records)")
                        n_t0_mission = gr.Number(value=init_conf.get("t0_hard_limit"), label="T0 Capacity (Start)")
                    
                    with gr.Row():
                        s_cpu = gr.Slider(10, 100, value=init_conf.get("limits", {}).get("max_cpu_percent"), label="CPU Limit %")
                        with gr.Column():
                            lbl_ram = gr.Markdown("Calc RAM...")
                            
                            btn_hard_flush = gr.Button("🗑️ Nuclear Flush DB", size="sm", variant="secondary")
                            with gr.Column(visible=False) as confirm_box:
                                gr.Markdown("⚠️ **WIPE ALL YAFAD DATA AND KILL PROXIES?**")
                                with gr.Row():
                                    btn_confirm_yes = gr.Button("🚨 YES, Nuke it!", variant="stop", size="sm")
                                    btn_confirm_no = gr.Button("Cancel", size="sm")
                                gr.Markdown("⚠️ *Note: You MUST restart main.go manually after flushing!*")

                    radio_mode = gr.Radio(
                        choices=["🧪 New Test Run (Flush DB & Reset)", "➕ Add Records (Keep Data)", "🌿 Migrate Legacy DB (Strangler Fig)"], 
                        value="🧪 New Test Run (Flush DB & Reset)", label="Mission Type"
                    )
                    
                    with gr.Column(visible=False) as mig_panel:
                        gr.Markdown("### 🔗 Legacy Database Credentials")
                        with gr.Row():
                            db_h = gr.Textbox(label="Host", value="localhost")
                            db_p = gr.Textbox(label="Port", value="5432")
                            db_n = gr.Textbox(label="DB Name", placeholder="legacy_crm_7967")
                        with gr.Row():
                            db_u = gr.Textbox(label="User", value="legacy_user_7967")
                            db_pw = gr.Textbox(label="Pass", type="password")
                        
                        mig_t0_cap = gr.Number(value=100000, label="Migration T0 Cap Size")
                        mig_flush_enabled = gr.Checkbox(label="🧹 Flush YaFaD DB at Start", value=False)
                        mig_nuke_legacy = gr.Checkbox(label="🔥 Nuke Legacy Tables after Migration (TRUNCATE)", value=False)
                        lbl_mig_ram = gr.Markdown("Calc RAM...")

                        btn_scan = gr.Button("🔍 Scan Legacy DB", size="sm")
                        sel_tabs = gr.CheckboxGroup(label="Select Tables for Osmosis", visible=False)
                        lbl_scan = gr.Markdown("")
                        
                        btn_scan.click(scan_database, inputs=[db_h, db_p, db_u, db_pw, db_n], outputs=[sel_tabs, lbl_scan])

                    def toggle_mode(mode):
                        return gr.update(visible=(mode == "🌿 Migrate Legacy DB (Strangler Fig)")), gr.update(interactive=(mode != "🌿 Migrate Legacy DB (Strangler Fig)"))
                    
                    radio_mode.change(fn=toggle_mode, inputs=radio_mode, outputs=[mig_panel, n_recs])

                    with gr.Row():
                        btn_start = gr.Button("🔥 IGNITION", variant="primary")
                        btn_pause = gr.Button("⏸️ PAUSE", variant="secondary")
                        btn_resume = gr.Button("▶️ RESUME", variant="secondary")
                        btn_stop = gr.Button("🛑 ABORT", variant="stop")
                    out_mission = gr.Label(label="Status")
                    
                    btn_hard_flush.click(lambda: (gr.update(visible=False), gr.update(visible=True)), inputs=None, outputs=[btn_hard_flush, confirm_box])
                    btn_confirm_no.click(lambda: (gr.update(visible=True), gr.update(visible=False)), inputs=None, outputs=[btn_hard_flush, confirm_box])
                    btn_confirm_yes.click(fn=hard_flush_yafad_db, inputs=None, outputs=out_mission).then(
                        lambda: (gr.update(visible=True), gr.update(visible=False)), inputs=None, outputs=[btn_hard_flush, confirm_box]
                    )

                    btn_start.click(
                        start_mission, 
                        inputs=[n_recs, n_t0_mission, s_cpu, radio_mode, db_h, db_p, db_u, db_pw, db_n, sel_tabs, mig_t0_cap, mig_flush_enabled, mig_nuke_legacy], 
                        outputs=out_mission
                    )
                    btn_pause.click(pause_mission, outputs=out_mission)
                    btn_resume.click(resume_mission, outputs=out_mission)
                    btn_stop.click(stop_mission, outputs=out_mission)

                with gr.TabItem("🎛️ Tuning"):
                    gr.Markdown("### Dynamic Architecture")
                    with gr.Row():
                        n_t0_tune = gr.Number(value=init_conf.get("t0_hard_limit"), label="T0 Capacity (Live Update)")
                        s_cpu_tune = gr.Slider(10, 100, value=init_conf.get("limits", {}).get("max_cpu_percent"), label="CPU Limit % (Live)")
                    expert_toggle = gr.Checkbox(label="🧠 Expert Mode", value=False)
                    
                    with gr.Column(visible=False) as expert_panel:
                        gr.Markdown("### Pulse Control")
                        w_high = gr.Slider(100, 250, value=init_conf.get("watermarks", {}).get("high"), label="High Watermark")
                        w_low = gr.Slider(50, 200, value=init_conf.get("watermarks", {}).get("low"), label="Low Watermark")
                        s_buoy = gr.Slider(0, 1, value=init_conf.get("buoyancy_factor"), label="Buoyancy")
                        
                        gr.Markdown("### PID Controller")
                        pid = init_conf.get("pid_settings", {})
                        s_kp = gr.Slider(0, 5, value=pid.get("kp"), label="Kp")
                        s_ki = gr.Slider(0, 1, value=pid.get("ki"), label="Ki")
                        s_kd = gr.Slider(0, 2, value=pid.get("kd"), label="Kd")
                    
                    btn_tune = gr.Button("Update Live Physics")
                    out_tune = gr.Label()
                    
                    expert_toggle.change(fn=lambda x: gr.update(visible=x), inputs=expert_toggle, outputs=expert_panel)
                    btn_tune.click(
                        update_tuning, 
                        inputs=[n_t0_tune, s_cpu_tune, s_kp, s_ki, s_kd, s_buoy, w_high, w_low], 
                        outputs=[out_tune, n_t0_tune]
                    )

                with gr.TabItem("⚙️ Settings"):
                    txt_grafana = gr.Textbox(label="Grafana URL", value=init_conf.get("grafana_url"))
                    t_vanish = gr.Textbox(value=init_conf.get("vanish_threshold"), label="Vanish Threshold")
                    btn_graf = gr.Button("Reload View")
                    btn_set = gr.Button("Save Settings")
                    lbl_set = gr.Label()
                    btn_graf.click(update_grafana_url, inputs=txt_grafana, outputs=[html_grafana, lbl_set])
                    btn_set.click(update_general, inputs=[t_vanish], outputs=lbl_set)

    btn_refresh_all.click(reload_ui_values, inputs=None, outputs=[n_recs, n_t0_mission, s_cpu, n_t0_tune, s_cpu_tune, w_high, w_low, s_buoy, s_kp, s_ki, s_kd, txt_grafana, t_vanish, out_mission])
    n_t0_mission.change(calc_sim_ram, inputs=[n_t0_mission], outputs=lbl_ram)
    mig_t0_cap.change(calc_mig_ram, inputs=[mig_t0_cap], outputs=lbl_mig_ram)
    app.load(calc_sim_ram, inputs=[n_t0_mission], outputs=lbl_ram)
    app.load(calc_mig_ram, inputs=[mig_t0_cap], outputs=lbl_mig_ram)

if __name__ == "__main__":
    app.launch(server_name="0.0.0.0", server_port=DASHBOARD_PORT, theme=gr.themes.Monochrome())