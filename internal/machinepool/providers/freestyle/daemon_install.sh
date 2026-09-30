set -eu
install -d -m 700 /etc/omnara
install -m 700 /dev/stdin /etc/omnara/managed-daemon.sh.new
if cmp -s /etc/omnara/managed-daemon.sh.new /etc/omnara/managed-daemon.sh &&
	systemctl is-active --quiet omnara-daemon.service; then
	rm /etc/omnara/managed-daemon.sh.new
	exit 0
fi
mv /etc/omnara/managed-daemon.sh.new /etc/omnara/managed-daemon.sh
install -m 644 /dev/stdin /etc/systemd/system/omnara-daemon.service <<'EOF'
[Unit]
Description=Omnara machine daemon
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=root
ExecStart=/etc/omnara/managed-daemon.sh
Restart=on-failure
RestartSec=2
KillMode=process

[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload
systemctl enable omnara-daemon.service
systemctl reset-failed omnara-daemon.service 2>/dev/null || :
systemctl restart omnara-daemon.service
systemctl is-active --quiet omnara-daemon.service
