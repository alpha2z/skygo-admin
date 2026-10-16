#!/usr/bin/env python3
"""Exercise real container replacement and new Agent heartbeats without a registry."""
import json, os, pathlib, subprocess, tempfile, uuid
root = pathlib.Path(__file__).resolve().parents[1]
def run(*args, **kwargs):
    return subprocess.check_output(list(args), text=True, **kwargs).strip()
base = os.environ.get('SKYGO_ADMIN_DOCKER_CLI_IMAGE', 'docker:29-cli')
arch = run('docker', 'image', 'inspect', base, '--format', '{{.Architecture}}')
prefix = 'skygo-admin-update-test-' + uuid.uuid4().hex[:10]
with tempfile.TemporaryDirectory(prefix=prefix) as temp:
    work = pathlib.Path(temp)
    env = dict(os.environ, GOWORK='off', CGO_ENABLED='0', GOOS='linux', GOARCH=arch)
    subprocess.run(['go','build','-o',str(work/'ops-agent'),'./cmd/ops-agent'], cwd=root,env=env,check=True)
    subprocess.run(['go','test','-c','-o',str(work/'agent.test'),'./internal/agent'],cwd=root,env=env,check=True)
    images = []
    volume = prefix+'-socket'
    subprocess.run(['docker','volume','create',volume],check=True,stdout=subprocess.DEVNULL)
    try:
        for version in ('old','new'):
            tag = prefix+':'+version
            (work/'Dockerfile').write_text('FROM '+base+'\nLABEL synthetic.update="'+prefix+'-'+version+'"\n')
            subprocess.run(['docker','build','--network=none','--pull=false','--provenance=false','-t',tag,str(work)],check=True,stdout=subprocess.DEVNULL)
            images.append(tag)
        ids = [run('docker','image','inspect',tag,'--format','{{.Id}}') for tag in images]
        subprocess.run(['docker','run','--rm','--network=host','-v',temp+':'+temp,'-v','/var/run/docker.sock:/var/run/docker.sock',
            '-v',volume+':/run/agent-updater-smoke','-e','SKYGO_ADMIN_UPDATE_SOCKET_VOLUME='+volume,
            '-e','SKYGO_ADMIN_UPDATE_FIXTURE='+temp,'-e','SKYGO_ADMIN_UPDATE_OLD='+ids[0],'-e','SKYGO_ADMIN_UPDATE_NEW='+ids[1],
            '-e','SKYGO_ADMIN_UPDATE_PLATFORM=linux/'+arch,'--entrypoint',str(work/'agent.test'),base,'-test.v','-test.run','^TestAgentSelfUpdateRealDocker$'],check=True)
    finally:
        if (work/'compose.json').exists():
            subprocess.run(['docker','compose','--project-name','agent-update-'+__import__('hashlib').sha256(temp.encode()).hexdigest()[:12],
                '--file',str(work/'compose.json'),'--env-file',str(work/'compose.env'),'down','--timeout','10'],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
        subprocess.run(['docker','volume','rm',volume],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
        for tag in images:
            subprocess.run(['docker','image','rm',tag],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
