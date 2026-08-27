#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Provision a Penguin base LXC. Run this as root INSIDE the target container
# (Debian/Ubuntu). It installs the penguin-agent service and the common
# game-server runtime (SteamCMD + 32-bit libraries), then the container is
# converted to a Proxmox template.
#
# Prerequisites pushed into the container before running:
#   /usr/local/bin/penguin-agent        (linux/amd64 binary, chmod +x)
#   /root/penguin-agent.service          (this repo's deploy/penguin-agent.service)
# Environment:
#   PENGUIN_AGENT_TOKEN                   the shared agent bearer token
set -euo pipefail

: "${PENGUIN_AGENT_TOKEN:?export PENGUIN_AGENT_TOKEN before running}"
export DEBIAN_FRONTEND=noninteractive

# 1. Base + game-server runtime dependencies (SteamCMD needs i386 + 32-bit libs).
dpkg --add-architecture i386
apt-get update
apt-get install -y --no-install-recommends \
  ca-certificates curl tar xz-utils tzdata procps \
  lib32gcc-s1 libsdl2-2.0-0:i386

# 2. SteamCMD from Valve's redistributable tarball.
mkdir -p /opt/steamcmd
curl -fsSL https://steamcdn-a.akamaihd.net/client/installer/steamcmd_linux.tar.gz \
  | tar -xz -C /opt/steamcmd
ln -sf /opt/steamcmd/steamcmd.sh /usr/local/bin/steamcmd

# 3. Unprivileged runtime user + data directory (Pelican convention).
id container >/dev/null 2>&1 || useradd -m -d /home/container -s /bin/bash container
mkdir -p /home/container
chown -R container:container /home/container

# 4. penguin-agent service + shared token.
install -d -m 700 /etc/penguin-agent
printf 'PENGUIN_AGENT_TOKEN=%s\n' "$PENGUIN_AGENT_TOKEN" > /etc/penguin-agent/agent.env
chmod 600 /etc/penguin-agent/agent.env
install -Dm644 /root/penguin-agent.service /etc/systemd/system/penguin-agent.service
systemctl enable penguin-agent.service

# 5. Shrink for templating.
apt-get clean
rm -rf /var/lib/apt/lists/*

echo "penguin base template provisioned (agent + steamcmd)"
