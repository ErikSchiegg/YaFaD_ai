#!/bin/bash

# --- CONFIGURATION ---
VERSION="v0.2.0"
RELEASE_DIR="YaFaD_ai_${VERSION}"
CORE_PATH="./core/target/release"

echo "🧬 YaFaD_ai Release Builder ($VERSION)"
echo "-------------------------------------"

# 1. Clean previous builds
echo "🧹 Cleaning up..."
rm -rf "$RELEASE_DIR"
rm -f "${RELEASE_DIR}.zip"
mkdir -p "$RELEASE_DIR/bin"
mkdir -p "$RELEASE_DIR/lib"
mkdir -p "$RELEASE_DIR/assets"

# 2. Build the Rust Core (The Brain)
echo "🦀 Building Rust Core..."
cd core
cargo build --release
if [ $? -ne 0 ]; then
    echo "❌ Rust build failed."
    exit 1
fi
cd ..

# Copy the shared library to the release lib folder
# (Detects Linux .so or Mac .dylib automatically)
cp core/target/release/libyafad_core.* "$RELEASE_DIR/lib/"
echo "✅ Rust Core packaged."

# 3. Build Go Binaries (The Nervous System)
# We set CGO flags to ensure the binary knows where to look for the library relative to itself ($ORIGIN)
echo "🐹 Building Go Executables..."

# List of all your active components
APPS=("setup_db" "seed_db" "decay_worker" "user_simulator" "archive_gardener" "fractal_decay")

export CGO_LDFLAGS="-L${PWD}/core/target/release -lyafad_core -Wl,-rpath,\$ORIGIN/../lib"
export CGO_CPPFLAGS="-I${PWD}/core"

for app in "${APPS[@]}"; do
    echo "   - Building $app..."
    go build -o "$RELEASE_DIR/bin/$app" "$app.go"
    if [ $? -ne 0 ]; then
        echo "❌ Failed to build $app"
        exit 1
    fi
done

# 4. Copy Documentation & Assets
echo "📄 Copying Assets & Docs..."
cp README.md "$RELEASE_DIR/"
cp -r assets/* "$RELEASE_DIR/assets/" 2>/dev/null

# 5. Create a "Start All" script for the user
cat <<EOT >> "$RELEASE_DIR/start_system.sh"
#!/bin/bash
echo "🚀 Launching YaFaD_ai System..."

# Add lib folder to library path
export LD_LIBRARY_PATH=\$(pwd)/lib:\$LD_LIBRARY_PATH

# 1. Start Decay Engine (Gravity)
./bin/decay_worker &
PID_DECAY=\$!
echo "   [Started] Decay Engine (PID: \$PID_DECAY)"

# 2. Start Archive Gardener (Cleanup)
./bin/archive_gardener &
PID_GARDENER=\$!
echo "   [Started] Archive Gardener (PID: \$PID_GARDENER)"

# 3. Start Fractal Engine (Deep Storage)
./bin/fractal_decay &
PID_FRACTAL=\$!
echo "   [Started] Fractal Engine (PID: \$PID_FRACTAL)"

echo "✅ System Active. Press Ctrl+C to stop all."
wait
EOT
chmod +x "$RELEASE_DIR/start_system.sh"

# 6. Zip it up!
echo "📦 Compressing..."
zip -r "${RELEASE_DIR}.zip" "$RELEASE_DIR"

echo "-------------------------------------"
echo "🎉 RELEASE COMPLETE: ${RELEASE_DIR}.zip"
echo "👉 Upload this file to your GitHub Release page!"
