package plugins

import "testing"

func TestConstraint(t *testing.T) {
	c, err := ParseConstraint(">=1.26")
	if err != nil {
		t.Fatal(err)
	}
	for panel, want := range map[string]bool{
		"v1.26.0": true, "v1.26.3": true, "v1.27.0": true, "v2.0.0": true,
		"v1.25.9": false, "v1.0.0": false,
		"dev": true, "": true, "v1.24.4 (abc, built x)": false,
	} {
		if got := c.Satisfied(panel); got != want {
			t.Errorf(">=1.26 satisfied by %q = %v, want %v", panel, got, want)
		}
	}
	for _, bad := range []string{"1.26", ">1.26", ">=1", ">=1.26.x", "latest"} {
		if _, err := ParseConstraint(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestRange(t *testing.T) {
	for in, cases := range map[string]map[string]bool{
		"1.26.0 - 1.26.x": {"v1.26.0": true, "v1.26.9": true, "v1.27.0": false, "v1.25.9": false, "dev": true},
		"1.26.x":          {"v1.26.0": true, "v1.26.4": true, "v1.27.0": false},
		"1.26.2":          {"v1.26.2": true, "v1.26.3": false},
		"1.24 - 1.26.x":   {"v1.24.0": true, "v1.25.3": true, "v1.26.9": true, "v1.27.0": false},
	} {
		r, err := ParseRange(in)
		if err != nil {
			t.Errorf("%q: %v", in, err)
			continue
		}
		for panel, want := range cases {
			if got := r.Within(panel); got != want {
				t.Errorf("%q within %q = %v, want %v", panel, in, got, want)
			}
		}
	}
	for _, bad := range []string{"", "soon", "1.27.0 - 1.26.0", "1.x"} {
		if _, err := ParseRange(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestPanelVersion(t *testing.T) {
	for in, want := range map[string]string{"v1.24.4": "1.24.4", "v1.24.4 (abc, built x)": "1.24.4", "dev": "dev", "": "dev", "dev (none, built unknown)": "dev"} {
		if got := PanelVersion(in); got != want {
			t.Errorf("%q → %q, want %q", in, got, want)
		}
	}
}
