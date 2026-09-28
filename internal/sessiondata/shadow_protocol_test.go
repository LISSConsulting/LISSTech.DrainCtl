package sessiondata

import (
	"strings"
	"testing"
)

func TestShadowURIAndParseShadowURI(t *testing.T) {
	uri, err := ShadowURI("rdsh-01.example.test", 42)
	if err != nil {
		t.Fatalf("ShadowURI() error = %v", err)
	}
	if uri != "drainctl-shadow://shadow?host=rdsh-01.example.test&session=42" {
		t.Fatalf("ShadowURI() = %q", uri)
	}
	target, err := ParseShadowURI(uri)
	if err != nil {
		t.Fatalf("ParseShadowURI() error = %v", err)
	}
	if target != (ShadowTarget{Host: "rdsh-01.example.test", SessionID: 42}) {
		t.Fatalf("ParseShadowURI() = %#v", target)
	}
}

func TestParseShadowURIRejectsNonCanonicalOrAmbiguousInput(t *testing.T) {
	overseize := "drainctl-shadow://shadow?host=" + strings.Repeat("a", MaxShadowProtocolURILength) + "&session=1"
	for _, raw := range []string{
		"drainctl-shadow://shadow?host=rdsh-01.example.test&session=1&session=2",
		"drainctl-shadow://shadow?host=rdsh-01.example.test&host=other.example.test&session=1",
		"drainctl-shadow://shadow?host=rdsh-01.example.test&session=1&arg=/noConsentPrompt",
		"drainctl-shadow://shadow?host=rdsh-01.example.test%26session=2&session=1",
		"drainctl-shadow://shadow?session=1&host=rdsh-01.example.test",
		"drainctl-shadow://shadow?host=rdsh-01.example.test&session=01",
		"drainctl-shadow://shadow?host=RDSH-01.example.test&session=1",
		"drainctl-shadow://shadow/extra?host=rdsh-01.example.test&session=1",
		"drainctl-shadow://user@shadow?host=rdsh-01.example.test&session=1",
		"drainctl-shadow://shadow?host=rdsh-01.example.test&session=1#fragment",
		"drainctl-shadow://shadow?host=rdsh-01.example.test&session=4294967296",
		overseize,
	} {
		if _, err := ParseShadowURI(raw); err == nil {
			t.Errorf("ParseShadowURI(%q) accepted invalid URI", raw)
		}
	}
}
