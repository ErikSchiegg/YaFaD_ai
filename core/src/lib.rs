use std::f64::consts::E;

const PHI: f64 = 1.61803398875;

// --- 1. Infrastructure Logic ---
#[no_mangle]
pub extern "C" fn calculate_cascade_size(base_size: f64, level: i32) -> f64 {
    base_size * PHI.powi(level)
}

// --- 2. Biological Decay Logic ---
#[no_mangle]
pub extern "C" fn calculate_decay(u_last: f64, lambda: f64, delta_t: f64) -> f64 {
    u_last * E.powf(-lambda * delta_t)
}

// --- 3. Organic Capacity Planning ---
#[no_mangle]
pub extern "C" fn calculate_ideal_capacity(total_records: f64, tier: i32) -> f64 {
    let sum_powers = 1.0 + PHI + PHI.powi(2) + PHI.powi(3) + PHI.powi(4);
    let base_size = total_records / sum_powers;
    let ideal_size = base_size * PHI.powi(tier);
    ideal_size * 1.20
}

// --- 4. Applying config policies ---

#[repr(C)]
pub enum Action {
    Keep = 0,
    Migrate = 1,
    Vaporize = 2, // The Event Horizon triggered
}

#[repr(C)]
pub struct DecayResult {
    pub new_utility: f64,
    pub action: Action,
}

// WICHTIG: Hier fehlte #[no_mangle] und extern "C"
#[no_mangle]
pub extern "C" fn calculate_decay_with_horizon(
    current_utility: f64, 
    lambda: f64, 
    time_delta: f64, 
    horizon_threshold: f64
) -> DecayResult {
    
    // 1. Calculate the standard exponential decay
    let new_u = current_utility * (-lambda * time_delta).exp();

    // 2. Check the Event Horizon
    if new_u < horizon_threshold {
        return DecayResult {
            new_utility: 0.0,
            action: Action::Vaporize, 
        };
    }

    // 3. Standard Behavior
    DecayResult {
        new_utility: new_u,
        action: Action::Keep,
    }
}