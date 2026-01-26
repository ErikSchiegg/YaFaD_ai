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
// --- 3. Organic Capacity Planning ---
#[no_mangle]
pub extern "C" fn calculate_ideal_capacity(total_records: f64, tier: i32) -> f64 {
    // Wir berechnen den "Share" basierend auf der Summe der Potenzen von PHI für 5 Tiers.
    // Reihe: 1 + 1.618 + 2.618 + 4.236 + 6.854 ≈ 16.326
    let sum_powers = 1.0 + PHI + PHI.powi(2) + PHI.powi(3) + PHI.powi(4);
    
    // Die Basisgröße (T0) ist ein Bruchteil der Gesamtmasse
    let base_size = total_records / sum_powers;
    
    // Die ideale Größe für das angefragte Tier
    let ideal_size = base_size * PHI.powi(tier);
    
    // Wir addieren eine "Atem-Marge" von 20% (Safety Margin)
    ideal_size * 1.20
}