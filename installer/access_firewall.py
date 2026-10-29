"""Host firewall for private management access.

One nftables table, inet hakopod_access, limits the dashboard port and the
K3s API (6443) to the access policy's networks. Loopback, cluster nodes and
the cluster's Pod and Service networks are always allowed, so SSH tunnels,
agents and in-cluster clients of the Kubernetes API keep working. Nothing
else on the host is touched: HAProxy's 80 and 443, kubelet and flannel stay
as the operator configured them.
"""
import ipaddress
import argparse
import json
import os
import shutil
import subprocess
import sys
from pathlib import Path

TABLE = 'hakopod_access'
SAVED = Path('/var/lib/hakopod/maintenance/access-firewall.nft')
CLUSTER_NETWORKS = ('10.42.0.0/16', '10.43.0.0/16')  # host.py k3s.yaml cluster-cidr, service-cidr
KUBECTL = ['/opt/hakopod/tools/kubectl', '--kubeconfig=/etc/hakopod/admin-kubeconfig', '--request-timeout=10s']
MAX_CIDRS = 256


def networks(values):
    if not isinstance(values, list) or len(values) > MAX_CIDRS:
        raise ValueError('allow_cidrs must be a list of at most 256 CIDRs')
    out = []
    for value in values:
        if not isinstance(value, str):
            raise ValueError('allow_cidrs must contain strings')
        network = ipaddress.ip_network(value, strict=True)
        if network.prefixlen == 0:
            raise ValueError('allow_cidrs cannot contain a default route')
        out.append(network)
    return out


def ruleset(allow, ports, tunnel=None):
    """Render the table. Re-declaring and deleting it first makes `nft -f`
    replace it in one transaction. With tunnel, the ports also accept
    anything arriving on that interface."""
    v4 = sorted({str(n) for n in allow if n.version == 4})
    v6 = sorted({str(n) for n in allow if n.version == 6})
    port_set = ', '.join(str(p) for p in sorted(set(ports)))
    lines = [f'table inet {TABLE} {{}}', f'delete table inet {TABLE}', f'table inet {TABLE} {{',
             '  set allow4 { type ipv4_addr; flags interval; ' + (f'elements = {{ {", ".join(v4)} }}; ' if v4 else '') + '}',
             '  set allow6 { type ipv6_addr; flags interval; ' + (f'elements = {{ {", ".join(v6)} }}; ' if v6 else '') + '}',
             '  chain input {',
             '    type filter hook input priority filter - 10; policy accept;',
             f'    tcp dport {{ {port_set} }} iif "lo" accept',
             *([f'    tcp dport {{ {port_set} }} iifname {{ {", ".join(f'"{name}"' for name in tunnel)} }} accept'] if tunnel else []),
             f'    tcp dport {{ {port_set} }} ip saddr @allow4 accept',
             f'    tcp dport {{ {port_set} }} ip6 saddr @allow6 accept',
             f'    tcp dport {{ {port_set} }} counter drop',
             '  }', '}']
    return '\n'.join(lines) + '\n'


def cluster_addresses():
    raw = subprocess.check_output([*KUBECTL, 'get', 'nodes', '-o', 'json'], timeout=20, text=True)
    out = [ipaddress.ip_network(n) for n in CLUSTER_NETWORKS]
    for node in json.loads(raw)['items']:
        for address in node.get('status', {}).get('addresses', []):
            if address.get('type') in ('InternalIP', 'ExternalIP'):
                out.append(ipaddress.ip_network(address['address']))
    return out


def ports(config):
    out = [6443]
    if config.get('dashboard_mode') == 'https':
        out.append(int(config['dashboard_port']))
    return out


def apply(body, config, run=subprocess.run, addresses=cluster_addresses, saved=SAVED):
    if not isinstance(body, dict) or set(body) != {'enabled', 'allow_cidrs', 'tunnel_only'} or not isinstance(body['enabled'], bool) or not isinstance(body['tunnel_only'], bool):
        raise ValueError('Send enabled, allow_cidrs and tunnel_only only')
    allow = networks(body['allow_cidrs'])
    if not body['enabled']:
        if saved.exists():
            remove(run, saved)
        return {'applied': False}
    if not allow:
        raise ValueError('An enabled firewall needs at least one allowed CIDR')
    if body['tunnel_only']:
        # The K3s API is reachable only over the tunnel, from nodes and from
        # the cluster. The dashboard port stays open so signed webhooks
        # arrive; the dashboard's own gate refuses every other request.
        text = ruleset(addresses(), [6443], tunnel=('hakopod-wg', 'hakopod-wgs'))
    else:
        text = ruleset(allow + addresses(), ports(config))
    if saved.is_file() and saved.read_text() == text:
        return {'applied': True}
    run(['nft', '-f', '-'], input=text, text=True, check=True, timeout=20)
    saved.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    saved.write_text(text)
    saved.chmod(0o600)
    return {'applied': True}


def remove(run=subprocess.run, saved=SAVED):
    run(['nft', '-f', '-'], input=f'table inet {TABLE} {{}}\ndelete table inet {TABLE}\n', text=True, check=True, timeout=20)
    saved.unlink(missing_ok=True)


import access_vpn  # noqa: E402  (installed next to this file)
import access_vpn_server  # noqa: E402

STATE_COMMAND = ['systemd-run', '--wait', '--pipe', '--quiet', '--collect', '--uid=hakopod-api', '--gid=hakopod-api',
                 '--property=EnvironmentFile=/etc/hakopod/api.env', '/opt/hakopod/current/hakopod-server', 'access-firewall-state']
HOME = Path('/opt/hakopod/access')
UNITS = Path('/etc/systemd/system')


def sync():
    """One timer tick: read the desired state through the release's server
    binary as the API user, then make nftables match it. If the state cannot
    be read, the current rules stay; `nft delete table inet hakopod_access`
    or the access reset command is the way out."""
    try:
        state = json.loads(subprocess.check_output(STATE_COMMAND, text=True, timeout=60))
    except (subprocess.SubprocessError, ValueError) as error:
        print('Access firewall state is unavailable; keeping the current rules: ' + str(error), file=sys.stderr)
        return 1
    code = 0
    try:
        apply(state['firewall'], json.loads(Path('/etc/hakopod/config.json').read_text()))
    except (subprocess.SubprocessError, ValueError, KeyError) as error:
        print('Access firewall could not be applied: ' + str(error), file=sys.stderr)
        code = 1
    # The tunnel is independent of the firewall: one failing must not stop the other.
    try:
        status = access_vpn.apply(state.get('vpn'))
    except (subprocess.SubprocessError, ValueError, OSError) as error:
        status, code = {'public_key': '', 'latest_handshake': None, 'rx_bytes': 0, 'tx_bytes': 0, 'error': str(error)[:500]}, 1
    try:
        server = access_vpn_server.apply(state.get('vpn_server'), json.loads(Path('/etc/hakopod/config.json').read_text()))
    except (subprocess.SubprocessError, ValueError, OSError, KeyError) as error:
        server, code = {'server_public_key': '', 'error': str(error)[:500], 'peers': []}, 1
    if server is not None:
        subprocess.run([*STATE_COMMAND[:-1], 'access-vpn-server-report'], input=json.dumps(server), text=True, timeout=60)
    if status is not None or state.get('vpn') is None:
        report = status or {'public_key': '', 'latest_handshake': None, 'rx_bytes': 0, 'tx_bytes': 0, 'error': ''}
        subprocess.run([*STATE_COMMAND[:-1], 'access-vpn-report'], input=json.dumps(report), text=True, timeout=60)
    return code


def install():
    if os.geteuid() != 0:
        raise ValueError('Install the private-access module as root')
    for tool, package in (('nft', 'nftables'), ('wg', 'wireguard-tools')):
        if shutil.which(tool) is None:
            raise ValueError(f'{tool} is required: install the {package} package, then run this module again')
    HOME.mkdir(mode=0o755, parents=True, exist_ok=True)
    for name in ('access_firewall.py', 'access_vpn.py', 'access_vpn_server.py'):
        shutil.copyfile(Path(__file__).resolve().parent / name, HOME / name)
        os.chmod(HOME / name, 0o644)
    (UNITS / 'hakopod-access-firewall.service').write_text("""[Unit]
Description=Hakopod management access firewall
After=network-online.target hakopod-k3s.service

[Service]
Type=oneshot
ExecStart=/usr/bin/python3 /opt/hakopod/access/access_firewall.py --sync
""")
    (UNITS / 'hakopod-access-firewall.timer').write_text("""[Unit]
Description=Keep the Hakopod management access firewall in step with the access policy

[Timer]
OnBootSec=5s
OnUnitActiveSec=10s
AccuracySec=1s

[Install]
WantedBy=timers.target
""")
    subprocess.run(['systemctl', 'daemon-reload'], check=True, timeout=60)
    subprocess.run(['systemctl', 'enable', '--now', 'hakopod-access-firewall.timer'], check=True, timeout=60)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--sync', action='store_true', help='Apply the current access policy (run by the timer)')
    args = parser.parse_args()
    if args.sync:
        raise SystemExit(sync())
    parser.print_help()
