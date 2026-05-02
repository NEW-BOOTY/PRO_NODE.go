# Pro Node – Hyper‑Fast Access Point Simulator

**Permanent, resilient networking node with intelligent QoS, bridging, mesh (Linux), and 5 GHz optimisation – all from a single binary.**

Copyright (c) Devin B. Royal. All rights reserved.

---

## Overview

Pro Node turns a Linux (Debian/Ubuntu) or macOS machine into a **hyper‑fast wireless gateway**.
It creates virtual interfaces, bridges them, applies fast‑path quality‑of‑service, selects the least‑congested 5 GHz channel, and starts a WPA2‑AES access point (Linux).
The result is measurably lower latency for interactive traffic and better throughput under load.

📡 **Linux** – true 802.11ac AP mode via `hostapd`.  
🍎 **macOS** – simulated AP via software bridging and IP aliases (`en1`/`en2`), with QoS shaping.

A persistence service is automatically generated so the node survives reboots.

---

## Quick start

# 1. Build
go build -o pro_node pro_node.go

# 2. Install OS packages (first time only)
sudo ./pro_node --install-deps

# 3. Activate the hyper‑fast node
sudo ./pro_node --runPress Ctrl+C to stop.
Run sudo ./pro_node --status in another terminal to see live details.

Command‑line flags

Flag	Description
--run	Full setup: interfaces, channel selection, QoS, AP, speedtest, service generation.
--status	Print SSID, channel, MTU, client count (Linux), mesh neighbours.
--cleanup	Tear down all virtual interfaces, QoS rules, and services.
--install-deps	Install required packages (hostapd, iw, bridge-utils, tc, speedtest-cli, etc.).
--generate-service	Create systemd (Linux) or LaunchDaemon (macOS) unit without full setup.
--config	Display current configuration (passphrase hidden).
--help	Show usage.
All commands must be run as root ( sudo ).

Configuration

The program reads settings from:

Linux: /etc/pro_node.conf
macOS: /usr/local/etc/pro_node.conf
Any variable can also be overridden by environment variables (prefixed with PRO_).

Available settings

Key / Env var	Default	Description
SSID / PRO_SSID	ProNode-Hyper	Access point name (max 32 chars, sanitised)
PASSWORD / PRO_PASSWORD	HyperFast!2025	WPA2 passphrase (≥8 chars recommended)
CHANNEL / PRO_CHANNEL	auto	5 GHz channel (36,40,44,48,149,153,157,161,165, or auto for dynamic)
MTU / PRO_MTU	2304	MTU for all interfaces
DOWNLOAD_BANDWIDTH / PRO_DOWN	1000mbit	Root HTB rate (Linux) or pipe bandwidth (macOS)
UPLOAD_BANDWIDTH / PRO_UP	1000mbit	(used for symmetry; current implementation shapes on egress bridge)
SSH_PRIORITY_RATE / PRO_SSH_RATE	2mbit	Guaranteed rate for premium traffic (SSH, VoIP)
HTTP_LIMIT_RATE / PRO_HTTP_LIMIT	20mbit	Maximum rate for HTTP/HTTPS bulk traffic
VOIP_PORTS	5060,5061	Comma‑separated UDP ports given premium priority
ADMIN_EMAIL / PRO_EMAIL	(empty)	Email address for notifications
SMTP_HOST, SMTP_PORT, SMTP_USER, SMTP_PASS	localhost:25	SMTP relay settings (for sendmail style delivery)
MAC_ALLOW, MAC_DENY	(empty)	Comma‑separated MAC addresses for access control
ENABLE_MESH	true	Enable batman-adv mesh (Linux only)
BRAND_MESSAGE / PRO_BRAND	GOT UM. Hyper‑Fast Node Activated	Custom success message
Example config file:

SSID=MyHyperAP
PASSWORD=secret123!
CHANNEL=149
DOWNLOAD_BANDWIDTH=500mbit
SSH_PRIORITY_RATE=5mbit
HTTP_LIMIT_RATE=30mbit
ADMIN_EMAIL=admin@example.com
Logging & notifications

All actions are recorded as JSON to:

Linux: /var/log/pro_node.log
macOS: /usr/local/var/log/pro_node.log
The log is also mirrored to stderr when running interactively.
If ADMIN_EMAIL is configured, the program sends a summary after setup (and on failure) via sendmail.

How it works (under the hood)

1. Wireless optimisation

Dynamic 5 GHz channel selection – the tool scans the environment and picks the channel with the fewest neighbouring APs.
802.11ac / VHT80 (Linux) – hostapd configuration with 80 MHz channels, short guard interval, and beamforming.
High MTU (2304) – reduces packet fragmentation.
Power saving disabled – radio stays at full TX power.
2. Virtual interfaces & bridging

Linux: wlan1 (and optionally wlan2) in AP mode, bridged via br0. Optionally bat0 mesh interface.
macOS: A software bridge (bridge100) binds en0. IP aliases 192.168.44.1 and 192.168.45.1 simulate en1 and en2 as distinct gateway points.
3. Fast‑path QoS (quality of service)

Traffic is classified into three priority tiers:

Premium (SSH, VoIP) – highest priority, guaranteed bandwidth.
Limited (HTTP/HTTPS) – capped bandwidth to protect interactive flows.
Default – fair share of the remaining capacity.
Linux uses tc with HTB classes and SFQ;
macOS uses pfctl + dnctl pipes and queues.

4. Persistence

Linux: systemd unit at /etc/systemd/system/pro-node.service.
macOS: LaunchDaemon plist at /Library/LaunchDaemons/com.pro.node.plist.
Both are configured to restart on failure and run the binary with --run.

5. Cleanup

--cleanup removes everything: virtual interfaces, bridge, QoS rules, mesh, hostapd, and re‑enables power saving. The machine returns to its original state.

Dependencies

The program invokes external tools. Install them automatically with:

bash
sudo ./pro_node --install-deps
Linux (via apt): hostapd, iw, batctl, bridge-utils, tc, iproute2, python3, speedtest-cli, mailutils.
macOS (via brew): speedtest-cli (plus built‑in networksetup, pfctl, dnctl).

Building from source

Requires Go 1.18+.

go build -o pro_node pro_node.go
No external Go dependencies – pure standard library.

