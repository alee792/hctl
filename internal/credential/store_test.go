package credential

import (
	"errors"
	"testing"
)

type fakeStore struct{ value string }

func (f fakeStore) Get(string) (string, error) {
	if f.value == "" {
		return "", errors.New("missing")
	}
	return f.value, nil
}
func (fakeStore) Set(string, string) error { return nil }
func (fakeStore) Delete(string) error      { return nil }

func TestResolvePrefersDeploymentEnvironment(t *testing.T) {
	t.Setenv("HCTL_DISCORD_TOKEN", "injected")
	value, err := Resolve(fakeStore{value: "stored"}, "default")
	if err != nil || value != "injected" {
		t.Fatalf("resolve = %q, %v", value, err)
	}
}

func TestResolveUsesStoreWithoutEnvironment(t *testing.T) {
	t.Setenv("HCTL_DISCORD_TOKEN", "")
	value, err := Resolve(fakeStore{value: "stored"}, "default")
	if err != nil || value != "stored" {
		t.Fatalf("resolve = %q, %v", value, err)
	}
}
