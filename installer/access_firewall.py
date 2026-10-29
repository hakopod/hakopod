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


def ruleset(allow, ports):
    """Render the table. Re-declaring and deleting it first makes `nft -f`
    replace it in one transaction."""
    v4 = sorted({str(n) for n in allow if n.version == 4})
    v6 = sorted({str(n) for n in allow if n.version == 6})
    port_set = ', '.join(str(p) for p in sorted(set(ports)))
    lines = [f'table inet {TABLE} {{}}', f'delete table inet {TABLE}', f'table inet {TABLE} {{',
             '  set allow4 { type ipv4_addr; flags interval; ' + (f'elements = {{ {", ".join(v4)} }}; ' if v4 else '') + '}',
             '  set allow6 { type ipv6_addr; flags interval; ' + (f'elements = {{ {", ".join(v6)} }}; ' if v6 else '') + '}',
             '  chain input {',
             '    type filter hook input priority filter - 10; policy accept;',
             f'    tcp dport {{ {port_set} }} iif "lo" accept',
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
    if not isinstance(body, dict) or set(body) != {'enabled', 'allow_cidrs'} or not isinstance(body['enabled'], bool):
        raise ValueError('Send enabled and allow_cidrs only')
    allow = networks(body['allow_cidrs'])
    if not body['enabled']:
        if saved.exists():
            remove(run, saved)
        return {'applied': False}
    if not allow:
        raise ValueError('An enabled firewall needs at least one allowed CIDR')
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
    apply(state, json.loads(Path('/etc/hakopod/config.json').read_text()))
    return 0


def install():
    if os.geteuid() != 0:
        raise ValueError('Install the private-access module as root')
    if shutil.which('nft') is None:
        raise ValueError('nftables is required: install the nftables package, then run this module again')
    HOME.mkdir(mode=0o755, parents=True, exist_ok=True)
    shutil.copyfile(Path(__file__).resolve(), HOME / 'access_firewall.py')
    os.chmod(HOME / 'access_firewall.py', 0o644)
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
