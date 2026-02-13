#!/bin/bash

# Konfiguration
VERSION="v0.8.6"
RELEASE_DIR="release_${VERSION}"
RUST_DIR="core"  # KORRIGIERT: Hier liegt die Cargo.toml
GRAFANA_SRC="grafana/grafana.json"

echo "🚀 Starting YaFaD Build Process for ${VERSION}..."

# 1. Aufräumen & Dependencies checken
echo "🧹 Tidy up Go modules..."
go mod tidy

# 2. Rust Cortex bauen
echo "🦀 Building Rust Cortex in '${RUST_DIR}'..."
# Check ob Ordner existiert
if [ ! -d "$RUST_DIR" ]; then
    echo "❌ Error: Directory '$RUST_DIR' not found!"
    exit 1
fi

cd "$RUST_DIR"
cargo build --release
if [ $? -ne 0 ]; then
    echo "❌ Rust build failed!"
    exit 1
fi
cd .. # Zurück zum Root

# 3. Release Directory erstellen
if [ -d "$RELEASE_DIR" ]; then
    rm -rf "$RELEASE_DIR"
fi
mkdir -p "$RELEASE_DIR"

# 4. Shared Library kopieren
echo "📦 Copying Shared Library..."
# Wir suchen im target/release Ordner von Rust
RUST_TARGET="${RUST_DIR}/target/release"

# Versuche spezifische Namen oder nimm die erste .so Datei, die wir finden
if [ -f "${RUST_TARGET}/libyafad_cortex.so" ]; then
    cp "${RUST_TARGET}/libyafad_cortex.so" "$RELEASE_DIR/"
    echo "   -> Found libyafad_cortex.so"
elif [ -f "${RUST_TARGET}/libyafad_core.so" ]; then
    cp "${RUST_TARGET}/libyafad_core.so" "$RELEASE_DIR/"
    echo "   -> Found libyafad_core.so"
else
    # Fallback: Nimm irgendeine .so Datei (hoffen wir, es ist die richtige)
    cp "${RUST_TARGET}/"*.so "$RELEASE_DIR/" 2>/dev/null
    if [ $? -ne 0 ]; then
         echo "❌ Error: No .so library found in ${RUST_TARGET}!"
         exit 1
    fi
    echo "   -> Found library via wildcard"
fi

# 5. Go Binaries bauen
echo "🐹 Building Go Microservices..."

echo "   - Building Core (main)..."
go build -o "${RELEASE_DIR}/yafad_core" main.go

echo "   - Building Proxy..."
go build -o "${RELEASE_DIR}/yafad_proxy" proxy.go

echo "   - Building Decay Worker..."
go build -o "${RELEASE_DIR}/yafad_worker" decay_worker.go

echo "   - Building User Simulator..."
go build -o "${RELEASE_DIR}/yafad_simulator" user_simulator.go

# 6. Assets kopieren
echo "📄 Copying Assets..."
if [ -f "README.md" ]; then
    cp README.md "$RELEASE_DIR/"
fi

mkdir -p "${RELEASE_DIR}/grafana"
if [ -f "$GRAFANA_SRC" ]; then
    cp "$GRAFANA_SRC" "${RELEASE_DIR}/grafana/"
fi

# 7. Archivieren
echo "🎁 Creating Release Archive..."
tar -czvf "yafad-${VERSION}-linux-amd64.tar.gz" "$RELEASE_DIR"

echo "✅ Build complete!"
echo "   -> Archive: yafad-${VERSION}-linux-amd64.tar.gz"