package version

import "testing"

func TestValidate(t *testing.T) {
	for _, value := range []string{"0.1.0-dev", "1.2.3", "1.2.3-alpha.1+build.7"} {
		if err := Validate(value); err != nil {
			t.Errorf("Validate(%q): %v", value, err)
		}
	}
	for _, value := range []string{"v1.2.3", "01.2.3", "1.2.3-alpha..1", "1.2.3-01", "1.2.3+", "1.2"} {
		if err := Validate(value); err == nil {
			t.Errorf("Validate(%q) succeeded", value)
		}
	}
}

func TestCompareSemanticVersions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		left  string
		right string
		want  int
	}{
		{left: "0.1.0", right: "0.1.0", want: 0},
		{left: "0.1.0+left", right: "0.1.0+right", want: 0},
		{left: "0.1.0-dev", right: "0.1.0", want: -1},
		{left: "1.0.0-alpha.2", right: "1.0.0-alpha.10", want: -1},
		{left: "1.0.0-alpha", right: "1.0.0-alpha.1", want: -1},
		{left: "1.0.0-1", right: "1.0.0-alpha", want: -1},
		{left: "10.0.0", right: "2.0.0", want: 1},
	}
	for _, test := range tests {
		got, err := Compare(test.left, test.right)
		if err != nil {
			t.Fatalf("Compare(%q, %q): %v", test.left, test.right, err)
		}
		if got != test.want {
			t.Fatalf("Compare(%q, %q) = %d, want %d", test.left, test.right, got, test.want)
		}
	}
}

func TestCompareRejectsInvalidVersion(t *testing.T) {
	t.Parallel()
	if _, err := Compare("01.0.0", "1.0.0"); err == nil {
		t.Fatal("invalid semantic version was accepted")
	}
}
