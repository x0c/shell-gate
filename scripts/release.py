#!/usr/bin/env python3
"""Build checksummed release packages locally from committed source."""
import argparse
import hashlib
import os
from pathlib import Path
import re
import subprocess
import tarfile
import zipfile

root = Path(__file__).resolve().parent.parent
parser = argparse.ArgumentParser()
parser.add_argument("--version", required=True)
parser.add_argument("--source-commit", default="HEAD", help="reviewed commit with the same source tree")
args = parser.parse_args()
if not re.fullmatch(r"v\d+\.\d+\.\d+(?:-[a-z0-9.]+)?", args.version):
    parser.error("version must be a v-prefixed semantic version")
if subprocess.check_output(["git", "status", "--porcelain"], cwd=root).strip():
    parser.error("commit the releasable source tree first")
subprocess.run(["python3", "scripts/check-public.py"], cwd=root, check=True)
commit = subprocess.check_output(["git", "rev-parse", args.source_commit], cwd=root).decode().strip()
if subprocess.check_output(["git", "rev-parse", commit + "^{tree}"], cwd=root) != subprocess.check_output(["git", "rev-parse", "HEAD^{tree}"], cwd=root):
    parser.error("source commit must have the same tree as HEAD")
out = root / "dist" / args.version
out.mkdir(parents=True, exist_ok=True)
checksums = []
for platform in ("linux", "darwin", "windows"):
    for arch in ("amd64", "arm64"):
        name = "shell-gate" + (".exe" if platform == "windows" else "")
        binary = out / name
        env = dict(os.environ, GOOS=platform, GOARCH=arch, CGO_ENABLED="0", GOMAXPROCS="2")
        with (out / f"build-{platform}-{arch}.log").open("w") as log:
            subprocess.run(["go", "build", "-buildvcs=false", "-trimpath", "-ldflags", f"-s -w -X main.version={args.version}", "-o", str(binary), "./cmd/shell-gate"], cwd=root, env=env, stdout=log, stderr=subprocess.STDOUT, check=True)
        archive = out / f"shell-gate_{args.version}_{platform}_{arch}.{'zip' if platform == 'windows' else 'tar.gz'}"
        assets = [(binary, name), (root / "LICENSE", "LICENSE"), (root / "THIRD_PARTY_NOTICES.md", "THIRD_PARTY_NOTICES.md")]
        if platform == "windows":
            with zipfile.ZipFile(archive, "w", compression=zipfile.ZIP_DEFLATED) as target:
                for source, target_name in assets: target.write(source, target_name)
        else:
            with tarfile.open(archive, "w:gz") as target:
                for source, target_name in assets: target.add(source, arcname=target_name)
        checksums.append(f"{hashlib.sha256(archive.read_bytes()).hexdigest()}  {archive.name}")
        binary.unlink()
        print(archive.name, flush=True)
(out / "SHA256SUMS").write_text("\n".join(checksums) + "\n")
(out / "SOURCE_COMMIT").write_text(commit + "\n")
print(f"Packages built from {commit}. Full compiler logs retained in {out.relative_to(root)}.")
