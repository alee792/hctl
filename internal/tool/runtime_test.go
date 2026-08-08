package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHostCatalogMayExceedCallLineLimit(t *testing.T) {
	response, err := json.Marshal(map[string]any{
		"id": "test-1",
		"result": map[string]any{
			"padding": strings.Repeat("x", maxHostCallLine),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response) <= maxHostCallLine || len(response) > maxHostCatalogLine {
		t.Fatalf("test response size = %d", len(response))
	}

	for _, test := range []struct {
		name    string
		method  string
		wantErr string
	}{
		{name: "catalog", method: "list"},
		{name: "call", method: "call", wantErr: "bounded response size"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			responsePath := filepath.Join(root, "response.json")
			if err := os.WriteFile(responsePath, append(response, '\n'), 0o600); err != nil {
				t.Fatal(err)
			}
			client, err := startClient(context.Background(), root, "test", os.Environ(), "/bin/sh", "-c", `IFS= read -r request; /bin/cat "$1"`, "host", responsePath)
			if err != nil {
				t.Fatal(err)
			}
			defer client.close()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			var target map[string]string
			err = client.request(ctx, test.method, nil, &target)
			if test.wantErr == "" {
				if err != nil || len(target["padding"]) != maxHostCallLine {
					t.Fatalf("large catalog response failed: padding=%d err=%v", len(target["padding"]), err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("oversized call response was not rejected: %v", err)
			}
		})
	}
}
