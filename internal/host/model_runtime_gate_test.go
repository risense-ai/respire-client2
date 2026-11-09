package host

import "testing"

func TestRuntimeAtLeast(t *testing.T) {
	cases := []struct {
		bin  string
		min  string
		want bool
	}{
		{"1.0.19", minVariantRuntimeVersion, false},
		{"1.0.20-dev.4", minVariantRuntimeVersion, false},
		{"1.0.20-dev.5", minVariantRuntimeVersion, true},
		{"1.0.20-dev.15", minVariantRuntimeVersion, true},
		{"1.0.20", minVariantRuntimeVersion, true},
		{"1.0.21-dev.1", minVariantRuntimeVersion, true},
		{"1.0.20-dev.5", minProfileRuntimeVersion, false},
		{"1.0.20-dev.9", minProfileRuntimeVersion, false},
		{"1.0.20-dev.10", minProfileRuntimeVersion, true},
		{"1.0.20", minProfileRuntimeVersion, true},
		{"", minVariantRuntimeVersion, false},
		{"garbage", minVariantRuntimeVersion, false},
		{"1.0", minVariantRuntimeVersion, false},
	}
	for _, c := range cases {
		if got := runtimeAtLeast(c.bin, c.min); got != c.want {
			t.Fatalf("runtimeAtLeast(%q, %q) = %v, want %v", c.bin, c.min, got, c.want)
		}
	}
}
