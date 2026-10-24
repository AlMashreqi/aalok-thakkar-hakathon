#!/bin/sh
# Download the raw data sources into data/raw (see README for citations).
set -e
cd "$(dirname "$0")/../data" && mkdir -p raw && cd raw
curl -sSfL -o scowl.tar.gz https://downloads.sourceforge.net/project/wordlist/SCOWL/2020.12.07/scowl-2020.12.07.tar.gz
tar xzf scowl.tar.gz
# wordfreq 3.1.1: only its English frequency table is used
python3 -m pip download --no-deps -q -d . wordfreq==3.1.1
mkdir -p wordfreq-3.1.1
unzip -o -q -j wordfreq-3.1.1-py3-none-any.whl wordfreq/data/large_en.msgpack.gz -d wordfreq-3.1.1
# evaluation only (never read by the program):
for f in missp aspell wikipedia holbrook-missp; do curl -sSfLO "https://www.dcs.bbk.ac.uk/~ROGER/$f.dat"; done
