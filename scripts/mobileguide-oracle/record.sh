#!/usr/bin/env bash
# Records every WebToMobileAnalysisService.Analyze call clio's MobilePageConverter test suite makes, with its
# inputs and clio's answer, into internal/mobileguide/testdata/clio-analyze-records.tar.gz (the Go engine's
# oracle, read by internal/mobileguide/oracle_test.go).
#
#   scripts/mobileguide-oracle/record.sh <clio source checkout at the release tag> [work dir]
#
# The checkout is copied, never modified. In the copy clio's Analyze is renamed AnalyzeCore and wrapped by
# WebToMobileAnalysisService.Oracle.cs, which writes one JSON file per call when CLIO_ORACLE_DIR is set; the
# MobilePageConverter tests are built on their own (oracle-tests.csproj, so clio.mcp.e2e is not needed) and run.
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
repo="$(cd "$here/../.." && pwd)"
source_dir="${1:?clio source checkout}"
work="${2:-$(mktemp -d)}"
copy="$work/clio-src"
records="$work/records"

rm -rf "$copy" "$records"
mkdir -p "$copy" "$records"
rsync -a --exclude .git --exclude TestResults --exclude bin --exclude obj "$source_dir/" "$copy/"
converter="$copy/clio/Command/McpServer/Tools/MobilePageConverter"
python3 - "$converter/WebToMobileAnalysisService.cs" <<'PY'
import sys
path = sys.argv[1]
text = open(path, encoding="utf-8-sig").read()
old = "public static MobilePageConversionGuide Analyze("
if text.count(old) != 1:
    sys.exit("Analyze signature not found exactly once")
open(path, "w", encoding="utf-8").write(text.replace(old, "private static MobilePageConversionGuide AnalyzeCore("))
PY
cp "$here/OracleRecorder.cs" "$here/WebToMobileAnalysisService.Oracle.cs" "$converter/"
mkdir -p "$copy/oracle-tests"
cp "$here/oracle-tests.csproj" "$copy/oracle-tests/"
dotnet build "$copy/oracle-tests/oracle-tests.csproj" -v q -nologo
CLIO_ORACLE_DIR="$records" dotnet test "$copy/oracle-tests/oracle-tests.csproj" --no-build -v q -nologo
COPYFILE_DISABLE=1 tar -C "$records" -czf "$repo/internal/mobileguide/testdata/clio-analyze-records.tar.gz" .
echo "recorded $(ls "$records" | wc -l | tr -d ' ') Analyze calls"
