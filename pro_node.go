// =============================================================================
// pro_node.go – Cross‑Platform Hyper‑Fast Access Point Simulator
//
// Copyright (c) 2025 Devin B. Royal. All rights reserved.
// Original IP – engineered to deliver a permanent “Pro” networking node with
// hyper‑fast AP simulation, fast‑path QoS, bridging, mesh (Linux), and
// persistent system services.
//
// Build:
//   go build -o pro_node pro_node.go
//
// Usage (must run as root):
//   pro_node --run               # full setup + measurement + service
//   pro_node --status            # live stats (SSID, channel, clients, mesh)
//   pro_node --cleanup           # tear down all virtual interfaces and QoS
//   pro_node --install-deps      # install required packages
//   pro_node --generate-service  # create systemd / launchd unit
//   pro_node --help              # this text
//
// Configuration:
//   Linux:  /etc/pro_node.conf
//   macOS:  /usr/local/etc/pro_node.conf
//   Environment variables override config values (see loadConfig()).
//
// Logging (JSON):
//   Linux:  /var/log/pro_node.log
//   macOS:  /usr/local/var/log/pro_node.log
//
// Architecture:
//   - Physical interface: wlan0 (Linux) / en0 (macOS)
//   - Virtual AP:         wlan1/wlan2 (Linux) / en1/en2 (macOS, simulated)
//   - Bridge:             br0 (Linux) / bridge100 (macOS)
//   - Mesh:               bat0 via batman-adv (Linux only)
//   - QoS:                tc + HTB + SFQ (Linux), pfctl + dnctl (macOS)
//
// Perceived speed gains come from:
//   - Dynamic 5 GHz channel selection (least‑congested)
//   - 802.11ac VHT80 + short GI + beamforming (Linux hostapd)
//   - MTU 2304 on all interfaces
//   - Disabled Wi‑Fi power saving
//   - Fast‑path queuing: SSH/VoIP → premium, HTTP → limited, bulk → fair
//
// Limitations:
//   - macOS cannot create a true software AP; we simulate via Internet Sharing,
//     bridge interfaces, and QoS rules. en1/en2 are provided as aliases.
//   - Mesh (batman-adv) is Linux‑only.
//   - Requires external tools installed by --install‑deps.
// =============================================================================

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"math"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ---------------------------------------------------------------------------
// Structured logging
// ---------------------------------------------------------------------------

type LogEntry struct {
	Timestamp string `json:"timestamp"`
	Level     string `json:"level"`
	Component string `json:"component"`
	Message   string `json:"message"`
	TraceID   string `json:"trace_id,omitempty"`
}

var (
	logWriter io.Writer
	traceID   string
)

func initLog(w io.Writer, tid string) {
	logWriter = w
	traceID = tid
}

func log(level, comp, msg string) {
	entry := LogEntry{
		Timestamp: time.Now().Format(time.RFC3339Nano),
		Level:     level,
		Component: comp,
		Message:   msg,
		TraceID:   traceID,
	}
	data, _ := json.Marshal(entry)
	fmt.Fprintln(logWriter, string(data))
}

func logInfo(comp, msg string)  { log("INFO", comp, msg) }
func logWarn(comp, msg string)  { log("WARN", comp, msg) }
func logError(comp, msg string) { log("ERROR", comp, msg) }
func logBrand(comp, msg string) {
	logInfo(comp, fmt.Sprintf("⚡ %s ⚡", msg))
}

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------

type Config struct {
	SSID               string
	Passphrase         string // never logged
	Channel            string // "auto" or explicit 5GHz channel
	MTU                int
	DownloadBandwidth  string
	UploadBandwidth    string
	SSHPriorityRate    string
	HTTPLimitRate      string
	VoIPPorts          []string
	AdminEmail         string
	SMTPHost           string
	SMTPPort           string
	SMTPUser           string
	SMTPPass           string // never logged
	MACAllowList       []string
	MACDenyList        []string
	BrandMessage       string
	EnableMesh         bool // Linux only
	ForceChannel       bool
}

var config = Config{
	SSID:              "ProNode-Hyper",
	Passphrase:        "HyperFast!2025",
	Channel:           "auto",
	MTU:               2304,
	DownloadBandwidth: "1000mbit",
	UploadBandwidth:   "1000mbit",
	SSHPriorityRate:   "2mbit",
	HTTPLimitRate:     "20mbit",
	VoIPPorts:         []string{"5060", "5061", "10000-20000"},
	AdminEmail:        "",
	SMTPHost:          "localhost",
	SMTPPort:          "25",
	BrandMessage:      "GOT UM. Hyper‑Fast Node Activated",
	EnableMesh:        true,
}

// OS‑specific paths and interface names
var (
	osName       string
	physIface    string
	virtIface1   string
	virtIface2   string
	bridgeIface  string
	meshIface    string
	configFile   string
	logFile      string
	serviceDir   string
	serviceName  string
	serviceCmd   string // path to our own binary
	isLinux      bool
	isMacOS      bool
)

func detectOS() {
	switch runtime.GOOS {
	case "linux":
		osName = "Linux"
		isLinux = true
		physIface = "wlan0"
		virtIface1 = "wlan1"
		virtIface2 = "wlan2"
		bridgeIface = "br0"
		meshIface = "bat0"
		configFile = "/etc/pro_node.conf"
		logFile = "/var/log/pro_node.log"
		serviceDir = "/etc/systemd/system"
		serviceName = "pro-node.service"
		serviceCmd = "/usr/local/bin/pro_node"
	case "darwin":
		osName = "macOS"
		isMacOS = true
		physIface = "en0"
		virtIface1 = "en1"
		virtIface2 = "en2"
		bridgeIface = "bridge100"
		meshIface = "" // no mesh on macOS
		configFile = "/usr/local/etc/pro_node.conf"
		logFile = "/usr/local/var/log/pro_node.log"
		serviceDir = "/Library/LaunchDaemons"
		serviceName = "com.pro.node.plist"
		serviceCmd = "/usr/local/bin/pro_node"
	default:
		fmt.Fprintf(os.Stderr, "Unsupported OS: %s\n", runtime.GOOS)
		os.Exit(1)
	}
}

// ---------------------------------------------------------------------------
// Command runner with retry & backoff
// ---------------------------------------------------------------------------

func runCmd(ctx context.Context, tag string, args ...string) (string, error) {
	const maxRetries = 5
	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		cmd := exec.CommandContext(ctx, args[0], args[1:]...)
		out, err := cmd.CombinedOutput()
		if err == nil {
			logInfo(tag, fmt.Sprintf("cmd success: %s", strings.Join(args, " ")))
			return string(out), nil
		}
		lastErr = fmt.Errorf("%s: %v (output: %s)", tag, err, string(out))
		logWarn(tag, fmt.Sprintf("attempt %d/%d failed: %v", attempt+1, maxRetries, lastErr))
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(time.Duration(math.Pow(2, float64(attempt))) * time.Second):
		}
	}
	return "", lastErr
}

// run with at most one attempt (for cleanup etc.)
func runCmdOnce(tag string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		logWarn(tag, fmt.Sprintf("cmd failed: %v (output: %s)", err, string(out)))
		return err
	}
	logInfo(tag, fmt.Sprintf("cmd ok: %s", strings.Join(args, " ")))
	return nil
}

// ---------------------------------------------------------------------------
// Configuration loading & validation
// ---------------------------------------------------------------------------

func loadConfig() {
	if data, err := ioutil.ReadFile(configFile); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			parts := strings.SplitN(line, "=", 2)
			if len(parts) != 2 {
				continue
			}
			key := strings.TrimSpace(parts[0])
			val := strings.TrimSpace(parts[1])
			switch key {
			case "SSID": config.SSID = sanitizeSSID(val)
			case "PASSWORD": config.Passphrase = val
			case "CHANNEL": config.Channel = sanitizeChannel(val)
			case "MTU": config.MTU = atoi(val)
			case "DOWNLOAD_BANDWIDTH": config.DownloadBandwidth = val
			case "UPLOAD_BANDWIDTH": config.UploadBandwidth = val
			case "SSH_PRIORITY_RATE": config.SSHPriorityRate = val
			case "HTTP_LIMIT_RATE": config.HTTPLimitRate = val
			case "VOIP_PORTS": config.VoIPPorts = strings.Split(val, ",")
			case "ADMIN_EMAIL": config.AdminEmail = val
			case "SMTP_HOST": config.SMTPHost = val
			case "SMTP_PORT": config.SMTPPort = val
			case "SMTP_USER": config.SMTPUser = val
			case "SMTP_PASS": config.SMTPPass = val
			case "BRAND_MESSAGE": config.BrandMessage = val
			case "ENABLE_MESH": config.EnableMesh = strings.ToLower(val) == "true"
			case "MAC_ALLOW":
				for _, m := range strings.Split(val, ",") {
					m = strings.TrimSpace(m)
					if m != "" {
						config.MACAllowList = append(config.MACAllowList, m)
					}
				}
			case "MAC_DENY":
				for _, m := range strings.Split(val, ",") {
					m = strings.TrimSpace(m)
					if m != "" {
						config.MACDenyList = append(config.MACDenyList, m)
					}
				}
			}
		}
	}

	// environment overrides
	if v := os.Getenv("PRO_SSID"); v != "" {
		config.SSID = sanitizeSSID(v)
	}
	if v := os.Getenv("PRO_PASSWORD"); v != "" {
		config.Passphrase = v
	}
	if v := os.Getenv("PRO_CHANNEL"); v != "" {
		config.Channel = sanitizeChannel(v)
	}
	if v := os.Getenv("PRO_MTU"); v != "" {
		config.MTU = atoi(v)
	}
	if v := os.Getenv("PRO_DOWN"); v != "" {
		config.DownloadBandwidth = v
	}
	if v := os.Getenv("PRO_UP"); v != "" {
		config.UploadBandwidth = v
	}
	if v := os.Getenv("PRO_SSH_RATE"); v != "" {
		config.SSHPriorityRate = v
	}
	if v := os.Getenv("PRO_HTTP_LIMIT"); v != "" {
		config.HTTPLimitRate = v
	}
	if v := os.Getenv("PRO_EMAIL"); v != "" {
		config.AdminEmail = v
	}
	if v := os.Getenv("PRO_BRAND"); v != "" {
		config.BrandMessage = v
	}

	if config.MTU < 1 {
		config.MTU = 2304
	}
}

func sanitizeSSID(s string) string {
	re := regexp.MustCompile(`[^a-zA-Z0-9 ._\-]`)
	s = re.ReplaceAllString(s, "")
	if len(s) > 32 {
		s = s[:32]
	}
	return s
}

func sanitizeChannel(c string) string {
	if c == "auto" {
		return "auto"
	}
	valid := map[string]bool{
		"36": true, "40": true, "44": true, "48": true,
		"149": true, "153": true, "157": true, "161": true, "165": true,
	}
	if valid[c] {
		return c
	}
	return "auto"
}

func atoi(s string) int {
	i, _ := strconv.Atoi(s)
	return i
}

// ---------------------------------------------------------------------------
// Dynamic channel selection (5 GHz)
// ---------------------------------------------------------------------------

func scanAndSelectChannel(ctx context.Context) string {
	logInfo("channel", "Starting 5 GHz channel scan")
	var args []string
	if isLinux {
		args = []string{"iw", "dev", physIface, "scan", "freq", "5180", "5200", "5220", "5240", "5745", "5765", "5785", "5805", "5825"}
	} else {
		args = []string{"/System/Library/PrivateFrameworks/Apple80211.framework/Versions/Current/Resources/airport", "-s"}
	}
	out, err := runCmd(ctx, "channel-scan", args...)
	if err != nil {
		logWarn("channel", "scan failed, defaulting to channel 36")
		return "36"
	}

	scores := map[string]int{
		"36": 0, "40": 0, "44": 0, "48": 0,
		"149": 0, "153": 0, "157": 0, "161": 0, "165": 0,
	}
	for ch := range scores {
		scores[ch] = strings.Count(out, ch)
	}

	best := "36"
	bestScore := 99999
	for ch, sc := range scores {
		if sc < bestScore {
			bestScore = sc
			best = ch
		}
	}
	logInfo("channel", fmt.Sprintf("Selected channel %s (congestion score %d)", best, bestScore))
	return best
}

// ---------------------------------------------------------------------------
// Interface and bridge setup
// ---------------------------------------------------------------------------

func setupVirtualInterfaces(ctx context.Context) error {
	logInfo("iface", "Creating virtual/bridge infrastructure")
	if isLinux {
		// create virtual AP interfaces
		if err := runCmdOnce("iface-wlan1", "iw", "dev", physIface, "interface", "add", virtIface1, "type", "__ap"); err != nil {
			return fmt.Errorf("add wlan1: %v", err)
		}
		// optional wlan2
		runCmdOnce("iface-wlan2", "iw", "dev", physIface, "interface", "add", virtIface2, "type", "__ap") // best effort

		// set MTU
		for _, iface := range []string{physIface, virtIface1, virtIface2} {
			runCmdOnce("mtu-"+iface, "ip", "link", "set", "dev", iface, "mtu", strconv.Itoa(config.MTU))
		}
		// disable power saving
		runCmdOnce("power-save-off", "iw", "dev", physIface, "set", "power_save", "off")

		// bridge
		runCmdOnce("bridge-add", "ip", "link", "add", "name", bridgeIface, "type", "bridge")
		runCmdOnce("bridge-add-phys", "ip", "link", "set", "dev", physIface, "master", bridgeIface)
		runCmdOnce("bridge-add-virt1", "ip", "link", "set", "dev", virtIface1, "master", bridgeIface)
		runCmdOnce("bridge-up", "ip", "link", "set", "dev", bridgeIface, "up")

		// mesh (batman-adv) if enabled
		if config.EnableMesh {
			runCmdOnce("modprobe-batman", "modprobe", "batman-adv")
			runCmdOnce("mesh-add", "ip", "link", "add", "name", meshIface, "type", "batadv")
			runCmdOnce("mesh-master", "ip", "link", "set", "dev", bridgeIface, "master", meshIface)
			runCmdOnce("mesh-up", "ip", "link", "set", "dev", meshIface, "up")
		}
	} else if isMacOS {
		// Disable power saving (airport)
		runCmdOnce("power-off", "pmset", "-a", "womp", "0")
		// Create software bridge (bridge100 often used by Internet Sharing)
		if err := runCmdOnce("bridge-create", "ifconfig", bridgeIface, "create"); err != nil {
			logWarn("iface", "bridge create failed, might already exist")
		}
		runCmdOnce("bridge-add-en0", "ifconfig", bridgeIface, "addm", physIface)
		// Bring up
		runCmdOnce("bridge-up", "ifconfig", bridgeIface, "up")
		// Set MTU
		for _, iface := range []string{physIface, bridgeIface} {
			runCmdOnce("mtu-"+iface, "ifconfig", iface, "mtu", strconv.Itoa(config.MTU))
		}
		// Simulate en1 / en2 as aliases to bridge (IP aliases)
		// We'll assign them IP addresses in the same subnet to simulate separate interfaces.
		runCmdOnce("en1-alias", "ifconfig", bridgeIface, "inet", "192.168.44.1", "netmask", "255.255.255.0", "alias")
		runCmdOnce("en2-alias", "ifconfig", bridgeIface, "inet", "192.168.45.1", "netmask", "255.255.255.0", "alias")
		logInfo("iface", "Simulated en1 (192.168.44.1) and en2 (192.168.45.1) on bridge")
	}
	logBrand("iface", "Virtual interfaces and bridge ready")
	return nil
}

// ---------------------------------------------------------------------------
// QoS & Fast‑Path Shaping
// ---------------------------------------------------------------------------

func applyQoS(ctx context.Context) error {
	logInfo("qos", "Applying fast‑path QoS")
	if isLinux {
		// Clean existing qdisc
		runCmdOnce("qos-clean", "tc", "qdisc", "del", "dev", bridgeIface, "root")

		// HTB root
		if _, err := runCmd(ctx, "qos-root", "tc", "qdisc", "add", "dev", bridgeIface, "root", "handle", "1:", "htb", "default", "30"); err != nil {
			return err
		}
		// Root class
		if _, err := runCmd(ctx, "qos-root-class", "tc", "class", "add", "dev", bridgeIface, "parent", "1:", "classid", "1:1", "htb", "rate", config.DownloadBandwidth, "ceil", config.DownloadBandwidth); err != nil {
			return err
		}
		// Premium class (SSH, VoIP)
		premRate := config.SSHPriorityRate
		if _, err := runCmd(ctx, "qos-prem-class", "tc", "class", "add", "dev", bridgeIface, "parent", "1:1", "classid", "1:10", "htb", "rate", premRate, "ceil", config.DownloadBandwidth, "prio", "0"); err != nil {
			return err
		}
		// HTTP limited class
		if _, err := runCmd(ctx, "qos-http-class", "tc", "class", "add", "dev", bridgeIface, "parent", "1:1", "classid", "1:20", "htb", "rate", config.HTTPLimitRate, "ceil", config.DownloadBandwidth, "prio", "2"); err != nil {
			return err
		}
		// Default bulk class
		if _, err := runCmd(ctx, "qos-def-class", "tc", "class", "add", "dev", bridgeIface, "parent", "1:1", "classid", "1:30", "htb", "rate", "5mbit", "ceil", config.DownloadBandwidth, "prio", "1"); err != nil {
			return err
		}

		// Attach SFQ to classes
		for _, cid := range []string{"1:10", "1:20", "1:30"} {
			handle := strings.Replace(cid, ":", "", 1)
			runCmdOnce("qos-sfq-"+cid, "tc", "qdisc", "add", "dev", bridgeIface, "parent", cid, "handle", handle+":", "sfq", "perturb", "10")
		}

		// Filters for SSH (22), VoIP common ports, HTTP (80,443)
		// SSH
		if _, err := runCmd(ctx, "qos-filter-ssh", "tc", "filter", "add", "dev", bridgeIface, "protocol", "ip", "parent", "1:0", "prio", "1", "u32", "match", "ip", "dport", "22", "0xffff", "flowid", "1:10"); err != nil {
			return err
		}
		// VoIP – typical ports
		for _, port := range config.VoIPPorts {
			if strings.Contains(port, "-") {
				// port range not easily done with u32 single match; skip in this streamlined version
				continue
			}
			runCmdOnce("qos-filter-voip-"+port, "tc", "filter", "add", "dev", bridgeIface, "protocol", "ip", "parent", "1:0", "prio", "1", "u32", "match", "ip", "dport", port, "0xffff", "flowid", "1:10")
		}
		// HTTP/HTTPS
		for _, port := range []string{"80", "443"} {
			runCmdOnce("qos-filter-http-"+port, "tc", "filter", "add", "dev", bridgeIface, "protocol", "ip", "parent", "1:0", "prio", "2", "u32", "match", "ip", "dport", port, "0xffff", "flowid", "1:20")
		}
	} else if isMacOS {
		// macOS: use pfctl + dnctl
		// Flush existing
		runCmdOnce("pfctl-disable", "pfctl", "-d")
		// dnctl pipes
		runCmdOnce("dnctl-pipe-root", "dnctl", "pipe", "1", "config", "bw", config.DownloadBandwidth)
		runCmdOnce("dnctl-queue-ssh", "dnctl", "queue", "1", "config", "pipe", "1", "mask", "src-ip", "0xffffffff")

		// Build pf rules
		var rules strings.Builder
		rules.WriteString("set block-policy return\n")
		rules.WriteString("scrub in all\n")
		if len(config.MACAllowList) > 0 {
			rules.WriteString(fmt.Sprintf("table <allowmac> { %s }\n", strings.Join(config.MACAllowList, " ")))
			rules.WriteString("block in quick from !<allowmac>\n")
		}
		if len(config.MACDenyList) > 0 {
			rules.WriteString(fmt.Sprintf("table <denymac> { %s }\n", strings.Join(config.MACDenyList, " ")))
			rules.WriteString("block in quick from <denymac>\n")
		}

		// Fast‑path tags
		rules.WriteString("pass in quick proto tcp from any to any port 22 keep state queue (ssh_queue)\n")
		for _, port := range config.VoIPPorts {
			if !strings.Contains(port, "-") {
				rules.WriteString(fmt.Sprintf("pass in quick proto udp from any to any port %s keep state queue (ssh_queue)\n", port))
			}
		}
		rules.WriteString("pass in quick proto tcp from any to any port {80,443} keep state queue (http_queue)\n")
		rules.WriteString("pass in all\n")

		tmpFile := "/tmp/pro_node_pf.conf"
		if err := ioutil.WriteFile(tmpFile, []byte(rules.String()), 0644); err != nil {
			return err
		}
		if err := runCmdOnce("pfctl-load", "pfctl", "-f", tmpFile); err != nil {
			return err
		}
		runCmdOnce("pfctl-enable", "pfctl", "-e")
	}
	logBrand("qos", "Fast‑path QoS rules active")
	return nil
}

// ---------------------------------------------------------------------------
// hostapd (Linux AP)
// ---------------------------------------------------------------------------

func startHostAPd(ctx context.Context, channel string) error {
	if !isLinux {
		return nil
	}
	conf := fmt.Sprintf(`interface=%s
driver=nl80211
ssid=%s
hw_mode=a
channel=%s
ieee80211ac=1
vht_oper_chwidth=1
vht_oper_centr_freq_seg0_idx=%s
vht_capab=[SHORT-GI-80][BEAMFORMEE][BEAMFORMER]
wpa=2
wpa_passphrase=%s
wpa_key_mgmt=WPA-PSK
wpa_pairwise=CCMP
rsn_pairwise=CCMP
auth_algs=1
`, virtIface1, config.SSID, channel, channel, config.Passphrase)

	// MAC filtering
	if len(config.MACAllowList) > 0 || len(config.MACDenyList) > 0 {
		deny := "0"
		if len(config.MACDenyList) > 0 {
			deny = "1"
		}
		conf += fmt.Sprintf("macaddr_acl=%s\n", deny)
		if len(config.MACAllowList) > 0 {
			ioutil.WriteFile("/tmp/pro_node_accept", []byte(strings.Join(config.MACAllowList, "\n")), 0644)
			conf += "accept_mac_file=/tmp/pro_node_accept\n"
		}
		if len(config.MACDenyList) > 0 {
			ioutil.WriteFile("/tmp/pro_node_deny", []byte(strings.Join(config.MACDenyList, "\n")), 0644)
			conf += "deny_mac_file=/tmp/pro_node_deny\n"
		}
	}

	tmpConf := "/tmp/pro_node_hostapd.conf"
	if err := ioutil.WriteFile(tmpConf, []byte(conf), 0600); err != nil {
		return err
	}
	if _, err := runCmd(ctx, "hostapd-start", "hostapd", "-B", tmpConf); err != nil {
		return err
	}
	logInfo("hostapd", "Access point active")
	return nil
}

// ---------------------------------------------------------------------------
// Speedtest measurement
// ---------------------------------------------------------------------------

func runSpeedtest(ctx context.Context, label string) (download, upload, ping float64) {
	args := []string{"speedtest-cli", "--json"}
	out, err := runCmd(ctx, "speedtest-"+label, args...)
	if err != nil {
		logError("speedtest", fmt.Sprintf("%s failed: %v", label, err))
		return 0, 0, 0
	}
	var result struct {
		Download float64 `json:"download"`
		Upload   float64 `json:"upload"`
		Ping     float64 `json:"ping"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		logError("speedtest", fmt.Sprintf("JSON parse error: %v", err))
		return 0, 0, 0
	}
	return result.Download / 1e6, result.Upload / 1e6, result.Ping
}

// ---------------------------------------------------------------------------
// Status display
// ---------------------------------------------------------------------------

func showStatus(ctx context.Context) {
	fmt.Println("=== Pro Node Hyper‑Fast Status ===")
	fmt.Printf("SSID:        %s\n", config.SSID)
	fmt.Printf("Channel:     %s\n", config.Channel)
	fmt.Printf("MTU:         %d\n", config.MTU)
	if isLinux {
		out, err := runCmd(ctx, "status-clients", "hostapd_cli", "-i", virtIface1, "all_sta")
		if err == nil {
			clients := strings.Count(out, "dot11RSNAStatsSTAAddress")
			fmt.Printf("Clients:     %d\n", clients)
		}
		out, err = runCmd(ctx, "mesh-neigh", "batctl", "n")
		if err == nil {
			fmt.Println("Mesh neighbors:")
			fmt.Println(out)
		}
	} else {
		fmt.Println("Clients:     (simulated, check arp -a)")
	}
}

// ---------------------------------------------------------------------------
// Persistence (service files)
// ---------------------------------------------------------------------------

func generateServiceFiles() error {
	if isLinux {
		unit := `[Unit]
Description=Pro Node Hyper‑Fast Access Point
After=network.target
[Service]
Type=simple
RemainAfterExit=yes
Restart=on-failure
RestartSec=5
ExecStart=%s --run
ExecStop=%s --cleanup
Environment="PRO_SSID=%s"
Environment="PRO_PASSWORD=%s"
[Install]
WantedBy=multi-user.target
`
		content := fmt.Sprintf(unit, serviceCmd, serviceCmd, config.SSID, config.Passphrase)
		path := filepath.Join(serviceDir, serviceName)
		if err := ioutil.WriteFile(path, []byte(content), 0644); err != nil {
			return err
		}
		runCmdOnce("systemctl-reload", "systemctl", "daemon-reload")
		logInfo("service", "systemd unit created at "+path)
	} else if isMacOS {
		plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>com.pro.node</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
		<string>--run</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<dict>
		<key>SuccessfulExit</key>
		<false/>
	</dict>
	<key>EnvironmentVariables</key>
	<dict>
		<key>PRO_SSID</key>
		<string>%s</string>
		<key>PRO_PASSWORD</key>
		<string>%s</string>
	</dict>
</dict>
</plist>`
		content := fmt.Sprintf(plist, serviceCmd, config.SSID, config.Passphrase)
		os.MkdirAll(serviceDir, 0755)
		path := filepath.Join(serviceDir, serviceName)
		if err := ioutil.WriteFile(path, []byte(content), 0644); err != nil {
			return err
		}
		runCmdOnce("launchctl-load", "launchctl", "load", "-w", path)
		logInfo("service", "LaunchDaemon plist created at "+path)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Cleanup (teardown)
// ---------------------------------------------------------------------------

func cleanup(ctx context.Context) {
	logInfo("cleanup", "Performing teardown")
	if isLinux {
		runCmdOnce("cleanup-hostapd", "killall", "hostapd")
		runCmdOnce("cleanup-tc", "tc", "qdisc", "del", "dev", bridgeIface, "root")
		if config.EnableMesh {
			runCmdOnce("cleanup-mesh-down", "ip", "link", "set", "dev", meshIface, "down")
		}
		runCmdOnce("cleanup-bridge-down", "ip", "link", "set", "dev", bridgeIface, "down")
		for _, iface := range []string{virtIface1, virtIface2} {
			runCmdOnce("cleanup-del-"+iface, "iw", "dev", iface, "del")
		}
		runCmdOnce("cleanup-del-bridge", "ip", "link", "del", bridgeIface)
		runCmdOnce("cleanup-power-on", "iw", "dev", physIface, "set", "power_save", "on")
	} else if isMacOS {
		runCmdOnce("cleanup-pf-disable", "pfctl", "-d")
		runCmdOnce("cleanup-dnctl-flush", "dnctl", "flush")
		runCmdOnce("cleanup-bridge-down", "ifconfig", bridgeIface, "down")
		runCmdOnce("cleanup-bridge-destroy", "ifconfig", bridgeIface, "destroy")
		runCmdOnce("cleanup-power-on", "pmset", "-a", "womp", "1")
	}
	logInfo("cleanup", "Cleanup completed")
}

// ---------------------------------------------------------------------------
// Signal handler
// ---------------------------------------------------------------------------

func setupSignalHandler(cancel context.CancelFunc) {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		logInfo("signal", "Received termination signal")
		cancel()
	}()
}

// ---------------------------------------------------------------------------
// Install dependencies
// ---------------------------------------------------------------------------

func installDeps(ctx context.Context) error {
	logInfo("install", "Installing dependencies")
	if isLinux {
		pkgs := []string{"hostapd", "iw", "batctl", "bridge-utils", "tc", "iproute2", "python3", "speedtest-cli", "mailutils"}
		if _, err := runCmd(ctx, "apt-update", "apt-get", "update"); err != nil {
			return err
		}
		args := append([]string{"apt-get", "install", "-y"}, pkgs...)
		if _, err := runCmd(ctx, "apt-install", args...); err != nil {
			// fallback: try pip for speedtest-cli
			runCmdOnce("pip-speedtest", "pip3", "install", "speedtest-cli")
			return fmt.Errorf("apt install failed: %v", err)
		}
	} else if isMacOS {
		if _, err := exec.LookPath("brew"); err != nil {
			// install homebrew
			cmd := exec.Command("/bin/bash", "-c", "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)")
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			if err := cmd.Run(); err != nil {
				return fmt.Errorf("Homebrew installation failed: %v", err)
			}
		}
		runCmdOnce("brew-update", "brew", "update")
		runCmdOnce("brew-install-speedtest", "brew", "install", "speedtest-cli")
	}
	return nil
}

// ---------------------------------------------------------------------------
// Email notification (optional)
// ---------------------------------------------------------------------------

func notify(ctx context.Context, subject, body string) {
	if config.AdminEmail == "" || config.SMTPHost == "" {
		return
	}
	// rudimentary SMTP sender (no auth for localhost; for external servers implement AUTH)
	msg := fmt.Sprintf("To: %s\r\nSubject: %s\r\n\r\n%s", config.AdminEmail, subject, body)
	// Since we want to avoid heavy dependencies, we'll just pipe to sendmail if available
	cmd := exec.CommandContext(ctx, "sendmail", "-t")
	cmd.Stdin = strings.NewReader(msg)
	if err := cmd.Run(); err != nil {
		logWarn("notify", "Failed to send email: "+err.Error())
	} else {
		logInfo("notify", "Email sent to "+config.AdminEmail)
	}
}

// ---------------------------------------------------------------------------
// Main
// ---------------------------------------------------------------------------

func main() {
	// Flags
	var (
		flagRun            bool
		flagStatus         bool
		flagCleanup        bool
		flagInstallDeps    bool
		flagGenService     bool
		flagShowConfig     bool
		flagHelp           bool
	)
	flag.BoolVar(&flagRun, "run", false, "Full hyper‑fast setup and run")
	flag.BoolVar(&flagStatus, "status", false, "Show live status")
	flag.BoolVar(&flagCleanup, "cleanup", false, "Tear down everything")
	flag.BoolVar(&flagInstallDeps, "install-deps", false, "Install required OS packages")
	flag.BoolVar(&flagGenService, "generate-service", false, "Generate persistence service file")
	flag.BoolVar(&flagShowConfig, "config", false, "Print current configuration (secrets redacted)")
	flag.BoolVar(&flagHelp, "help", false, "Show usage")
	flag.Parse()

	if flagHelp {
		fmt.Println(`Pro Node Hyper‑Fast AP Simulator

Usage:
  pro_node [flags]

Flags:
  --run                Full setup: interfaces, channel, QoS, AP, speedtest, service
  --status             Show current SSID, channel, clients, mesh (Linux)
  --cleanup            Remove all virtual interfaces, QoS, services
  --install-deps       Install required packages (Linux: apt, macOS: brew)
  --generate-service   Create systemd/launchd unit for persistence
  --config             Print current config (passwords hidden)
  --help               This text

Must be run as root.`)
		return
	}

	// OS detection and root check
	detectOS()
	if os.Geteuid() != 0 {
		fmt.Fprintf(os.Stderr, "Error: must be run as root.\n")
		os.Exit(1)
	}

	// Setup logging
	os.MkdirAll(filepath.Dir(logFile), 0755)
	f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Cannot open log: %v\n", err)
		f = os.Stderr
	}
	defer f.Close()
	// Use multi-writer to also output to stderr during interactive use
	initLog(io.MultiWriter(f, os.Stderr), time.Now().Format("20060102-150405"))

	loadConfig()

	if flagShowConfig {
		fmt.Println("Current configuration (passwords redacted):")
		fmt.Printf("  SSID:            %s\n", config.SSID)
		fmt.Printf("  Channel:         %s\n", config.Channel)
		fmt.Printf("  MTU:             %d\n", config.MTU)
		fmt.Printf("  Download bw:     %s\n", config.DownloadBandwidth)
		fmt.Printf("  Upload bw:       %s\n", config.UploadBandwidth)
		fmt.Printf("  SSH priority:    %s\n", config.SSHPriorityRate)
		fmt.Printf("  HTTP limit:      %s\n", config.HTTPLimitRate)
		fmt.Println("  Passphrase:      ********")
		return
	}

	// Create a root context with cancellation for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle signals for long‑running --run
	setupSignalHandler(cancel)

	// Command routing
	switch {
	case flagInstallDeps:
		if err := installDeps(ctx); err != nil {
			logError("main", fmt.Sprintf("install-deps failed: %v", err))
			os.Exit(1)
		}
	case flagGenService:
		if err := generateServiceFiles(); err != nil {
			logError("main", fmt.Sprintf("generate-service: %v", err))
			os.Exit(1)
		}
	case flagStatus:
		showStatus(ctx)
	case flagCleanup:
		cleanup(ctx)
	case flagRun:
		logBrand("main", config.BrandMessage)

		// Pre‑speedtest
		preDL, preUL, prePing := runSpeedtest(ctx, "pre")
		logInfo("measure", fmt.Sprintf("Pre‑setup: DL %.2f Mbps, UL %.2f Mbps, Ping %.2f ms", preDL, preUL, prePing))

		// Channel selection
		channel := config.Channel
		if channel == "auto" {
			channel = scanAndSelectChannel(ctx)
		}

		// Setup interfaces
		if err := setupVirtualInterfaces(ctx); err != nil {
			logError("main", "Interface setup failed: "+err.Error())
			cleanup(ctx)
			os.Exit(1)
		}

		// Apply QoS
		if err := applyQoS(ctx); err != nil {
			logError("main", "QoS setup failed: "+err.Error())
			cleanup(ctx)
			os.Exit(1)
		}

		// Start AP (Linux)
		if err := startHostAPd(ctx, channel); err != nil {
			logError("main", "hostapd failed: "+err.Error())
			cleanup(ctx)
			os.Exit(1)
		}

		// Post‑speedtest
		postDL, postUL, postPing := runSpeedtest(ctx, "post")
		logInfo("measure", fmt.Sprintf("Post‑setup: DL %.2f Mbps, UL %.2f Mbps, Ping %.2f ms", postDL, postUL, postPing))

		// Summary notification
		summary := fmt.Sprintf("Hyper‑Fast Node is live.\nPre:  DL %.2f, UL %.2f, Ping %.2f\nPost: DL %.2f, UL %.2f, Ping %.2f\nSSID: %s, Channel: %s",
			preDL, preUL, prePing, postDL, postUL, postPing, config.SSID, channel)
		notify(ctx, "Pro Node Setup Complete", summary)

		// Generate persistence service (idempotent)
		if err := generateServiceFiles(); err != nil {
			logWarn("main", "Service file generation failed: "+err.Error())
		}

		logBrand("main", "Hyper‑fast node active – press Ctrl+C to stop")
		// Block until context cancelled (by signal)
		<-ctx.Done()
		logInfo("main", "Shutting down")
		cleanup(context.Background()) // new context for cleanup
	default:
		fmt.Fprintln(os.Stderr, "No action specified. Use --help for usage.")
		os.Exit(1)
	}
}