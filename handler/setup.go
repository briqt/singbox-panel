package handler

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/briqt/singbox-panel/model"
)

type SetupHandler struct {
	Nodes  *model.NodeStore
	Config *ConfigHandler
	Ops    *NodeOpsHandler
}

type AutoSetupReq struct {
	Domain    string   `json:"domain"`
	Mode      string   `json:"mode"`
	Protocols []string `json:"protocols"`
	Ports     struct {
		Hysteria2   int `json:"hysteria2"`
		Reality     int `json:"reality"`
		HTTPUpgrade int `json:"httpupgrade"`
	} `json:"ports"`
}

var realitySNIs = []string{
	"www.apple.com",
	"www.microsoft.com",
	"www.amazon.com",
	"www.cloudflare.com",
	"www.mozilla.org",
	"www.samsung.com",
	"www.intel.com",
	"www.nvidia.com",
	"swcdn.apple.com",
	"updates.cdn-apple.com",
}

func (h *SetupHandler) HandleAutoSetup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}

	nodeID := parseNodeIDFromConfigPath(r.URL.Path)
	node, err := h.Nodes.Get(nodeID)
	if err != nil {
		writeError(w, http.StatusNotFound, "node not found")
		return
	}
	if node.ProxyType != "singbox" {
		writeError(w, http.StatusBadRequest, "auto-setup is only supported for singbox nodes")
		return
	}

	var req AutoSetupReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}

	domain := req.Domain
	if domain == "" {
		domain = node.Domain
	}
	if domain != "" && !validDomainName(domain) {
		writeError(w, http.StatusBadRequest, "invalid domain")
		return
	}

	protocols, assessment, err := suggestedProtocolsForRequest(req, node)
	if err != nil {
		status := http.StatusBadRequest
		if assessment != nil {
			status = http.StatusUnprocessableEntity
		}
		writeJSON(w, status, map[string]any{"error": err.Error(), "assessment": assessment})
		return
	}
	req.Protocols = protocols
	seenProtocols := make(map[string]bool, len(req.Protocols))
	for _, protocol := range req.Protocols {
		if seenProtocols[protocol] {
			writeError(w, http.StatusBadRequest, "duplicate protocol: "+protocol)
			return
		}
		seenProtocols[protocol] = true
		switch protocol {
		case "hysteria2", "vless-httpupgrade":
			if domain == "" {
				writeError(w, http.StatusBadRequest, protocol+" requires a domain")
				return
			}
		case "vless-reality":
		default:
			writeError(w, http.StatusBadRequest, "unsupported protocol: "+protocol)
			return
		}
	}
	for protocol, port := range map[string]int{
		"hysteria2": req.Ports.Hysteria2, "vless-reality": req.Ports.Reality, "vless-httpupgrade": req.Ports.HTTPUpgrade,
	} {
		if port < 0 || port > 65535 {
			writeError(w, http.StatusBadRequest, "invalid port for "+protocol)
			return
		}
	}

	// Connect to node
	client, err := h.Config.sshConnect(node)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ssh connect failed: "+err.Error())
		return
	}
	defer client.Close()

	type inboundResult struct {
		Protocol string `json:"protocol"`
		Port     int    `json:"port"`
		Status   string `json:"status"`
		Details  any    `json:"details,omitempty"`
	}
	var results []inboundResult

	// Existing protocols are updated in place when their domain or requested
	// port changes. Reality credentials remain stable unless the inbound is
	// explicitly deleted first.
	existingInbounds, err := h.Nodes.ListInbounds(node.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	existingProtos := map[string]model.NodeInbound{}
	for _, ib := range existingInbounds {
		existingProtos[ib.Protocol] = ib
	}
	if domain != node.Domain {
		selected := make(map[string]bool, len(req.Protocols))
		for _, protocol := range req.Protocols {
			selected[protocol] = true
		}
		for _, inbound := range existingInbounds {
			if (inbound.Protocol == "hysteria2" || inbound.Protocol == "vless-httpupgrade") && !selected[inbound.Protocol] {
				writeError(w, http.StatusConflict, "domain migration must include every existing domain-bound protocol")
				return
			}
		}
	}

	var createdInboundIDs []int
	var updatedInbounds []model.NodeInbound
	rollbackDatabase := func() error {
		var rollbackErrors []error
		for _, inboundID := range createdInboundIDs {
			if err := h.Nodes.DeleteInbound(inboundID); err != nil {
				rollbackErrors = append(rollbackErrors, fmt.Errorf("remove inbound %d: %w", inboundID, err))
			}
		}
		for _, inbound := range updatedInbounds {
			if _, err := h.Nodes.UpdateInbound(inbound.ID, model.CreateInboundReq{
				Tag: inbound.Tag, Protocol: inbound.Protocol, Port: inbound.Port, Settings: inbound.Settings,
			}); err != nil {
				rollbackErrors = append(rollbackErrors, fmt.Errorf("restore inbound %d: %w", inbound.ID, err))
			}
		}
		if domain != node.Domain {
			oldDomain := node.Domain
			if _, err := h.Nodes.Update(node.ID, model.UpdateNodeReq{Domain: &oldDomain}); err != nil {
				rollbackErrors = append(rollbackErrors, fmt.Errorf("restore node domain: %w", err))
			}
		}
		return errors.Join(rollbackErrors...)
	}

	hadError := false
	for _, proto := range req.Protocols {
		existing, exists := existingProtos[proto]
		switch proto {
		case "hysteria2":
			if domain == "" {
				results = append(results, inboundResult{Protocol: proto, Status: "error", Details: "domain is required"})
				hadError = true
				continue
			}
			runner := func(command string) (string, error) { return sshRun(client, command) }
			var oldSettings map[string]any
			currentPort := 0
			if exists {
				json.Unmarshal(existing.Settings, &oldSettings)
				currentPort = existing.Port
			}
			// The obfuscator password is a credential clients must carry, so it
			// is generated once and preserved across runs like the Reality keys.
			obfsPassword, _ := oldSettings["obfs_password"].(string)
			if obfsPassword == "" {
				obfsPassword = randomHex(16)
			}
			desiredPort := req.Ports.Hysteria2
			if desiredPort == 0 {
				desiredPort = selectListenPort(runner, "udp", currentPort)
			}
			if exists {
				oldDomain, _ := oldSettings["domain"].(string)
				_, hadObfs := oldSettings["obfs_password"].(string)
				if oldDomain == domain && desiredPort == existing.Port && hadObfs {
					results = append(results, inboundResult{Protocol: proto, Port: existing.Port, Status: "skipped", Details: "already configured"})
					continue
				}
			}
			ips, dnsErr := net.LookupHost(domain)
			if dnsErr != nil {
				results = append(results, inboundResult{Protocol: proto, Status: "error", Details: "DNS lookup failed: " + dnsErr.Error()})
				hadError = true
				continue
			}
			dnsOK := false
			for _, ip := range ips {
				if ip == node.Host {
					dnsOK = true
				}
			}
			if !dnsOK {
				results = append(results, inboundResult{Protocol: proto, Status: "error", Details: fmt.Sprintf("DNS: %s → %v, expected %s", domain, ips, node.Host)})
				hadError = true
				continue
			}
			port := desiredPort
			certPath := fmt.Sprintf("/etc/sing-box/tls/%s.crt", domain)
			keyPath := fmt.Sprintf("/etc/sing-box/tls/%s.key", domain)
			// Shares renewCertScript with the renew endpoint so there is one
			// authority for how a cert gets issued. It skips work only when the
			// existing cert is *valid*, not merely present.
			certOut, certErr := sshRun(client, renewCertScript(domain, certPath, keyPath, false, certRenewBeforeDays))
			certTgt := certTarget{Protocol: proto, Domain: domain, CertPath: certPath, KeyPath: keyPath}
			certState := readCertStatuses(client, []certTarget{certTgt}, time.Now())[certTgt.InboundID]
			if certState == nil || certState.Error != "" || certState.Expired {
				details := "cert install failed"
				if certState != nil && certState.Error != "" {
					details = "cert install failed: " + certState.Error
				} else if certState != nil && certState.Expired {
					details = "cert install failed: certificate on node is expired"
				}
				if certErr != nil {
					details += "; ssh: " + certErr.Error()
				}
				if tail := tailLines(certOut, 5); tail != "" {
					details += "; acme: " + tail
				}
				results = append(results, inboundResult{Protocol: proto, Port: port, Status: "error", Details: details})
				hadError = true
				continue
			}
			settings := mustMarshal(map[string]any{
				"domain": domain, "cert_path": certPath, "key_path": keyPath,
				"obfs_password": obfsPassword,
			})
			inboundReq := model.CreateInboundReq{Tag: "hysteria2", Protocol: "hysteria2", Port: port, Settings: settings}
			status := "ok"
			if exists {
				updatedInbounds = append(updatedInbounds, existing)
				if _, err := h.Nodes.UpdateInbound(existing.ID, inboundReq); err != nil {
					results = append(results, inboundResult{Protocol: proto, Port: port, Status: "error", Details: err.Error()})
					hadError = true
					continue
				}
				status = "updated"
			} else {
				inbound, err := h.Nodes.CreateInbound(node.ID, inboundReq)
				if err != nil {
					results = append(results, inboundResult{Protocol: proto, Port: port, Status: "error", Details: err.Error()})
					hadError = true
					continue
				}
				createdInboundIDs = append(createdInboundIDs, inbound.ID)
			}
			results = append(results, inboundResult{Protocol: proto, Port: port, Status: status})

		case "vless-reality":
			runner := func(command string) (string, error) { return sshRun(client, command) }
			// An existing Reality inbound used to be skipped outright, which
			// froze whatever handshake target and port it was first given. That
			// made every later improvement unreachable on exactly the nodes that
			// needed it. Re-evaluate both here, but keep the credentials: the
			// keypair and short ID are what issued subscriptions authenticate
			// with, so rotating them would break every client.
			var oldSettings map[string]any
			currentPort := 0
			if exists {
				json.Unmarshal(existing.Settings, &oldSettings)
				currentPort = existing.Port
			}
			privateKey, _ := oldSettings["private_key"].(string)
			publicKey, _ := oldSettings["public_key"].(string)
			shortID, _ := oldSettings["short_id"].(string)
			storedSNI, _ := oldSettings["sni"].(string)

			port := req.Ports.Reality
			if port == 0 {
				port = selectListenPort(runner, "tcp", currentPort)
			}
			if privateKey == "" || publicKey == "" {
				keypairOut, err := sshRun(client, node.SingboxBin+" generate reality-keypair")
				if err != nil {
					results = append(results, inboundResult{Protocol: proto, Port: port, Status: "error", Details: "keypair generation failed"})
					hadError = true
					continue
				}
				privateKey, publicKey = parseKeypair(keypairOut)
				if privateKey == "" || publicKey == "" {
					results = append(results, inboundResult{Protocol: proto, Port: port, Status: "error", Details: "keypair output was invalid"})
					hadError = true
					continue
				}
			}
			if shortID == "" {
				shortIDOut, _ := sshRun(client, node.SingboxBin+" generate rand 8 --hex")
				shortID = trimOutput(shortIDOut)
				if shortID == "" {
					shortID = randomHex(8)
				}
			}
			// Re-probe only when the stored target no longer behaves like the
			// site it impersonates. Rotating a healthy SNI on every run would
			// invalidate live subscriptions for no gain.
			sni := storedSNI
			if sni == "" || !realityDestStillQualifies(runner, sni) {
				probed, probeErr := selectRealitySNI(runner)
				if probeErr != nil {
					results = append(results, inboundResult{Protocol: proto, Port: port, Status: "error", Details: probeErr.Error()})
					hadError = true
					continue
				}
				sni = probed
			}
			if exists && port == currentPort && sni == storedSNI {
				results = append(results, inboundResult{Protocol: proto, Port: existing.Port, Status: "skipped", Details: "already configured"})
				continue
			}
			settings := mustMarshal(map[string]any{
				"sni": sni, "private_key": privateKey, "public_key": publicKey,
				"short_id": shortID, "handshake_server": sni, "handshake_port": 443,
				"fingerprint": "chrome",
			})
			inboundReq := model.CreateInboundReq{Tag: "vless-reality", Protocol: "vless-reality", Port: port, Settings: settings}
			status := "ok"
			if exists {
				updatedInbounds = append(updatedInbounds, existing)
				if _, err := h.Nodes.UpdateInbound(existing.ID, inboundReq); err != nil {
					results = append(results, inboundResult{Protocol: proto, Port: port, Status: "error", Details: err.Error()})
					hadError = true
					continue
				}
				status = "updated"
			} else {
				inbound, err := h.Nodes.CreateInbound(node.ID, inboundReq)
				if err != nil {
					results = append(results, inboundResult{Protocol: proto, Port: port, Status: "error", Details: err.Error()})
					hadError = true
					continue
				}
				createdInboundIDs = append(createdInboundIDs, inbound.ID)
			}
			results = append(results, inboundResult{Protocol: proto, Port: port, Status: status, Details: map[string]string{
				"public_key": publicKey, "short_id": shortID, "sni": sni,
			}})

		case "vless-httpupgrade":
			if domain == "" {
				results = append(results, inboundResult{Protocol: proto, Status: "error", Details: "domain is required"})
				hadError = true
				continue
			}
			var oldSettings map[string]any
			if exists {
				json.Unmarshal(existing.Settings, &oldSettings)
				oldDomain, _ := oldSettings["domain"].(string)
				if oldDomain == domain && (req.Ports.HTTPUpgrade == 0 || req.Ports.HTTPUpgrade == existing.Port) {
					results = append(results, inboundResult{Protocol: proto, Port: existing.Port, Status: "skipped", Details: "already configured"})
					continue
				}
			}
			port := req.Ports.HTTPUpgrade
			if port == 0 {
				if exists {
					port = existing.Port
				} else {
					port = 443
				}
			}
			path := ""
			if exists {
				path, _ = oldSettings["path"].(string)
			}
			if path == "" {
				path = "/" + randomHex(8)
			}

			// HTTPUpgrade behind CF requires Origin Certificate (cert_path + key_path)
			// Check if cert files already exist on node, or if provided in request
			certPath := fmt.Sprintf("/etc/sing-box/tls/%s.crt", domain)
			keyPath := fmt.Sprintf("/etc/sing-box/tls/%s.key", domain)
			certCheck, _ := sshRun(client, fmt.Sprintf("test -f %s && test -f %s && echo OK", certPath, keyPath))
			if !contains(certCheck, "OK") {
				results = append(results, inboundResult{Protocol: proto, Port: port, Status: "error", Details: "CF Origin Certificate required: upload cert/key via node settings first"})
				hadError = true
				continue
			}

			settings := map[string]any{"domain": domain, "path": path, "cert_path": certPath, "key_path": keyPath}
			inboundReq := model.CreateInboundReq{Tag: "vless-httpupgrade", Protocol: "vless-httpupgrade", Port: port, Settings: mustMarshal(settings)}
			status := "ok"
			if exists {
				updatedInbounds = append(updatedInbounds, existing)
				if _, err := h.Nodes.UpdateInbound(existing.ID, inboundReq); err != nil {
					results = append(results, inboundResult{Protocol: proto, Port: port, Status: "error", Details: err.Error()})
					hadError = true
					continue
				}
				status = "updated"
			} else {
				inbound, err := h.Nodes.CreateInbound(node.ID, inboundReq)
				if err != nil {
					results = append(results, inboundResult{Protocol: proto, Port: port, Status: "error", Details: err.Error()})
					hadError = true
					continue
				}
				createdInboundIDs = append(createdInboundIDs, inbound.ID)
			}
			results = append(results, inboundResult{Protocol: proto, Port: port, Status: status, Details: map[string]string{"path": path}})
		default:
			results = append(results, inboundResult{Protocol: proto, Status: "error", Details: "unsupported protocol"})
			hadError = true
		}
	}

	if hadError {
		if rollbackErr := rollbackDatabase(); rollbackErr != nil {
			writeError(w, http.StatusInternalServerError, "auto-setup failed and database rollback failed: "+rollbackErr.Error())
			return
		}
		var failureDetails []string
		for _, result := range results {
			if result.Status == "error" {
				failureDetails = append(failureDetails, result.Protocol+": "+fmt.Sprint(result.Details))
			}
		}
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"error":    "auto-setup failed; no changes were applied: " + strings.Join(failureDetails, "; "),
			"inbounds": results,
			"push":     "not attempted",
		})
		return
	}

	if domain != node.Domain {
		if _, err := h.Nodes.Update(node.ID, model.UpdateNodeReq{Domain: &domain}); err != nil {
			if rollbackErr := rollbackDatabase(); rollbackErr != nil {
				writeError(w, http.StatusInternalServerError, "update node domain failed and rollback failed: "+rollbackErr.Error())
				return
			}
			writeError(w, http.StatusInternalServerError, "update node domain: "+err.Error())
			return
		}
	}

	// Generate and push under the node lock so concurrent panel operations
	// cannot publish a configuration snapshot created before a newer change.
	syncResults := h.Config.SyncNodes([]int{node.ID})
	if syncErr := syncFailure(syncResults); syncErr != nil {
		if rollbackErr := rollbackDatabase(); rollbackErr != nil {
			writeError(w, http.StatusInternalServerError, "push failed and database rollback failed: "+rollbackErr.Error())
			return
		}
		restoreResults := h.Config.SyncNodes([]int{node.ID})
		if restoreErr := syncFailure(restoreResults); restoreErr != nil {
			writeError(w, http.StatusBadGateway, "changes rolled back, but restoring the previous node config failed: "+restoreErr.Error())
			return
		}
		writeError(w, http.StatusBadGateway, "push failed; changes rolled back: "+syncErr.Error())
		return
	}

	// Kernel baseline last: it never blocks the setup. A node whose sysctls did
	// not take is still a working node, just a slower one, and the caller gets
	// the read-back so the shortfall is visible instead of silent.
	tuning, tuneErr := applyKernelTuning(client)
	if tuneErr != nil {
		tuning = &KernelTuneResult{Note: "kernel tuning failed: " + tuneErr.Error()}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"inbounds": results, "push": "ok", "sync": syncResults, "node": node.Name,
		"assessment": assessment, "kernel_tuning": tuning,
	})
}

func randomPort() int {
	n, _ := rand.Int(rand.Reader, big.NewInt(30000))
	return int(n.Int64()) + 20000
}

// conventionalPorts are the ports a real HTTPS service plausibly listens on,
// in preference order.
//
// A random 20000-49999 port is an anomaly the protocol cannot paper over:
// REALITY borrows a big site's certificate, but no Apple or Microsoft edge
// serves TLS on 31795, and Hysteria2's QUIC on a fixed high UDP port is what
// carrier UDP QoS looks for. 443 first, then the alternates Cloudflare also
// terminates HTTPS on, so a scan sees a boring port either way.
var conventionalPorts = []int{443, 8443, 2053, 2083, 2087, 2096}

// selectListenPort returns the most conventional free port for network ("tcp"
// or "udp") on the node. currentPort is the port this inbound already owns; it
// counts as free so that re-running setup on an already-correct node is a
// no-op instead of walking down the preference list every time.
func selectListenPort(run commandRunner, network string, currentPort int) int {
	occupied, err := listeningPorts(run, network)
	if err != nil {
		// Never fail setup over a probe: fall back to what the node already
		// uses, or to the old random behaviour for a fresh inbound.
		if currentPort != 0 {
			return currentPort
		}
		return randomPort()
	}
	delete(occupied, currentPort)
	for _, port := range conventionalPorts {
		if !occupied[port] {
			return port
		}
	}
	if currentPort != 0 {
		return currentPort
	}
	return randomPort()
}

// listeningPorts reads the node's listening sockets for one L4 network. TCP and
// UDP are asked separately: Reality is TCP and Hysteria2 is UDP, so the same
// port number can legitimately be free for one and taken by the other.
func listeningPorts(run commandRunner, network string) (map[int]bool, error) {
	flag := "-lnt"
	if network == "udp" {
		flag = "-lnu"
	}
	out, err := run("ss " + flag + " 2>/dev/null || netstat -ln" + strings.TrimPrefix(flag, "-ln"))
	if err != nil && strings.TrimSpace(out) == "" {
		return nil, fmt.Errorf("unable to read listening ports on node")
	}
	ports := parseListeningPorts(out)
	if len(ports) == 0 {
		// A node always has at least sshd listening. An empty parse means the
		// output shape was not what we expected, and treating "parsed nothing"
		// as "everything is free" would hand out a port already in use.
		return nil, fmt.Errorf("listening-port probe returned no usable rows")
	}
	return ports, nil
}

func parseListeningPorts(output string) map[int]bool {
	ports := map[int]bool{}
	for _, line := range strings.Split(output, "\n") {
		for _, field := range strings.Fields(line) {
			idx := strings.LastIndex(field, ":")
			if idx < 0 {
				continue
			}
			port, err := strconv.Atoi(field[idx+1:])
			if err != nil || port <= 0 || port > 65535 {
				continue
			}
			ports[port] = true
		}
	}
	return ports
}

func randomHex(bytes int) string {
	b := make([]byte, bytes)
	rand.Read(b)
	return hex.EncodeToString(b)
}

type commandRunner func(command string) (string, error)

func selectRealitySNI(run commandRunner) (string, error) {
	out, err := run(realityProbeScript())
	if err != nil && strings.TrimSpace(out) == "" {
		return "", fmt.Errorf("unable to probe Reality handshake targets")
	}
	host, _ := parseFastestRealityProbe(out)
	if host == "" {
		return "", fmt.Errorf("no Reality handshake target served HTTP/2 over TLS 1.3 with a non-error status")
	}
	return host, nil
}

// realityProbeScript measures every candidate from the node itself. It reports
// three things per host, not one: a handshake target has to *behave* like the
// site it impersonates, and latency alone cannot tell us that.
func realityProbeScript() string {
	return realityProbeScriptFor(realitySNIs...)
}

func realityProbeScriptFor(hosts ...string) string {
	var script strings.Builder
	for _, host := range hosts {
		fmt.Fprintf(&script,
			"(metric=$(curl -sS -o /dev/null --connect-timeout 3 --max-time 5 --tlsv1.3 --tls-max 1.3 -w '%%{http_version} %%{http_code} %%{time_appconnect}' 'https://%s/' 2>/dev/null) && printf '%s %%s\\n' \"$metric\") &\n",
			host, host)
	}
	script.WriteString("wait\n")
	return script.String()
}

// realityDestQualifies decides whether a probed candidate may serve as a
// handshake target.
//
// Ranking on latency alone picks CDN edges, and a CDN edge is exactly the wrong
// answer: updates.cdn-apple.com won on all three direct nodes while answering
// HTTP/1.1 with a 403. REALITY forwards a failed probe to this target, so a
// prober sees a "big-site" TLS certificate attached to a server that speaks no
// h2 and refuses the root path — an inconsistency the real site never shows.
// The property that matters is behavioural plausibility; latency only breaks
// ties among candidates that already have it.
// realityDestStillQualifies re-runs the same judgement against one already
// chosen target. A dest that was fine when the node was provisioned can stop
// being fine — sites drop h2, start redirecting, or begin refusing the node's
// region — and the node would keep impersonating it regardless.
func realityDestStillQualifies(run commandRunner, host string) bool {
	out, err := run(realityProbeScriptFor(host))
	if err != nil && strings.TrimSpace(out) == "" {
		// Probe failure is not evidence the target is bad; leave it alone
		// rather than churning a working node's SNI on a transient SSH error.
		return true
	}
	probed, _ := parseRealityProbe(out, map[string]bool{host: true})
	return probed == host
}

func realityDestQualifies(httpVersion string, statusCode int) bool {
	if httpVersion != "2" && httpVersion != "3" {
		return false
	}
	return statusCode >= 200 && statusCode < 400
}

func parseFastestRealityProbe(output string) (string, float64) {
	allowed := make(map[string]bool, len(realitySNIs))
	for _, host := range realitySNIs {
		allowed[host] = true
	}
	return parseRealityProbe(output, allowed)
}

// parseRealityProbe keeps an allow-list so a probe line can only ever name a
// host we asked about; the output is shell-produced text and must not be able
// to nominate an arbitrary handshake target.
func parseRealityProbe(output string, allowed map[string]bool) (string, float64) {
	bestHost := ""
	bestLatency := 0.0
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 4 || !allowed[fields[0]] {
			continue
		}
		statusCode, err := strconv.Atoi(fields[2])
		if err != nil {
			continue
		}
		if !realityDestQualifies(fields[1], statusCode) {
			continue
		}
		latency, err := strconv.ParseFloat(fields[3], 64)
		if err != nil || latency <= 0 {
			continue
		}
		if bestHost == "" || latency < bestLatency {
			bestHost = fields[0]
			bestLatency = latency
		}
	}
	return bestHost, bestLatency
}

func parseKeypair(output string) (privateKey, publicKey string) {
	for _, line := range splitLines(output) {
		if contains(line, "PrivateKey:") {
			privateKey = trimOutput(line[len("PrivateKey:"):])
		}
		if contains(line, "PublicKey:") {
			publicKey = trimOutput(line[len("PublicKey:"):])
		}
	}
	return
}

func splitLines(s string) []string {
	var result []string
	for _, line := range strings.Split(s, "\n") {
		result = append(result, strings.TrimSpace(line))
	}
	return result
}

func trimOutput(s string) string {
	return strings.TrimSpace(s)
}

func contains(s, sub string) bool {
	return strings.Contains(s, sub)
}

func mustMarshal(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
