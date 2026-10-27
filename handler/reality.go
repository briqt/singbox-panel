package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/briqt/singbox-panel/model"
)

// Reality targets are measured, not guessed.
//
// The candidate list in setup.go is a starting point, not an answer: whether a
// handshake target is usable depends on the node's own vantage point. The same
// host can answer HTTP/2 with 200 from Tokyo and fail to connect from Los
// Angeles — www.deepseek.com does exactly that — so "is this target good?" is a
// per-node question with a per-node answer. These two endpoints make that answer
// observable over HTTP instead of in someone's terminal.

type realityTargetProbe struct {
	Host      string  `json:"host"`
	HTTP      string  `json:"http,omitempty"`
	Status    int     `json:"status,omitempty"`
	LatencyMS float64 `json:"latency_ms,omitempty"`
	Qualifies bool    `json:"qualifies"`
	Current   bool    `json:"current,omitempty"`
	Note      string  `json:"note,omitempty"`
}

type realityTargetsResponse struct {
	Node     string               `json:"node"`
	Current  string               `json:"current,omitempty"`
	ProbedAt string               `json:"probed_at"`
	Targets  []realityTargetProbe `json:"targets"`
}

// HandleRealityTargets probes every candidate from the node and reports what the
// node actually sees. It is read-only: nothing is written, nothing restarts.
func (h *NodeOpsHandler) HandleRealityTargets(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.NotFound(w, r)
		return
	}
	node, ok := h.loadNode(w, r)
	if !ok {
		return
	}
	inbounds, err := h.Nodes.ListInbounds(node.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	current := ""
	for _, inbound := range inbounds {
		if inbound.Protocol == "vless-reality" {
			current = realitySNIOf(inbound)
			break
		}
	}

	// ?host=... measures something the candidate list does not cover, which is
	// how a target that only works from this one node gets found at all.
	candidates := append([]string(nil), realitySNIs...)
	for _, extra := range r.URL.Query()["host"] {
		host := strings.TrimSpace(extra)
		if host == "" || !validDomainName(host) || slices.Contains(candidates, host) {
			continue
		}
		candidates = append(candidates, host)
	}

	client, err := h.Config.sshConnect(node)
	if err != nil {
		writeError(w, http.StatusBadGateway, "ssh connect failed: "+err.Error())
		return
	}
	defer client.Close()

	out, runErr := sshRun(client, realityProbeScriptFor(candidates...))
	if runErr != nil && strings.TrimSpace(out) == "" {
		writeError(w, http.StatusBadGateway, "probe failed: "+runErr.Error())
		return
	}

	rows := probeAllRealityTargetsFor(out, candidates)
	for i := range rows {
		rows[i].Current = rows[i].Host == current
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Qualifies != rows[j].Qualifies {
			return rows[i].Qualifies
		}
		if !rows[i].Qualifies {
			return rows[i].Host < rows[j].Host
		}
		return rows[i].LatencyMS < rows[j].LatencyMS
	})

	writeJSON(w, http.StatusOK, realityTargetsResponse{
		Node:     node.Name,
		Current:  current,
		ProbedAt: time.Now().UTC().Format(time.RFC3339),
		Targets:  rows,
	})
}

// HandleRealityTarget switches one node's Reality handshake target.
//
// It deliberately does not run auto-setup: ports, keys, short_id and the
// subscribed identity stay exactly as they are, because those are the fields
// that invalidate a client's existing subscription. Only the camouflage target
// changes, and only after the node itself confirms the new target answers TLS
// 1.3 with HTTP/2 and a status a real site would return.
func (h *NodeOpsHandler) HandleRealityTarget(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	node, ok := h.loadNode(w, r)
	if !ok {
		return
	}

	var req struct {
		SNI string `json:"sni"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if !validDomainName(req.SNI) {
		writeError(w, http.StatusBadRequest, "sni must be a domain name")
		return
	}

	inbounds, err := h.Nodes.ListInbounds(node.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var target *model.NodeInbound
	for i := range inbounds {
		if inbounds[i].Protocol == "vless-reality" {
			target = &inbounds[i]
			break
		}
	}
	if target == nil {
		writeError(w, http.StatusBadRequest, "node has no vless-reality inbound")
		return
	}

	client, err := h.Config.sshConnect(node)
	if err != nil {
		writeError(w, http.StatusBadGateway, "ssh connect failed: "+err.Error())
		return
	}
	out, runErr := sshRun(client, realityProbeScriptFor(req.SNI))
	client.Close()
	if runErr != nil && strings.TrimSpace(out) == "" {
		writeError(w, http.StatusBadGateway, "probe failed: "+runErr.Error())
		return
	}
	if probed, _ := parseRealityProbe(out, map[string]bool{req.SNI: true}); probed != req.SNI {
		writeError(w, http.StatusUnprocessableEntity,
			fmt.Sprintf("%s did not answer TLS 1.3 with HTTP/2 and a non-error status from this node", req.SNI))
		return
	}

	settings := map[string]any{}
	if len(target.Settings) > 0 {
		if err := json.Unmarshal(target.Settings, &settings); err != nil {
			writeError(w, http.StatusInternalServerError, "stored settings are not readable: "+err.Error())
			return
		}
	}
	previous, _ := settings["sni"].(string)
	if previous == req.SNI {
		writeJSON(w, http.StatusOK, map[string]any{
			"node": node.Name, "previous": previous, "sni": req.SNI, "status": "unchanged",
		})
		return
	}
	settings["sni"] = req.SNI
	settings["handshake_server"] = req.SNI
	raw, err := json.Marshal(settings)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	original := append(json.RawMessage(nil), target.Settings...)
	if _, err := h.Nodes.UpdateInbound(target.ID, model.CreateInboundReq{
		Tag: target.Tag, Protocol: target.Protocol, Port: target.Port, Settings: raw,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "store new target: "+err.Error())
		return
	}

	// The database is not the node. If the push fails the record would claim a
	// camouflage target the running config does not have, so put it back rather
	// than leaving the two disagreeing.
	results := h.Config.SyncNodes([]int{node.ID})
	if err := syncFailure(results); err != nil {
		if _, rollbackErr := h.Nodes.UpdateInbound(target.ID, model.CreateInboundReq{
			Tag: target.Tag, Protocol: target.Protocol, Port: target.Port, Settings: original,
		}); rollbackErr != nil {
			writeError(w, http.StatusInternalServerError,
				"push failed and the stored target could not be restored: "+rollbackErr.Error())
			return
		}
		writeError(w, http.StatusBadGateway, "push failed, stored target reverted: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"node":     node.Name,
		"previous": previous,
		"sni":      req.SNI,
		"status":   "updated",
		"push":     results[0],
	})
}

func (h *NodeOpsHandler) loadNode(w http.ResponseWriter, r *http.Request) (*model.Node, bool) {
	node, err := h.Nodes.Get(parseNodeIDFromConfigPath(r.URL.Path))
	if err != nil {
		writeError(w, http.StatusNotFound, "node not found")
		return nil, false
	}
	return node, true
}

func realitySNIOf(inbound model.NodeInbound) string {
	settings := map[string]any{}
	if len(inbound.Settings) > 0 {
		_ = json.Unmarshal(inbound.Settings, &settings)
	}
	if sni, _ := settings["sni"].(string); sni != "" {
		return sni
	}
	if server, _ := settings["handshake_server"].(string); server != "" {
		return server
	}
	return ""
}

// probeAllRealityTargets turns the probe's shell output into one row per
// candidate. The probe only prints hosts that answered, so a candidate missing
// from the output is reported as a failure rather than silently dropped — that
// absence is the whole point of asking.
func probeAllRealityTargets(output string) []realityTargetProbe {
	return probeAllRealityTargetsFor(output, realitySNIs)
}

func probeAllRealityTargetsFor(output string, candidates []string) []realityTargetProbe {
	allowed := make(map[string]bool, len(candidates))
	for _, host := range candidates {
		allowed[host] = true
	}
	rows := make([]realityTargetProbe, 0, len(candidates))
	seen := make(map[string]bool, len(candidates))
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 4 || !allowed[fields[0]] || seen[fields[0]] {
			continue
		}
		seen[fields[0]] = true
		row := realityTargetProbe{Host: fields[0], HTTP: fields[1]}
		if code, err := strconv.Atoi(fields[2]); err == nil {
			row.Status = code
		}
		if latency, err := strconv.ParseFloat(fields[3], 64); err == nil {
			row.LatencyMS = roundMilliseconds(latency)
		}
		row.Qualifies = row.Status != 0 && realityDestQualifies(row.HTTP, row.Status)
		if !row.Qualifies {
			row.Note = describeProbeFailure(row)
		}
		rows = append(rows, row)
	}
	for _, host := range candidates {
		if !seen[host] {
			rows = append(rows, realityTargetProbe{
				Host: host, Qualifies: false,
				Note: "no response: connect failed or timed out from this node",
			})
		}
	}
	return rows
}

func describeProbeFailure(row realityTargetProbe) string {
	if row.HTTP != "2" && row.HTTP != "3" {
		return fmt.Sprintf("answered HTTP/%s; REALITY needs HTTP/2 or /3", row.HTTP)
	}
	return fmt.Sprintf("root path returned %d; a real site would not", row.Status)
}

func roundMilliseconds(seconds float64) float64 {
	return float64(int(seconds*10000)) / 10
}
