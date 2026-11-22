set -eu
d="$HOME/.omnara-revyl"
l=ai.omnara.daemon
u="gui/$(id -u)"
umask 077
mkdir -p "$d"
: >"$d/managed-daemon.sh.new"
i=0
while [ "$i" -lt "$2" ]; do
	p="$HOME/$1-$(printf %03d "$i")"
	cat "$p" >>"$d/managed-daemon.sh.new"
	rm -f "$p"
	i=$((i + 1))
done
running() {
	launchctl print "$u/$l" 2>/dev/null | grep -q 'state = running'
}
if cmp -s "$d/managed-daemon.sh.new" "$d/managed-daemon.sh" && running; then
	rm "$d/managed-daemon.sh.new"
	exit 0
fi
mv "$d/managed-daemon.sh.new" "$d/managed-daemon.sh"
x() { printf %s "$1" | sed 's/&/\&amp;/g; s/</\&lt;/g; s/>/\&gt;/g'; }
h=$(x "$HOME")
cat >"$d/$l.plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key><string>$l</string>
	<key>ProgramArguments</key>
	<array><string>/bin/sh</string><string>$h/.omnara-revyl/managed-daemon.sh</string></array>
	<key>EnvironmentVariables</key>
	<dict>
		<key>HOME</key><string>$h</string>
		<key>PATH</key><string>$(x "$PATH")</string>
	</dict>
	<key>WorkingDirectory</key><string>$h</string>
	<key>RunAtLoad</key><true/>
	<key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>
	<key>ProcessType</key><string>Interactive</string>
	<key>StandardOutPath</key><string>$h/.omnara-revyl/daemon.log</string>
	<key>StandardErrorPath</key><string>$h/.omnara-revyl/daemon.log</string>
</dict>
</plist>
EOF
launchctl bootout "$u/$l" 2>/dev/null || :
i=0
until launchctl bootstrap "$u" "$d/$l.plist" 2>/dev/null; do
	i=$((i + 1))
	[ "$i" -lt 10 ] || exit 1
	sleep 1
done
i=0
until running; do
	i=$((i + 1))
	[ "$i" -lt 10 ] || exit 1
	sleep 1
done
