#!/bin/bash

echo "🏗️  Building YaFaD v0.4.0 Release..."

# 1. Aufräumen & Dependencies checken
echo "🧹 Tidy up Go modules..."
go mod tidy

# 2. Rust Core bauen (Release Mode - High Optimization)
echo "🦀 Building Rust Core (libyafad_core)..."
cd core
cargo build --release
if [ $? -ne 0 ]; then
    echo "❌ Rust build failed!"
    exit 1
fi
cd ..

# 3. Output Directory erstellen
mkdir -p release_v0.4.0

# 4. Shared Library kopieren
# WICHTIG: Die Go-Binary braucht diese Bibliothek zur Laufzeit!
echo "📦 Copying Shared Library..."
cp core/target/release/libyafad_core.so release_v0.4.0/

# 5. Go Binaries bauen
echo "🐹 Building Go Binaries..."

# Binary 1: The Engine (Metabolism)
# Wir nennen es 'yafad_engine', basierend auf decay_worker.go
go build -o release_v0.4.0/yafad_engine decay_worker.go

# Binary 2: The Simulator (User)
go build -o release_v0.4.0/yafad_simulator user_simulator.go

# 6. README kopieren (Optional aber empfohlen)
if [ -f "README.md" ]; then
    cp README.md release_v0.4.0/
fi

echo "✅ Build complete! Artifacts are in 'release_v0.4.0/'"
ls -lh release_v0.4.0/