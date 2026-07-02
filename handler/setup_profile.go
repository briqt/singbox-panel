package handler

import (
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/briqt/singbox-panel/model"
)

type SetupMode string

const (
	SetupModeAuto    SetupMode = "auto"
	SetupModeDirect  SetupMode = "direct"
	SetupModeCDN     SetupMode = "cdn"
	SetupModeReality SetupMode = "reality"
)

type SetupAssessment struct {
	Mode                 SetupMode `json:"mode"`
	EffectiveMode        SetupMode `json:"effective_mode,omitempty"`
	Domain               string    `json:"domain"`
	DNSStatus            string    `json:"dns_status"`
	DNSRecords           []string  `json:"dns_records"`
	DirectToNode         bool      `json:"direct_to_node"`
	SuggestedProtocols   []string  `json:"suggested_protocols"`
	RequiresConfirmation bool      `json:"requires_confirmation"`
	Warnings             []string  `json:"warnings"`
}

type hostLookup func(string) ([]string, error)

func normalizeSetupMode(mode string) (SetupMode, error) {
	if mode == "" {
		return SetupModeAuto, nil
	}
	switch SetupMode(mode) {
	case SetupModeAuto, SetupModeDirect, SetupModeCDN, SetupModeReality:
		return SetupMode(mode), nil
	default:
		return "", fmt.Errorf("invalid setup mode %q", mode)
	}
}

func assessSetup(mode SetupMode, domain, nodeHost string, lookup hostLookup) (SetupAssessment, error) {
	assessment := SetupAssessment{
		Mode:       mode,
		Domain:     domain,
		DNSStatus:  "not_required",
		DNSRecords: []string{},
		Warnings:   []string{},
	}

	if mode == SetupModeReality || domain == "" {
		if domain == "" && (mode == SetupModeDirect || mode == SetupModeCDN) {
			return assessment, fmt.Errorf("%s mode requires a domain", mode)
		}
		assessment.EffectiveMode = SetupModeReality
		assessment.SuggestedProtocols = []string{"vless-reality"}
		return assessment, nil
	}
	if !validDomainName(domain) {
		return assessment, fmt.Errorf("invalid domain")
	}

	records, err := lookup(domain)
	if err != nil || len(records) == 0 {
		assessment.DNSStatus = "error"
		assessment.RequiresConfirmation = true
		assessment.Warnings = append(assessment.Warnings, "domain DNS lookup failed; no deployment mode was inferred")
		return assessment, fmt.Errorf("DNS lookup failed for %s", domain)
	}
	assessment.DNSStatus = "resolved"
	assessment.DNSRecords = uniqueStrings(records)
	assessment.DirectToNode = recordsPointToNode(records, nodeHost, lookup)

	switch mode {
	case SetupModeDirect:
		if !assessment.DirectToNode {
			return assessment, fmt.Errorf("direct mode requires the domain to resolve to the node host")
		}
		assessment.EffectiveMode = SetupModeDirect
		assessment.SuggestedProtocols = []string{"hysteria2", "vless-reality"}
		assessment.Warnings = append(assessment.Warnings, "Hysteria2 requires client-side UDP connectivity; Reality is kept as the TCP fallback")
	case SetupModeCDN:
		assessment.EffectiveMode = SetupModeCDN
		assessment.SuggestedProtocols = []string{"vless-httpupgrade", "vless-reality"}
		if assessment.DirectToNode {
			assessment.Warnings = append(assessment.Warnings, "domain currently resolves directly to the node; confirm that CDN mode is intentional")
		}
	case SetupModeAuto:
		if assessment.DirectToNode {
			assessment.EffectiveMode = SetupModeDirect
			assessment.SuggestedProtocols = []string{"hysteria2", "vless-reality"}
			assessment.Warnings = append(assessment.Warnings, "Hysteria2 requires client-side UDP connectivity; Reality is kept as the TCP fallback")
		} else {
			assessment.SuggestedProtocols = []string{"vless-httpupgrade", "vless-reality"}
			assessment.RequiresConfirmation = true
			assessment.Warnings = append(assessment.Warnings, "DNS does not point directly to the node, but this alone cannot prove that the domain uses a supported CDN; select CDN mode explicitly")
		}
	default:
		return assessment, fmt.Errorf("invalid setup mode %q", mode)
	}
	return assessment, nil
}

func recordsPointToNode(records []string, nodeHost string, lookup hostLookup) bool {
	nodeAddresses := []string{nodeHost}
	if net.ParseIP(nodeHost) == nil {
		if resolved, err := lookup(nodeHost); err == nil {
			nodeAddresses = resolved
		}
	}
	for _, record := range records {
		recordIP := net.ParseIP(record)
		if recordIP == nil {
			continue
		}
		for _, address := range nodeAddresses {
			addressIP := net.ParseIP(address)
			if addressIP != nil && recordIP.Equal(addressIP) {
				return true
			}
		}
	}
	return false
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func (h *SetupHandler) HandleSetupAssessment(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
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
		writeError(w, http.StatusBadRequest, "setup assessment is only supported for singbox nodes")
		return
	}
	mode, err := normalizeSetupMode(r.URL.Query().Get("mode"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	domain := strings.TrimSpace(r.URL.Query().Get("domain"))
	if domain == "" {
		domain = node.Domain
	}
	assessment, err := assessSetup(mode, domain, node.Host, net.LookupHost)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"error":      err.Error(),
			"assessment": assessment,
		})
		return
	}
	writeJSON(w, http.StatusOK, assessment)
}

func suggestedProtocolsForRequest(req AutoSetupReq, node *model.Node) ([]string, *SetupAssessment, error) {
	mode, err := normalizeSetupMode(req.Mode)
	if err != nil {
		return nil, nil, err
	}
	if len(req.Protocols) > 0 {
		if mode == SetupModeAuto {
			return req.Protocols, nil, nil
		}
		domain := strings.TrimSpace(req.Domain)
		if domain == "" {
			domain = node.Domain
		}
		assessment, assessErr := assessSetup(mode, domain, node.Host, net.LookupHost)
		return req.Protocols, &assessment, assessErr
	}
	domain := strings.TrimSpace(req.Domain)
	if domain == "" {
		domain = node.Domain
	}
	assessment, err := assessSetup(mode, domain, node.Host, net.LookupHost)
	if err != nil {
		return nil, &assessment, err
	}
	if assessment.RequiresConfirmation {
		return nil, &assessment, fmt.Errorf("automatic setup needs an explicit deployment mode")
	}
	return assessment.SuggestedProtocols, &assessment, nil
}
