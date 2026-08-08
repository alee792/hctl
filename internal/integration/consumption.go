package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"hctl/internal/rootfs"
)

// Consumption is a non-secret receipt written by a capability-specific apply
// consumer after it successfully selects an exact installed package. It
// contains no workspace path, executable path, environment value, or runtime
// data.
type Consumption struct {
	SchemaVersion  int      `json:"schema_version"`
	PackageID      string   `json:"package_id"`
	ManifestSHA256 string   `json:"manifest_sha256"`
	AgentID        string   `json:"agent_id"`
	AgentName      string   `json:"agent_name"`
	Capabilities   []string `json:"capabilities"`
}

// RecordConsumption records diagnostic ownership only after offline package
// resolution has succeeded. It does not grant trust or affect enablement.
func (s *Store) RecordConsumption(ctx context.Context, packageID, agentID, agentName string, capabilityIDs []string) error {
	return s.locked(ctx, func() error {
		entry, found, err := s.loadInstalled(packageID)
		if err != nil {
			return err
		}
		if !found || !entry.State.Enabled {
			return fmt.Errorf("integration package %q is not enabled for consumption", packageID)
		}
		compatible, err := entry.Package.Manifest().Compatibility.Contains(s.hctlVersion)
		if err != nil || !compatible {
			return fmt.Errorf("integration package %q is not compatible with this hctl", packageID)
		}
		for _, identity := range entry.State.Artifacts {
			artifact, ok := artifactByID(entry.Package.Manifest(), identity.ID)
			if !ok {
				return fmt.Errorf("integration package %q is not verified for consumption", packageID)
			}
			if err := s.verifyCachedArtifact(artifact); err != nil {
				return fmt.Errorf("integration package %q is not verified for consumption", packageID)
			}
		}
		if !validConsumerText(agentID, 512) || !validConsumerText(agentName, 128) {
			return errors.New("integration consuming agent identity is invalid")
		}
		declared := map[string]bool{}
		for _, capability := range entry.State.Capabilities {
			declared[capability.ID] = true
		}
		selected := append([]string(nil), capabilityIDs...)
		sort.Strings(selected)
		if len(selected) == 0 || len(selected) > len(declared) {
			return errors.New("integration consuming capabilities are invalid")
		}
		for index, id := range selected {
			if !declared[id] || index > 0 && selected[index-1] == id {
				return fmt.Errorf("integration consuming capability %q is invalid", id)
			}
		}
		receipt := Consumption{SchemaVersion: 1, PackageID: packageID, ManifestSHA256: entry.Package.Identity(), AgentID: agentID, AgentName: agentName, Capabilities: selected}
		data, err := json.MarshalIndent(receipt, "", "  ")
		if err != nil {
			return errors.New("cannot encode integration consumption receipt")
		}
		directory := "consumers/" + packageID
		if err := rootfs.EnsurePrivateDir(s.root, directory); err != nil {
			return err
		}
		return rootfs.WriteAtomic(s.root, directory+"/"+consumerFilename(agentID), append(data, '\n'), 0o600)
	})
}

// Consumers returns only receipts bound to the currently installed exact
// manifest. Stale receipts from an earlier exact version never claim current
// consumption.
func (s *Store) Consumers(ctx context.Context, packageID string) ([]Consumption, error) {
	var result []Consumption
	err := s.locked(ctx, func() error {
		entry, found, err := s.loadInstalled(packageID)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("integration package %q is not installed", packageID)
		}
		directory := filepath.Join(s.root, "consumers", packageID)
		items, err := os.ReadDir(directory)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return errors.New("cannot read integration consumption receipts")
		}
		for _, item := range items {
			if item.IsDir() || filepath.Ext(item.Name()) != ".json" {
				return errors.New("integration consumption receipts contain an unsafe entry")
			}
			data, mode, exists, err := rootfs.ReadOptional(s.root, "consumers/"+packageID+"/"+item.Name(), maxManifestBytes)
			if err != nil || !exists || mode.Perm()&0o077 != 0 {
				return errors.New("integration consumption receipt is unsafe")
			}
			var receipt Consumption
			if err := decodeStrict(data, &receipt); err != nil || receipt.SchemaVersion != 1 || receipt.PackageID != packageID || !validConsumerText(receipt.AgentID, 512) || !validConsumerText(receipt.AgentName, 128) || item.Name() != consumerFilename(receipt.AgentID) {
				return errors.New("integration consumption receipt is invalid")
			}
			if receipt.ManifestSHA256 != entry.Package.Identity() {
				continue
			}
			declared := map[string]bool{}
			for _, capability := range entry.State.Capabilities {
				declared[capability.ID] = true
			}
			if len(receipt.Capabilities) == 0 || len(receipt.Capabilities) > len(declared) {
				return errors.New("integration consumption receipt capabilities are invalid")
			}
			for index, id := range receipt.Capabilities {
				if !declared[id] || index > 0 && receipt.Capabilities[index-1] >= id {
					return errors.New("integration consumption receipt capabilities are invalid")
				}
			}
			result = append(result, receipt)
		}
		sort.Slice(result, func(i, j int) bool { return result[i].AgentID < result[j].AgentID })
		return nil
	})
	return result, err
}

func (s *Store) removeConsumption(packageID string) error {
	relativeDirectory := "consumers/" + packageID
	directory := filepath.Join(s.root, filepath.FromSlash(relativeDirectory))
	if _, err := os.Lstat(directory); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return errors.New("cannot inspect integration consumption receipts")
	}
	if err := rootfs.RequirePrivateDir(s.root, relativeDirectory); err != nil {
		return errors.New("refusing to remove unsafe integration consumption receipts")
	}
	storeRoot, err := os.OpenRoot(s.root)
	if err != nil {
		return errors.New("cannot confine integration consumption removal")
	}
	defer func() { _ = storeRoot.Close() }()
	consumerRoot, err := storeRoot.OpenRoot(relativeDirectory)
	if err != nil {
		return errors.New("refusing to remove unsafe integration consumption receipts")
	}
	defer func() { _ = consumerRoot.Close() }()
	handle, err := consumerRoot.Open(".")
	if err != nil {
		return errors.New("cannot inspect integration consumption receipts")
	}
	items, err := handle.ReadDir(-1)
	closeErr := handle.Close()
	if err != nil || closeErr != nil {
		return errors.New("cannot inspect integration consumption receipts")
	}
	for _, item := range items {
		info, err := item.Info()
		if err != nil || item.IsDir() || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || filepath.Ext(item.Name()) != ".json" {
			return errors.New("refusing to remove unsafe integration consumption receipts")
		}
	}
	for _, item := range items {
		if err := consumerRoot.Remove(item.Name()); err != nil {
			return errors.New("cannot remove integration consumption receipt")
		}
	}
	return nil
}

func validConsumerText(value string, maximum int) bool {
	if !utf8.ValidString(value) || value == "" || strings.TrimSpace(value) != value || len([]byte(value)) > maximum {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func consumerFilename(agentID string) string {
	digest := sha256.Sum256([]byte(agentID))
	return hex.EncodeToString(digest[:]) + ".json"
}
