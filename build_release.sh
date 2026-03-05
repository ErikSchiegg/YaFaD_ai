#!/bin/bash

# Konfiguration
VERSION="v0.9.3"
RELEASE_DIR="release_${VERSION}"
RUST_DIR="core"
GRAFANA_SRC="grafana/grafana.json"

echo "🚀 Starting YaFaD Build Process for ${VERSION}..."

# 1. Aufräumen & Dependencies checken
echo "🧹 Tidy up Go modules..."
go mod tidy

# 2. Rust Cortex bauen
echo "🦀 Building Rust Cortex in '${RUST_DIR}'..."
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
RUST_TARGET="${RUST_DIR}/target/release"

# Intelligente Suche nach der .so Datei
FOUND_LIB=false
for lib in "${RUST_TARGET}"/*.so; do
    if [ -e "$lib" ]; then
        cp "$lib" "$RELEASE_DIR/"
        echo "   -> Found library: $(basename "$lib")"
        FOUND_LIB=true
        break
    fi
done

if [ "$FOUND_LIB" = false ]; then
     echo "❌ Error: No .so library found in ${RUST_TARGET}!"
     exit 1
fi

# 5. Go Binaries bauen
echo "🐹 Building Go Application..."

# A) Das Hauptprogramm (YaFaD)
echo "   - Building Main Engine (yafad)..."
go build -o "${RELEASE_DIR}/yafad" main.go
if [ $? -ne 0 ]; then
    echo "❌ Build failed for main.go"
    exit 1
fi

# B) Der Simulator (Generator)
# Da main.go versucht, generator.go zu kompilieren, legen wir den Source bei
# ODER wir kompilieren ihn vor. Besser: Vorkompilieren für Performance.
if [ -f "generator.go" ]; then
    echo "   - Building Simulator (yafad_sim)..."
    go build -o "${RELEASE_DIR}/yafad_sim" generator.go
    # Wir kopieren auch den Source, falls main.go ihn neu bauen will (Safety Fallback)
    cp generator.go "$RELEASE_DIR/"
fi

# 6. Assets & Dashboard kopieren
echo "📄 Copying Assets..."

# Das Python Dashboard MUSS mit rein!
if [ -f "dashboard.py" ]; then
    echo "   - Copying Dashboard UI..."
    cp dashboard.py "$RELEASE_DIR/"
else
    echo "⚠️ Warning: dashboard.py not found!"
fi

if [ -f "README.md" ]; then
    cp README.md "$RELEASE_DIR/"
fi

# Config Template (optional, falls vorhanden)
if [ -f "yafad_config.json" ]; then
    cp yafad_config.json "$RELEASE_DIR/yafad_config.example.json"
fi

mkdir -p "${RELEASE_DIR}/grafana"
if [ -f "$GRAFANA_SRC" ]; then
    cp "$GRAFANA_SRC" "${RELEASE_DIR}/grafana/"
fi

# 7. Archivieren (ZIP ist besser für GitHub Releases als tar.gz, aber tar ist auch ok)
echo "🎁 Creating Release Archive..."

# Wir erstellen ein ZIP, das ist user-freundlicher für den Upload
zip -r "yafad-${VERSION}-linux-amd64.zip" "$RELEASE_DIR"

echo "✅ Build complete!"
echo "   -> Archive: yafad-${VERSION}-linux-amd64.zip"