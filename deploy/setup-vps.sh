#!/usr/bin/env bash
# Deploy script for dl2 API server on a Hetzner VPS (Ubuntu/Debian assumed).
# Run this ON THE VPS as a user with sudo access, not on your local machine.
#
# What this does:
#   1. Creates a dedicated unprivileged 'dl2' system user (don't run as root)
#   2. Builds the server binary from source (requires Go on the VPS, or you
#      can instead scp a pre-built Linux binary from your GitHub Release)
#   3. Generates a random auth token and writes it to the systemd
#      environment file
#   4. Installs and enables the systemd service
#   5. Configures the firewall (ufw) to only allow SSH + HTTP/HTTPS
#      (the dl2 server itself stays on localhost, reached only via Caddy)
#
# Prerequisites before running:
#   - A domain/subdomain's A record pointed at this VPS's IP
#   - Caddy installed (see https://caddyserver.com/docs/install#debian-ubuntu-raspbian)
#   - This repo cloned to /opt/dl2-src on the VPS (or scp the binary directly)

set -euo pipefail

echo "== dl2 server deployment =="

# 1. Dedicated unprivileged user
if ! id "dl2" &>/dev/null; then
  echo "Creating dl2 system user..."
  sudo useradd --system --no-create-home --shell /usr/sbin/nologin dl2
fi

# 2. Directories
sudo mkdir -p /opt/dl2/downloads
sudo chown -R dl2:dl2 /opt/dl2

# 3. Build (assumes you're running this from inside the cloned repo, or
#    adjust the path). If you'd rather not install Go on the VPS, skip this
#    and instead: scp a pre-built linux-amd64 binary from your GitHub
#    Release to /opt/dl2/server and chmod +x it.
if command -v go &>/dev/null; then
  echo "Building server binary..."
  go build -o /tmp/dl2-server ./cmd/server
  sudo mv /tmp/dl2-server /opt/dl2/server
  sudo chown dl2:dl2 /opt/dl2/server
  sudo chmod +x /opt/dl2/server
else
  echo "Go not found. Either install Go, or scp a pre-built binary to /opt/dl2/server manually."
  echo "Pre-built binaries: https://github.com/ibimo-o/Downloading-2.0/releases"
fi

# 4. Generate a real auth token if one doesn't already exist (don't
#    overwrite an existing deployment's token on re-run).
ENV_FILE="/opt/dl2/dl2-server.env"
if [ ! -f "$ENV_FILE" ]; then
  TOKEN=$(openssl rand -hex 32)
  echo "DL2_TOKEN=${TOKEN}" | sudo tee "$ENV_FILE" > /dev/null
  sudo chmod 600 "$ENV_FILE"
  sudo chown dl2:dl2 "$ENV_FILE"
  echo ""
  echo "Generated auth token (save this -- you'll need it to call the API):"
  echo "  ${TOKEN}"
  echo ""
else
  echo "Existing $ENV_FILE found, leaving token unchanged."
fi

# 5. Install systemd service
sudo cp deploy/dl2-server.service /etc/systemd/system/dl2-server.service
sudo systemctl daemon-reload
sudo systemctl enable dl2-server
sudo systemctl restart dl2-server

echo ""
echo "Service status:"
sudo systemctl status dl2-server --no-pager || true

# 6. Firewall: only SSH + HTTP/HTTPS reachable from outside. The dl2
#    server itself binds to localhost only and is reached via Caddy's
#    reverse proxy on 80/443.
if command -v ufw &>/dev/null; then
  sudo ufw allow OpenSSH
  sudo ufw allow 80/tcp
  sudo ufw allow 443/tcp
  echo "y" | sudo ufw enable || true
  sudo ufw status
fi

echo ""
echo "== Next steps =="
echo "1. Point your domain's A record at this VPS's IP, if not done already."
echo "2. Edit deploy/Caddyfile, replace dl2.yourdomain.com with your real domain."
echo "3. sudo cp deploy/Caddyfile /etc/caddy/Caddyfile && sudo systemctl reload caddy"
echo "4. Test: curl -X POST https://dl2.yourdomain.com/download \\"
echo "          -H \"X-DL2-Token: <token from above>\" \\"
echo "          -H \"Content-Type: application/json\" \\"
echo "          -d '{\"url\":\"http://speedtest.tele2.net/100MB.zip\"}'"
