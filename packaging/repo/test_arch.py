#!/usr/bin/env python3
"""Exercise installer routing, release provenance and the package build without touching system config or keys."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
SOURCE_VERSION = 'package main\n\nconst version = "0.4.2"\n'


def executable(path, content):
    path.write_text('#!/bin/bash\nset -eu\n' + content)
    path.chmod(0o755)


def commit_fixture(root, *extra):
    for args in (['init', '-q'], ['add', '.'], ['-c', 'user.name=Test', '-c', 'user.email=test@example.com', 'commit', '-qm', 'fixture'], *extra):
        subprocess.run(['git', *args], cwd=root, check=True)
    return subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=root, text=True).strip()


class InstallerTests(unittest.TestCase):
    def run_installer(self, arch, earlier_install=False):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            binary = root / 'bin'
            binary.mkdir()
            etc = root / 'etc'
            (etc / 'pacman.d').mkdir(parents=True)
            (etc / 'pacman.conf').write_text('[other]\n')
            home = root / 'home'
            hook = home / '.config/omarchy/hooks/pre-refresh-pacman.d'
            hook.mkdir(parents=True)
            if earlier_install:
                (home / '.local/bin').mkdir(parents=True)
                (home / '.local/bin/wacli').write_text('go install build')
                (home / '.config/systemd/user').mkdir(parents=True)
                (home / '.config/systemd/user/wacli-sync.service').write_text('[Service]\n')
            executable(binary / 'sudo', 'exec "$@"\n')
            executable(binary / 'chown', 'exit 0\n')
            executable(binary / 'install', 'while [[ $# -gt 0 ]]; do case "$1" in -d) shift;; -o|-g) shift 2;; *) mkdir -p "$1"; shift;; esac; done\n')
            executable(binary / 'uname', f'echo {arch}\n')
            executable(binary / 'curl', 'echo "curl $*" >> "$CALL_LOG"; while [[ $1 != -o ]]; do shift; done; echo public-key > "$2"\n')
            executable(binary / 'gpg', "echo 'fpr:::::::::52130299581DAD685226900CA89CA1A6A1E74251:'\n")
            executable(binary / 'getent', f"echo 'root:x:0:0::{home}:/bin/bash'\n")
            for command in ('pacman-key', 'pacman', 'omarchy-pkg-add', 'systemctl'):
                executable(binary / command, 'echo "$0 $*" >> "$CALL_LOG"\n')
            source = (ROOT / 'packaging/repo/install.sh').read_text().replace('/etc/', f'{etc}/')
            script = root / 'install.sh'
            script.write_text(source)
            log = root / 'calls'
            env = dict(os.environ, PATH=f'{binary}:{os.environ["PATH"]}', CALL_LOG=str(log), USER='root', SUDO_USER='root')
            env.pop('WACLI_RELEASES_URL', None)
            result = subprocess.run(['bash', str(script)], env=env, capture_output=True, text=True)
            if arch not in ('x86_64', 'aarch64'):
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse(log.exists())
                self.assertEqual(list((etc / 'pacman.d').iterdir()), [])
                return result
            self.assertEqual(result.returncode, 0, result.stderr)
            repo = 'wacli-aarch64' if arch == 'aarch64' else 'wacli'
            other = 'wacli' if arch == 'aarch64' else 'wacli-aarch64'
            conf = (etc / f'pacman.d/{repo}.conf').read_text()
            self.assertIn(f'[{repo}]\n', conf)
            self.assertIn('Server = https://github.com/ferdousbhai/wacli/releases/latest/download\n', conf)
            self.assertIn('SigLevel = Required DatabaseRequired', conf)
            self.assertFalse((etc / f'pacman.d/{other}.conf').exists())
            self.assertEqual((etc / 'pacman.conf').read_text().count(f'Include = {etc}/pacman.d/{repo}.conf'), 1)
            calls = log.read_text()
            self.assertIn(f'releases/latest/download/{repo}-signing-key.asc', calls)
            self.assertIn('pacman-key --lsign-key 52130299581DAD685226900CA89CA1A6A1E74251', calls)
            self.assertIn('omarchy-pkg-add wacli', calls)
            # The service is the user's to enable, after pairing.
            self.assertNotIn('systemctl', calls)
            self.assertTrue((hook / repo).exists())
            self.assertIn('systemctl --user enable --now wacli-sync', result.stdout)
            self.assertIn('wacli skill install', result.stdout)
            return result

    def test_x86(self):
        result = self.run_installer('x86_64')
        self.assertNotIn('Note:', result.stdout)

    def test_arm(self): self.run_installer('aarch64')

    def test_unsupported(self): self.run_installer('riscv64')

    def test_points_out_earlier_install_without_removing_it(self):
        result = self.run_installer('aarch64', earlier_install=True)
        self.assertIn('rm ~/.local/bin/wacli', result.stdout)
        self.assertIn('rm ~/.config/systemd/user/wacli-sync.service', result.stdout)


class VerificationTests(unittest.TestCase):
    def test_exact_release_and_native_architecture(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'bin').mkdir()
            (root / 'bin/verify-release').write_text((ROOT / 'bin/verify-release').read_text())
            executable(root / 'uname', 'echo aarch64\n')
            executable(root / 'bin/arch-run', 'printf "%s\\0" "$@" > "$CALL_LOG"\n')
            log = root / 'args'
            env = dict(os.environ, PATH=f'{root}:{os.environ["PATH"]}', CALL_LOG=str(log))
            subprocess.run(['bash', str(root / 'bin/verify-release'), '0.4.2', 'aarch64'], env=env, check=True)
            args = log.read_bytes().decode().split('\0')[:-1]
            self.assertIn('WACLI_RELEASES_URL=https://github.com/ferdousbhai/wacli/releases/download/v0.4.2', args)
            self.assertIn('EXPECTED_ARCH=aarch64', args)
            self.assertNotIn('releases/latest', '\n'.join(args))
            subprocess.run(['bash', '-n'], input=args[-1], text=True, check=True)
            log.unlink()
            result = subprocess.run(['bash', str(root / 'bin/verify-release'), '0.4.2', 'x86_64'], env=env, capture_output=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertFalse(log.exists())


class PackageBuildTests(unittest.TestCase):
    def test_committed_source_cache_and_provenance(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'bin').mkdir()
            (root / 'cmd/wacli').mkdir(parents=True)
            (root / 'packaging/aur/wacli').mkdir(parents=True)
            (root / 'bin/build-package').write_text((ROOT / 'bin/build-package').read_text())
            (root / 'cmd/wacli/main.go').write_text(SOURCE_VERSION)
            recipe = root / 'packaging/aur/wacli/PKGBUILD'
            recipe.write_text('source=(original)\nsha256sums=(SKIP)\n')
            commit = commit_fixture(root)
            recipe.write_text('uncommitted recipe must not be built\n')
            mocks = root / 'mocks'
            mocks.mkdir()
            executable(mocks / 'uname', 'echo aarch64\n')
            executable(mocks / 'go', 'exit 0\n')
            executable(mocks / 'makepkg', '''
grep -qx 'source=(source.tar.gz)' PKGBUILD
sum=$(sha256sum source.tar.gz | cut -d" " -f1)
grep -qx "sha256sums=('$sum')" PKGBUILD
tar -tzf source.tar.gz | grep -qx wacli-0.4.2/cmd/wacli/main.go
mkdir -p "$GOMODCACHE/cache" "$GOCACHE" pkg/usr/bin
printf '#!/bin/sh\\necho wacli version 0.4.2\\n' > pkg/usr/bin/wacli
chmod +x pkg/usr/bin/wacli
printf 'pkgname = wacli\\npkgver = 0.4.2-1\\narch = aarch64\\n' > pkg/.PKGINFO
tar -C pkg -czf "$PKGDEST/wacli-0.4.2-1-aarch64.pkg.tar.zst" .PKGINFO usr
''')
            executable(mocks / 'bsdtar', 'exec tar "$@"\n')
            modcache = root / 'module cache'
            output = root / 'output'
            env = dict(os.environ, PATH=f'{mocks}:{os.environ["PATH"]}', GOMODCACHE=str(modcache))
            env.pop('GOCACHE', None)
            subprocess.run(['bash', str(root / 'bin/build-package'), 'aarch64', str(output)], env=env, check=True)
            self.assertEqual((output / 'source-commit.txt').read_text().strip(), commit)
            self.assertTrue((modcache / 'cache').is_dir())
            self.assertTrue((root / 'target/package-cache/aarch64/gobuild').is_dir())
            result = subprocess.run(['bash', str(root / 'bin/build-package'), 'x86_64'], env=env, capture_output=True)
            self.assertNotEqual(result.returncode, 0)


class ReleaseTests(unittest.TestCase):
    def run_release(self, failure=''):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'bin').mkdir()
            (root / 'cmd/wacli').mkdir(parents=True)
            (root / 'packaging/repo').mkdir(parents=True)
            (root / 'packaging/aur/wacli').mkdir(parents=True)
            (root / 'bin/release').write_text((ROOT / 'bin/release').read_text())
            (root / 'cmd/wacli/main.go').write_text(SOURCE_VERSION)
            pkgver = '0.4.1' if failure == 'pkgver' else '0.4.2'
            (root / 'packaging/aur/wacli/PKGBUILD').write_text(f'pkgname=wacli\npkgver={pkgver}\n')
            (root / 'packaging/repo/install.sh').write_text('installer fixture\n')
            commit = commit_fixture(root, ['tag', 'v0.4.2'])
            binary = root / 'mocks'
            binary.mkdir()
            log = root / 'calls'
            log.touch()
            executable(binary / 'gh', '''
echo "gh $*" >> "$CALL_LOG"
case "$1 $2" in
  'api '*) echo "$COMMIT" ;;
  'run list') echo 123 ;;
  'run watch') [[ $FAILURE != ci ]] ;;
  'run view') printf '%s\\tpush\\tsuccess\\tNative packages\\n' "$COMMIT" ;;
  'run download')
    arch=x86_64
    [[ "$*" != *wacli-aarch64* ]] || arch=aarch64
    dest=${!#}; mkdir -p "$dest/meta"
    provenance=$COMMIT
    [[ $FAILURE != source || $arch != aarch64 ]] || provenance=wrong
    echo "$provenance" > "$dest/source-commit.txt"
    metadata_arch=$arch
    [[ $FAILURE != architecture || $arch != aarch64 ]] || metadata_arch=x86_64
    printf 'pkgname = wacli\\npkgver = 0.4.2-1\\narch = %s\\n' "$metadata_arch" > "$dest/meta/.PKGINFO"
    tar -C "$dest/meta" -czf "$dest/wacli-0.4.2-1-$arch.pkg.tar.zst" .PKGINFO ;;
  'release view') echo true ;;
esac
''')
            executable(binary / 'bsdtar', 'exec tar "$@"\n')
            executable(binary / 'gpg', '''
echo "gpg $*" >> "$CALL_LOG"
if [[ "$*" == *--detach-sign* ]]; then
  [[ $FAILURE != signing ]] || exit 1
  touch "${!#}.sig"
elif [[ "$*" == *--export* ]]; then echo public-key; fi
''')
            executable(binary / 'repo-add', '''
repo=${@: -2:1}
repo=${repo%.db.tar.gz}
for suffix in db.tar.gz db.tar.gz.sig files.tar.gz files.tar.gz.sig; do echo database > "$repo.$suffix"; done
for suffix in db db.sig files files.sig; do
  target=${suffix/.sig/}.tar.gz
  [[ $suffix != *.sig ]] || target+=.sig
  ln -s "$repo.$target" "$repo.$suffix"
done
''')
            env = dict(os.environ, PATH=f'{binary}:{os.environ["PATH"]}', CALL_LOG=str(log), COMMIT=commit, FAILURE=failure)
            result = subprocess.run(['bash', 'bin/release', '0.4.2'], cwd=root, env=env, capture_output=True, text=True)
            calls = log.read_text()
            if failure:
                self.assertNotEqual(result.returncode, 0)
                self.assertNotIn('gh release upload', calls)
                if failure in ('source', 'architecture', 'ci', 'pkgver'):
                    self.assertNotIn('--detach-sign', calls)
            else:
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                assets = next(root.glob('target/release-repo-*/assets'))
                for repo in ('wacli', 'wacli-aarch64'):
                    self.assertTrue((assets / f'{repo}.db').is_file())
                    self.assertFalse((assets / f'{repo}.db').is_symlink())
                    self.assertTrue((assets / f'{repo}-signing-key.asc').exists())
                for arch in ('x86_64', 'aarch64'):
                    self.assertTrue((assets / f'wacli-0.4.2-1-{arch}.pkg.tar.zst.sig').exists())
                self.assertIn('gh release upload v0.4.2', calls)
                self.assertIn('gh release edit v0.4.2', calls)
                self.assertEqual((assets / 'install.sh').read_text(), 'installer fixture\n')

    def test_success(self): self.run_release()
    def test_wrong_source(self): self.run_release('source')
    def test_wrong_architecture(self): self.run_release('architecture')
    def test_ci_failure(self): self.run_release('ci')
    def test_signing_failure(self): self.run_release('signing')
    def test_pkgver_mismatch(self): self.run_release('pkgver')


if __name__ == '__main__':
    unittest.main()
