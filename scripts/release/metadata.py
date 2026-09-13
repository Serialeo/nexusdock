#!/usr/bin/env python3
"""Derive image tags only for validated main commits and version tags."""

from __future__ import annotations

import os
import re
import sys


def main() -> int:
    ref = os.environ["GITHUB_REF"]
    sha = os.environ["GITHUB_SHA"]
    if not re.fullmatch(r"[0-9a-f]{40}", sha):
        raise SystemExit("GITHUB_SHA must be a full commit SHA")

    image = "ghcr.io/serialeo/nexusdock"
    tags = [f"{image}:sha-{sha[:7]}"]
    release = ref.startswith("refs/tags/")
    prerelease = False
    version = ""
    if release:
        version = ref.removeprefix("refs/tags/")
        match = re.fullmatch(
            r"v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z.-]+))?",
            version,
        )
        if match is None or len(version) > 128:
            raise SystemExit("release tag must be vMAJOR.MINOR.PATCH[-PRERELEASE] and fit a Docker tag")
        suffix = match.group(4)
        if suffix is not None:
            for identifier in suffix.split("."):
                if not identifier or (identifier.isdigit() and len(identifier) > 1 and identifier.startswith("0")):
                    raise SystemExit("release tag contains an invalid SemVer prerelease identifier")
            prerelease = True
        tags.extend([f"{image}:{version}", f"{image}:{version[1:]}"])
        # 预发布只能得到精确版本标签，不能推进稳定版与 latest。
        if not prerelease:
            tags.extend([f"{image}:{match.group(1)}.{match.group(2)}", f"{image}:latest"])
    elif ref != "refs/heads/main":
        raise SystemExit("candidate publication is restricted to main")

    output = (
        f"release={str(release).lower()}\n"
        f"prerelease={str(prerelease).lower()}\n"
        f"version={version}\n"
        "tags<<NEXUS_IMAGE_TAGS\n" + "\n".join(tags) + "\nNEXUS_IMAGE_TAGS\n"
    )
    with open(os.environ["GITHUB_OUTPUT"], "a", encoding="utf-8") as destination:
        destination.write(output)
    return 0


if __name__ == "__main__":
    sys.exit(main())
