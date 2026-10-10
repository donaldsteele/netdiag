#!/usr/bin/env bash
# Refresh internal/oui/oui.csv.gz from the IEEE MA-L registry.
# Output: "HEX6,Organization" lines (gzipped), header dropped.
set -euo pipefail
cd "$(dirname "$0")/.."
tmp="$(mktemp)"
trap 'rm -f "$tmp"' EXIT
curl -fsSL --retry 3 -o "$tmp" https://standards-oui.ieee.org/oui/oui.csv
# CSV columns: Registry,Assignment,Organization Name,Organization Address
# Extract with a tolerant awk: Assignment is field 2 (never quoted-comma), name may be quoted.
tr -d '\r' < "$tmp" | tail -n +2 | awk '
{
  s=$0; sub(/^[^,]*,/,"",s)
  a=substr(s,1,6); s=substr(s,8)
  if (substr(s,1,1)=="\"") { s=substr(s,2); i=index(s,"\""); 
    # handle doubled quotes
    n=""; while (i>0 && substr(s,i+1,1)=="\"") { n=n substr(s,1,i); s=substr(s,i+2); i=index(s,"\"") }
    n=n substr(s,1,i-1)
  } else { i=index(s,","); n=(i>0)?substr(s,1,i-1):s }
  gsub(/"/,"\"\"",n)
  if (n ~ /,/ ) n="\"" n "\""
  print a "," n
}' | gzip -9n > internal/oui/oui.csv.gz
ls -la internal/oui/oui.csv.gz
