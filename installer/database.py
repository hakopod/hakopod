"""Read-only checks for an operator-provisioned Hakopod PostgreSQL database."""

from dataclasses import dataclass
import ipaddress
import json
import os
from pathlib import Path
import re
import selectors
import ssl
import stat
import subprocess
import time
from urllib.parse import parse_qsl, quote, unquote, urlencode, urlsplit, urlunsplit

MAX_URL_BYTES = 16384
MAX_OUTPUT_BYTES = 65536
SUPPORTED_MAJORS = range(14, 19)
MODES = ('managed', 'local', 'external')


def _fail(message):
    # Errors deliberately exclude input values and subprocess diagnostics.
    raise ValueError(message) from None


def _text(value):
    return isinstance(value, str) and not any(ord(c) < 32 or ord(c) == 127 for c in value)


def _path(value, label):
    if not _text(value) or len(value) > 4096 or not value.startswith('/'):
        _fail(label + ' must be an absolute path without control characters')
    return value


def _read_file(path, *, secret, maximum):
    _path(str(path), 'Database input file')
    fd = None
    try:
        fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
        info = os.fstat(fd)
        if not stat.S_ISREG(info.st_mode) or info.st_uid not in (0, os.geteuid()):
            _fail('Database input must be a regular file owned by root or the current user')
        mode = stat.S_IMODE(info.st_mode)
        if secret and mode not in (0o400, 0o600):
            _fail('Database URL file must have mode 0400 or 0600')
        if not secret and (mode & 0o133 or not mode & 0o400):
            _fail('Database CA file must be owner-readable and not executable or writable by others')
        if info.st_size > maximum:
            _fail('Database input file exceeds its size limit')
        with os.fdopen(fd, 'rb') as stream:
            fd = None
            value = stream.read(maximum + 1)
        if len(value) > maximum:
            _fail('Database input file exceeds its size limit')
        return value
    except OSError:
        _fail('Cannot read the protected database input file; check its path, owner and permissions')
    finally:
        if fd is not None:
            os.close(fd)


def read_url_file(path):
    """Return secret text. Callers must never print or include it in command arguments."""
    try:
        value = _read_file(path, secret=True, maximum=MAX_URL_BYTES).decode('utf-8')
    except UnicodeError:
        _fail('Database URL file must contain UTF-8 text')
    # A single final line ending is convenient for protected files made with printf.
    value = value[:-2] if value.endswith('\r\n') else value.removesuffix('\n')
    if not value or not _text(value) or any(c.isspace() for c in value):
        _fail('Database URL file must contain one URL without whitespace or control characters')
    return value


def _decode(value):
    try:
        decoded = unquote(value, encoding='utf-8', errors='strict')
    except UnicodeError:
        _fail('Database URL contains invalid encoding')
    if not _text(decoded):
        _fail('Database URL contains encoded control characters')
    return decoded


@dataclass(repr=False)
class Connection:
    mode: str
    host: str
    port: int
    database: str
    username: str
    password: str | None
    sslmode: str
    ca_file: str
    connect_timeout: int = 10

    def summary(self):
        host = '[' + self.host + ']' if ':' in self.host else self.host
        endpoint = host + ':' + str(self.port)
        return {'mode': self.mode, 'endpoint': endpoint + '/' + self.database}

    def __repr__(self):
        return 'Connection(' + repr(self.summary()) + ')'

    def uri(self, *, ca_path=None):
        """Return a secret URI for a protected runtime file, never for argv or logs."""
        ca = self.ca_file if ca_path is None else ca_path
        if ca:
            _path(ca, 'Database CA file')
        auth = quote(self.username, safe='')
        if self.password is not None:
            auth += ':' + quote(self.password, safe='')
        host = '[' + self.host + ']' if ':' in self.host else self.host
        query = {'sslmode': self.sslmode, 'connect_timeout': str(self.connect_timeout)}
        authority = auth + '@' + host + ':' + str(self.port)
        if ca:
            query['sslrootcert'] = ca
        return urlunsplit(('postgresql', authority, '/' + quote(self.database, safe=''), urlencode(query), ''))


def parse_url(url, mode, ca_file='', *, inspect_files=True):
    """Validate a deliberately small URI surface shared by libpq and pgx."""
    if mode not in ('local', 'external'):
        _fail('Existing PostgreSQL connections require local or external database mode')
    if not _text(url) or not url or len(url.encode('utf-8')) > MAX_URL_BYTES or any(c.isspace() for c in url):
        _fail('Database URL must be a bounded URL without whitespace or control characters')
    if '#' in url or re.search(r'%(?![0-9a-fA-F]{2})', url):
        _fail('Database URL must not contain a fragment or invalid percent encoding')
    try:
        parts = urlsplit(url)
        port = parts.port
        pairs = parse_qsl(parts.query, keep_blank_values=True, strict_parsing=True, encoding='utf-8', errors='strict')
        hostname = parts.hostname
    except (ValueError, UnicodeError):
        _fail('Database URL is malformed')
    if parts.scheme not in ('postgres', 'postgresql') or parts.netloc.count('@') > 1:
        _fail('Database URL must use postgres:// or postgresql:// with encoded credentials')
    query = {}
    allowed = {'sslmode', 'sslrootcert', 'connect_timeout', 'host', 'port', 'user'}
    for key, value in pairs:
        if not _text(key) or not _text(value) or key in query:
            _fail('Database URL contains duplicate parameters or control characters')
        if key not in allowed:
            _fail('Database URL contains an unsupported connection parameter')
        query[key] = value
    database = _decode(parts.path[1:]) if parts.path.startswith('/') else ''
    if not re.fullmatch(r'[A-Za-z_][A-Za-z0-9_-]{0,62}', database) or database.lower() in ('postgres', 'template0', 'template1'):
        _fail('Choose a dedicated named database, not postgres or a template database')
    username = _decode(parts.username) if parts.username is not None else query.get('user', '')
    if not username or len(username.encode('utf-8')) > 63 or (parts.username is not None and 'user' in query):
        _fail('Database URL requires one explicit PostgreSQL user')
    password = _decode(parts.password) if parts.password is not None else None
    if not password:
        _fail('Existing PostgreSQL requires an explicit password in its protected URL file')
    host = _decode(hostname) if hostname else query.get('host', '')
    if hostname and 'host' in query:
        _fail('Database URL must not override its host in query parameters')
    if 'port' in query:
        if hostname or port is not None or not re.fullmatch(r'[0-9]{1,5}', query['port']):
            _fail('Database URL has an invalid or conflicting port')
        port = int(query['port'])
    port = 5432 if port is None else port
    if not 1 <= port <= 65535:
        _fail('Database port must be between 1 and 65535')
    if host.startswith('/'):
        _fail('Local database mode requires loopback TCP; Unix sockets are not supported by the isolated API service')
    else:
        try:
            address = ipaddress.ip_address(host)
        except ValueError:
            address = None
            if not re.fullmatch(r'(?=.{1,253}\Z)[A-Za-z0-9](?:[A-Za-z0-9.-]*[A-Za-z0-9])?', host):
                _fail('Database URL requires a valid single TCP host')
            if any(not label or len(label) > 63 or label.startswith('-') or label.endswith('-') for label in host.split('.')):
                _fail('Database URL requires a valid single hostname')
        if address is not None and (address.is_unspecified or address.is_multicast or '%' in host):
            _fail('Database URL requires a unicast endpoint')
        if mode == 'local' and not ((address is not None and address.is_loopback) or host.lower() == 'localhost'):
            _fail('Local database mode only accepts loopback TCP addresses')
        if mode == 'external' and (host.lower() == 'localhost' or (address is not None and address.is_loopback)):
            _fail('Use local database mode for a loopback endpoint')
    sslmode = query.get('sslmode', 'prefer')
    if sslmode not in ('disable', 'allow', 'prefer', 'require', 'verify-ca', 'verify-full'):
        _fail('Database URL has an invalid sslmode')
    if mode == 'external' and sslmode != 'verify-full':
        _fail('External PostgreSQL requires sslmode=verify-full; TLS downgrade is not allowed')
    query_ca = query.get('sslrootcert', '')
    if query_ca and ca_file and query_ca != ca_file:
        _fail('Database CA file conflicts with the URL sslrootcert parameter')
    ca_file = ca_file or query_ca
    if ca_file:
        _path(ca_file, 'Database CA file')
        if sslmode not in ('verify-full', 'verify-ca'):
            _fail('A database CA file requires certificate verification')
        if inspect_files:
            _read_file(ca_file, secret=False, maximum=1024 * 1024)
    timeout = query.get('connect_timeout', '10')
    if not re.fullmatch(r'[0-9]{1,2}', timeout) or not 1 <= int(timeout) <= 30:
        _fail('Database connect_timeout must be between 1 and 30 seconds')
    return Connection(mode, host, port, database, username, password, sslmode, ca_file, int(timeout))


def validate_config(config, *, inspect_files=False):
    """Review paths without reading secrets by default; never connect to the database."""
    mode = config.get('database_mode', 'managed')
    source = config.get('database_url_file', '')
    ca_file = config.get('database_ca_file', '')
    if mode not in MODES:
        _fail('database_mode must be managed, local or external')
    if mode == 'managed':
        if source or ca_file:
            _fail('Managed database mode does not accept an existing URL or CA file')
        return {'mode': mode, 'endpoint': 'dedicated Hakopod PostgreSQL'}
    _path(source, 'database_url_file')
    if ca_file:
        _path(ca_file, 'database_ca_file')
    if inspect_files:
        return parse_url(read_url_file(source), mode, ca_file).summary()
    return {'mode': mode, 'endpoint': 'from protected database URL file; not yet checked'}


def runtime_url(config, *, ca_path=None):
    """Normalize a validated secret for a protected file after any CA copy."""
    validate_config(config)
    if config.get('database_mode', 'managed') == 'managed':
        _fail('Managed PostgreSQL credentials are generated by the installer')
    connection = parse_url(read_url_file(config['database_url_file']), config['database_mode'], config.get('database_ca_file', ''))
    return connection.uri(ca_path=ca_path)


# Only catalog reads run, inside a transaction that cannot change the database.
# All namespace-bound object kinds count, including functions and extensions.
PREFLIGHT_SQL = """
BEGIN READ ONLY;
SELECT pg_catalog.json_build_object(
  'version_num', pg_catalog.current_setting('server_version_num')::integer,
  'database', pg_catalog.current_database(), 'username', CURRENT_USER,
  'writable', NOT pg_catalog.pg_is_in_recovery()
    AND pg_catalog.current_setting('default_transaction_read_only') = 'off',
  'owner', d.datdba = (SELECT oid FROM pg_catalog.pg_roles WHERE rolname = CURRENT_USER),
  'connect', pg_catalog.has_database_privilege(CURRENT_USER, d.oid, 'CONNECT'),
  'schema_access', COALESCE((SELECT pg_catalog.has_schema_privilege(CURRENT_USER, oid, 'USAGE')
    AND pg_catalog.has_schema_privilege(CURRENT_USER, oid, 'CREATE')
    FROM pg_catalog.pg_namespace WHERE nspname = 'public'), false),
  'default_schema', pg_catalog.current_schema(),
  'schema_empty', NOT EXISTS (
    SELECT 1 FROM pg_catalog.pg_namespace n WHERE n.nspname = 'public' AND (
      EXISTS (SELECT 1 FROM pg_catalog.pg_class WHERE relnamespace = n.oid) OR
      EXISTS (SELECT 1 FROM pg_catalog.pg_proc WHERE pronamespace = n.oid) OR
      EXISTS (SELECT 1 FROM pg_catalog.pg_type WHERE typnamespace = n.oid) OR
      EXISTS (SELECT 1 FROM pg_catalog.pg_operator WHERE oprnamespace = n.oid) OR
      EXISTS (SELECT 1 FROM pg_catalog.pg_opclass WHERE opcnamespace = n.oid) OR
      EXISTS (SELECT 1 FROM pg_catalog.pg_opfamily WHERE opfnamespace = n.oid) OR
      EXISTS (SELECT 1 FROM pg_catalog.pg_collation WHERE collnamespace = n.oid) OR
      EXISTS (SELECT 1 FROM pg_catalog.pg_conversion WHERE connamespace = n.oid) OR
      EXISTS (SELECT 1 FROM pg_catalog.pg_ts_config WHERE cfgnamespace = n.oid) OR
      EXISTS (SELECT 1 FROM pg_catalog.pg_ts_dict WHERE dictnamespace = n.oid) OR
      EXISTS (SELECT 1 FROM pg_catalog.pg_ts_parser WHERE prsnamespace = n.oid) OR
      EXISTS (SELECT 1 FROM pg_catalog.pg_ts_template WHERE tmplnamespace = n.oid) OR
      EXISTS (SELECT 1 FROM pg_catalog.pg_extension WHERE extnamespace = n.oid)
    )) AND NOT EXISTS (
      SELECT 1 FROM pg_catalog.pg_namespace WHERE nspname NOT IN ('public', 'pg_catalog', 'information_schema', 'pg_toast')
      AND nspname !~ '^pg_(toast_)?temp_[0-9]+$'
    ) AND NOT EXISTS (
      SELECT 1 FROM pg_catalog.pg_extension WHERE extname <> 'plpgsql'
    ),
  'tls', EXISTS (SELECT 1 FROM pg_catalog.pg_stat_ssl
    WHERE pid = pg_catalog.pg_backend_pid() AND ssl)
) FROM pg_catalog.pg_database d WHERE d.datname = pg_catalog.current_database();
ROLLBACK;
"""


def _capture(argv, env, timeout):
    """Drain both pipes with fixed memory and wall-clock bounds; diagnostics stay private."""
    process = None
    try:
        process = subprocess.Popen(argv, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                                   stderr=subprocess.PIPE, env=env)
        deadline = time.monotonic() + timeout
        captured = {'stdout': bytearray(), 'stderr': bytearray()}
        total = 0
        with selectors.DefaultSelector() as selector:
            selector.register(process.stdout, selectors.EVENT_READ, 'stdout')
            selector.register(process.stderr, selectors.EVENT_READ, 'stderr')
            while selector.get_map():
                remaining = deadline - time.monotonic()
                if remaining <= 0:
                    _fail('PostgreSQL preflight timed out; no database changes were made')
                for key, _ in selector.select(remaining):
                    chunk = os.read(key.fd, min(8192, MAX_OUTPUT_BYTES - total + 1))
                    if not chunk:
                        selector.unregister(key.fileobj)
                        continue
                    total += len(chunk)
                    if total > MAX_OUTPUT_BYTES:
                        _fail('PostgreSQL preflight exceeded its output limit; diagnostics were withheld')
                    captured[key.data].extend(chunk)
        code = process.wait(timeout=max(0.01, deadline - time.monotonic()))
        return code, bytes(captured['stdout']), bytes(captured['stderr'])
    except subprocess.TimeoutExpired:
        _fail('PostgreSQL preflight timed out; no database changes were made')
    except OSError:
        _fail('Cannot run psql for PostgreSQL preflight; install the PostgreSQL client package')
    finally:
        if process is not None:
            if process.poll() is None:
                process.kill()
                process.wait()
            process.stdout.close()
            process.stderr.close()


def _base_env():
    # Do not inherit PG*, .pgpass, .psqlrc, SSL or service settings from the caller.
    return {'PATH': os.defpath, 'LC_ALL': 'C', 'HOME': '/nonexistent', 'PGPASSFILE': '/dev/null',
            'PGGSSENCMODE': 'disable'}


def _system_ca_file():
    paths = ssl.get_default_verify_paths()
    for path in (paths.openssl_cafile, '/etc/ssl/certs/ca-certificates.crt',
                 '/etc/pki/tls/certs/ca-bundle.crt', '/etc/ssl/cert.pem'):
        if path and Path(path).is_file():
            return path
    _fail('System CA bundle was not found; provide database_ca_file for certificate verification')


def command_environment(connection, timeout=20):
    """Explicit libpq fields for child processes; never put a secret URI in argv."""
    env = _base_env()
    env.update(PGHOST=connection.host, PGPORT=str(connection.port), PGDATABASE=connection.database,
               PGUSER=connection.username, PGSSLMODE=connection.sslmode,
               PGCONNECT_TIMEOUT=str(min(connection.connect_timeout, max(1, int(timeout)))))
    if connection.password is not None:
        env['PGPASSWORD'] = connection.password
    if connection.ca_file:
        env['PGSSLROOTCERT'] = connection.ca_file
    elif connection.sslmode in ('verify-full', 'verify-ca') and not connection.host.startswith('/'):
        env['PGSSLROOTCERT'] = _system_ca_file()
    return env


def preflight(config, *, resume=False, installed_url_path=None, timeout=20, psql='psql'):
    """Check an existing database without writes.

    The caller must verify its owned installation marker and credential digest
    before passing resume=True. A resume reads the installed credential file;
    it never depends on a temporary file used by the original interactive prompt.
    """
    validate_config(config)
    if config.get('database_mode', 'managed') == 'managed':
        return validate_config(config)
    if type(timeout) not in (int, float) or not 1 <= timeout <= 60:
        _fail('PostgreSQL preflight timeout must be between 1 and 60 seconds')
    if resume and not installed_url_path:
        _fail('Database resume requires an owned installation and its saved database URL file')
    source = installed_url_path if resume else config['database_url_file']
    # The saved URI already names the installed CA; the original source may be gone.
    ca = '' if resume else config.get('database_ca_file', '')
    connection = parse_url(read_url_file(source), config['database_mode'], ca)
    env = command_environment(connection, timeout)
    env.update(PGOPTIONS='-c statement_timeout=10000 -c lock_timeout=3000',
               PGAPPNAME='hakopod-installer-preflight')
    argv = [psql, '--no-psqlrc', '--no-password', '--quiet', '--tuples-only', '--no-align',
            '--set=ON_ERROR_STOP=1', '--command', PREFLIGHT_SQL]
    code, stdout, _ = _capture(argv, env, timeout)
    if code:
        _fail('PostgreSQL connection or read-only preflight failed; check credentials, TLS and database access. Server diagnostics were withheld because they may contain credentials')
    try:
        facts = json.loads(stdout)
    except (ValueError, UnicodeError):
        _fail('PostgreSQL preflight returned an invalid response; diagnostics were withheld')
    if not isinstance(facts, dict) or type(facts.get('version_num')) is not int:
        _fail('PostgreSQL preflight returned incomplete database facts')
    major = facts['version_num'] // 10000
    if major not in SUPPORTED_MAJORS:
        _fail('Existing PostgreSQL must run a supported major version from 14 through 18')
    if facts.get('database') != connection.database or facts.get('username') != connection.username:
        _fail('PostgreSQL session does not match the requested database and user')
    for field, message in (
        ('writable', 'PostgreSQL must be a writable primary, not a replica or read-only database'),
        ('owner', 'The PostgreSQL login must own the dedicated target database'),
        ('connect', 'The PostgreSQL login requires CONNECT access to the target database'),
        ('schema_access', 'The PostgreSQL login requires USAGE and CREATE privileges on the public schema'),
    ):
        if facts.get(field) is not True:
            _fail(message)
    if facts.get('default_schema') != 'public':
        _fail('The PostgreSQL login must use public as its default schema')
    if type(facts.get('schema_empty')) is not bool:
        _fail('PostgreSQL preflight did not report the public schema contents')
    if not facts['schema_empty'] and not resume:
        _fail('A fresh installation requires an empty public schema and no other user schemas or extensions; existing contents need an explicit resume of the owned installation')
    if connection.mode == 'external' and facts.get('tls') is not True:
        _fail('External PostgreSQL must negotiate verified TLS')
    result = dict(connection.summary(), server_major=major,
                  server_version=str(major) + '.' + str(facts['version_num'] % 10000))
    # Informational only: runtime connectivity does not require equal client major.
    try:
        code, output, _ = _capture([psql, '--version'], _base_env(), min(3, timeout))
        match = re.search(rb'PostgreSQL\) ([0-9]{1,2})(?:\.|\s)', output) if code == 0 else None
    except ValueError:
        match = None
    result['client_major'] = int(match[1]) if match else None
    return result
