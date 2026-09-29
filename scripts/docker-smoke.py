#!/usr/bin/env python3
"""Offline-friendly runtime verification using only public Alpine and fresh builds."""
import hashlib,json,os,pathlib,secrets,subprocess,tempfile,time,urllib.request,socket
ROOT=pathlib.Path(__file__).resolve().parents[1]
PROJECT='skygo-admin-smoke-'+secrets.token_hex(4)
def run(args,**kw):
 return subprocess.run(args,check=True,stdout=subprocess.PIPE,stderr=subprocess.PIPE,text=True,**kw).stdout.strip()
def free_port():
 with socket.socket() as s:
  s.bind(('127.0.0.1',0));return s.getsockname()[1]
def get(url):
 with urllib.request.urlopen(url,timeout=3) as r:return json.load(r)
with tempfile.TemporaryDirectory(prefix='skygo-admin-smoke-') as temp:
 tmp=pathlib.Path(temp);compose=tmp/'compose.json';runtime=tmp/'runtime'
 env=dict(os.environ,GOWORK='off',CGO_ENABLED='0',GOOS='linux')
 try:arch=run(['docker','image','inspect','alpine:3.23','--format','{{.Architecture}}'])
 except subprocess.CalledProcessError:
  run(['docker','pull','alpine:3.23']);arch=run(['docker','image','inspect','alpine:3.23','--format','{{.Architecture}}'])
 env['GOARCH']=arch
 run(['go','build','-trimpath','-buildvcs=false','-o',str(tmp/'node'),'./examples/nodes'],cwd=ROOT,env=env)
 run(['go','build','-trimpath','-buildvcs=false','-o',str(tmp/'admin-api'),'./cmd/admin-api'],cwd=ROOT,env=env)
 run(['go','run','./cmd/init-local','-out',str(runtime)],cwd=ROOT,env=dict(os.environ,GOWORK='off'))
 for binary in ('node','admin-api'):
  recipe=tmp/('Dockerfile.'+binary);recipe.write_text(f'FROM alpine:3.23\nCOPY {binary} /usr/local/bin/{binary}\nENTRYPOINT ["{binary}"]\n')
  run(['docker','build','-f',str(recipe),'-t',PROJECT+'/'+binary+':local',str(tmp)])
 endpoints=[{'nodeId':name,'address':name+':19001'} for name in ('node-a','node-b')]
 nodes=json.dumps(endpoints,separators=(',',':'));snapshot={'schemaVersion':1,'revision':hashlib.sha256(nodes.encode()).hexdigest(),'nodes':endpoints}
 config=tmp/'config';config.mkdir();(config/'registry.json').write_text(json.dumps(snapshot,separators=(',',':')))
 services={'mysql':{'image':'mysql:8.4','environment':{'MYSQL_DATABASE':'skygo_admin','MYSQL_USER':'admin','MYSQL_PASSWORD_FILE':'/run/private/mysql-password','MYSQL_ROOT_PASSWORD_FILE':'/run/private/mysql-root-password'},'volumes':[str(runtime/'secrets')+':/run/private:ro']},'api':{'image':PROJECT+'/admin-api:local','environment':{'ADMIN_LISTEN_ADDRESS':'0.0.0.0:18391','ADMIN_MYSQL_DSN_FILE':'/run/private/mysql-dsn','ADMIN_JWT_SECRET_FILE':'/run/private/jwt','ADMIN_BOOTSTRAP_TOKEN_FILE':'/run/private/bootstrap','ADMIN_SIGNING_KEY_FILE':'/run/private/signing','ADMIN_COOKIE_SECURE':'false','ADMIN_EMAIL_CONFIRMATION_ENABLED':'false'},'volumes':[str(runtime/'secrets')+':/run/private:ro',str(ROOT/'admin-web')+':/app/web:ro'],'ports':['127.0.0.1::18391']}}
 services['api']['environment']['ADMIN_WEB_ROOT']='/app/web'
 services['registry']={'image':'registry:2','ports':['127.0.0.1:'+str(free_port())+':5000']}
 for name in ('node-a','node-b'):
  services[name]={'image':'${'+name.upper().replace('-','_')+'_IMAGE:-'+PROJECT+'/node:local}','environment':{'NODE_ID':name,'NODE_LISTEN':'0.0.0.0:19001','NODE_HTTP':'0.0.0.0:19081','NODE_SECRET_FILE':'/run/private/cluster','NODE_REGISTRY':'/run/config/registry.json'},'volumes':[str(runtime/'secrets')+':/run/private:ro',str(config)+':/run/config:ro'],'ports':['127.0.0.1:'+str(free_port())+':19081']}
 compose.write_text(json.dumps({'services':services}));base=['docker','compose','-p',PROJECT,'-f',str(compose)]
 try:
  run(base+['up','-d','mysql'])
  for _ in range(90):
   result=subprocess.run(base+['exec','-T','mysql','mysqladmin','--protocol=tcp','--host=127.0.0.1','ping','--silent'],capture_output=True)
   if result.returncode==0:break
   time.sleep(1)
  else:raise RuntimeError('database not ready')
  run(base+['run','--rm','api','-migrate']);run(base+['up','-d','api','node-a','node-b','registry'])
  urls={name:'http://'+run(base+['port',name,port]) for name,port in [('api','18391'),('node-a','19081'),('node-b','19081')]}
  for _ in range(40):
   try:
    if all(get(url+'/healthz').get('ok') for url in urls.values()):break
   except Exception:pass
   time.sleep(1)
  else:raise RuntimeError('services not healthy')
  for name,other in [('node-a','node-b'),('node-b','node-a')]:
   if get(urls[name]+'/peers').get(other) is not True:raise RuntimeError('Skygo peer discovery failed')
  with urllib.request.urlopen(urls['api']+'/',timeout=3) as response:
   if 'Skygo Admin' not in response.read().decode():raise RuntimeError('frontend not served')
  registry='localhost:'+run(base+['port','registry','5000']).rsplit(':',1)[1]+'/example/node'
  images=[]
  for version in ('v1','v2'):
   if version=='v2':
    recipe=tmp/'Dockerfile.node-v2';recipe.write_text('FROM '+PROJECT+'/node:local\nLABEL synthetic_revision=2\n')
    run(['docker','build','-f',str(recipe),'-t',PROJECT+'/node:v2',str(tmp)])
   source=PROJECT+'/node:'+('local' if version=='v1' else 'v2');target=registry+':'+version
   run(['docker','tag',source,target]);run(['docker','push',target])
   digests=json.loads(run(['docker','image','inspect',target,'--format','{{json .RepoDigests}}']))
   images.append(next(d for d in digests if d.startswith(registry+'@')))
  fixture={'id':'node-a','compose_file':str(compose),'project':PROJECT,'compose_service':'node-a','image_variable':'NODE_A_IMAGE','allowed_images':[registry],'health_url':urls['node-a']+'/healthz','config_path':str(config/'registry.json'),'skygo_registry':True,'test_images':images}
  path=tmp/'fixture.json';path.write_text(json.dumps(fixture))
  result=subprocess.run(['go','test','-count=1','-run','TestDockerServiceLifecycle','./internal/agent'],cwd=ROOT,env=dict(os.environ,GOWORK='off',SKYGO_ADMIN_DOCKER_FIXTURE=str(path)),capture_output=True,text=True)
  print(result.stdout);print(result.stderr)
  if result.returncode:raise SystemExit(result.returncode)
  print('PASS: standalone API/database/frontend; two authenticated Skygo nodes; agent lifecycle, digest-pinned deployment/rollback, configuration and log policy.')
 finally:
  subprocess.run(base+['down','--volumes','--remove-orphans'],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
  if 'registry' in locals():
   for version in ('v1','v2'):subprocess.run(['docker','image','rm',registry+':'+version],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
  subprocess.run(['docker','image','rm',PROJECT+'/node:v2'],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
  for binary in ('node','admin-api'):subprocess.run(['docker','image','rm',PROJECT+'/'+binary+':local'],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
