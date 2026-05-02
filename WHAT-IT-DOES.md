When you run sudo pro_node --run, the program builds a high‑performance wireless “Pro Node” on your machine. Here’s exactly what happens, and what gets created at every step.

1. Configuration loading

Reads /etc/pro_node.conf (Linux) or /usr/local/etc/pro_node.conf (macOS) for SSID, passphrase, channel, MTU, bandwidth/QoS rates, etc.
Environment variables like PRO_SSID can override any setting.
Defaults if nothing is set:
SSID = ProNode-Hyper, passphrase = HyperFast!2025, MTU = 2304, channel = auto (meaning “choose the best 5 GHz channel later”).
2. Pre‑setup speed test

Uses speedtest-cli to measure your current internet download, upload, and ping.
Stores results (e.g., Pre‑setup: DL 150.2 Mbps, UL 45.1 Mbps, Ping 12 ms) in the JSON log.
3. Dynamic 5 GHz channel selection (if channel = auto)

Linux: calls iw dev wlan0 scan on the 5 GHz band.
macOS: uses /System/Library/PrivateFrameworks/Apple80211.framework/.../airport -s.
Counts how many networks already sit on each allowed channel (36, 40, 44, 48, 149, 153, 157, 161, 165) and picks the least congested one.
This reduces interference – one of the main reasons you later see better performance.
4. Virtual interfaces & bridge creation

Linux

What is created	What it does
wlan1 (and optionally wlan2)	Virtual Wi‑Fi interfaces in AP mode (hostapd later uses wlan1)
br0	Software bridge that ties together wlan0 (physical), wlan1, and optionally wlan2
bat0	Mesh interface (batman‑adv) – if ENABLE_MESH=true – that loops br0 into a mesh network
MTU 2304	Set on all interfaces to reduce packet fragmentation
Power saving disabled	TX power stays at maximum
All virtual interfaces are linked to the same physical radio, but the kernel’s packet scheduler and bridge keep traffic flowing intelligently.
macOS

What is created	What it does
bridge100	A software bridge created with ifconfig bridge create; en0 (your physical Wi‑Fi) is added as a member
IP aliases 192.168.44.1 and 192.168.45.1	Simulated “en1” and “en2” – they are not real hardware but appear as additional IP addresses on the bridge. macOS cannot create a true software AP, so this is the simulation.
MTU 2304	Applied to en0 and bridge100
Power management suppressed	pmset -a womp 0 prevents aggressive sleep/Wi‑Fi power‑down
The “en1/en2 simulation” means other devices can reach these IPs if routing is set up; the point is to have two logically separate entry points for traffic, each subject to the QoS rules.
5. Fast‑path QoS (quality of service)

This is the core of the “hyper‑fast” part. The program classifies traffic into three tiers:

Tier	Examples	What it gets
Premium	SSH (port 22), VoIP (5060, 5061, etc.)	Guaranteed bandwidth (SSH_PRIORITY_RATE, e.g., 2 Mbit) and highest priority – always sent first.
Limited	HTTP/HTTPS (ports 80, 443)	Capped at HTTP_LIMIT_RATE (e.g., 20 Mbit) – this prevents one big download from starving everything else.
Default	Everything else	Gets the remainder of your bandwidth, fairly shared (via SFQ on Linux, default queue on macOS).
How it’s implemented

Linux

tc (traffic control) with HTB (hierarchical token bucket) classes.
Filters match on destination port, directing packets to the right class.
Each class has an SFQ (stochastic fairness queue) so no single flow inside a class dominates.
macOS

pfctl (packet filter) with anchors for MAC allow/deny lists.
dnctl creates pipes and queues; traffic to port 22 and VoIP goes to a high‑priority queue, HTTP to a limited queue.
The macOS firewall is activated with pfctl -e.
This is the “fast‑path packet handling” – latency‑critical flows jump the queue, bulk flows are gently throttled, and the overall effect is that real‑time work (SSH, VoIP) stays snappy even when a big download is running.

6. Access Point activation (Linux only)

hostapd is started with a configuration that:

Uses 802.11ac (VHT80) with 80 MHz channels, short guard interval, and beamforming flags.
Enforces WPA2‑AES (CCMP) – no TKIP.
Applies MAC address filtering (allow/deny lists) if configured.
The AP broadcasts the SSID you set (default ProNode-Hyper).
On macOS this step is skipped because you can’t create a true AP from the command line without Apple‑signed drivers; the bridge + QoS simulation takes its place.
7. Post‑setup speed test

Another speedtest-cli run.
The log now shows two sets of measurements – pre and post.
Why you may see higher numbers:

The channel is less crowded.
VHT80 (Linux) gives more raw throughput.
High MTU reduces overhead.
No power saving means the radio transmits at full strength.
QoS prevents other traffic from destroying your test.
The program logs something like:

text
Post‑setup: DL 210.5 Mbps, UL 42.8 Mbps, Ping 9 ms
8. Persistence service

The program creates a service file so that the whole setup survives reboots:

Linux: /etc/systemd/system/pro-node.service
It calls pro_node --run at boot. systemctl daemon-reload is executed.
macOS: /Library/LaunchDaemons/com.pro.node.plist
It loads with launchctl load -w and runs pro_node --run on boot.
If you run the program again, the service file is simply overwritten with the latest config.
9. What “hyper‑fast AP simulation” actually means

The phrase “AP simulation” acknowledges two things:

Linux – it’s a real AP. wlan1 in AP mode + hostapd = a genuine access point.
macOS – it’s simulated. You cannot create a true software AP with ifconfig alone, but by building a bridge, assigning IP aliases, and applying smart QoS, you mimic a multi‑interface, low‑latency gateway. Other devices can connect to this macOS machine (either via Ethernet or existing Wi‑Fi) and experience the same fast‑path prioritisation.
The “hyper‑fast” part comes from the combination of:

Interference‑free channel.
Wide 80 MHz channels & 802.11ac enhancements (Linux).
Jumbo‑like MTU.
Zero power‑saving delays.
QoS that always keeps your SSH/VoIP at the front of the line.
10. Cleanup when you stop

When you press Ctrl+C or run sudo pro_node --cleanup, everything is reversed:

hostapd is killed.
tc rules are removed from br0.
Virtual interfaces (wlan1, wlan2, br0, bat0) are deleted.
Power saving is re‑enabled.
pfctl and dnctl (macOS) are flushed and disabled.
The bridge (Linux/macOS) is torn down.
Your machine returns to its normal network state.

Summary: what you get after --run

Item	What it is
A new Wi‑Fi network (Linux)	Broadcasts your SSID with WPA2‑AES, wide 5 GHz channel, and MAC filtering.
Optimised underlying radio	Least‑congested channel, high MTU, no power‑saving.
Smart traffic shaping	SSH/VoIP never lag; bulk downloads are politely limited.
Bridged virtual interfaces (both OS)	Clients on different subnets share the same upstream, with QoS applied.
Mesh capability (Linux)	batman‑adv mesh routing active if you enable mesh.
Permanent service	The node restarts automatically on boot.
Complete audit log	Every action is recorded in JSON.
The goal is a resilient, low‑latency, high‑throughput node that behaves as if you had dedicated, professional‑grade AP hardware – but running on a standard Linux or macOS computer.