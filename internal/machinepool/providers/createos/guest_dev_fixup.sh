[ -e /dev/fd ] || ln -s /proc/self/fd /dev/fd
[ -e /dev/stdin ] || ln -s /proc/self/fd/0 /dev/stdin
[ -e /dev/stdout ] || ln -s /proc/self/fd/1 /dev/stdout
[ -e /dev/stderr ] || ln -s /proc/self/fd/2 /dev/stderr
grep -qs ' /dev/shm ' /proc/mounts || { mkdir -p /dev/shm && mount -t tmpfs -o nosuid,nodev tmpfs /dev/shm; }
