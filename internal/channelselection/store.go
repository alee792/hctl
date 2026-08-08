package channelselection

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
)

const maxStoreBytes = 64 << 10

type document struct {
	SchemaVersion int                          `json:"schema_version"`
	Agents        map[string]map[string]string `json:"agents"`
}

// Store owns only the non-secret selection that binds an agent and channel
// kind to an adapter-owned profile. It deliberately knows no vendor fields.
type Store struct {
	Path string
}

func DefaultStore() (Store, error) {
	root, err := os.UserConfigDir()
	if err != nil || root == "" {
		return Store{}, errors.New("cannot resolve the user configuration directory")
	}
	return Store{Path: filepath.Join(root, "hctl", "channel-selections.json")}, nil
}

func (store Store) Get(agentID, channelKind string) (string, error) {
	document, _, err := store.load()
	if err != nil {
		return "", err
	}
	return document.Agents[agentID][channelKind], nil
}

func (store Store) Set(agentID, channelKind, profileID string) error {
	if agentID == "" || channelKind == "" || profileID == "" {
		return errors.New("channel selection requires agent, channel, and profile ids")
	}
	document, _, err := store.load()
	if err != nil {
		return err
	}
	if document.Agents[agentID] == nil {
		document.Agents[agentID] = map[string]string{}
	}
	document.Agents[agentID][channelKind] = profileID
	return store.save(document)
}

// Delete removes the binding only when it still selects profileID. This keeps
// a concurrent or newer operator choice from being removed accidentally.
func (store Store) Delete(agentID, channelKind, profileID string) error {
	document, exists, err := store.load()
	if err != nil || !exists {
		return err
	}
	channels := document.Agents[agentID]
	if channels[channelKind] != profileID {
		return nil
	}
	delete(channels, channelKind)
	if len(channels) == 0 {
		delete(document.Agents, agentID)
	}
	return store.save(document)
}

func (store Store) load() (document, bool, error) {
	empty := document{SchemaVersion: 1, Agents: map[string]map[string]string{}}
	info, err := os.Lstat(store.Path)
	if errors.Is(err, os.ErrNotExist) {
		return empty, false, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 || info.Size() > maxStoreBytes {
		return document{}, false, errors.New("channel selection store must be a bounded owner-only regular file")
	}
	data, err := os.ReadFile(store.Path)
	if err != nil {
		return document{}, false, errors.New("cannot read channel selection store")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var result document
	if err := decoder.Decode(&result); err != nil || result.SchemaVersion != 1 || result.Agents == nil {
		return document{}, false, errors.New("channel selection store is invalid")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return document{}, false, errors.New("channel selection store is invalid")
	}
	for agentID, channels := range result.Agents {
		if agentID == "" || channels == nil {
			return document{}, false, errors.New("channel selection store is invalid")
		}
		for channelKind, profileID := range channels {
			if channelKind == "" || profileID == "" {
				return document{}, false, errors.New("channel selection store is invalid")
			}
		}
	}
	return result, true, nil
}

func (store Store) save(document document) error {
	data, err := json.Marshal(document)
	if err != nil || len(data) > maxStoreBytes {
		return errors.New("cannot encode channel selection store")
	}
	directory := filepath.Dir(store.Path)
	if err := os.MkdirAll(directory, 0o700); err != nil || os.Chmod(directory, 0o700) != nil {
		return errors.New("cannot protect channel selection directory")
	}
	if info, err := os.Lstat(store.Path); err == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
		return errors.New("refusing to replace unsafe channel selection store")
	}
	temporary, err := os.CreateTemp(directory, ".channel-selections-*")
	if err != nil {
		return errors.New("cannot stage channel selection store")
	}
	name := temporary.Name()
	defer func() { _ = os.Remove(name) }()
	if temporary.Chmod(0o600) != nil {
		_ = temporary.Close()
		return errors.New("cannot protect channel selection store")
	}
	if _, err := temporary.Write(data); err != nil || temporary.Sync() != nil || temporary.Close() != nil || os.Rename(name, store.Path) != nil {
		return errors.New("cannot save channel selection store")
	}
	return nil
}
