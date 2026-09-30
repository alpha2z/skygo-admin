#!/usr/bin/env python3
"""Run the real Agent archive path inside Linux with the daemon filesystem visible."""
import os,pathlib,subprocess,tempfile
root=pathlib.Path(__file__).resolve().parents[1]
try:arch=subprocess.check_output(['docker','image','inspect','docker:29-cli','--format','{{.Architecture}}'],text=True,stderr=subprocess.DEVNULL).strip()
except subprocess.CalledProcessError:
 subprocess.run(['docker','pull','docker:29-cli'],check=True)
 arch=subprocess.check_output(['docker','image','inspect','docker:29-cli','--format','{{.Architecture}}'],text=True).strip()
with tempfile.TemporaryDirectory(prefix='skygo-admin-archive-test-') as temp:
 binary=pathlib.Path(temp)/'agent.test'
 subprocess.run(['go','test','-c','-o',str(binary),'./internal/agent'],cwd=root,env=dict(os.environ,GOWORK='off',CGO_ENABLED='0',GOOS='linux',GOARCH=arch),check=True)
 subprocess.run(['docker','run','--rm','-e','SKYGO_ADMIN_ARCHIVE_DOCKER_TEST=1','-v',temp+':/tests:ro','-v','/var/run/docker.sock:/var/run/docker.sock','-v','/var/lib/docker:/var/lib/docker:ro','docker:29-cli','/tests/agent.test','-test.v','-test.run','^TestArchiveAgentDocker$'],check=True)
