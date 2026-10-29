"""Tailscale, OpenVPN and IPsec connectors for private management access.

At most one runs. Each is brought up only when its configuration changes,
torn down when it is removed, and never allowed to take the server's default
route or run scripts. Secrets reach this module from the state command and
are written only to root-owned 0600 files.
"""
import hashlib
import ipaddress
import json
import os
import re
import shutil
import subprocess
from pathlib import Path

STATE = Path('/var/lib/hakopod/maintenance')
OPENVPN_DIR = Path('/etc/hakopod/openvpn')
OPENVPN_UNIT = Path('/etc/systemd/system/hakopod-openvpn.service')
SWANCTL = Path('/etc/swanctl/conf.d/hakopod.conf')
RUN = Path('/run/hakopod-access')
RESERVED = (ipaddress.ip_network('10.42.0.0/16'), ipaddress.ip_network('10.43.0.0/16'))
OPENVPN_DIRECTIVES = {
    'client', 'proto', 'remote', 'port', 'rport', 'resolv-retry', 'nobind', 'persist-key', 'persist-tun',
    'remote-cert-tls', 'verify-x509-name', 'cipher', 'data-ciphers', 'data-ciphers-fallback', 'auth',
    'tls-version-min', 'tls-cipher', 'tls-ciphersuites', 'key-direction', 'verb', 'mute', 'tun-mtu', 'mssfix',
    'keepalive', 'ping', 'ping-restart', 'connect-retry', 'connect-retry-max', 'server-poll-timeout',
    'remote-random', 'explicit-exit-notify', 'reneg-sec', 'auth-nocache'}
OPENVPN_BLOCKS = {'ca', 'cert', 'key', 'tls-auth', 'tls-crypt', 'tls-crypt-v2'}
IKE_ID = re.compile(r'^[A-Za-z0-9@._:=,-]{1,128}$')
HOST = re.compile(r'^(?=.{1,253}$)[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?(?:\.[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*$')


def networks(values):
    out = []
    if not isinstance(values, list) or not 0 < len(values) <= 64:
        raise ValueError('Routes need between 1 and 64 networks')
    for raw in values:
        network = ipaddress.ip_network(raw, strict=True)
        if network.prefixlen == 0 or any(network.overlaps(r) for r in RESERVED if r.version == network.version):
            raise ValueError('Routes cannot take the default route or the cluster networks')
        out.append(network)
    return out


def write_secret(path, text):
    path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    temporary = path.with_suffix('.tmp')
    fd = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, 'w') as handle:
        handle.write(text)
    os.replace(temporary, path)


def changed(name, value, state=STATE):
    """True when value differs from the last one applied for name. Only a
    digest is kept, never the secret."""
    digest = hashlib.sha256(json.dumps(value, sort_keys=True).encode()).hexdigest()
    marker = state / f'{name}.sha256'
    if marker.is_file() and marker.read_text() == digest:
        return False
    return digest


def remember(name, digest, state=STATE):
    state.mkdir(mode=0o700, parents=True, exist_ok=True)
    (state / f'{name}.sha256').write_text(digest)


def forget(name, state=STATE):
    marker = state / f'{name}.sha256'
    existed = marker.exists()
    marker.unlink(missing_ok=True)
    return existed


def openvpn_config(profile, routes):
    """Keep only inert directives and inline key blocks, then force the
    device, no pushed routes and no scripts."""
    out, block = [], ''
    for raw in profile.replace('\r\n', '\n').split('\n'):
        line = raw.strip()
        if block:
            out.append(raw)
            if line == f'</{block}>':
                block = ''
            continue
        if not line or line[0] in '#;':
            continue
        if line.startswith('<') and line.endswith('>'):
            name = line.strip('<>')
            if name not in OPENVPN_BLOCKS:
                raise ValueError('Unsupported OpenVPN block')
            block = name
            out.append(line)
            continue
        name = line.split()[0].lower()
        if name in ('dev', 'dev-type'):
            continue
        if name not in OPENVPN_DIRECTIVES:
            raise ValueError(f'OpenVPN directive {name} is not allowed')
        out.append(line)
    if block:
        raise ValueError('Unterminated OpenVPN block')
    out += ['dev hakopod-ovpn', 'dev-type tun', 'route-nopull', 'script-security 1']
    for network in routes:
        out.append(f'route {network.network_address} {network.netmask}' if network.version == 4 else f'route-ipv6 {network}')
    return '\n'.join(out) + '\n'


def swanctl_config(ipsec, psk):
    remote = ipsec['remote_address']
    try:
        ipaddress.ip_address(remote)
    except ValueError:
        if not HOST.match(remote):
            raise ValueError('Invalid IPsec remote address') from None
    if not IKE_ID.match(ipsec['local_id']) or not IKE_ID.match(ipsec['remote_id']):
        raise ValueError('Invalid IKE identity')
    if not 20 <= len(psk) <= 256 or any(c in psk for c in '"\n\r'):
        raise ValueError('Invalid IPsec pre-shared key')
    remote_ts = ','.join(str(n) for n in networks(ipsec['remote_networks']))
    return f'''connections {{
  hakopod {{
    version = 2
    remote_addrs = {remote}
    local {{
      auth = psk
      id = {ipsec['local_id']}
    }}
    remote {{
      auth = psk
      id = {ipsec['remote_id']}
    }}
    children {{
      hakopod {{
        local_ts = dynamic
        remote_ts = {remote_ts}
        start_action = start
        dpd_action = restart
        close_action = start
      }}
    }}
  }}
}}
secrets {{
  ike-hakopod {{
    id-1 = {ipsec['local_id']}
    id-2 = {ipsec['remote_id']}
    secret = "{psk}"
  }}
}}
'''


def tailscale(config, run, state):
    if shutil.which('tailscale') is None:
        raise ValueError('Install Tailscale on the server to use the Tailscale connector')
    key = config['auth_key']
    if not 16 <= len(key) <= 512 or any(c.isspace() for c in key):
        raise ValueError('Invalid Tailscale auth key')
    if not re.match(r'^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$', config['hostname']):
        raise ValueError('Invalid Tailscale hostname')
    tags = config['tags'] or []
    if any(not re.match(r'^tag:[a-z0-9][a-z0-9-]{0,62}$', tag) for tag in tags):
        raise ValueError('Invalid Tailscale tag')
    login = config['login_server']
    if login and (not login.startswith('https://') or any(c.isspace() for c in login)):
        raise ValueError('Invalid Tailscale login server')
    digest = changed('tailscale', config, state)
    if digest:
        run(['systemctl', 'enable', '--now', 'tailscaled'], check=True, timeout=60)
        key_file = RUN / 'tailscale.key'
        write_secret(key_file, key)
        try:
            # No DNS, no pushed routes and no netfilter changes: K3s owns iptables.
            run(['tailscale', 'up', '--reset', f'--auth-key=file:{key_file}', f'--hostname={config["hostname"]}',
                 '--accept-routes=false', '--accept-dns=false', '--netfilter-mode=off', '--timeout=60s',
                 *([f'--advertise-tags={",".join(tags)}'] if tags else []), *([f'--login-server={login}'] if login else [])],
                check=True, timeout=90)
        finally:
            key_file.unlink(missing_ok=True)
        remember('tailscale', digest, state)
    status = json.loads(run(['tailscale', 'status', '--json'], capture_output=True, text=True, check=True, timeout=20).stdout)
    addresses = status.get('Self', {}).get('TailscaleIPs') or ['']
    return {'type': 'tailscale', 'connected': status.get('BackendState') == 'Running', 'address': addresses[0], 'error': ''}


def openvpn(config, run, state):
    if shutil.which('openvpn') is None:
        raise ValueError('Install OpenVPN on the server to use the OpenVPN connector')
    text = openvpn_config(config['profile'], networks(config['routes']))
    digest = changed('openvpn', text, state)
    if digest:
        write_secret(OPENVPN_DIR / 'hakopod.conf', text)
        OPENVPN_UNIT.write_text('''[Unit]
Description=Hakopod management OpenVPN connector
After=network-online.target

[Service]
ExecStart=/usr/sbin/openvpn --config /etc/hakopod/openvpn/hakopod.conf
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
''')
        run(['systemctl', 'daemon-reload'], check=True, timeout=60)
        run(['systemctl', 'enable', 'hakopod-openvpn'], check=True, timeout=60)
        run(['systemctl', 'restart', 'hakopod-openvpn'], check=True, timeout=60)
        remember('openvpn', digest, state)
    active = run(['systemctl', 'is-active', 'hakopod-openvpn'], capture_output=True, text=True, timeout=20).stdout.strip() == 'active'
    raw = run(['ip', '-j', '-4', 'address', 'show', 'dev', 'hakopod-ovpn'], capture_output=True, text=True, timeout=10).stdout or '[]'
    info = (json.loads(raw) or [{}])[0].get('addr_info') or [{}]
    return {'type': 'openvpn', 'connected': active and bool(info[0].get('local')), 'address': info[0].get('local', ''), 'error': ''}


def ipsec(config, run, state):
    if shutil.which('swanctl') is None:
        raise ValueError('Install strongSwan (swanctl) on the server to use the IPsec connector')
    text = swanctl_config(config, config['psk'])
    digest = changed('ipsec', text, state)
    if digest:
        write_secret(SWANCTL, text)
        run(['swanctl', '--load-all'], check=True, timeout=60)
        remember('ipsec', digest, state)
    sas = run(['swanctl', '--list-sas', '--ike', 'hakopod'], capture_output=True, text=True, timeout=20).stdout
    return {'type': 'ipsec', 'connected': 'ESTABLISHED' in sas and 'INSTALLED' in sas, 'address': '', 'error': ''}


def teardown(keep, run, state):
    if keep != 'tailscale' and forget('tailscale', state):
        run(['tailscale', 'logout'], timeout=60)
    if keep != 'openvpn' and forget('openvpn', state):
        run(['systemctl', 'disable', '--now', 'hakopod-openvpn'], timeout=60)
        (OPENVPN_DIR / 'hakopod.conf').unlink(missing_ok=True)
    if keep != 'ipsec' and forget('ipsec', state):
        SWANCTL.unlink(missing_ok=True)
        run(['swanctl', '--terminate', '--ike', 'hakopod'], timeout=60)
        run(['swanctl', '--load-all'], timeout=60)


def apply(state_doc, run=subprocess.run, state=STATE):
    """Bring the configured connector up and the others down. Returns the
    status to report, or None when no connector is configured."""
    for name, handler in (('tailscale', tailscale), ('openvpn', openvpn), ('ipsec', ipsec)):
        config = state_doc.get(name)
        if config is not None:
            teardown(name, run, state)
            try:
                return handler(config, run, state)
            except (ValueError, KeyError, subprocess.SubprocessError, OSError) as error:
                return {'type': name, 'connected': False, 'address': '', 'error': str(error)[:500]}
    teardown(None, run, state)
    return None
