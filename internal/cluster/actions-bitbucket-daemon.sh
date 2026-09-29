set -eu
export PATH="/usr/local/sbin/.iptables-legacy:$PATH"
iface=$(ip route show | awk '$1=="default" {for (i=1;i<NF;i++) if ($i=="dev") {print $(i+1); exit}}')
[ -n "$iface" ]
addr=$(ip -4 -o addr show dev "$iface" | awk '{for (i=1;i<NF;i++) if ($i=="inet") {split($(i+1),a,"/"); print a[1]; exit}}')
mtu=$(ip -o link show dev "$iface" | awk '{for (i=1;i<NF;i++) if ($i=="mtu") {print $(i+1); exit}}')
[ -n "$addr" ] && [ -n "$mtu" ]
echo 1 > /proc/sys/net/ipv4/ip_forward
iptables -t nat -A POSTROUTING -o "$iface" -j SNAT --to-source "$addr" -p tcp
iptables -t nat -A POSTROUTING -o "$iface" -j SNAT --to-source "$addr" -p udp
# Bitbucket's native manager expects these standard private Docker paths. The
# socket, data and working directory are separate bounded disk-backed volumes.
# No TCP listener, node socket or manager credential directory is exposed.
exec dockerd --data-root=/var/lib/docker --host=unix:///var/run/docker.sock --group=1001 --iptables=false --ip6tables=false --mtu="$mtu" --storage-driver=vfs --feature=containerd-snapshotter=false --default-pids-limit=512 --log-driver=json-file --log-opt=max-size=10m --log-opt=max-file=2
