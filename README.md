# <p align="center"><img src="assets/logo.png" alt="YaFaD_ai Logo" width="100%"></p>

**YaFaD_ai** (Yet another Fast Data access) is a bio-inspired, high-performance middleware designed to redefine data management. Instead of treating data as static entries, YaFaD treats it as **dynamic memory**, using biological principles to predictively accelerate access and optimize infrastructure costs.

---

## Why <img src="assets/logo_small.png" alt="YaFaD_ai Logo" height="28" style="vertical-align: -5px;">?

In the age of AI, traditional databases are often bottlenecked by the sheer volume of "noise"—data that is stored but rarely used. YaFaD_ai mimics the efficiency of the human brain to solve this.

### Strategic Value

* **Cost-Efficient Scaling:** Actively "forgets" trivial data to reduce hardware overhead and cloud costs.
* **Hybrid Powerhouse:** Combines **Go’s** rapid orchestration with **Rust’s** uncompromising memory safety and speed.
* **Predictive Performance:** Uses a "Pheromone Principle" to anticipate data needs, ensuring sub-millisecond latency.

---

## <img src="assets/logo_brain.png" alt="YaFaD_ai Brain" height="28"> Core Concepts

### 1. The Utility Index (The Pheromone Principle)

Just as ants mark paths, YaFaD assigns a **Utility Index** to every record.

* **Reinforcement:** Frequently accessed data gains "scent," moving into high-speed tiers.
* **Relevance:** Automatically prioritizes data critical to current business operations.

### 2. Golden Ratio Cascading

We utilize the **Golden Ratio $\Phi$** and Fibonacci structures to manage sub-tables. This allows the system to scale organically, keeping data density and access speed in perfect equilibrium.

---

## <img src="assets/logo_stats.png" alt="YaFaD_ai Logic" height="28"> The Logic of "Forgetfulness" (Decay)

A system is only as fast as its ability to shed dead weight. YaFaD_ai maintains peak performance by applying a mathematical decay function to data relevance. If a record isn't reinforced by activity, its importance fades—mimicking biological memory.

### The Utility Formula

$$U_{now} = U_{last} \cdot e^{-\lambda \cdot \Delta t}$$

**Parameters:**
* **$U$ (Utility Index):** The current importance score of the data record.
* **$\lambda$ (Decay Constant):** The "Administrator Factor"—tunable to your specific business requirements.
* **$\Delta t$:** The time elapsed since the last data access.

---

## 🧬 Feature: The Homeostatic Regulator (PID Control)

To maintain the **Golden Ratio ($\Phi \approx 1.618$)** across storage tiers regardless of input velocity, YaFaD_ai employs a biological feedback loop inspired by the endocrine system. Just as the body regulates insulin levels, our **Homeostatic Regulator** dynamically adjusts the decay constant ($\lambda$) in real-time.

### The Concept
Instead of a static decay rate, the system utilizes a **Proportional-Integral-Derivative (PID) Controller** to monitor the "health" of the data flow. It continuously measures the ratio between adjacent tiers and modulates "gravity" to prevent both starvation and overflow.

### Control Logic

**1. Configuration (Admin Parameters)**
* **Basis Lambda ($\lambda_{base}$):** The starting decay rate (e.g., `0.005`).
* **Dynamic Range:** The safe operating window for the decay factor (e.g., `0.001` to `0.05`).

**2. The Feedback Loop**
The regulator calculates the deviation (Error) from the ideal Golden Ratio for every cascade cycle:

$$Error = \frac{Count(T_{target})}{Count(T_{source})} - 1.618$$

**3. Homeostatic Response**
* **If Error > 0 (Target Overflow):** The tier below is filling too fast. The system **decreases $\lambda$** (inhibits decay) to slow down the flow.
* **If Error < 0 (Target Starvation):** The tier below is empty. The system **increases $\lambda$** (accelerates decay) to flush data downwards.

> **Result:** A self-healing infrastructure that autonomously scales its internal "pressure" to handle sudden traffic spikes or long periods of inactivity without manual intervention.

---

## 🛠 Project Status & Roadmap

| Component | Status | Tech Stack |
| :--- | :--- | :--- |
| **High-Performance Core** | ✅ Stable | Rust |
| **Orchestration Layer** | 🚧 In Progress | Go |
| **Utility-Driven Cache** | ✅ Functional | AI Logic |
| **Decay Engine (Gravity)** | ✅ Functional | Rust/Go |
| **Distributed Sync** | 📅 Roadmap | Cloud-Native |

---

## 🧪 Evaluation & Benchmarking

YaFaD_ai includes a comprehensive test suite to evaluate the **Synaptic Buffer** and **Golden Ratio Cascading** logic (Development Roadmap).

### 1. Prerequisites

Set your environment variables for PostgreSQL communication:

```bash
set -x DB_USER your_user
set -x DB_PASSWORD your_password
set -x DB_NAME yafad_test

```

### 2. 🚀 Running the Evaluation

Navigate to the project folder and open **four** terminal windows:

1. **Terminal 1 (Monitor):** Start the core engine and dashboard.
```bash
go run setup_db.go

```


2. **Terminal 2 (Seeding):** Create the evaluation data mass (e.g., 90MB).
```bash
go run seed_db.go

```


3. **Terminal 3 (Gravity):** Start the biological decay worker to trigger the Golden Ratio cascade.
```bash
go run decay_worker.go

```


4. **Terminal 4 (Traffic):** Emulate user behavior and data reinforcement.
```bash
go run user_simulator.go

```

<p align="center"><img src="assets/test_suite.png" alt="YaFaD_ai Test Suite" width="100%"></p>

---

## 🛠 Troubleshooting & CGO Setup

Building a Go/Rust hybrid requires tight coordination. Here are solutions to common hurdles:

### 1. The Rust-Go Bridge (Linking)

If you see `undefined reference to 'calculate_decay'`:

* **No Mangle:** Ensure `core/src/lib.rs` uses `#[no_mangle]` and `pub extern "C"`.
* **Library Type:** `core/Cargo.toml` must have `crate-type = ["cdylib", "staticlib"]`.
* **Force Rebuild:** Run `cd core && cargo clean && cargo build --release`.

### 2. Shared Library Path

If the program fails at startup (`cannot open shared object file`):

* **RPATH:** We use `-Wl,-rpath` in Go `LDFLAGS` to bake the path into the binary.
* **Manual Override:** `LD_LIBRARY_PATH=./core/target/release go run decay_worker.go`.

### 3. Database Integrity

* **Unique Constraints:** If `seed_db.go` fails, run:
`TRUNCATE buffer_tier, table0, table1, table2, table3, table4;`
* **CGO Cache:** If linking still fails after a fix, run `go clean -cache`.

---

[Add Sponsor Button Link]

---
