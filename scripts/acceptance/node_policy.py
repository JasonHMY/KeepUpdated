#!/usr/bin/env python3
"""Opt-in Node command semantics check using supplied managers and synthetic packages.

No managers are downloaded. All writes are sandboxed to a new output directory.
This verifies native argv semantics, not runtime updates or a public registry.
"""
import argparse
import base64
import hashlib
import http.server
import io
import json
import os
from pathlib import Path
import subprocess
import tarfile
import threading


def tree_digest(root):
    digest = hashlib.sha256()
    for path in sorted(root.rglob("*")):
        digest.update(str(path.relative_to(root)).encode())
        if path.is_symlink():
            digest.update(os.readlink(path).encode())
        elif path.is_file():
            digest.update(path.read_bytes())
    return digest.hexdigest()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("npm", "pnpm", "bun", "node", "out"):
        parser.add_argument("--" + name, type=Path, required=True)
    args = parser.parse_args()
    for name in ("npm", "pnpm", "bun", "node"):
        path = getattr(args, name)
        if not path.is_absolute() or not path.is_file():
            parser.error("manager and Node paths must be absolute existing files")
    sandbox = Path("/usr/bin/sandbox-exec")
    if not sandbox.is_file():
        parser.error("this isolated acceptance check requires macOS sandbox-exec")
    base = args.out.resolve()
    base.mkdir(parents=True, exist_ok=False)
    profile = '(version 1)(allow default)(deny network*)(allow network-outbound (remote ip "localhost:*"))(deny file-write*)' + \
        '(allow file-write* (subpath ' + json.dumps(str(base)) + ') (literal "/dev/null"))'
    blobs = {}
    for version in ("1.0.0", "1.1.0", "2.0.0"):
        buffer = io.BytesIO()
        manifest = json.dumps({"name": "ku-node-fixture", "version": version,
                               "bin": {"ku-node-fixture": "cli.js"}, "engines": {"node": ">=999.0.0" if version == "2.0.0" else ">=20.0.0"}}).encode()
        with tarfile.open(fileobj=buffer, mode="w:gz") as archive:
            for name, data, mode in (("package/package.json", manifest, 0o644),
                                     ("package/cli.js", b"#!/usr/bin/env node\n", 0o755)):
                entry = tarfile.TarInfo(name)
                entry.size, entry.mode = len(data), mode
                archive.addfile(entry, io.BytesIO(data))
        blobs[version] = buffer.getvalue()

    class Registry(http.server.BaseHTTPRequestHandler):
        def do_GET(self):
            if self.path.endswith(".tgz"):
                version = self.path.rsplit("/", 1)[-1].removeprefix("ku-node-fixture-").removesuffix(".tgz")
                data = blobs[version]
            else:
                versions = {version: {
                    "name": "ku-node-fixture", "version": version,
                    "bin": {"ku-node-fixture": "cli.js"}, "engines": {"node": ">=999.0.0" if version == "2.0.0" else ">=20.0.0"},
                    "dist": {"tarball": url + "/ku-node-fixture/-/ku-node-fixture-" + version + ".tgz",
                             "shasum": hashlib.sha1(blob).hexdigest(),
                             "integrity": "sha512-" + base64.b64encode(hashlib.sha512(blob).digest()).decode()}
                } for version, blob in blobs.items()}
                data = json.dumps({"name": "ku-node-fixture", "dist-tags": {"latest": "2.0.0"},
                                   "versions": versions}).encode()
            self.send_response(200)
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)

        def log_message(self, *_):
            pass

    server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Registry)
    url = "http://127.0.0.1:" + str(server.server_port)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    results = []
    try:
        for owner, layout, expected in (("npm", "global-lib", "11.19.1"),
                                        ("pnpm", ".pnpm", "10.18.3"),
                                        ("pnpm", "node_modules/.pnpm", "10.18.3"),
                                        ("bun", "global-node-modules", "1.4.3")):
            root = base / (owner + "-" + layout.replace("/", "-"))
            home, bins = root / "home", root / "bin"
            home.mkdir(parents=True); bins.mkdir()
            executable = str(getattr(args, owner))
            env = {"PATH": os.pathsep.join([str(bins), str(args.node.parent), "/usr/bin", "/bin"]),
                   "HOME": str(home), "TMPDIR": str(root), "LC_ALL": "C",
                   "npm_config_cache": str(root / "cache"), "npm_config_userconfig": str(home / ".npmrc"),
                   "npm_config_globalconfig": str(root / "npmrc"), "npm_config_update_notifier": "false",
                   "COREPACK_ENABLE_NETWORK": "0", "PNPM_HOME": str(bins),
                   "XDG_CACHE_HOME": str(root / "xdg-cache"), "XDG_DATA_HOME": str(root / "xdg-data"),
                   "BUN_INSTALL": str(root / "bun"),
                   "BUN_INSTALL_CACHE_DIR": str(root / "bun-cache"),
                   "BUN_INSTALL_GLOBAL_DIR": str(root / "global"), "BUN_INSTALL_BIN": str(bins)}
            (home / ".npmrc").write_text("registry=" + url + "\nengine-strict=true\n")
            (root / "npmrc").write_text("")
            if owner == "pnpm":
                native_rc = home / "Library/Preferences/pnpm/rc"
                native_rc.parent.mkdir(parents=True)
                original_rc = "store-dir=" + str(root / "native-store") + "\nresolution-mode=highest\n"
                native_rc.write_text(original_rc)

            def run(argv, expect_engine_failure=False):
                cwd = scope if owner == "bun" and argv[0] in ("info", "pm") else home
                result = subprocess.run([str(sandbox), "-p", profile, executable] + argv,
                                        cwd=cwd, env=env, stdin=subprocess.DEVNULL,
                                        capture_output=True, text=True, timeout=45)
                if expect_engine_failure:
                    assert result.returncode != 0 and "ERR_PNPM_UNSUPPORTED_ENGINE" in result.stdout + result.stderr
                    return result.stdout.strip()
                if result.returncode:
                    raise RuntimeError(owner + " command failed: " + result.stdout + result.stderr)
                return result.stdout.strip()

            assert run(["--version"]) == expected
            if owner == "npm":
                scope = root / "prefix"
                flags = ["--global", "--prefix", str(scope), "--ignore-scripts", "--no-audit", "--no-fund"]
                manifest, package, verb = scope / "lib/package.json", scope / "lib/node_modules/ku-node-fixture/package.json", "install"
            else:
                scope = root / ("global/5" if owner == "pnpm" else "global")
                flags = ["--global", "--ignore-scripts"]
                if owner == "pnpm":
                    flags += ["--config.global-dir=" + str(root / "global"), "--ignore-pnpmfile"]
                manifest, package, verb = scope / "package.json", scope / "node_modules/ku-node-fixture/package.json", "add"
            run([verb] + flags + ["--", "ku-node-fixture@1.0.0"])
            if owner != "npm":
                manifest.write_text(json.dumps({"dependencies": {"ku-node-fixture": "^1.0.0"}}))
            if owner == "pnpm" and layout == "node_modules/.pnpm":
                # Model an existing legacy store before invoking the update.
                store, legacy = scope / ".pnpm", scope / "node_modules/.pnpm"
                legacy.mkdir(exist_ok=True)
                for child in store.iterdir():
                    assert not (legacy / child.name).exists()
                    child.rename(legacy / child.name)
                store.rmdir()
                link = scope / "node_modules/ku-node-fixture"
                link.unlink(); link.symlink_to(scope / "node_modules/.pnpm/ku-node-fixture@1.0.0/node_modules/ku-node-fixture")
                modules = scope / "node_modules/.modules.yaml"
                modules.write_text(modules.read_text().replace("virtualStoreDir: ../.pnpm", "virtualStoreDir: .pnpm"))
            before = manifest.read_bytes() if manifest.exists() else None
            before_tree = tree_digest(scope)
            command = [verb] + flags + ["--no-save", "--", "ku-node-fixture@1.1.0"]
            query_scope = ["--global", "--prefix", str(scope)]
            if owner == "pnpm":
                query_scope = ["--global=false", "--dir", str(scope), "--ignore-workspace",
                               "--config.ignore-pnpmfile=true", "--config.manage-package-manager-versions=false", "--config.use-node-version=null",
                               "--config.bin=" + str(bins), "--config.virtual-store-dir=" + layout,
                               "--config.node-linker=isolated"]
                command = ["update"] + query_scope + ["--no-save", "--depth=0", "--ignore-scripts", "--", "ku-node-fixture@1.1.0"]
                # Exercise the native rc, not an XDG override or .npmrc alias.
                assert run(["config", "get", "store-dir"] + query_scope) == str(root / "native-store")
                assert run(["config", "get", "resolution-mode"] + query_scope) == "highest"
                guarded_rc = original_rc + "use-node-version=99.0.0\n"
                native_rc.write_text(guarded_rc)
                assert run(["config", "get", "use-node-version"] + query_scope) == "null"
                assert not (bins / "nodejs").exists()
                assert run(["config", "get", "global-bin-dir"] + query_scope) == "undefined"
                global_scope = ["--global", "--config.global-dir=" + str(root / "global"),
                                "--config.manage-package-manager-versions=false", "--config.ignore-pnpmfile=true", "--config.use-node-version=null"]
                assert run(["root"] + global_scope) == str(scope / "node_modules")
                direct = json.loads(run(["list", "--depth=0", "--json"] + global_scope))
                assert direct[0]["dependencies"]["ku-node-fixture"]["version"] == "1.0.0"
            if owner == "bun":
                query = ["info", "ku-node-fixture@^1.0.0", "--json"]
                assert run(["pm", "bin", "--global"]) == str(bins)
            else:
                query = ["view", "ku-node-fixture@^1.0.0", "name", "version", "scripts",
                         "dependencies", "optionalDependencies", "--json"] + query_scope
            native = json.loads(run(query))
            candidates = native if isinstance(native, list) else [native]
            assert max(row["version"] for row in candidates) == "1.1.0"
            assert (manifest.read_bytes() if manifest.exists() else None) == before
            assert tree_digest(scope) == before_tree, "query changed the installation"
            run(command)
            assert json.loads(package.read_text())["version"] == "1.1.0"
            assert (manifest.read_bytes() if manifest.exists() else None) == before
            entrypoint = (scope / "bin" if owner == "npm" else bins) / "ku-node-fixture"
            if owner == "pnpm":
                run(command[:-1] + ["ku-node-fixture@2.0.0"], expect_engine_failure=True)
                assert json.loads(package.read_text())["version"] == "1.1.0"
                assert manifest.read_bytes() == before
                assert native_rc.read_text() == guarded_rc
                assert not (bins / "nodejs").exists()
                assert "storeDir: " + str(root / "native-store") in (scope / "node_modules/.modules.yaml").read_text()
                assert "ku-node-fixture@1.1.0" in entrypoint.read_text()
                assert layout in str(package.resolve().relative_to(scope))
            else:
                assert entrypoint.resolve() == package.parent / "cli.js"
            result = {"owner": owner, "version": expected, "layout": layout,
                      "selected": "1.1.0", "installed": "1.1.0", "selector_preserved": True,
                      "entrypoint_verified": True, "query_preserved_installation": True, "sandboxed": True}
            if owner == "pnpm":
                result.update(native_rc_verified=True, runtime_provisioning_disabled=True, engine_rejection_verified=True)
            results.append(result)
            print(json.dumps(result), flush=True)
        (base / "results.json").write_text(json.dumps(results, indent=2) + "\n")
    finally:
        server.shutdown(); server.server_close()


if __name__ == "__main__":
    main()
