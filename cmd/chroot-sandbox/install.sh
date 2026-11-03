#!/bin/sh
set -eu

chroot_sandbox=$(realpath "$1")
output=$(realpath -m "$2")
install -Dm755 "$chroot_sandbox" "$output/usr/local/bin/omnara-chroot-sandbox"

root="$output/usr/local/lib/omnara/chroot-sandbox"
for dependency in /usr/bin/sed $(ldd /usr/bin/sed | awk '{for (i=1; i<=NF; i++) if ($i ~ /^\//) print $i}'); do
    canonical=$(readlink -f "$dependency")
    install -Dm755 "$canonical" "$root$canonical"
    if [ "$dependency" != "$canonical" ]; then
        mkdir -p "$(dirname "$root$dependency")"
        ln -sf "$canonical" "$root$dependency"
    fi
done
mkdir -p "$root/usr/lib/locale"
cp -R /usr/lib/locale/C.utf8 "$root/usr/lib/locale/"
chmod -R go-w "$output/usr/local/lib/omnara"
setcap cap_sys_chroot=ep "$output/usr/local/bin/omnara-chroot-sandbox"
