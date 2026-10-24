import subprocess,json,pathlib,hashlib
p=subprocess.Popen(['./bin/cairn-mcp','--config','configs/mcp-issue10-env.yaml'],stdin=subprocess.PIPE,stdout=subprocess.PIPE,text=True)
def send(method,params=None,rid=None):
 request={'jsonrpc':'2.0','method':method}
 if params is not None: request['params']=params
 if rid is not None: request['id']=rid
 p.stdin.write(json.dumps(request,ensure_ascii=False)+'\n'); p.stdin.flush()
 if rid is None: return
 response=json.loads(p.stdout.readline()); print(json.dumps({'request':request,'response':response},ensure_ascii=False),flush=True)
 assert response.get('id')==rid and 'error' not in response
 return response['result']
try:
 r=send('initialize',{'protocolVersion':'2024-11-05','capabilities':{},'clientInfo':{'name':'issue10-semantic-acceptance','version':'1'}},1)
 assert r['protocolVersion']=='2024-11-05'
 send('notifications/initialized')
 r=send('tools/call',{'name':'domain_search','arguments':{'kg':'demo','keyword':'阻塞队列','limit':5}},2)
 assert not r.get('isError',False)
 text='\n'.join(c['text'] for c in r['content'] if c['type']=='text')
 assert '- ID: ccd9e2cf-1848-51c3-b013-229fbcef88b2' in text and '阻塞队列' in text and 'Bounded queue blocks when full or empty' in text
 assert 'No matching results found.' not in text
 print('PASS: real MCP nonempty known concept ID/name/queue summary match; zero-result success excluded',flush=True)
 p.stdin.close(); assert p.wait(timeout=10)==0
finally:
 if p.poll() is None: p.terminate();p.wait(timeout=5)
bundle=pathlib.Path('.local/cairnd-fake/fake-session-2342728636/bundles/demo/stable')
before=json.loads(pathlib.Path('.local/bundle-before.json').read_text()); after={f.name:hashlib.sha256(f.read_bytes()).hexdigest() for f in bundle.iterdir() if f.is_file()}; print(json.dumps({'bundle_before':before,'bundle_after':after,'byte_identical':before==after},indent=2)); assert before==after
