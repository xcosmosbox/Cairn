import subprocess, pathlib, json, datetime, os, sys
root=pathlib.Path(__file__).resolve().parents[1]
logs=root/'docs/coordination/reports/issue10-restore-20261024-env/logs'
env=os.environ.copy(); env.update(PATH=str(root/'.local/toolchain/go/bin')+os.pathsep+env['PATH'],GOPATH=str(root/'.local/gopath'),GOCACHE=str(root/'.cache/go-build'),GOTOOLCHAIN='local')
name=sys.argv[1]; cwd=root/(sys.argv[2]); args=sys.argv[3:]
start=datetime.datetime.now(datetime.timezone.utc).isoformat()
try:
 p=subprocess.run(args,cwd=cwd,env=env,stdout=subprocess.PIPE,stderr=subprocess.STDOUT,text=True); output=p.stdout; code=p.returncode
except OSError as e: output=str(e)+'\n'; code=127
r={'name':name,'command':args,'cwd':str(cwd.relative_to(root)),'started_at':start,'ended_at':datetime.datetime.now(datetime.timezone.utc).isoformat(),'exit_code':code}
(logs/(name+'.log')).write_text(json.dumps(r,ensure_ascii=False)+'\n'+output)
print(json.dumps(r)); print(output[-1800:]); sys.exit(code)
