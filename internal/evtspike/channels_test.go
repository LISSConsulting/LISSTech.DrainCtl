//go:build windows

package evtspike

import (
	"slices"
	"strings"
	"testing"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
)

func containsFold(list []string, name string) bool {
	for _, n := range list {
		if strings.EqualFold(n, name) {
			return true
		}
	}
	return false
}

func TestResolveChannels_DefaultsReturns54Items(t *testing.T) {
	got := ResolveChannels(dc.EvtSpikeConfig{})
	if len(got) != len(Defaults) {
		t.Fatalf("len(ResolveChannels(zero cfg)) = %d, want %d", len(got), len(Defaults))
	}
	if len(got) != 54 {
		t.Fatalf("Defaults list should have 54 channels, got %d", len(got))
	}
	if containsFold(got, SecurityChannel) {
		t.Errorf("zero config must not include %q", SecurityChannel)
	}
}

func TestResolveChannels_DisabledRemovesCaseInsensitive(t *testing.T) {
	cases := []struct {
		name    string
		disable string
		target  string
	}{
		{"exact", "Application", "Application"},
		{"lowercase", "application", "Application"},
		{"uppercase", "APPLICATION", "Application"},
		{"mixed", "MicroSOFT-Windows-NTLM/OperaTIONAL", "Microsoft-Windows-NTLM/Operational"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := dc.EvtSpikeConfig{DisabledChannels: []string{tc.disable}}
			got := ResolveChannels(cfg)
			if containsFold(got, tc.target) {
				t.Errorf("expected %q to be absent after disabling %q, got %v", tc.target, tc.disable, got)
			}
			if len(got) != len(Defaults)-1 {
				t.Errorf("len = %d, want %d (one default dropped)", len(got), len(Defaults)-1)
			}
		})
	}
}

func TestResolveChannels_AddedAppends(t *testing.T) {
	extras := []string{"Custom-App/Operational", "My-Service/Admin"}
	cfg := dc.EvtSpikeConfig{AddedChannels: extras}
	got := ResolveChannels(cfg)
	if len(got) != len(Defaults)+len(extras) {
		t.Fatalf("len = %d, want %d", len(got), len(Defaults)+len(extras))
	}
	for i, name := range extras {
		want := len(Defaults) + i
		if got[want] != name {
			t.Errorf("got[%d] = %q, want %q", want, got[want], name)
		}
	}
}

func TestResolveChannels_AddedDuplicateDedupes(t *testing.T) {
	cases := []struct {
		name string
		add  []string
	}{
		{"exact match of default", []string{"Application"}},
		{"case-insensitive match of default", []string{"application"}},
		{"duplicate within AddedChannels", []string{"Custom/One", "custom/one"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := dc.EvtSpikeConfig{AddedChannels: tc.add}
			got := ResolveChannels(cfg)
			seen := make(map[string]int)
			for _, n := range got {
				seen[strings.ToLower(n)]++
			}
			for k, c := range seen {
				if c > 1 {
					t.Errorf("channel %q appeared %d times, want 1", k, c)
				}
			}
		})
	}
}

func TestResolveChannels_SecurityEnabledAddsSecurity(t *testing.T) {
	cfg := dc.EvtSpikeConfig{SecurityChannelEnabled: true}
	got := ResolveChannels(cfg)
	if !containsFold(got, SecurityChannel) {
		t.Fatalf("expected %q in resolved channels, got %v", SecurityChannel, got)
	}
	if len(got) != len(Defaults)+1 {
		t.Errorf("len = %d, want %d", len(got), len(Defaults)+1)
	}
}

func TestResolveChannels_SecurityDisabledOmitsSecurityFromDefaults(t *testing.T) {
	orig := Defaults
	t.Cleanup(func() { Defaults = orig })
	Defaults = slices.Concat(orig, []string{SecurityChannel})

	cfg := dc.EvtSpikeConfig{SecurityChannelEnabled: false}
	got := ResolveChannels(cfg)
	if containsFold(got, SecurityChannel) {
		t.Errorf("Security must be dropped from Defaults when flag is false, got %v", got)
	}
	if len(got) != len(orig) {
		t.Errorf("len = %d, want %d (original defaults after Security dropped)", len(got), len(orig))
	}
}
