#!/usr/bin/env python3
"""Fail closed on private paths, business markers and common credential formats.
Reports locations/categories only, never the matching values. Complements manual
review and the CI secret scanner; passing is not a proof of absence of secrets.
"""
import argparse, pathlib, re, subprocess, sys, tarfile
ROOT=pathlib.Path(__file__).resolve().parents[1]
RULES={
 'private-key':re.compile(rb'-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----'),
 'access-token':re.compile(rb'(?:gh[pousr]_[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{30,}|AKIA[A-Z0-9]{16}|xox[baprs]-[A-Za-z0-9-]{20,})'),
 'private-path':re.compile(rb'(?:/Users/[^/\s]+/|/home/[^/\s]+/(?:workspace|\.ssh))'),
 'private-module':re.compile(rb'(?:sw-lib|sw-proto|sw-admin-api|sw_admin_|SW_OPS_|starwar|Starwar|userdata|game-config|battle-verifier|diamond|mail.campaign|economy.adjustment)'),
 'embedded-credential-url':re.compile(rb'[a-z]+://[^\s/:]{1,60}:[^\s/@]{8,}@'),
}
# The scanner necessarily names the signatures it rejects; only this source
# file is exempt from marker rules, never from credential formats.
failures=[]
def inspect(name,data,binary=False):
 for kind,pattern in RULES.items():
  if name.endswith('scripts/scan.py') and kind in ('private-module','private-path','embedded-credential-url'):continue
  # Go's standard html package embeds the diamond/diamondsuit entity names.
  # This exact exception is binary-only; source markers remain strict.
  if binary and kind=='private-module':pattern=re.compile(pattern.pattern.replace(b'|diamond|',b'|diamond(?!(?:suit)?;)|'))
  if pattern.search(data):failures.append((name,kind))
 base=pathlib.PurePosixPath(name).name
 if base=='.DS_Store' or base.startswith('._') or base=='.env' or (base.startswith('.env.') and base!='.env.example') or base.endswith(('.pem','.key','.p12','.log','.sql.gz')):failures.append((name,'private-file'))
def files(root):
 return sorted(p for p in root.rglob('*') if p.is_file() and p.name!='.DS_Store' and not p.name.startswith('._') and not any(x in {'.git','runtime','dist','node_modules','__pycache__'} for x in p.relative_to(root).parts))
def main():
 parser=argparse.ArgumentParser();parser.add_argument('--archive');args=parser.parse_args()
 for p in files(ROOT):
  if p.is_symlink():failures.append((str(p.relative_to(ROOT)),'symlink'));continue
  inspect(str(p.relative_to(ROOT)),p.read_bytes())
 git=subprocess.run(['git','rev-list','--objects','--all'],cwd=ROOT,stdout=subprocess.PIPE,stderr=subprocess.DEVNULL,text=True)
 for line in git.stdout.splitlines():
  oid,_,name=line.partition(' ')
  typ=subprocess.run(['git','cat-file','-t',oid],cwd=ROOT,capture_output=True,text=True)
  if typ.stdout.strip()=='blob':inspect('history/'+name,subprocess.check_output(['git','cat-file','blob',oid],cwd=ROOT))
 if args.archive:
  with tarfile.open(args.archive,'r:*') as archive:
   for member in archive:
    if member.issym() or member.islnk() or member.name.startswith('/') or '..' in pathlib.PurePosixPath(member.name).parts:failures.append((member.name,'unsafe-archive-member'))
    elif member.isfile():inspect(member.name,archive.extractfile(member).read())
 for name,kind in sorted(set(failures)):print(f'{kind}: {name}')
 if failures:return 1
 print('PASS: source/history'+('/archive' if args.archive else '')+' policy scan; no matching values printed.');return 0
if __name__=='__main__':sys.exit(main())
