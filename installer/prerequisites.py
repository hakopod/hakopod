#!/usr/bin/env python3
"""Install only missing distro packages selected by the reviewed host plan."""
import argparse
import importlib.util
import os
from pathlib import Path
import platform
import shutil
import subprocess

HERE = Path(__file__).resolve().parent
BASE = ('python3', 'bash', 'curl', 'ca-certificates', 'openssl', 'iproute2', 'util-linux', 'passwd', 'kmod', 'coreutils')


def requested(config):
    packages = list(BASE)
    if config.get('database_mode', 'managed') != 'managed':
        packages.append('postgresql-client')
    if config.get('install_docker', False) and not shutil.which('dockerd'):
        if shutil.which('docker'):
            raise ValueError('An existing Docker client has no local daemon; configure its matching daemon instead of replacing Docker packages')
        packages.append('docker.io')
    return packages


def missing(config):
    result = []
    for package in requested(config):
        query = subprocess.run(['dpkg-query', '-W', '-f=${db:Status-Status}', package], capture_output=True, text=True, timeout=10)
        if query.returncode != 0 or query.stdout.strip() != 'installed':
            result.append(package)
    return result


def plan(config):
    if platform.system() != 'Linux' or not shutil.which('dpkg-query'):
        print('Distro prerequisites on the target: ' + ', '.join(requested(config)))
        return
    packages = missing(config)
    print('Missing distro packages: ' + (', '.join(packages) if packages else 'none'))
    print('K3s uses its bundled containerd; Docker is ' + ('requested separately.' if config.get('install_docker', False) else 'not required.'))


def install(config):
    if platform.system() != 'Linux' or os.geteuid() != 0 or not shutil.which('apt-get'):
        raise ValueError('Dependency provisioning requires root and apt-get on the supported Linux host')
    packages = missing(config)
    if not packages:
        return
    environment = dict(os.environ, DEBIAN_FRONTEND='noninteractive')
    options = ['-o', 'DPkg::Lock::Timeout=120', '-o', 'Acquire::Retries=2',
               '-o', 'Acquire::http::Timeout=30', '-o', 'Acquire::https::Timeout=30']
    subprocess.run(['apt-get', *options, 'update', '-qq'], env=environment, timeout=600, check=True)
    subprocess.run(['apt-get', *options, 'install', '-y', '--no-install-recommends', *packages],
                   env=environment, timeout=1200, check=True)
    if missing(config):
        raise ValueError('Required distro packages are still missing after package installation')
    if 'docker.io' in packages:
        subprocess.run(['systemctl', 'enable', '--now', 'docker'], timeout=120, check=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=('plan', 'install'))
    parser.add_argument('--config', required=True)
    args = parser.parse_args()
    specification = importlib.util.spec_from_file_location('installer_host', HERE / 'host.py')
    host = importlib.util.module_from_spec(specification); specification.loader.exec_module(host)
    config = host.config(args.config)
    (plan if args.action == 'plan' else install)(config)


if __name__ == '__main__':
    try:
        main()
    except (ValueError, subprocess.SubprocessError) as error:
        raise SystemExit('Installer prerequisites: ' + str(error)) from None
