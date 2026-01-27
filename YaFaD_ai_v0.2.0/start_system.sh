#!/bin/bash
echo "🚀 Launching YaFaD_ai System..."

# Add lib folder to library path
export LD_LIBRARY_PATH=$(pwd)/lib:$LD_LIBRARY_PATH

# 1. Start Decay Engine (Gravity)
./bin/decay_worker &
PID_DECAY=$!
echo "   [Started] Decay Engine (PID: $PID_DECAY)"

# 2. Start Archive Gardener (Cleanup)
./bin/archive_gardener &
PID_GARDENER=$!
echo "   [Started] Archive Gardener (PID: $PID_GARDENER)"

# 3. Start Fractal Engine (Deep Storage)
./bin/fractal_decay &
PID_FRACTAL=$!
echo "   [Started] Fractal Engine (PID: $PID_FRACTAL)"

echo "✅ System Active. Press Ctrl+C to stop all."
wait
