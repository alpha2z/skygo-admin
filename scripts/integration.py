#!/usr/bin/env python3
"""Create a disposable MySQL fixture; never use existing data or credentials."""
import os, pathlib, secrets, subprocess, tempfile, time
root = pathlib.Path(__file__).resolve().parents[1]
name = 'skygo-admin-test-' + secrets.token_hex(5)
password = secrets.token_urlsafe(32)
def docker(*args):
    return subprocess.run(['docker', *args], check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True).stdout.strip()
with tempfile.TemporaryDirectory(prefix='skygo-admin-test-') as temp:
    envfile = pathlib.Path(temp) / 'mysql.env'
    envfile.write_text('MYSQL_ROOT_PASSWORD=' + password + '\nMYSQL_ROOT_HOST=%\nMYSQL_DATABASE=skygo_admin_test_local\n')
    envfile.chmod(0o600)
    try:
        docker('run', '-d', '--name', name, '--env-file', str(envfile), '-p', '127.0.0.1::3306', 'mysql:8.4')
        address = docker('port', name, '3306/tcp').strip()
        for _ in range(90):
            ready = subprocess.run(['docker','exec',name,'mysqladmin','--protocol=tcp','--host=127.0.0.1','ping','--silent'],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
            if ready.returncode == 0: break
            time.sleep(1)
        else: raise RuntimeError('isolated database did not become ready')
        env = dict(os.environ, GOWORK='off', SKYGO_ADMIN_TEST_DSN=f'root:{password}@tcp({address})/skygo_admin_test_local?parseTime=true&charset=utf8mb4&loc=UTC')
        result = subprocess.run(['go','test','-race','-count=1','./internal/...'],cwd=root,env=env,stdout=subprocess.PIPE,stderr=subprocess.STDOUT,text=True)
        # Never expose credentials even if an upstream test includes a DSN.
        print(result.stdout.replace(password,'[REDACTED]'))
        if result.returncode: raise SystemExit(result.returncode)
    finally:
        subprocess.run(['docker','rm','-f','-v',name],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
