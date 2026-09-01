package torrentlink

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/vertex-pro/vertex-pro/internal/torrentidentity"
)

type IdentityRecord struct {
	ID      string
	V1      string
	V2      string
	Version torrentidentity.Version
}

type SourceLink struct {
	SiteID     string
	SourceKey  string
	IdentityID string
}

type Result struct {
	Identity        IdentityRecord
	Source          SourceLink
	CreatedIdentity bool
	CreatedSource   bool
	DuplicateKind   string
}

type Store interface {
	LoadIdentityByKey(context.Context, string) (IdentityRecord, bool, error)
	LoadSource(context.Context, string, string) (SourceLink, bool, error)
	SaveIdentity(context.Context, IdentityRecord) error
	SaveSource(context.Context, SourceLink) error
}

type Linker struct {
	store Store
	mu    sync.Mutex
}

func New(store Store) (*Linker, error) {
	if store == nil {
		return nil, errors.New("torrent link store is required")
	}
	return &Linker{store: store}, nil
}

func (linker *Linker) Link(ctx context.Context, siteID, sourceKey string, identity torrentidentity.Identity) (Result, error) {
	if linker == nil || linker.store == nil {
		return Result{}, errors.New("torrent linker is not initialized")
	}
	siteID = strings.TrimSpace(siteID)
	sourceKey = strings.TrimSpace(sourceKey)
	if siteID == "" || sourceKey == "" {
		return Result{}, errors.New("site ID and source key are required")
	}
	aliases, version, err := aliasesFor(identity)
	if err != nil {
		return Result{}, err
	}
	linker.mu.Lock()
	defer linker.mu.Unlock()

	existingSource, exists, err := linker.store.LoadSource(ctx, siteID, sourceKey)
	if err != nil {
		return Result{}, fmt.Errorf("load source link: %w", err)
	}
	var record IdentityRecord
	identityFound := false
	createdIdentity := false
	for _, key := range aliases {
		candidate, found, err := linker.store.LoadIdentityByKey(ctx, key)
		if err != nil {
			return Result{}, fmt.Errorf("load identity: %w", err)
		}
		if !found {
			continue
		}
		if !identityFound {
			record, identityFound = candidate, true
		} else if candidate.ID != record.ID {
			return Result{}, errors.New("identity aliases resolve to different records")
		}
	}
	if exists {
		if existingSource.IdentityID == "" {
			return Result{}, errors.New("persisted source link has empty identity")
		}
		if identityFound && existingSource.IdentityID != record.ID {
			return Result{}, errors.New("source is already linked to another identity")
		}
		if !identityFound {
			return Result{}, errors.New("source link identity is not present")
		}
		return Result{Identity: record, Source: existingSource, DuplicateKind: "SAME_SOURCE"}, nil
	}
	if !identityFound {
		record = IdentityRecord{ID: identityID(aliases), V1: identity.V1, V2: identity.V2, Version: version}
		createdIdentity = true
		if err := linker.store.SaveIdentity(ctx, record); err != nil {
			return Result{}, fmt.Errorf("save identity: %w", err)
		}
		identityFound = true
	}
	link := SourceLink{SiteID: siteID, SourceKey: sourceKey, IdentityID: record.ID}
	if err := linker.store.SaveSource(ctx, link); err != nil {
		return Result{}, fmt.Errorf("save source link: %w", err)
	}
	kind := "NEW"
	if !createdIdentity {
		kind = "CROSS_SITE_DUPLICATE"
	}
	return Result{Identity: record, Source: link, CreatedIdentity: createdIdentity, CreatedSource: true, DuplicateKind: kind}, nil
}

func aliasesFor(identity torrentidentity.Identity) ([]string, torrentidentity.Version, error) {
	var aliases []string
	if identity.V1 != "" {
		if len(identity.V1) != 40 {
			return nil, "", errors.New("v1 identity must be 40 hex characters")
		}
		aliases = append(aliases, "v1:"+strings.ToLower(identity.V1))
	}
	if identity.V2 != "" {
		if len(identity.V2) != 64 {
			return nil, "", errors.New("v2 identity must be 64 hex characters")
		}
		aliases = append(aliases, "v2:"+strings.ToLower(identity.V2))
	}
	if len(aliases) == 0 {
		return nil, "", errors.New("identity has no digest")
	}
	if identity.Version != torrentidentity.VersionV1 && identity.Version != torrentidentity.VersionV2 && identity.Version != torrentidentity.VersionHybrid {
		return nil, "", errors.New("invalid identity version")
	}
	return aliases, identity.Version, nil
}

func identityID(aliases []string) string {
	digest := sha256.Sum256([]byte(strings.Join(aliases, "\x00")))
	return "identity-" + hex.EncodeToString(digest[:])
}

type MemoryStore struct {
	mu         sync.Mutex
	identities map[string]IdentityRecord
	sources    map[string]SourceLink
}

func (store *MemoryStore) LoadIdentityByKey(_ context.Context, key string) (IdentityRecord, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	record, ok := store.identities[key]
	return record, ok, nil
}
func (store *MemoryStore) LoadSource(_ context.Context, siteID, sourceKey string) (SourceLink, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	link, ok := store.sources[siteID+"\x00"+sourceKey]
	return link, ok, nil
}
func (store *MemoryStore) SaveIdentity(_ context.Context, record IdentityRecord) error {
	if record.ID == "" {
		return errors.New("identity ID is required")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.identities == nil {
		store.identities = make(map[string]IdentityRecord)
	}
	for _, key := range identityKeys(record) {
		if existing, ok := store.identities[key]; ok && existing.ID != record.ID {
			return errors.New("identity key already belongs to another record")
		}
		store.identities[key] = record
	}
	return nil
}
func (store *MemoryStore) SaveSource(_ context.Context, link SourceLink) error {
	if link.SiteID == "" || link.SourceKey == "" || link.IdentityID == "" {
		return errors.New("source link fields are required")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.sources == nil {
		store.sources = make(map[string]SourceLink)
	}
	key := link.SiteID + "\x00" + link.SourceKey
	if existing, ok := store.sources[key]; ok && existing.IdentityID != link.IdentityID {
		return errors.New("source already linked")
	}
	store.sources[key] = link
	return nil
}
func identityKeys(record IdentityRecord) []string {
	result := []string{}
	if record.V1 != "" {
		result = append(result, "v1:"+strings.ToLower(record.V1))
	}
	if record.V2 != "" {
		result = append(result, "v2:"+strings.ToLower(record.V2))
	}
	return result
}
