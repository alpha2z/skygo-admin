#!/usr/bin/env python3
"""Exercise the independent Web template on an isolated network and random port."""
import pathlib,subprocess,tempfile,shutil,os,urllib.request,time,uuid
root=pathlib.Path(__file__).resolve().parents[1];web_image=os.environ.get('SKYGO_ADMIN_WEB_IMAGE', 'ghcr.io/alpha2z/skygo-admin/admin-web:latest');suffix=uuid.uuid4().hex[:8];network='template-check-'+suffix;stub='template-api-'+suffix
with tempfile.TemporaryDirectory(prefix='skygo-web-template-') as tmp:
 work=pathlib.Path(tmp)/'web';shutil.copytree(root/'deploy/admin-web-templates',work)
 (work/'.env').write_text('COMPOSE_PROJECT_NAME=template-web-'+suffix+'\nADMIN_WEB_IMAGE='+web_image+'\nADMIN_WEB_BIND=127.0.0.1\nADMIN_WEB_PORT=0\nADMIN_NETWORK='+network+'\nADMIN_API_ORIGIN=http://admin-api:18391\n')
 try:
  subprocess.run(['docker','network','create',network],check=True,stdout=subprocess.DEVNULL)
  stubconf=pathlib.Path(tmp)/'stub.conf';stubconf.write_text('server { listen 18391; location = /healthz { return 200 "ok"; } location = /api/probe { return 200 "$uri?$args"; } }')
  subprocess.run(['docker','run','-d','--name',stub,'--network',network,'--network-alias','admin-api','-v',str(stubconf)+':/etc/nginx/conf.d/default.conf:ro','--entrypoint','nginx',web_image,'-g','daemon off;'],check=True,stdout=subprocess.DEVNULL)
  subprocess.run(['./start.sh'],cwd=work,check=True,stdout=subprocess.DEVNULL)
  subprocess.run(['docker','compose','up','-d','--wait','--wait-timeout','40'],cwd=work,check=True,stdout=subprocess.DEVNULL)
  address=subprocess.check_output(['docker','compose','port','admin-web','8080'],cwd=work,text=True).strip()
  for route,expected in [('/healthz',b'ok'),('/api/probe?example=1',b'/api/probe?example=1')]:
   with urllib.request.urlopen('http://'+address+route,timeout=10) as r:assert r.read()==expected
  subprocess.run(['docker','compose','exec','-T','admin-web','nginx','-t'],cwd=work,check=True)
  print('PASS: independent Web starts; nginx env substitution and dynamic API proxy work; query paths preserved.')
 except Exception:
  subprocess.run(['docker','logs',stub])
  subprocess.run(['docker','compose','logs','--tail','40'],cwd=work)
  raise
 finally:
  subprocess.run(['docker','compose','down'],cwd=work,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
  subprocess.run(['docker','rm','-f',stub],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
  subprocess.run(['docker','network','rm',network],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
