#!/usr/bin/env python3
"""Read-only production checks. Credentials and returned user data stay in memory."""
import base64
import json
import subprocess
import urllib.error
import urllib.request
import urllib.parse


def main():
    state = json.loads(subprocess.check_output(['docker', 'inspect', 'gowa-app-1']))[0]
    env = dict(item.split('=', 1) for item in state['Config']['Env'])
    address = state['NetworkSettings']['Networks']['caddy']['IPAddress']
    base = f'http://{address}:3001'
    headers = {'Authorization': 'Basic ' + base64.b64encode(env['APP_BASIC_AUTH'].encode()).decode(),
               'Content-Type': 'application/json', 'Accept': 'application/json, text/event-stream',
               'Host': urllib.parse.urlsplit(env['MCP_OAUTH_ISSUER_URL']).netloc}

    def request(path, body=None, authenticated=True):
        req = urllib.request.Request(base + path, data=body, headers=headers if authenticated else {
            key: value for key, value in headers.items() if key != 'Authorization'})
        try:
            with urllib.request.urlopen(req, timeout=12) as response:
                return response.status, response.headers, response.read()
        except urllib.error.HTTPError as error:
            return error.code, error.headers, error.read()

    status, _, _ = request('/mcp', b'{}', authenticated=False)
    assert status == 401, f'unauthenticated MCP returned {status}'
    print('PASS unauthenticated MCP rejected (401)')
    status, _, _ = request('/app/info')
    assert status == 200, f'authenticated application returned {status}'
    print('PASS authenticated application information (200)')
    status, _, body = request('/')
    assert status == 200 and b'<html' in body.lower(), f'authenticated dashboard returned {status}'
    print('PASS authenticated dashboard HTML (200)')

    def rpc(method, params):
        status, returned, raw = request('/mcp', json.dumps({
            'jsonrpc': '2.0', 'id': 1, 'method': method, 'params': params}).encode())
        assert status == 200, f'{method} returned {status}'
        if 'text/event-stream' in returned.get('Content-Type', ''):
            raw = next(line[6:] for line in raw.splitlines() if line.startswith(b'data: '))
        reply = json.loads(raw)
        assert 'error' not in reply, f'{method} returned a protocol error'
        return reply['result'], returned

    result, returned = rpc('initialize', {'protocolVersion': '2025-11-25', 'capabilities': {},
                                        'clientInfo': {'name': 'security-readonly-check', 'version': '1'}})
    headers['Mcp-Session-Id'] = returned['Mcp-Session-Id']
    headers['MCP-Protocol-Version'] = result['protocolVersion']
    status, _, _ = request('/mcp', b'{"jsonrpc":"2.0","method":"notifications/initialized"}')
    assert status == 202
    result, _ = rpc('tools/list', {})
    names = {tool['name'] for tool in result['tools']}
    expected = {'whatsapp_app', 'whatsapp_chat', 'whatsapp_send', 'whatsapp_message',
                'whatsapp_media', 'whatsapp_events', 'whatsapp_history', 'whatsapp_profile',
                'whatsapp_newsletter', 'whatsapp_schedule', 'whatsapp_group'}
    assert names == expected, 'tool catalog changed unexpectedly'
    print(f'PASS authenticated stateful MCP; all {len(names)} tools retained')
    req = urllib.request.Request(base + '/mcp', method='DELETE', headers=headers)
    with urllib.request.urlopen(req, timeout=12) as response:
        assert response.status in (200, 204)
    print('PASS diagnostic session closed; no WhatsApp message or account change performed')


if __name__ == '__main__':
    main()
