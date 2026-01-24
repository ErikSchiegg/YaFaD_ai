#[no_mangle]
pub extern "C" fn calculate_decay(u_last: f64, lambda: f64, delta_t: f64) -> f64 {
    u_last * f64::exp(-lambda * delta_t)
}
