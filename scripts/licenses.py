#!/usr/bin/env python3
"""Inventory pinned module license files; never publish local cache paths."""
import hashlib,json,os,pathlib,subprocess
root=pathlib.Path(__file__).resolve().parents[1]
raw=subprocess.check_output(['go','list','-m','-json','all'],cwd=root,env=dict(os.environ,GOWORK='off'),text=True)
decoder=json.JSONDecoder();modules=[]
while raw.strip():
 obj,end=decoder.raw_decode(raw.lstrip());raw=raw.lstrip()[end:];modules.append(obj)
lines=['# Dependency license inventory','','Generated from the pinned module graph. License files are identified and hashed;','this inventory is not a replacement for their terms. The Go toolchain and container','base images carry additional notices distributed with their respective artifacts.','','| Module | Version | License file | SHA-256 |','|---|---|---|---|']
for m in modules:
 if m.get('Main'):continue
 directory=m.get('Dir')
 if not directory:
  value=json.loads(subprocess.check_output(['go','mod','download','-json',m['Path']+'@'+m['Version']],cwd=root,env=dict(os.environ,GOWORK='off'),text=True));directory=value.get('Dir')
 if not directory:raise SystemExit('Missing downloaded module: '+m['Path'])
 files=[p for p in pathlib.Path(directory).iterdir() if p.is_file() and p.name.lower().startswith(('license','copying','notice'))]
 if not files:raise SystemExit('License review required for '+m['Path'])
 for p in sorted(files):lines.append(f"| `{m['Path']}` | `{m['Version']}` | `{p.name}` | `{hashlib.sha256(p.read_bytes()).hexdigest()}` |")
(root/'docs/dependencies.md').write_text('\n'.join(lines)+'\n');print('Updated dependency license inventory without cache paths.')
