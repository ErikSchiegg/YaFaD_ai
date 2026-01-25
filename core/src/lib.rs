use std::f64::consts::E;

const PHI: f64 = 1.61803398875;

// --- 1. Infrastructure Logic ---
#[no_mangle]
pub extern "C" fn calculate_cascade_size(base_size: f64, level: i32) -> f64 {
    // Formula: Size = Base * PHI ^ Level
    base_size * PHI.powi(level)
}

// --- 2. Biological Decay Logic ---
#[no_mangle]
pub extern "C" fn calculate_decay(u_last: f64, lambda: f64, delta_t: f64) -> f64 {
    // Formula: U_now = U_last * e^(-lambda * delta_t)
    u_last * E.powf(-lambda * delta_t)
}
