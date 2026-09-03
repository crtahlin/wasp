#!/usr/bin/env python3
#
# Guards issue #177 at the source, cheaply. The distribution was renamed
# bee-experimental -> wasp, and a package that does not declare the old names in
# Conflicts and Replaces cannot take over an existing install's files: dpkg then
# refuses the upgrade, and the only manual route out purges the data directory.
#
# The thorough check is packaging/test-deb-upgrade.sh, which inspects a built
# .deb and performs a real dpkg upgrade. That needs the package built, which
# means the full goreleaser build, which is too slow to run on every pull
# request. This asserts the same relationships directly in .goreleaser.yml, in a
# fraction of a second, so the drift that broke #177 cannot land unnoticed. The
# built-artifact test belongs in the release pipeline, where the debs already
# exist.
#
# Fails with a clear message if any deb or rpm package drops bee or
# bee-experimental from Conflicts or Replaces.

import sys

try:
    import yaml
except ImportError:
    sys.exit("check-package-relationships: PyYAML is required (pip install pyyaml)")

CONFIG = ".goreleaser.yml"
REQUIRED = {"bee", "bee-experimental"}
# #177 was a deb break; the rpm override carries the same fields and the same
# reasoning, so hold both to the rule.
FORMATS = ("deb", "rpm")
FIELDS = ("conflicts", "replaces")


def main() -> int:
    with open(CONFIG) as f:
        cfg = yaml.safe_load(f)

    nfpms = cfg.get("nfpms") or []
    if not nfpms:
        print(f"FAIL: no nfpms section in {CONFIG}")
        return 1

    problems = []
    checked = 0
    for i, pkg in enumerate(nfpms):
        name = pkg.get("package_name") or pkg.get("id") or f"nfpms[{i}]"
        formats = pkg.get("formats") or []
        overrides = pkg.get("overrides") or {}
        for fmt in FORMATS:
            if fmt not in formats:
                continue
            override = overrides.get(fmt) or {}
            for field in FIELDS:
                # An override replaces the top-level value; fall back to it when
                # the override does not set the field.
                values = set(override.get(field, pkg.get(field) or []))
                missing = REQUIRED - values
                checked += 1
                if missing:
                    problems.append(
                        f"{name} [{fmt}] {field} is missing "
                        f"{sorted(missing)} (has {sorted(values)})"
                    )

    if problems:
        print("FAIL: package relationship drift — see issue #177")
        for p in problems:
            print("  - " + p)
        print(
            "\nEvery deb and rpm package must list bee and bee-experimental in "
            "both Conflicts and Replaces, or an upgrade over an earlier install "
            "fails on file conflicts."
        )
        return 1

    if checked == 0:
        print("FAIL: no deb or rpm package found to check")
        return 1

    print(
        "ok: every nfpm deb/rpm package declares bee and bee-experimental "
        "in Conflicts and Replaces"
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
