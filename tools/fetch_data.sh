#!/bin/sh
# Download the raw data sources into data/raw (see README for citations).
set -e
cd "$(dirname "$0")/../data" && mkdir -p raw && cd raw
curl -sSfLO https://norvig.com/ngrams/count_1w.txt
curl -sSfL -o scowl.tar.gz https://downloads.sourceforge.net/project/wordlist/SCOWL/2020.12.07/scowl-2020.12.07.tar.gz
tar xzf scowl.tar.gz
# evaluation only (never read by the program):
for f in missp aspell wikipedia holbrook-missp; do curl -sSfLO "https://www.dcs.bbk.ac.uk/~ROGER/$f.dat"; done
