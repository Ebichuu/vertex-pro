package torrentidentity

import (
	"bytes"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/vertex-pro/vertex-pro/internal/torrentparse"
)

type Version string

const (
	VersionV1     Version = "v1"
	VersionV2     Version = "v2"
	VersionHybrid Version = "hybrid"
)

type Identity struct {
	Version Version
	V1      string
	V2      string
}

func FromTorrent(torrent torrentparse.Torrent) (Identity, error) {
	return FromInfo(torrent.Info)
}

func FromInfo(info torrentparse.Value) (Identity, error) {
	if info.Kind != 'd' {
		return Identity{}, errors.New("torrent info must be a dictionary")
	}
	canonical, err := encode(info)
	if err != nil {
		return Identity{}, fmt.Errorf("encode info dictionary: %w", err)
	}
	_, hasV1, err := detectV1(info)
	if err != nil {
		return Identity{}, err
	}
	_, hasV2, err := detectV2(info)
	if err != nil {
		return Identity{}, err
	}
	if !hasV1 && !hasV2 {
		return Identity{}, errors.New("torrent info has neither v1 nor v2 identity fields")
	}
	identity := Identity{}
	if hasV1 {
		digest := sha1.Sum(canonical)
		identity.V1 = hex.EncodeToString(digest[:])
	}
	if hasV2 {
		digest := sha256.Sum256(canonical)
		identity.V2 = hex.EncodeToString(digest[:])
	}
	switch {
	case hasV1 && hasV2:
		identity.Version = VersionHybrid
	case hasV1:
		identity.Version = VersionV1
	default:
		identity.Version = VersionV2
	}
	return identity, nil
}

func detectV1(info torrentparse.Value) (torrentparse.Value, bool, error) {
	pieces, ok := dictGet(info, "pieces")
	if !ok {
		return torrentparse.Value{}, false, nil
	}
	if pieces.Kind != 's' || len(pieces.Bytes) == 0 || len(pieces.Bytes)%20 != 0 {
		return torrentparse.Value{}, false, errors.New("v1 pieces must contain a non-empty multiple of 20 bytes")
	}
	return pieces, true, nil
}

func detectV2(info torrentparse.Value) (torrentparse.Value, bool, error) {
	metaVersion, hasMetaVersion := dictGet(info, "meta version")
	fileTree, hasFileTree := dictGet(info, "file tree")
	if !hasMetaVersion && !hasFileTree {
		return torrentparse.Value{}, false, nil
	}
	if hasMetaVersion && (metaVersion.Kind != 'i' || metaVersion.Int != 2) {
		return torrentparse.Value{}, false, errors.New("v2 meta version must be integer 2")
	}
	if hasFileTree && fileTree.Kind != 'd' {
		return torrentparse.Value{}, false, errors.New("v2 file tree must be a dictionary")
	}
	return fileTree, true, nil
}

func dictGet(value torrentparse.Value, key string) (torrentparse.Value, bool) {
	for _, entry := range value.Dict {
		if string(entry.Key) == key {
			return entry.Value, true
		}
	}
	return torrentparse.Value{}, false
}

func encode(value torrentparse.Value) ([]byte, error) {
	var buffer bytes.Buffer
	switch value.Kind {
	case 'i':
		if value.Int < 0 {
			fmt.Fprintf(&buffer, "i%de", value.Int)
		} else {
			fmt.Fprintf(&buffer, "i%de", value.Int)
		}
	case 's':
		fmt.Fprintf(&buffer, "%d:", len(value.Bytes))
		buffer.Write(value.Bytes)
	case 'l':
		buffer.WriteByte('l')
		for _, child := range value.List {
			encoded, err := encode(child)
			if err != nil {
				return nil, err
			}
			buffer.Write(encoded)
		}
		buffer.WriteByte('e')
	case 'd':
		buffer.WriteByte('d')
		var previous []byte
		for _, entry := range value.Dict {
			if len(entry.Key) == 0 || (previous != nil && bytes.Compare(entry.Key, previous) <= 0) {
				return nil, errors.New("info dictionary keys are not strictly sorted")
			}
			fmt.Fprintf(&buffer, "%d:", len(entry.Key))
			buffer.Write(entry.Key)
			encoded, err := encode(entry.Value)
			if err != nil {
				return nil, err
			}
			buffer.Write(encoded)
			previous = entry.Key
		}
		buffer.WriteByte('e')
	default:
		return nil, fmt.Errorf("unknown bencode kind %q", value.Kind)
	}
	return buffer.Bytes(), nil
}
