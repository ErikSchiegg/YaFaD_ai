#!/bin/bash

# Pfad zu deinem Conda (meistens hier, sonst 'which conda' fragen)
# Wir nutzen "conda run", das ist sauberer als activate im Skript
echo "🦁 Starting YaFaD Mission Control..."

# Prüfen, ob die Umgebung existiert, sonst meckern
if ! conda info --envs | grep -q "yafad"; then
    echo "❌ Conda environment 'yafad' not found!"
    echo "Please run: conda create -n yafad python=3.10 -y && conda activate yafad && pip install gradio psycopg2-binary"
    exit 1
fi

# Startet das Dashboard direkt in der yafad-Umgebung
conda run -n yafad python dashboard.py