"""Inspect unresolved conflicts; no blanket ours/theirs strategy is allowed."""
import subprocess
from pathlib import Path
files=subprocess.check_output(['git','diff','--name-only','--diff-filter=U'],text=True).splitlines()
print('UNRESOLVED FILES:',files,flush=True)
for path in files:
 text=Path(path).read_text()
 print('\nCONFLICT FILE',path,flush=True)
 lines=text.splitlines();inside=False
 for i,line in enumerate(lines):
  if line.startswith('<<<<<<<'):inside=True
  if inside:print(f'{i+1}: {line}',flush=True)
  if line.startswith('>>>>>>>'):inside=False
if files:raise SystemExit('Explicit conflict resolution required; main has not been changed by this job.')
