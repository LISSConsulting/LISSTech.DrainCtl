//go:build windows

package sessionlimit

import "testing"

func TestNormalize(t *testing.T) {
	tests := []struct {
		name  string
		value uint64
		want  int
		ok    bool
	}{
		{name: "finite", value: 250, want: 250, ok: true},
		{name: "largest finite", value: Unlimited - 1, want: int(Unlimited - 1), ok: true},
		{name: "unset", value: 0},
		{name: "deployed unlimited", value: Unlimited},
		{name: "policy unlimited", value: 999999},
		{name: "DWORD unlimited", value: 1<<32 - 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := Normalize(tt.value)
			if got != tt.want || ok != tt.ok {
				t.Errorf("Normalize(%d) = (%d, %t), want (%d, %t)", tt.value, got, ok, tt.want, tt.ok)
			}
		})
	}
}
