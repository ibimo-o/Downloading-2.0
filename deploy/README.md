# Deploying the dl2 API server

This covers running `cmd/server` as a real, internet-facing service on a
VPS (tested against Hetzner, should work on any Ubuntu/Debian box).

## Architecture

```
Internet --> Caddy (TLS, port 443) --> dl2 server (localhost:8787)
```

The dl2 server itself never listens on a public interface directly --
Caddy terminates TLS and reverse-proxies to it on localhost. This means:
- You get free, auto-renewing HTTPS certificates (Let's Encrypt via Caddy)
- The dl2 process itself is one layer removed from direct internet exposure

## Before you deploy: read SECURITY.md

This matters more once the server is actually internet-reachable. At
minimum, before deploying publicly:
- **Always set `-token`** (the setup script generates one for you
  automatically) -- without it, anyone who finds the URL can trigger
  arbitrary downloads to your disk.
- **Never pass `-allow-private`** on a public deployment -- it disables
  SSRF protection entirely.
- The default rate limit (5 req/s, burst 10 per IP) is a basic safeguard,
  not a substitute for a real WAF if you expect serious public traffic.

## One-time setup

1. **Get a VPS** (Hetzner CX-series is fine for this workload) and SSH in.

2. **Install Go** (if building from source) or skip and use a pre-built
   binary from [GitHub Releases](https://github.com/ibimo-o/Downloading-2.0/releases)
   instead:
   ```bash
   curl -fsSL https://go.dev/dl/go1.22.linux-amd64.tar.gz | sudo tar -C /usr/local -xz
   echo 'export PATH=$PATH:/usr/local/go/bin' >> ~/.bashrc && source ~/.bashrc
   ```

3. **Install Caddy** for automatic HTTPS:
   ```bash
   sudo apt install -y debian-keyring debian-archive-keyring apt-transport-https
   curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' | sudo gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
   curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' | sudo tee /etc/apt/sources.list.d/caddy-stable.list
   sudo apt update && sudo apt install caddy
   ```

4. **Clone this repo** to the VPS:
   ```bash
   git clone https://github.com/ibimo-o/Downloading-2.0.git
   cd Downloading-2.0
   ```

5. **Point your domain's A record** at the VPS's IP address (e.g.
   `dl2.yourdomain.com` -> VPS IP). Wait for DNS to propagate
   (`dig dl2.yourdomain.com` should show the right IP).

   **No domain yet?** Skip this step entirely and use
   [nip.io](https://nip.io) instead — `<ip-with-dashes>.nip.io` resolves
   directly to your VPS's IP with zero setup (e.g. IP `62.238.18.69`
   becomes `62-238-18-69.nip.io`). The provided `deploy/Caddyfile`
   already defaults to this. Swap in a real domain later by editing one
   line in the Caddyfile.

6. **Run the deploy script**:
   ```bash
   chmod +x deploy/setup-vps.sh
   ./deploy/setup-vps.sh
   ```
   This creates a dedicated `dl2` system user, builds/installs the
   binary, generates a real auth token, sets up the systemd service, and
   configures the firewall. **Save the printed token** -- you'll need it
   for every API call.

7. **Configure Caddy**:
   ```bash
   sudo cp deploy/Caddyfile /etc/caddy/Caddyfile
   # edit /etc/caddy/Caddyfile, replace dl2.yourdomain.com with your real domain
   sudo systemctl reload caddy
   ```

8. **Test it**:
   ```bash
   curl -X POST https://dl2.yourdomain.com/download \
     -H "X-DL2-Token: <your token>" \
     -H "Content-Type: application/json" \
     -d '{"url":"http://speedtest.tele2.net/100MB.zip"}'
   ```
   Should return `{"job_id":"job-1"}`. Check progress:
   ```bash
   curl https://dl2.yourdomain.com/status/job-1 -H "X-DL2-Token: <your token>"
   ```

## Day-to-day operations

```bash
# View logs
sudo journalctl -u dl2-server -f

# Restart after a config change
sudo systemctl restart dl2-server

# Check status
sudo systemctl status dl2-server

# Update to a new version
cd Downloading-2.0 && git pull
go build -o /tmp/dl2-server ./cmd/server
sudo mv /tmp/dl2-server /opt/dl2/server
sudo systemctl restart dl2-server
```

## Known limitations of this deployment setup

- Job state is in-memory in the dl2 process -- a restart (deploy, crash,
  reboot) loses in-progress job tracking. The download itself isn't lost
  (files already on disk stay), but you can't query its status anymore.
- No horizontal scaling story yet -- this is a single-instance deployment.
- Downloaded files accumulate in `/opt/dl2/downloads` with no automatic
  cleanup -- you'll want a cron job or manual cleanup policy for a
  long-running deployment.
