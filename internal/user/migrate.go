package user

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"
)

// MigrateV2 upgrades a version-1 account file to version 2, translating each
// account's protocol selection into the node ids the mapping places those
// protocols on. It is idempotent and a no-op for a missing, empty or already-v2
// file.
//
// A protocol that has no node in the mapping is dropped from the account
// (migration requirement 5); the rest of the account is preserved.
func MigrateV2(path string, mapping map[string]string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return false, nil
	}
	var raw migrateFile
	if err := json.Unmarshal(data, &raw); err != nil {
		return false, fmt.Errorf("parse %s: %w", path, err)
	}
	if raw.Version >= CurrentVersion {
		return false, nil
	}

	users := make([]User, 0, len(raw.Users))
	for _, legacy := range raw.Users {
		var l legacyUser
		if err := json.Unmarshal(legacy, &l); err != nil {
			return false, fmt.Errorf("parse %s: %w", path, err)
		}
		users = append(users, l.convert(mapping))
	}
	store := &Store{path: path, users: users}
	if err := store.Save(); err != nil {
		return false, err
	}
	return true, nil
}

// migrateFile reads the version and keeps each account unparsed, so a version-1
// account is decoded with the legacy shape rather than the current User struct.
type migrateFile struct {
	Version int               `json:"version"`
	Users   []json.RawMessage `json:"users"`
}

// convert turns one version-1 account into a version-2 account. The v1 file
// stored credentials keyed by protocol and selections as protocol keys; both are
// re-keyed by node id. Per-node usage starts empty: the v1 file only carried an
// aggregate, which stays in the aggregate fields.
func (l legacyUser) convert(mapping map[string]string) User {
	u := User{
		Name:          l.Name,
		Remark:        l.Remark,
		Token:         l.Token,
		Enabled:       l.Enabled,
		Credentials:   map[string]Credentials{},
		Usage:         map[string]NodeUsage{},
		QuotaBytes:    l.QuotaBytes,
		UsedBytes:     l.UsedBytes,
		UploadBytes:   l.UploadBytes,
		DownloadBytes: l.DownloadBytes,
		CreatedAt:     l.CreatedAt,
		ExpireAt:      l.ExpireAt,
		LastReset:     l.LastReset,
		Applied:       l.Applied,
	}
	for _, proto := range l.Protocols {
		if id := mapping[proto]; id != "" && !u.Selects(id) {
			u.Nodes = append(u.Nodes, id)
		}
	}
	sortNodes(u.Nodes)
	for proto, cred := range l.Credentials {
		id := mapping[proto]
		if id == "" {
			continue
		}
		u.Credentials[id] = Credentials{Protocol: proto, UUID: cred.UUID, Password: cred.Password}
	}
	return u
}

// legacyUser is the version-1 on-disk shape. It is read only by MigrateV2.
type legacyUser struct {
	Name          string                `json:"name"`
	Remark        string                `json:"remark,omitempty"`
	Token         string                `json:"token"`
	Enabled       bool                  `json:"enabled"`
	Protocols     []string              `json:"protocols,omitempty"`
	Credentials   map[string]legacyCred `json:"credentials,omitempty"`
	QuotaBytes    int64                 `json:"quota_bytes"`
	UsedBytes     int64                 `json:"used_bytes"`
	UploadBytes   int64                 `json:"upload_bytes"`
	DownloadBytes int64                 `json:"download_bytes"`
	CreatedAt     time.Time             `json:"created_at"`
	ExpireAt      time.Time             `json:"expire_at"`
	LastReset     time.Time             `json:"last_reset"`
	Applied       bool                  `json:"applied"`
}

type legacyCred struct {
	UUID     string `json:"uuid,omitempty"`
	Password string `json:"password,omitempty"`
}
