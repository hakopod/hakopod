"""Offline, unprivileged filesystem migration. Only the destination is writable."""
import hashlib
import json
import os
from pathlib import Path
import shutil
import stat
import sys

MAX_FILES = 50_000
MAX_DEPTH = 128
HEADROOM = 64 * 1024 * 1024


def inventory(root):
    items = []
    total = 0
    path_bytes = 0
    groups = {os.getgid(), *os.getgroups()}

    def visit(folder, depth):
        nonlocal total, path_bytes
        if depth > MAX_DEPTH:
            raise ValueError("directory depth exceeds migration limit")
        with os.scandir(folder) as entries:
            names = []
            for entry in entries:
                if len(names) + len(items) >= MAX_FILES:
                    raise ValueError("file count exceeds migration limit")
                names.append(entry.name)
            names.sort()
        for name in names:
            path = folder / name
            relative = path.relative_to(root)
            path_bytes += len(os.fsencode(str(relative)))
            if path_bytes > 8 * 1024 * 1024:
                raise ValueError("path inventory exceeds migration memory limit")
            # ext filesystems own this recovery directory; it is not application data.
            info = path.lstat()
            if str(relative) == "lost+found" and info.st_uid == 0 and stat.S_ISDIR(info.st_mode):
                continue
            if info.st_uid != os.getuid() or info.st_gid not in groups:
                raise ValueError("files require ownership unavailable to the service user")
            if not (stat.S_ISREG(info.st_mode) or stat.S_ISDIR(info.st_mode) or stat.S_ISLNK(info.st_mode)):
                raise ValueError("special files require an operator-managed migration")
            if info.st_mode & stat.S_ISUID or info.st_mode & stat.S_ISGID and not stat.S_ISDIR(info.st_mode):
                raise ValueError("set-id files require an operator-managed migration")
            if len(items) >= MAX_FILES:
                raise ValueError("file count exceeds migration limit")
            items.append((relative, info))
            if stat.S_ISREG(info.st_mode):
                # Conservative: require room for apparent file sizes, including sparse files.
                total += info.st_size
            if stat.S_ISDIR(info.st_mode):
                visit(path, depth + 1)
    visit(root, 0)
    return items, total


def metadata(path, info, source):
    os.chown(path, -1, info.st_gid, follow_symlinks=False)
    if not stat.S_ISLNK(info.st_mode):
        os.chmod(path, stat.S_IMODE(info.st_mode))
    os.utime(path, ns=(info.st_atime_ns, info.st_mtime_ns), follow_symlinks=False)
    for name in os.listxattr(source, follow_symlinks=False):
        value = os.getxattr(source, name, follow_symlinks=False)
        os.setxattr(path, name, value, follow_symlinks=False)


def fingerprint(root, items):
    digest = hashlib.sha256()
    links = {}
    for relative, original in items:
        path = root / relative
        info = path.lstat()
        entry = [str(relative), stat.S_IMODE(info.st_mode), stat.S_IFMT(info.st_mode),
                 info.st_uid, info.st_gid, info.st_mtime_ns]
        if stat.S_ISLNK(info.st_mode):
            entry.append(os.readlink(path))
        elif stat.S_ISREG(info.st_mode):
            content = hashlib.sha256()
            with path.open('rb') as stream:
                while data := stream.read(1024 * 1024):
                    content.update(data)
            key = (info.st_dev, info.st_ino)
            first = links.setdefault(key, str(relative))
            entry.extend([info.st_size, content.hexdigest(), first])
        attrs = {name: os.getxattr(path, name, follow_symlinks=False).hex()
                 for name in os.listxattr(path, follow_symlinks=False)}
        entry.append(attrs)
        digest.update(json.dumps(entry, sort_keys=True, ensure_ascii=True).encode() + b'\n')
    return digest.hexdigest()


def migrate(source, target, target_bytes):
    if source.resolve() == target.resolve() or source.is_symlink() or target.is_symlink():
        raise ValueError("source and destination must be distinct mounted directories")
    items, total = inventory(source)
    fs = os.statvfs(target)
    available = min(target_bytes, fs.f_bavail * fs.f_frsize)
    if total + max(HEADROOM, (total + 9) // 10) > available:
        raise ValueError("data and required free-space headroom do not fit the target volume")
    # The controller only mounts its new, operation-owned staging PVC here.
    for path in target.iterdir():
        if path.name == 'lost+found':
            continue
        if path.is_dir() and not path.is_symlink():
            shutil.rmtree(path)
        else:
            path.unlink()
    # A non-root helper cannot chown the filesystem's mount root. Services mount
    # this owned subdirectory; application-file ownership is preserved below it.
    target = target / 'data'
    target.mkdir(mode=0o700)
    links = {}
    for relative, info in items:
        src, dst = source / relative, target / relative
        if stat.S_ISDIR(info.st_mode):
            dst.mkdir(mode=0o700)
        elif stat.S_ISLNK(info.st_mode):
            dst.symlink_to(os.readlink(src))
        else:
            key = (info.st_dev, info.st_ino)
            if key in links:
                os.link(links[key], dst)
            else:
                with src.open('rb') as reader, dst.open('xb') as writer:
                    shutil.copyfileobj(reader, writer, 1024 * 1024)
                    writer.flush()
                    os.fsync(writer.fileno())
                links[key] = dst
    for relative, info in reversed(items):
        metadata(target / relative, info, source / relative)
    # Re-inventory both trees; do not mistake a matching subset for a full copy.
    current, _ = inventory(source)
    copied, _ = inventory(target)
    expected = fingerprint(source, current)
    if [str(p) for p, _ in current] != [str(p) for p, _ in copied] or expected != fingerprint(target, copied):
        raise ValueError("filesystem verification failed; original data is preserved")
    root_info = source.stat()
    root_group = root_info.st_gid
    if root_group not in {os.getgid(), *os.getgroups()}:
        # A local-path provisioner owns the mount root as root:root. Its
        # directory is infrastructure metadata; the new service-owned data
        # directory uses our existing group. Application files remain subject
        # to the strict ownership checks and full verification above.
        if root_info.st_uid != 0:
            raise ValueError("mount root requires an unavailable filesystem group")
        root_group = os.getgid()
    os.chown(target, -1, root_group)
    os.chmod(target, stat.S_IMODE(root_info.st_mode))
    os.sync()
    return {"verified": True, "files": len(current), "bytes": total, "sha256": expected}


if __name__ == '__main__':
    try:
        result = migrate(Path('/source'), Path('/target'), int(sys.argv[1]))
        result['operation'] = sys.argv[2]
        Path('/dev/termination-log').write_text(json.dumps(result))
    except Exception as error:
        # Do not put customer paths, file contents or filesystem exceptions in logs.
        message = str(error) if isinstance(error, ValueError) else 'filesystem copy failed; original data is preserved'
        Path('/dev/termination-log').write_text(json.dumps({'verified': False, 'error': message}))
        sys.exit(1)
