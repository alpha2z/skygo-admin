#!/usr/bin/env python3
"""Create non-secret provenance from GitHub identity and buildx digest metadata."""
import argparse,json,os,pathlib,re
p=argparse.ArgumentParser();p.add_argument('--metadata-dir',required=True);p.add_argument('--component',choices=['admin-api','admin-web','ops-agent','all'],required=True);p.add_argument('--platform',choices=['linux/amd64','linux/arm64'],required=True);p.add_argument('--out',required=True);args=p.parse_args()
repo=os.environ['GITHUB_REPOSITORY'];run=int(os.environ['GITHUB_RUN_ID']);attempt=int(os.environ['GITHUB_RUN_ATTEMPT']);images=[]
for component in ['admin-api','admin-web','ops-agent']:
 if args.component not in ('all',component):continue
 meta=json.loads((pathlib.Path(args.metadata_dir)/('metadata-'+component+'.json')).read_text());digest=meta['containerimage.digest']
 if not re.fullmatch('sha256:[a-f0-9]{64}',digest):raise SystemExit('Invalid build digest')
 images.append({'service':component,'platform':args.platform,'reference':'ghcr.io/'+repo.lower()+'/'+component+'@'+digest})
build={'repository':repo,'workflow':'build.yml','ref':os.environ['GITHUB_REF'],'source_commit':os.environ['GITHUB_SHA'],'run_id':run,'run_attempt':attempt,'started_at':os.environ['BUILD_STARTED']}
pathlib.Path(args.out).write_text(json.dumps({'version':2,'id':f'ci-{run}-{attempt}','build':build,'images':images},separators=(',',':'))+'\n')
