#!/usr/bin/env python3
"""Inspect our SDK packages without extracting archive-controlled paths."""
import io
import pathlib
import struct
import sys
import tarfile


def members(data):
    if data.startswith(b"!<arch>\n"):
        offset = 8
        while offset < len(data):
            header = data[offset:offset + 60]
            if len(header) != 60 or header[58:60] != b"`\n":
                raise ValueError("invalid ar header")
            size = int(header[48:58].strip())
            name = header[:16].decode().strip().rstrip("/")
            start = offset + 60
            if size < 0 or start + size > len(data):
                raise ValueError("invalid ar member size")
            yield name, data[start:start + size]
            offset = start + size + size % 2
    else:
        with tarfile.open(fileobj=io.BytesIO(data), mode="r:*") as archive:
            for item in archive:
                if item.isfile():
                    path = pathlib.PurePosixPath(item.name)
                    if path.is_absolute() or ".." in path.parts:
                        raise ValueError("unsafe archive path")
                    yield str(path), archive.extractfile(item).read()


def inspect(path, expected):
    outer = dict(members(path.read_bytes()))
    control_name = next(name for name in outer if name.startswith("control.tar"))
    data_name = next(name for name in outer if name.startswith("data.tar"))
    control = dict(members(outer[control_name]))["control"].decode()
    fields = dict(line.split(": ", 1) for line in control.splitlines() if ": " in line and not line[0].isspace())
    if fields.get("Package") != expected:
        raise ValueError("unexpected package name")
    content = dict(members(outer[data_name]))
    if expected == "mwan3-autobalancer":
        if fields.get("Architecture") != "aarch64_cortex-a53":
            raise ValueError("unexpected backend architecture")
        binary = content["usr/bin/mwan3-autobalancer"]
        if binary[:6] != b"\x7fELF\x02\x01" or struct.unpack_from("<H", binary, 18)[0] != 183:
            raise ValueError("backend is not little-endian AArch64 ELF64")
        offset = struct.unpack_from("<Q", binary, 32)[0]
        entry_size, count = struct.unpack_from("<HH", binary, 54)
        if entry_size < 4 or offset + entry_size * count > len(binary):
            raise ValueError("invalid ELF program headers")
        if any(struct.unpack_from("<I", binary, offset + index * entry_size)[0] == 3 for index in range(count)):
            raise ValueError("backend has a dynamic loader")
        required = {"etc/init.d/mwan3-autobalancer", "etc/config/mwan3_autobalancer", "usr/libexec/mwan3-autobalancer/watchdog", "usr/libexec/mwan3-autobalancer/restore", "usr/libexec/rpcd/mwan3.autobalancer"}
    else:
        required = {"www/luci-static/resources/view/network/mwan3-autobalancer.js", "usr/share/luci/menu.d/luci-app-mwan3-autobalancer.json", "usr/share/rpcd/acl.d/luci-app-mwan3-autobalancer.json"}
    if not required.issubset(content):
        raise ValueError("missing runtime files: " + str(sorted(required - content.keys())))
    print(f"Verified {path.name}: {fields['Architecture']}, {len(content)} files")


if __name__ == "__main__":
    root = pathlib.Path(sys.argv[1])
    for package in ("mwan3-autobalancer", "luci-app-mwan3-autobalancer"):
        paths = list(root.rglob(package + "_*.ipk"))
        if len(paths) != 1:
            raise ValueError(f"expected one {package} package, found {len(paths)}")
        inspect(paths[0], package)
