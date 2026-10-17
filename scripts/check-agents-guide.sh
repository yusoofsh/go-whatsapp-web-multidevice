#!/bin/sh
set -eu

root=$(git rev-parse --show-toplevel)
cd "$root"

files=$(git ls-files | grep -E '(^|/)AGENTS\.md$' || true)
if [ -z "$files" ]; then
  echo "no AGENTS.md files" >&2
  exit 1
fi

status=0
ui_path='src/views/'
workflow='release.yml'
for file in $files; do
  if grep -n -F -e "$ui_path" -e "$workflow" -- "$file"; then
    echo "forbidden text in $file" >&2
    status=1
  fi
done

if [ "$status" -eq 0 ]; then
  echo ok
fi
exit "$status"
