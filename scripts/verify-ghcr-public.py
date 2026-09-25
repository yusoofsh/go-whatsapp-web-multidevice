"""Verify the published OCI index without using the Actions/GitHub credential.
Only an anonymous GHCR pull token is requested; no credentials are printed.
"""
import hashlib
import json
import os
import re
import urllib.parse
import urllib.request

repository = os.environ['GITHUB_REPOSITORY'].lower()
revision = os.environ['GITHUB_SHA']
assert re.fullmatch(r'[a-z0-9_.-]+/[a-z0-9_.-]+', repository)
assert re.fullmatch(r'[0-9a-f]{40}', revision)
accept = ', '.join([
    'application/vnd.oci.image.index.v1+json',
    'application/vnd.oci.image.manifest.v1+json',
    'application/vnd.docker.distribution.manifest.list.v2+json',
    'application/vnd.docker.distribution.manifest.v2+json',
])
query = urllib.parse.urlencode({'service': 'ghcr.io', 'scope': f'repository:{repository}:pull'})
with urllib.request.urlopen('https://ghcr.io/token?' + query, timeout=20) as response:
    token = json.load(response)['token']
root = f'https://ghcr.io/v2/{repository}'

def get(path):
    request = urllib.request.Request(root + path, headers={'Authorization': 'Bearer ' + token, 'Accept': accept})
    with urllib.request.urlopen(request, timeout=30) as response:
        raw = response.read(2 * 1024 * 1024 + 1)
        assert len(raw) <= 2 * 1024 * 1024, 'Unexpected oversized registry metadata'
        return json.loads(raw), raw, response.headers.get('Docker-Content-Digest')

def digest(value):
    assert re.fullmatch(r'sha256:[a-f0-9]{64}', value), 'Invalid registry digest'
    return value

index, raw, header_digest = get('/manifests/sha-' + revision)
actual_digest = 'sha256:' + hashlib.sha256(raw).hexdigest()
assert actual_digest == header_digest, 'OCI index digest mismatch'
verified = []
for descriptor in index['manifests']:
    platform = descriptor.get('platform', {})
    arch = platform.get('architecture')
    if platform.get('os') != 'linux' or arch not in ('amd64', 'arm64'):
        continue
    manifest, child_raw, _ = get('/manifests/' + digest(descriptor['digest']))
    assert 'sha256:' + hashlib.sha256(child_raw).hexdigest() == descriptor['digest']
    configuration, config_raw, _ = get('/blobs/' + digest(manifest['config']['digest']))
    assert 'sha256:' + hashlib.sha256(config_raw).hexdigest() == manifest['config']['digest']
    assert configuration['architecture'] == arch and configuration['os'] == 'linux'
    assert configuration['config']['Labels']['org.opencontainers.image.revision'] == revision
    assert manifest['layers'], 'Image has no layers'
    verified.append('linux/' + arch)
assert set(verified) == {'linux/amd64', 'linux/arm64'}, verified
print(json.dumps({'image': 'ghcr.io/' + repository, 'tag': 'sha-' + revision,
                  'digest': actual_digest, 'revision': revision,
                  'public_anonymous_access': True, 'platforms': sorted(verified)}, indent=2))
