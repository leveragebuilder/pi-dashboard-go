# Pi Dashboard Go

Minimal Raspberry Pi dashboard as a single Go binary.

Features:
- temperature
- uptime
- RAM usage
- storage usage
- top RAM processes
- top CPU processes
- kill processes from the browser

No database, no framework, no frontend build step.

## Run locally

```bash
go run .
```

Then open:

```text
http://localhost:8088/
```

## Build for Raspberry Pi

```bash
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o pi-dashboard-go-linux-arm64 .
```

## systemd service

Install the binary and unit:

```bash
sudo mkdir -p /opt/pi-dashboard-go
sudo cp pi-dashboard-go-linux-arm64 /opt/pi-dashboard-go/pi-dashboard-go
sudo chmod 755 /opt/pi-dashboard-go/pi-dashboard-go
sudo cp pi-dashboard-go.service /etc/systemd/system/pi-dashboard-go.service
sudo systemctl daemon-reload
sudo systemctl enable --now pi-dashboard-go
```

Default port:
- `80`

Config via environment variables:
- `PI_DASHBOARD_ADDR`
- `PI_DASHBOARD_REFRESH_SECONDS`

## Notes

The first version has no authentication. If you expose it outside your LAN, add auth before doing that.
