#!/usr/bin/env python3
"""Build binaries and an explicit allowlisted source release without local data."""
import hashlib, json, os, pathlib, subprocess, tarfile
root=pathlib.Path(__file__).resolve().parents[1];out=root/'dist';out.mkdir(exist_ok=True)
subprocess.run(['python3','scripts/scan.py'],cwd=root,check=True)
for arch in ('amd64','arm64'):
 for name in ('admin-api','ops-agent'):
  subprocess.run(['go','build','-trimpath','-buildvcs=false','-ldflags=-s -w','-o',str(out/f'{name}-linux-{arch}'),f'./cmd/{name}'],cwd=root,env=dict(os.environ,GOWORK='off',CGO_ENABLED='0',GOOS='linux',GOARCH=arch),check=True)
allow=['cmd','internal','admin-web','deploy','docs','examples','scripts','.github','go.mod','go.sum','LICENSE','LICENSE.go-admin','NOTICE','README.md','README.zh-CN.md','CONTRIBUTING.md','SECURITY.md','AGENTS.md','Makefile','.gitignore','.dockerignore']
paths=[]
for name in allow:
 p=root/name
 if not p.exists():raise SystemExit('Missing release input: '+name)
 for f in ([p] if p.is_file() else sorted(p.rglob('*'))):
  if f.is_file() and '__pycache__' not in f.parts and f.name!='.DS_Store' and not f.name.startswith('._'):
   if f.is_symlink():raise SystemExit('Refusing symlink in release')
   paths.append(f)
archive=out/'skygo-admin-source.tar.gz'
with tarfile.open(archive,'w:gz') as tar:
 for p in paths:
  info=tar.gettarinfo(str(p),'skygo-admin/'+str(p.relative_to(root)));info.uid=info.gid=0;info.uname=info.gname='';info.mtime=0
  with p.open('rb') as stream:tar.addfile(info,stream)
subprocess.run(['python3','scripts/scan.py','--archive',str(archive)],cwd=root,check=True)
manifest=[{'path':str(p.relative_to(root)),'sha256':hashlib.sha256(p.read_bytes()).hexdigest()} for p in paths]
(out/'source-manifest.json').write_text(json.dumps(manifest,indent=2)+'\n')
files=[archive,out/'source-manifest.json']+[out/f'{name}-linux-{arch}' for arch in ('amd64','arm64') for name in ('admin-api','ops-agent')]
(out/'SHA256SUMS').write_text(''.join(hashlib.sha256(p.read_bytes()).hexdigest()+'  '+p.name+'\n' for p in files))
# Also inspect newly built executable bytes for private paths and project markers.
import importlib.util
spec=importlib.util.spec_from_file_location('release_scan',root/'scripts/scan.py')
scanner=importlib.util.module_from_spec(spec);spec.loader.exec_module(scanner)
for p in files:
 if p.name.startswith(('admin-api-linux-','ops-agent-linux-')):scanner.inspect(p.name,p.read_bytes(),binary=True)
if scanner.failures:
 for name,kind in scanner.failures:print(kind+': '+name)
 raise SystemExit(1)
print('Built and scanned source archive, four Linux binaries, source manifest and checksums in dist/.')
