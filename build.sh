#!/bin/sh
# Builds ./autocorrect (needs Go >= 1.25). data/lexicon.tsv ships prebuilt; to
# regenerate it from source: tools/fetch_data.sh && python3 tools/build_lexicon.py
set -e
cd "$(dirname "$0")"
go build -trimpath -o autocorrect .
