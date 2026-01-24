const PHI: f64 = 1.61803398875;

#[no_mangle]
pub extern "C" fn calculate_cascade_size(base_size: f64, level: i32) -> f64 {
    // Formel: Größe = Basis * PHI ^ Level
    base_size * PHI.powi(level)
}