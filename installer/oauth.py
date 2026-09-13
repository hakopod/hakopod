#!/usr/bin/env python3
"""Optional OAuth setup; credentials stay in protected files, never argv or plans."""
import argparse
import getpass
import json
import os
from pathlib import Path

PROVIDERS = ('google', 'github', 'gitlab')


def secret_file(path):
    source = Path(path)
    if not source.is_absolute() or source.is_symlink() or not source.is_file():
        raise ValueError('OAuth secret must be an absolute regular non-symlink file')
    if source.stat().st_mode & 0o077 or source.stat().st_size > 16384:
        raise ValueError('OAuth secret file must be private (0400 or 0600) and at most 16 KiB')
    return credential(source.read_text().strip(), 'client secret')


def credential(value, label):
    if not isinstance(value, str) or not value or len(value) > 16384 or any(ord(ch) < 32 for ch in value):
        raise ValueError('OAuth ' + label + ' is empty, too long or contains control characters')
    return value


def configured(values, environ=None):
    environ = os.environ if environ is None else environ
    if not isinstance(values, dict) or set(values) - set(PROVIDERS):
        raise ValueError('OAuth configuration contains an unsupported provider')
    result = {}
    for provider in PROVIDERS:
        entry = values.get(provider, {})
        if not isinstance(entry, dict) or set(entry) - {'client_id', 'client_secret_file'}:
            raise ValueError('OAuth provider accepts only client_id and client_secret_file')
        prefix = 'HAKOPOD_' + provider.upper()
        client = environ.get(prefix + '_CLIENT_ID', entry.get('client_id', ''))
        path = environ.get(prefix + '_CLIENT_SECRET_FILE', entry.get('client_secret_file', ''))
        raw = environ.get(prefix + '_CLIENT_SECRET', '')
        if raw and path:
            raise ValueError('OAuth secret has both environment and file inputs; keep only one')
        if client or path or raw:
            credential(client, 'client ID')
            if not path and not raw:
                raise ValueError('OAuth provider needs a client secret or protected secret file')
            if path and (not isinstance(path, str) or not path.startswith('/') or any(ord(ch) < 32 for ch in path)):
                raise ValueError('OAuth secret file requires an absolute path')
            if raw:
                credential(raw, 'client secret')
            result[provider] = {'client_id': client, 'client_secret_file': path or '@environment'}
    return result


def private_write(path, body):
    path = Path(path)
    # Root-controlled destinations are created exclusively; never follow symlinks.
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, 'w') as stream:
        stream.write(body)


def prompt(config_path, directory):
    c = json.loads(Path(config_path).read_text())
    values = configured(c.get('oauth', {}))
    for provider in PROVIDERS:
        if provider in values:
            print(provider.capitalize() + ' OAuth is already configured; preserving its inputs.')
            continue
        answer = input('Configure ' + provider.capitalize() + ' login now? [y/N]: ').strip().lower()
        if answer not in ('y', 'yes'):
            continue
        print('Register this exact callback: ' + c['dashboard_origin'] + '/api/v1/auth/oauth/' + provider + '/callback')
        client = credential(input('Client ID: ').strip(), 'client ID')
        path = input('Protected client secret file (0400 or 0600), or leave empty for hidden input: ').strip()
        if path:
            secret_file(path)
        else:
            path = str(Path(directory) / (provider + '-secret'))
            private_write(path, credential(getpass.getpass('Client secret (hidden): '), 'client secret') + '\n')
        values[provider] = {'client_id': client, 'client_secret_file': path}
    # Environment-only inputs are resolved by host.config again, never serialized.
    c['oauth'] = {provider: entry for provider, entry in values.items() if entry['client_secret_file'] != '@environment'}
    Path(config_path).write_text(json.dumps(c) + '\n')


def preserve(values, root=Path('/etc/hakopod'), environ=None):
    environ = os.environ if environ is None else environ
    env_path = root / 'oauth.env'
    if env_path.exists() or env_path.is_symlink():
        if env_path.is_symlink() or not env_path.is_file() or env_path.stat().st_mode & 0o077:
            raise ValueError('Preserved OAuth environment must be a private regular file')
        # An operator may rotate OAuth credentials independently of an interrupted
        # install. Resume must not replace that configuration or its secret files.
        return
    lines = []
    for provider, entry in values.items():
        prefix = 'HAKOPOD_' + provider.upper()
        source = entry['client_secret_file']
        value = credential(environ.get(prefix + '_CLIENT_SECRET', ''), 'client secret') if source == '@environment' else secret_file(source)
        target = root / 'secrets' / ('oauth-' + provider + '-secret')
        if target.exists() or target.is_symlink():
            secret_file(target)
        else:
            private_write(target, value + '\n')
        lines.extend([prefix + '_CLIENT_ID=' + json.dumps(entry['client_id']), prefix + '_CLIENT_SECRET_FILE=' + json.dumps(str(target))])
    private_write(env_path, '\n'.join(lines) + '\n')


def canonical(values, root='/etc/hakopod'):
    return {provider: {'client_id': entry['client_id'], 'client_secret_file': root + '/secrets/oauth-' + provider + '-secret'} for provider, entry in values.items()}


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('--config', required=True)
    parser.add_argument('--directory', required=True)
    args = parser.parse_args()
    try:
        prompt(args.config, args.directory)
    except (ValueError, OSError, EOFError):
        # Never include exception details: secret input could occur in codec errors.
        raise SystemExit('OAuth setup failed; check the client ID and protected secret input')
