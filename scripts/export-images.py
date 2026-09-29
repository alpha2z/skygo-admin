#!/usr/bin/env python3
"""Export already built local review images; never push a registry or deploy."""
import hashlib,json,pathlib,subprocess,sys
root=pathlib.Path(__file__).resolve().parents[1];out=root/'dist';out.mkdir(exist_ok=True)
tag=sys.argv[1] if len(sys.argv)>1 else 'v3-review'
manifest=[]
for name in ('admin-api','admin-web','ops-agent'):
 image='skygo-admin/'+name+':'+tag
 metadata=json.loads(subprocess.check_output(['docker','image','inspect',image]))[0]
 path=out/(name+'-image.tar')
 subprocess.run(['docker','save','-o',str(path),image],check=True)
 manifest.append({'file':path.name,'sha256':hashlib.sha256(path.read_bytes()).hexdigest(),'architecture':metadata['Os']+'/'+metadata['Architecture'],'image_id':metadata['Id'],'bytes':path.stat().st_size})
(out/'image-manifest.json').write_text(json.dumps(manifest,indent=2)+'\n')
checks=out/'SHA256SUMS'
lines=[line for line in checks.read_text().splitlines() if not line.endswith(('-image.tar','image-manifest.json'))] if checks.exists() else []
for item in manifest:lines.append(item['sha256']+'  '+item['file'])
lines.append(hashlib.sha256((out/'image-manifest.json').read_bytes()).hexdigest()+'  image-manifest.json')
checks.write_text('\n'.join(lines)+'\n')
print('Exported three local images and updated checksums; nothing pushed.')
