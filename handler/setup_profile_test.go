package handler

import (
	"errors"
	"reflect"
	"testing"
)

func staticLookup(records map[string][]string) hostLookup {
	return func(host string) ([]string, error) {
		values, exists := records[host]
		if !exists {
			return nil, errors.New("not found")
		}
		return values, nil
	}
}

func TestAssessSetupAutoDirect(t *testing.T) {
	assessment, err := assessSetup(SetupModeAuto, "node.example.com", "203.0.113.10", staticLookup(map[string][]string{
		"node.example.com": {"203.0.113.10"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if assessment.EffectiveMode != SetupModeDirect || assessment.RequiresConfirmation {
		t.Fatalf("unexpected assessment: %#v", assessment)
	}
	if !reflect.DeepEqual(assessment.SuggestedProtocols, []string{"hysteria2", "vless-reality"}) {
		t.Fatalf("unexpected protocols: %#v", assessment.SuggestedProtocols)
	}
}

func TestAssessSetupAutoDoesNotAssumeCDN(t *testing.T) {
	assessment, err := assessSetup(SetupModeAuto, "node.example.com", "203.0.113.10", staticLookup(map[string][]string{
		"node.example.com": {"198.51.100.20"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if assessment.EffectiveMode != "" || !assessment.RequiresConfirmation {
		t.Fatalf("non-direct DNS was silently classified as CDN: %#v", assessment)
	}
	if !reflect.DeepEqual(assessment.SuggestedProtocols, []string{"vless-httpupgrade", "vless-reality"}) {
		t.Fatalf("unexpected suggestion: %#v", assessment.SuggestedProtocols)
	}
}

func TestAssessSetupExplicitCDNAndRealityModes(t *testing.T) {
	cdn, err := assessSetup(SetupModeCDN, "node.example.com", "203.0.113.10", staticLookup(map[string][]string{
		"node.example.com": {"198.51.100.20"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cdn.EffectiveMode != SetupModeCDN || cdn.RequiresConfirmation {
		t.Fatalf("unexpected CDN assessment: %#v", cdn)
	}

	reality, err := assessSetup(SetupModeReality, "", "203.0.113.10", staticLookup(nil))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reality.SuggestedProtocols, []string{"vless-reality"}) {
		t.Fatalf("unexpected Reality protocols: %#v", reality.SuggestedProtocols)
	}
}

func TestAssessSetupDirectRequiresMatchingDNS(t *testing.T) {
	_, err := assessSetup(SetupModeDirect, "node.example.com", "203.0.113.10", staticLookup(map[string][]string{
		"node.example.com": {"198.51.100.20"},
	}))
	if err == nil {
		t.Fatal("direct mode accepted DNS that does not point to the node")
	}
}
