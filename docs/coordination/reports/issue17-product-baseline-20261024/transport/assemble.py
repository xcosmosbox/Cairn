#!/usr/bin/env python3
"""Verify ordered chunks, reconstruct archive and validate every original file."""
import argparse
import hashlib
import io
import json
from pathlib import Path
import tarfile

p=argparse.ArgumentParser(); p.add_argument('--output',type=Path,required=True); args=p.parse_args()
root=Path(__file__).resolve().parent
manifest=json.loads((root/'manifest.json').read_text())
chunks=[]
for row in manifest['ordered_parts']:
 data=(root/row['path']).read_bytes()
 if len(data)!=row['bytes'] or hashlib.sha256(data).hexdigest()!=row['sha256']: raise SystemExit('part mismatch: '+row['path'])
 chunks.append(data)
archive=b''.join(chunks)
if len(archive)!=manifest['archive_bytes'] or hashlib.sha256(archive).hexdigest()!=manifest['archive_sha256']: raise SystemExit('archive mismatch')
args.output.mkdir(parents=True,exist_ok=False)
with tarfile.open(fileobj=io.BytesIO(archive),mode='r:gz') as tar:
 for row in manifest['files']:
  if Path(row['path']).name!=row['path']: raise SystemExit('unsafe member')
  stream=tar.extractfile(row['path']); data=stream.read() if stream else b''
  if len(data)!=row['bytes'] or hashlib.sha256(data).hexdigest()!=row['sha256']: raise SystemExit('member mismatch: '+row['path'])
  (args.output/row['path']).write_bytes(data)
print(json.dumps({'archive_sha256':manifest['archive_sha256'],'files_verified':len(manifest['files']),'raw_samples':manifest['raw_rows']}))
