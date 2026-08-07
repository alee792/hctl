package version

import "testing"

func TestValidate(t *testing.T) {
	for _, value := range []string{"0.9.0-dev", "1.2.3", "1.2.3-alpha.1+build.7"} {
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
