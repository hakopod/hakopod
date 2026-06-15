#!/usr/bin/env python3
"""Bundle/restore the public UI submodule without private-repository access."""
import argparse
import gzip
import hashlib
import io
from pathlib import Path, PurePosixPath
import shutil
import tarfile
import tempfile

ROOT = Path(__file__).resolve().parents[1]
LIMIT = 10 * 1024 * 1024
EXCLUDED = {".git", "node_modules", "dist", ".local", "__pycache__"}


def source_files(source):
    files = []
    for path in sorted(source.rglob("*")):
        relative = path.relative_to(source)
        if any(part in EXCLUDED for part in relative.parts):
            continue
        if path.is_symlink():
            raise ValueError(f"UI source cannot contain a symlink: {relative}")
        if path.is_file():
            files.append(path)
    if not files or len(files) > 512 or sum(p.stat().st_size for p in files) > LIMIT:
        raise ValueError("UI source exceeds 512 files/10 MiB or is empty")
    return files


def pack(source, destination):
    files = source_files(source)
    destination.parent.mkdir(parents=True, exist_ok=True)
    with destination.open("wb") as stream:
        with gzip.GzipFile(fileobj=stream, mode="wb", filename="", mtime=0) as compressed:
            with tarfile.open(fileobj=compressed, mode="w") as bundle:
                for path in files:
                    data = path.read_bytes()
                    info = tarfile.TarInfo("ui/" + path.relative_to(source).as_posix())
                    info.size = len(data)
                    info.mode = 0o644
                    info.mtime = 0
                    bundle.addfile(info, io.BytesIO(data))
    checksum = hashlib.sha256(destination.read_bytes()).hexdigest()
    destination.with_suffix(destination.suffix + ".sha256").write_text(
        f"{checksum}  {destination.name}\n"
    )
    return len(files)


def restore(archive, destination):
    if destination.is_symlink():
        raise ValueError("Refusing symlink UI destination")
    if destination.exists() and any(destination.iterdir()):
        if (destination / "package.json").is_file():
            return False
        raise ValueError("Refusing to replace a nonempty UI directory")
    if archive.stat().st_size > LIMIT:
        raise ValueError("UI archive exceeds 10 MiB")
    expected = archive.with_suffix(archive.suffix + ".sha256").read_text().split()[0]
    if hashlib.sha256(archive.read_bytes()).hexdigest() != expected:
        raise ValueError("UI source checksum mismatch")
    destination.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix=".ui-source-", dir=destination.parent) as temporary:
        staging = Path(temporary) / "ui"
        staging.mkdir()
        with tarfile.open(archive, mode="r:gz") as bundle:
            total, seen = 0, set()
            for member in bundle:
                name = PurePosixPath(member.name)
                total += member.size
                if (not member.isfile() or name.is_absolute() or len(name.parts) < 2
                        or name.parts[0] != "ui" or ".." in name.parts
                        or member.name != name.as_posix() or member.name in seen
                        or any(part in EXCLUDED for part in name.parts)
                        or total > LIMIT or len(seen) >= 512):
                    raise ValueError("Unsafe or oversized UI source archive")
                seen.add(member.name)
                target = staging.joinpath(*name.parts[1:])
                target.parent.mkdir(parents=True, exist_ok=True)
                content = bundle.extractfile(member)
                if content is None:
                    raise ValueError("Unreadable UI source archive member")
                with content, target.open("xb") as output:
                    shutil.copyfileobj(content, output, length=65536)
                target.chmod(0o644)
        if not (staging / "package.json").is_file():
            raise ValueError("UI source bundle has no package.json")
        if destination.exists():
            destination.rmdir()
        staging.rename(destination)
    return True


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=["pack", "restore"])
    args = parser.parse_args()
    archive, source = ROOT / "third_party/ui/ui-source.tar.gz", ROOT / "packages/ui"
    if args.command == "pack":
        print(f"Bundled {pack(source, archive)} public UI source files.")
    else:
        print("Restored public UI sources." if restore(archive, source)
              else "UI sources already present; preserved the working copy.")


if __name__ == "__main__":
    main()
