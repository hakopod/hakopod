"""WireGuard connector for private management access.

The server joins the administrator's WireGuard network as a client on one
interface, hakopod-wg. Its private key is generated here and never leaves
the host; only the public key is reported to the dashboard. Only the
networks in allowed_ips are routed through the tunnel.
"""
import base64
import datetime
import ipaddress
import json
import os
import re
import subprocess
from pathlib import Path

IFACE = 'hakopod-wg'
KEY = Path('/etc/hakopod/wireguard/private.key')
RESERVED = (ipaddress.ip_network('10.42.0.0/16'), ipaddress.ip_network('10.43.0.0/16'))
HOST = re.compile(r'^(?=.{1,253}$)[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?(?:\.[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*\.?$')


def validate(vpn):
    """The state comes from the release binary, but every value still ends up
    in argv of a root command, so it is checked again here."""
    if not isinstance(vpn, dict) or set(vpn) != {'address', 'peer_public_key', 'peer_endpoint', 'allowed_ips', 'keepalive'}:
        raise ValueError('Unexpected VPN fields')
    ipaddress.ip_interface(vpn['address'])
    if len(base64.b64decode(vpn['peer_public_key'], validate=True)) != 32:
        raise ValueError('Invalid peer public key')
    host, _, port = vpn['peer_endpoint'].rpartition(':')
    host = host[1:-1] if host.startswith('[') and host.endswith(']') else host
    try:
        ipaddress.ip_address(host)
    except ValueError:
        if not HOST.match(host):
            raise ValueError('Invalid peer endpoint') from None
    if not port.isdigit() or not 0 < int(port) < 65536:
        raise ValueError('Invalid peer endpoint port')
    if not isinstance(vpn['allowed_ips'], list) or not 0 < len(vpn['allowed_ips']) <= 64:
        raise ValueError('allowed_ips needs between 1 and 64 networks')
    for raw in vpn['allowed_ips']:
        network = ipaddress.ip_network(raw, strict=True)
        if network.prefixlen == 0 or any(network.overlaps(r) for r in RESERVED if r.version == network.version):
            raise ValueError('allowed_ips cannot take the default route or the cluster networks')
    if not isinstance(vpn['keepalive'], int) or not 0 < vpn['keepalive'] <= 3600:
        raise ValueError('Invalid keepalive')


def public_key(run=subprocess.run, key=KEY):
    if not key.exists():
        key.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
        private = run(['wg', 'genkey'], capture_output=True, text=True, check=True, timeout=10).stdout.strip()
        fd = os.open(key, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(fd, 'w') as handle:
            handle.write(private + '\n')
    return run(['wg', 'pubkey'], input=key.read_text(), capture_output=True, text=True, check=True, timeout=10).stdout.strip()


def link_exists(run):
    return run(['ip', 'link', 'show', 'dev', IFACE], capture_output=True, timeout=10).returncode == 0


def apply(vpn, run=subprocess.run, key=KEY):
    """Make hakopod-wg match vpn, or remove it when vpn is None. Returns the
    status to report, or None when there is no connector."""
    if vpn is None:
        if link_exists(run):
            run(['ip', 'link', 'del', 'dev', IFACE], check=True, timeout=10)
        return None
    validate(vpn)
    status = {'public_key': public_key(run, key), 'latest_handshake': None, 'rx_bytes': 0, 'tx_bytes': 0, 'error': ''}
    if not link_exists(run):
        run(['ip', 'link', 'add', 'dev', IFACE, 'type', 'wireguard'], check=True, timeout=10)
    peer = vpn['peer_public_key']
    run(['wg', 'set', IFACE, 'private-key', str(key), 'peer', peer, 'endpoint', vpn['peer_endpoint'],
         'allowed-ips', ','.join(vpn['allowed_ips']), 'persistent-keepalive', str(vpn['keepalive'])], check=True, timeout=10)
    peers = run(['wg', 'show', IFACE, 'peers'], capture_output=True, text=True, check=True, timeout=10).stdout.split()
    for other in peers:
        if other != peer:
            run(['wg', 'set', IFACE, 'peer', other, 'remove'], check=True, timeout=10)
    addresses = json.loads(run(['ip', '-j', 'address', 'show', 'dev', IFACE], capture_output=True, text=True, check=True, timeout=10).stdout or '[]')
    wanted = ipaddress.ip_interface(vpn['address'])
    for item in addresses[0].get('addr_info', []) if addresses else []:
        current = ipaddress.ip_interface(f"{item['local']}/{item['prefixlen']}")
        if current != wanted:
            run(['ip', 'address', 'del', str(current), 'dev', IFACE], check=True, timeout=10)
    run(['ip', 'address', 'replace', str(wanted), 'dev', IFACE], check=True, timeout=10)
    run(['ip', 'link', 'set', 'up', 'dev', IFACE], check=True, timeout=10)
    for network in vpn['allowed_ips']:
        run(['ip', 'route', 'replace', network, 'dev', IFACE], check=True, timeout=10)
    routes = json.loads(run(['ip', '-j', 'route', 'show', 'dev', IFACE], capture_output=True, text=True, check=True, timeout=10).stdout or '[]')
    for route in routes:
        if route.get('protocol') != 'kernel' and route.get('dst') not in vpn['allowed_ips']:
            run(['ip', 'route', 'del', route['dst'], 'dev', IFACE], check=True, timeout=10)
    dump = run(['wg', 'show', IFACE, 'dump'], capture_output=True, text=True, check=True, timeout=10).stdout.splitlines()
    for line in dump[1:]:
        fields = line.split('\t')
        if fields[0] == peer and len(fields) >= 7:
            if int(fields[4]):
                status['latest_handshake'] = datetime.datetime.fromtimestamp(int(fields[4]), datetime.timezone.utc).isoformat()
            status['rx_bytes'], status['tx_bytes'] = int(fields[5]), int(fields[6])
    return status
