#!/usr/bin/env bash
#
# Verifies that a built wasp .deb can upgrade an installed package from one of
# this fork's earlier names, rather than failing on file conflicts.
#
# This guards issue #177: the distribution was renamed bee-experimental -> wasp,
# and a package that declares Replaces/Conflicts for only upstream's `bee` name
# cannot take over the files owned by a bee-experimental install. dpkg then
# refuses with "trying to overwrite ..., which is also in package
# bee-experimental", and the only manual route out includes a purge that runs
# `userdel -r bee` and deletes the data directory.
#
# Two checks:
#   1. static  — the .deb's control file lists both old names in Replaces and
#                Conflicts. Cheap, and cannot rot.
#   2. dynamic — install a synthetic bee-experimental package, then install the
#                real wasp .deb over it inside a Debian container, and assert
#                the upgrade succeeds and wasp owns /usr/bin/bee.
#
# Usage: packaging/test-deb-upgrade.sh path/to/wasp_<version>_<arch>.deb
# The dynamic check needs docker; it is skipped with a warning if docker is
# absent, so the static check still runs in any environment.

set -euo pipefail

DEB=${1:?usage: test-deb-upgrade.sh <wasp .deb>}
[ -f "$DEB" ] || { echo "no such file: $DEB" >&2; exit 1; }

# --- 1. static: the control metadata names both predecessors ----------------
control=$(dpkg-deb -f "$DEB" Replaces Conflicts)
fail=0
for field in Replaces Conflicts; do
  line=$(printf '%s\n' "$control" | sed -n "s/^$field: //p")
  for name in bee bee-experimental; do
    case ",${line//[[:space:]]/}," in
      *",$name,"*) ;;
      *) echo "FAIL: $field does not list '$name' (got: ${line:-<empty>})"; fail=1 ;;
    esac
  done
done
[ "$fail" -eq 0 ] && echo "ok: control lists bee and bee-experimental in Replaces and Conflicts"
[ "$fail" -eq 0 ] || exit 1

# --- 2. dynamic: a real dpkg upgrade from a bee-experimental fixture ---------
if ! command -v docker >/dev/null 2>&1; then
  echo "skip: docker not available, dynamic upgrade check not run"
  exit 0
fi

arch=$(dpkg-deb -f "$DEB" Architecture)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
cp "$DEB" "$work/wasp.deb"

# Build a minimal old package that ships the same files wasp does, so dpkg has
# a genuine file-ownership conflict to resolve. Only the paths and the package
# name matter to the relationship logic under test.
old="$work/build-old"
mkdir -p "$old/DEBIAN" "$old/usr/bin" "$old/lib/systemd/system" "$old/etc/bee"
printf '#!/bin/sh\necho stub\n' > "$old/usr/bin/bee"; chmod 755 "$old/usr/bin/bee"
echo '[Unit]' > "$old/lib/systemd/system/bee.service"
echo 'api-addr: :1633' > "$old/etc/bee/bee.yaml"
echo '/etc/bee/bee.yaml' > "$old/DEBIAN/conffiles"
cat > "$old/DEBIAN/control" <<EOF
Package: bee-experimental
Version: 1:0.1.0~test.1
Architecture: $arch
Maintainer: fork <noreply@localhost>
Description: pre-rename fixture standing in for an old install
EOF

docker run --rm -v "$work":/w:ro "debian:bookworm-slim" bash -euo pipefail -c '
  cd /tmp && cp /w/*.deb /w/build-old -r . 2>/dev/null || true
  cp -r /w/build-old .
  dpkg-deb -b build-old old.deb >/dev/null
  cp /w/wasp.deb .
  echo "  installing bee-experimental fixture"
  dpkg -i old.deb >/dev/null
  echo "  installing wasp over it"
  dpkg -i --force-confold wasp.deb >/dev/null
  owner=$(dpkg -S /usr/bin/bee | cut -d: -f1)
  [ "$owner" = "wasp" ] || { echo "FAIL: /usr/bin/bee owned by $owner, not wasp"; exit 1; }
  dpkg-query -W -f="${Package} ${Version}\n" wasp
  echo "  ok: wasp took over the bee-experimental install"
' | sed 's/^/  /'

echo "ok: dpkg upgrade from bee-experimental to wasp succeeds"
