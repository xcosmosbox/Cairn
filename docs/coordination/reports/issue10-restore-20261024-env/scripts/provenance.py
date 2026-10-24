import pathlib,sqlite3,json,subprocess
root=pathlib.Path('.local/cairnd-fake/fake-session-2342728636'); bundle=root/'bundles/demo/stable'; m=json.loads((bundle/'kb-manifest.json').read_text()); c=sqlite3.connect('file:'+str(bundle/'knowledge.db')+'?mode=ro',uri=True); c.row_factory=sqlite3.Row
node=dict(c.execute('select id,name,provenance,source_refs from nodes where id=?',('ccd9e2cf-1848-51c3-b013-229fbcef88b2',)).fetchone()); sources=[dict(r) for r in c.execute('select * from node_sources where node_uuid=?',(node['id'],))]; assert node['source_refs']=='fake' and node['provenance']=='llm_inferred'; assert sources==[{'node_uuid':node['id'],'member_id':'fake-concept-blocking-queue','skill':'fake','file_path':'SKILL.md','start_line':6,'end_line':8}]
print(json.dumps({'node':node,'sources':sources,'manifest_source_repo':m['source_repo'],'manifest_source_commit':m['source_commit'],'mcp_source_citation_scope':'domain_search and resolve_node format ID/name/type/domain/summary/confidence; no source-file citation field exposed by these tools at this baseline'},ensure_ascii=False,indent=2))
for gitdir in root.rglob('*source.git'):
 r=subprocess.run(['git','--git-dir',str(gitdir),'show',m['source_commit']+':SKILL.md'],capture_output=True,text=True)
 if r.returncode==0:
  lines=r.stdout.splitlines(); print(json.dumps({'gitdir':str(gitdir),'command':['git','--git-dir',str(gitdir),'show',m['source_commit']+':SKILL.md'],'exit_code':r.returncode,'source_lines_6_8':lines[5:8]},ensure_ascii=False)); assert 'Bounded queue blocks when full or empty' in lines[5:8]; break
else: raise AssertionError('source Git object could not be checked')
print('PASS: source ledger UUID/member/skill/path/line span and manifest-pinned Git source match')
