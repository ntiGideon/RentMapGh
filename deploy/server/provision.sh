#!/usr/bin/env bash
# One-time setup of a fresh Ubuntu 24.04 (or 22.04) VPS for RentMap. Run as
# root; safe to run again (it re-applies the same settings).
#
#   scp deploy/server/provision.sh root@<ip>:
#   ssh root@<ip> bash provision.sh git@github.com:ntiGideon/RentMapGh.git
#
# It: updates the system and turns on security updates, sets UTC, adds swap,
# installs Docker, creates the `deploy` user (your root SSH keys, docker,
# sudo), turns off SSH passwords and root login, opens only 22/80/443,
# creates a GitHub deploy key, clones the repo to /opt/rentmap and installs
# the cron jobs and log rotation. Then follow deploy/RUNBOOK.md → First deploy.
set -euo pipefail

REPO_URL=${1:-}
USER_NAME=${DEPLOY_USER:-deploy}
APP_DIR=/opt/rentmap
SWAP_GB=${SWAP_GB:-2}

[ "$(id -u)" = 0 ] || { echo "provision: run as root" >&2; exit 1; }
. /etc/os-release
[ "$ID" = ubuntu ] || { echo "provision: written for Ubuntu, this is $PRETTY_NAME" >&2; exit 1; }
step() { echo; echo "== $*"; }

# Refuse to lock ourselves out: SSH passwords get turned off below.
if [ ! -s /root/.ssh/authorized_keys ] && [ ! -s "/home/$USER_NAME/.ssh/authorized_keys" ]; then
	echo "provision: no SSH keys in /root/.ssh/authorized_keys — add yours first (ssh-copy-id root@<ip>)" >&2
	exit 1
fi

step "system packages and security updates"
export DEBIAN_FRONTEND=noninteractive
apt-get update -q
apt-get upgrade -yq
apt-get install -yq --no-install-recommends ca-certificates curl git jq ufw fail2ban unattended-upgrades cron logrotate rclone
cat > /etc/apt/apt.conf.d/20auto-upgrades <<'CONF'
APT::Periodic::Update-Package-Lists "1";
APT::Periodic::Unattended-Upgrade "1";
APT::Periodic::AutocleanInterval "7";
CONF
timedatectl set-timezone UTC

step "swap"
if [ -z "$(swapon --noheadings)" ]; then
	fallocate -l "${SWAP_GB}G" /swapfile
	chmod 600 /swapfile
	mkswap /swapfile > /dev/null
	swapon /swapfile
	grep -q '^/swapfile' /etc/fstab || echo '/swapfile none swap sw 0 0' >> /etc/fstab
	sysctl -qw vm.swappiness=10
	echo 'vm.swappiness=10' > /etc/sysctl.d/90-rentmap.conf
	echo "added ${SWAP_GB} GB swap"
else
	echo "swap already on"
fi

step "Docker"
if ! command -v docker > /dev/null; then
	install -m 0755 -d /etc/apt/keyrings
	curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
	chmod a+r /etc/apt/keyrings/docker.asc
	echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu $VERSION_CODENAME stable" \
		> /etc/apt/sources.list.d/docker.list
	apt-get update -q
	apt-get install -yq docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
fi
# Container logs are capped (the default json-file log grows forever);
# live-restore keeps containers running while dockerd itself is upgraded.
cat > /etc/docker/daemon.json <<'CONF'
{
  "log-driver": "local",
  "log-opts": { "max-size": "20m", "max-file": "5" },
  "live-restore": true
}
CONF
systemctl enable --now docker > /dev/null 2>&1
systemctl restart docker

step "user $USER_NAME"
if ! id "$USER_NAME" > /dev/null 2>&1; then
	adduser --disabled-password --gecos "" "$USER_NAME" > /dev/null
fi
usermod -aG docker "$USER_NAME"
# Membership of the docker group is root-equivalent anyway; passwordless
# sudo just makes that explicit and lets the only login user administer.
echo "$USER_NAME ALL=(ALL) NOPASSWD:ALL" > "/etc/sudoers.d/90-$USER_NAME"
chmod 440 "/etc/sudoers.d/90-$USER_NAME"
home=$(getent passwd "$USER_NAME" | cut -d: -f6)
install -d -m 700 -o "$USER_NAME" -g "$USER_NAME" "$home/.ssh"
if [ -s /root/.ssh/authorized_keys ]; then
	touch "$home/.ssh/authorized_keys"
	sort -u /root/.ssh/authorized_keys "$home/.ssh/authorized_keys" -o "$home/.ssh/authorized_keys"
fi
chown "$USER_NAME:$USER_NAME" "$home/.ssh/authorized_keys"
chmod 600 "$home/.ssh/authorized_keys"
if [ ! -f "$home/.ssh/id_ed25519" ]; then
	sudo -u "$USER_NAME" ssh-keygen -q -t ed25519 -N "" -C "rentmap-deploy@$(hostname)" -f "$home/.ssh/id_ed25519"
fi
sudo -u "$USER_NAME" sh -c "ssh-keyscan -t ed25519 github.com 2> /dev/null >> ~/.ssh/known_hosts; sort -u -o ~/.ssh/known_hosts ~/.ssh/known_hosts"
grep -q 'alias dc=' "$home/.bashrc" || echo "alias dc=$APP_DIR/deploy/server/dc" >> "$home/.bashrc"

step "SSH: keys only, no root login"
cat > /etc/ssh/sshd_config.d/10-rentmap.conf <<'CONF'
PasswordAuthentication no
KbdInteractiveAuthentication no
PermitRootLogin no
CONF
sshd -t
systemctl reload ssh 2> /dev/null || systemctl reload sshd
systemctl enable --now fail2ban > /dev/null 2>&1

step "firewall: 22, 80, 443 only"
# Only Caddy publishes ports (80/443). Docker bypasses ufw for published
# ports, so never publish db or web in compose.prod.yml.
ufw default deny incoming > /dev/null
ufw default allow outgoing > /dev/null
ufw allow OpenSSH > /dev/null
ufw allow 80/tcp > /dev/null
ufw allow 443/tcp > /dev/null
ufw allow 443/udp > /dev/null
ufw --force enable > /dev/null
ufw status | sed -n '1,12p'

step "directories"
install -d -o "$USER_NAME" -g "$USER_NAME" -m 755 "$APP_DIR" /var/log/rentmap
install -d -o "$USER_NAME" -g "$USER_NAME" -m 700 /var/backups/rentmap /etc/rentmap

step "code"
if [ ! -d "$APP_DIR/.git" ] && [ -n "$REPO_URL" ]; then
	if ! sudo -u "$USER_NAME" git clone -q "$REPO_URL" "$APP_DIR"; then
		echo
		echo "Couldn't clone $REPO_URL. Add this deploy key to the repo (GitHub → Settings →"
		echo "Deploy keys → Add, read-only), then run this script again:"
		echo
		cat "$home/.ssh/id_ed25519.pub"
		exit 1
	fi
fi

if [ -d "$APP_DIR/.git" ]; then
	step "cron jobs and log rotation"
	install -m 644 "$APP_DIR/deploy/server/rentmap.cron" /etc/cron.d/rentmap
	install -m 644 "$APP_DIR/deploy/server/logrotate.conf" /etc/logrotate.d/rentmap
	if [ ! -f /etc/rentmap/ops.env ]; then
		install -m 600 -o "$USER_NAME" -g "$USER_NAME" "$APP_DIR/deploy/server/ops.env.example" /etc/rentmap/ops.env
	fi
	if [ ! -f "$APP_DIR/deploy/.env.prod" ]; then
		install -m 600 -o "$USER_NAME" -g "$USER_NAME" "$APP_DIR/deploy/env.prod.example" "$APP_DIR/deploy/.env.prod"
	fi
	echo
	echo "Done. Next, as $USER_NAME (ssh $USER_NAME@<ip>), see deploy/RUNBOOK.md → First deploy:"
	echo "  1. fill $APP_DIR/deploy/.env.prod"
	echo "  2. $APP_DIR/deploy/server/deploy.sh"
	echo "  3. $APP_DIR/deploy/backup/pitr.sh setup"
	echo "  4. heartbeat URLs into /etc/rentmap/ops.env (deploy/monitor/betterstack.sh prints them)"
else
	echo
	echo "Done, but there's no code yet: run again with the repo URL."
	echo "Deploy key to add to the repo first (read-only):"
	cat "$home/.ssh/id_ed25519.pub"
fi
