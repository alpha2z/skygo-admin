#!/usr/bin/env python3
"""Inspect local image metadata and owned runtime files without printing content."""
import importlib.util,json,pathlib,subprocess,sys,tempfile
root=pathlib.Path(__file__).resolve().parents[1]
spec=importlib.util.spec_from_file_location('scan',root/'scripts/scan.py');scan=importlib.util.module_from_spec(spec);spec.loader.exec_module(scan)
tag=sys.argv[1] if len(sys.argv)>1 else 'v3-review'
for name in ('admin-api','admin-web','ops-agent'):
 image='skygo-admin/'+name+':'+tag
 metadata=subprocess.check_output(['docker','image','inspect',image]);scan.inspect(name+'/metadata.json',metadata)
 container=subprocess.check_output(['docker','create',image],text=True).strip()
 try:
  with tempfile.TemporaryDirectory(prefix='skygo-admin-image-review-') as directory:
   paths={'admin-api':['/usr/local/bin/admin-api','/app/admin-web'],'admin-web':['/usr/share/nginx/html','/etc/nginx/conf.d/default.conf'],'ops-agent':['/usr/local/bin/ops-agent']}[name]
   for source in paths:
    subprocess.run(['docker','cp',container+':'+source,directory],check=True,stdout=subprocess.DEVNULL)
   for path in pathlib.Path(directory).rglob('*'):
    if path.is_file():scan.inspect(name+'/'+str(path.relative_to(directory)),path.read_bytes(),binary=path.name in ('admin-api','ops-agent'))
 finally:subprocess.run(['docker','rm',container],check=True,stdout=subprocess.DEVNULL)
if scan.failures:
 for name,kind in scan.failures:print(kind+': '+name)
 raise SystemExit(1)
print('PASS: three image configurations and owned runtime files; no matching values printed.')
