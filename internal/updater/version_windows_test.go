//go:build windows

package updater

import "testing"

func TestParseVersion(t *testing.T) {
	tests := []struct {
		in      string
		want    version
		wantErr bool
	}{
		{"v26.6.17", version{26, 6, 17}, false},
		{"26.6.17", version{26, 6, 17}, false},
		{"v26.9.0", version{26, 9, 0}, false},
		{"v26.6", version{}, true},      // missing N
		{"v26.6.17.1", version{}, true}, // extra component
		{"v26.foo.17", version{}, true}, // non-numeric month
		{"vfoo.6.17", version{}, true},  // non-numeric year
		{"v26.6.foo", version{}, true},  // non-numeric N
		{"", version{}, true},           // empty
		{"banana", version{}, true},     // garbage
		{"v26.-1.17", version{}, true},  // negative month
		{"v26.0.17", version{}, true},   // zero month
		{"v26.13.17", version{}, true},  // month exceeds 12
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := parseVersion(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseVersion(%q) err = %v, wantErr = %v", tt.in, err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("parseVersion(%q) = %+v, want %+v", tt.in, got, tt.want)
			}
		})
	}
}

func TestVersionLess(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		// String-comparison would flip these; numeric-comparison gets them right.
		{"26.9.21", "26.10.5", true},  // month: 9 < 10 numerically
		{"26.10.5", "26.9.21", false}, // reverse
		{"26.6.9", "26.6.10", true},   // n: 9 < 10 numerically
		{"26.6.10", "26.6.9", false},  // reverse
		// Year boundary.
		{"25.12.99", "26.1.0", true},
		// Equality is NOT less.
		{"26.6.17", "26.6.17", false},
		// Trivial ordering.
		{"1.1.1", "1.1.2", true},
		{"1.1.2", "1.1.1", false},
	}
	for _, tt := range tests {
		t.Run(tt.a+"_vs_"+tt.b, func(t *testing.T) {
			a, err := parseVersion(tt.a)
			if err != nil {
				t.Fatalf("parseVersion(%q): %v", tt.a, err)
			}
			b, err := parseVersion(tt.b)
			if err != nil {
				t.Fatalf("parseVersion(%q): %v", tt.b, err)
			}
			if got := a.less(b); got != tt.want {
				t.Errorf("%q.less(%q) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestVersionString(t *testing.T) {
	v, err := parseVersion("v26.6.17")
	if err != nil {
		t.Fatalf("parseVersion: %v", err)
	}
	if got := v.String(); got != "26.6.17" {
		t.Errorf("String() = %q, want %q", got, "26.6.17")
	}
}
