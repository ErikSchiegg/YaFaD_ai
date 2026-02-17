import pandas as pd
import numpy as np
from sklearn.linear_model import LinearRegression
import json

# Lade die Trainingsdaten (Dein erfolgreicher 10M Lauf oder der aktuelle)
# Wir nutzen die Logik: Wie hat das System auf Druck (T0) und Veränderung reagiert?
try:
    df = pd.read_csv('yafad_metrics.csv')
except FileNotFoundError:
    print("❌ Keine CSV gefunden. Bitte erst einen Lauf machen!")
    exit()

print(f"📊 Analysiere {len(df)} Datenpunkte...")

# 1. Feature Engineering (Die Sinne schärfen)
# Wir berechnen relative Füllstände und Geschwindigkeiten
cap_t0_est = df['t0'].max() / 1.1 # Schätzung der Kapazität
if cap_t0_est == 0: cap_t0_est = 1000

df['pressure_t0'] = df['t0'] / cap_t0_est
df['pressure_t1'] = df['t1'] / (cap_t0_est * 1.618)
df['velocity_t0'] = df['t0'].diff().fillna(0) # Wie schnell füllt es sich?
df['acceleration_t0'] = df['velocity_t0'].diff().fillna(0) # Beschleunigt es?

# Wir wollen vorhersagen, welches Lambda (Decay) notwendig war
# Um Rauschen zu filtern, glätten wir die Daten leicht
X = df[['pressure_t0', 'velocity_t0', 'acceleration_t0']]
y = df['lambda']

# 2. Training (Linear Regression für Portabilität nach Go)
# Wir suchen die Formel: Lambda = a*Pressure + b*Velocity + c*Accel + Intercept
model = LinearRegression()
model.fit(X, y)

weights = {
    "w_pressure": model.coef_[0],
    "w_velocity": model.coef_[1],
    "w_accel": model.coef_[2],
    "intercept": model.intercept_
}

print("\n🧠 Cortex Training Complete!")
print("   Erkannte Zusammenhänge:")
print(f"   • Druck-Sensitivität: {weights['w_pressure']:.6f}")
print(f"   • Reaktions-Speed:    {weights['w_velocity']:.6f}")
print(f"   • Dämpfung (Accel):   {weights['w_accel']:.6f}")

# 3. Export für Go
with open('brain_weights.json', 'w') as f:
    json.dump(weights, f, indent=4)

print("✅ 'brain_weights.json' gespeichert. Das Go-Backend wird dies laden.")