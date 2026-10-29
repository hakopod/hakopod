"""Built-in WireGuard server for private management access.

One interface, hakopod-wgs, listens on the configured UDP port with one peer
per device. Devices generate their own keys in the browser; only public keys
arrive here. A NAT table forwards the dashboard port and the K3s API from the
server's tunnel address to the node address they listen on, so profiles
route only the VPN network and never this host's public address.
"""
import base64
import datetime
import ipaddress
import os
import subprocess
from pathlib import Path

IFACE = 'hakopod-wgs'
KEY = Path('/etc/hakopod/wireguard/server.key')
TABLE = 'hakopod_vpn_server'
RESERVED = (ipaddress.ip_network('10.42.0.0/16'), ipaddress.ip_network('10.43.0.0/16'))


def validate(server):
    if not isinstance(server, dict) or set(server) != {'listen_port', 'address', 'peers'}:
        raise ValueError('Unexpected VPN server fields')
    if not isinstance(server['listen_port'], int) or not 0 < server['listen_port'] < 65536:
        raise ValueError('Invalid listen port')
    address = ipaddress.ip_interface(server['address'])
    if address.version != 4 or any(address.network.overlaps(r) for r in RESERVED):
        raise ValueError('Invalid server address')
    if not isinstance(server['peers'], list) or len(server['peers']) > 1024:
        raise ValueError('Invalid peers')
    for peer in server['peers']:
        if not isinstance(peer, dict) or set(peer) != {'public_key', 'address'}:
            raise ValueError('Unexpected peer fields')
        if len(base64.b64decode(peer['public_key'], validate=True)) != 32:
            raise ValueError('Invalid peer key')
        if ipaddress.ip_address(peer['address']) not in address.network:
            raise ValueError('Peer address outside the VPN network')


def server_key(run, key):
    if not key.exists():
        key.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
        private = run(['wg', 'genkey'], capture_output=True, text=True, check=True, timeout=10).stdout.strip()
        fd = os.open(key, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(fd, 'w') as handle:
            handle.write(private + '\n')
    return run(['wg', 'pubkey'], input=key.read_text(), capture_output=True, text=True, check=True, timeout=10).stdout.strip()


def forwarding(address, node_ip, ports):
    """NAT the dashboard and K3s API from the tunnel address to the node."""
    port_set = ', '.join(str(p) for p in sorted(set(ports)))
    return '\n'.join([f'table ip {TABLE} {{}}', f'delete table ip {TABLE}', f'table ip {TABLE} {{',
                      '  chain prerouting {', '    type nat hook prerouting priority dstnat; policy accept;',
                      f'    iifname "{IFACE}" ip daddr {address} tcp dport {{ {port_set} }} dnat to {node_ip}',
                      '  }', '}']) + '\n'


def apply(server, config, run=subprocess.run, key=KEY):
    """Make hakopod-wgs match server, or remove it when server is None."""
    exists = run(['ip', 'link', 'show', 'dev', IFACE], capture_output=True, timeout=10).returncode == 0
    if server is None:
        if exists:
            run(['ip', 'link', 'del', 'dev', IFACE], check=True, timeout=10)
            run(['nft', '-f', '-'], input=f'table ip {TABLE} {{}}\ndelete table ip {TABLE}\n', text=True, check=True, timeout=20)
        return None
    validate(server)
    node_ip = str(ipaddress.ip_address(config['node_ip']))
    public = server_key(run, key)
    if not exists:
        run(['ip', 'link', 'add', 'dev', IFACE, 'type', 'wireguard'], check=True, timeout=10)
    run(['wg', 'set', IFACE, 'listen-port', str(server['listen_port']), 'private-key', str(key)], check=True, timeout=10)
    wanted = {peer['public_key']: peer['address'] for peer in server['peers']}
    current = run(['wg', 'show', IFACE, 'peers'], capture_output=True, text=True, check=True, timeout=10).stdout.split()
    for peer in current:
        if peer not in wanted:
            run(['wg', 'set', IFACE, 'peer', peer, 'remove'], check=True, timeout=10)
    for peer, address in wanted.items():
        run(['wg', 'set', IFACE, 'peer', peer, 'allowed-ips', address + '/32'], check=True, timeout=10)
    run(['ip', 'address', 'replace', server['address'], 'dev', IFACE], check=True, timeout=10)
    run(['ip', 'link', 'set', 'up', 'dev', IFACE], check=True, timeout=10)
    ports = [6443] + ([int(config['dashboard_port'])] if config.get('dashboard_mode') == 'https' else [])
    address = str(ipaddress.ip_interface(server['address']).ip)
    run(['nft', '-f', '-'], input=forwarding(address, node_ip, ports), text=True, check=True, timeout=20)
    report = {'server_public_key': public, 'error': '', 'peers': []}
    for line in run(['wg', 'show', IFACE, 'latest-handshakes'], capture_output=True, text=True, check=True, timeout=10).stdout.splitlines():
        peer, _, seconds = line.partition('\t')
        if seconds.strip().isdigit() and int(seconds):
            at = datetime.datetime.fromtimestamp(int(seconds), datetime.timezone.utc).isoformat()
            report['peers'].append({'public_key': peer, 'latest_handshake': at})
    return report
