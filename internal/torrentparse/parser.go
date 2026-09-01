package torrentparse

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
)

type Limits struct {
	MaxDepth     int
	MaxNodes     int
	MaxStringLen int64
	MaxFiles     int
	MaxTotalSize int64
}

func DefaultLimits() Limits {
	return Limits{MaxDepth: 64, MaxNodes: 100000, MaxStringLen: 1 << 20, MaxFiles: 100000, MaxTotalSize: 1 << 50}
}

type Value struct {
	Kind  byte
	Int   int64
	Bytes []byte
	List  []Value
	Dict  []DictEntry
}

type DictEntry struct {
	Key   []byte
	Value Value
}

type File struct {
	Path      []string
	SizeBytes int64
}

type Torrent struct {
	Info      Value
	Name      string
	Files     []File
	TotalSize int64
}

func Parse(data []byte, limits Limits) (Torrent, error) {
	limits = withDefaults(limits)
	if len(data) == 0 {
		return Torrent{}, errors.New("torrent is empty")
	}
	parser := parser{data: data, limits: limits}
	root, err := parser.parseValue(0)
	if err != nil {
		return Torrent{}, fmt.Errorf("parse bencode: %w", err)
	}
	if parser.pos != len(data) {
		return Torrent{}, errors.New("trailing bytes after bencode value")
	}
	if root.Kind != 'd' {
		return Torrent{}, errors.New("torrent root must be a dictionary")
	}
	info, ok := dictGet(root, "info")
	if !ok || info.Kind != 'd' {
		return Torrent{}, errors.New("torrent info dictionary is required")
	}
	nameValue, ok := dictGet(info, "name")
	if !ok || nameValue.Kind != 's' {
		return Torrent{}, errors.New("torrent info.name is required")
	}
	name, err := safeComponent(nameValue.Bytes)
	if err != nil {
		return Torrent{}, fmt.Errorf("invalid torrent name: %w", err)
	}
	filesValue, hasFiles := dictGet(info, "files")
	files := make([]File, 0, 1)
	if hasFiles {
		if filesValue.Kind != 'l' {
			return Torrent{}, errors.New("torrent info.files must be a list")
		}
		if len(filesValue.List) == 0 {
			return Torrent{}, errors.New("torrent files list is empty")
		}
		if len(filesValue.List) > limits.MaxFiles {
			return Torrent{}, errors.New("torrent file count exceeds limit")
		}
		for index, value := range filesValue.List {
			file, err := parseFile(value)
			if err != nil {
				return Torrent{}, fmt.Errorf("torrent file %d: %w", index, err)
			}
			if len(file.Path) == 0 {
				return Torrent{}, fmt.Errorf("torrent file %d path is empty", index)
			}
			files = append(files, file)
		}
	} else {
		length, ok := dictGet(info, "length")
		if !ok || length.Kind != 'i' || length.Int < 0 {
			return Torrent{}, errors.New("single-file torrent length is invalid")
		}
		files = append(files, File{Path: []string{name}, SizeBytes: length.Int})
	}
	var total int64
	for _, file := range files {
		if file.SizeBytes > limits.MaxTotalSize-total {
			return Torrent{}, errors.New("torrent total size exceeds limit")
		}
		total += file.SizeBytes
	}
	return Torrent{Info: info, Name: name, Files: files, TotalSize: total}, nil
}

func parseFile(value Value) (File, error) {
	if value.Kind != 'd' {
		return File{}, errors.New("file entry must be a dictionary")
	}
	length, ok := dictGet(value, "length")
	if !ok || length.Kind != 'i' || length.Int < 0 {
		return File{}, errors.New("file length is invalid")
	}
	pathValue, ok := dictGet(value, "path")
	if !ok || pathValue.Kind != 'l' {
		return File{}, errors.New("file path must be a list")
	}
	path := make([]string, len(pathValue.List))
	for index, component := range pathValue.List {
		if component.Kind != 's' {
			return File{}, errors.New("file path component must be a string")
		}
		name, err := safeComponent(component.Bytes)
		if err != nil {
			return File{}, fmt.Errorf("path component %d: %w", index, err)
		}
		path[index] = name
	}
	return File{Path: path, SizeBytes: length.Int}, nil
}

func safeComponent(raw []byte) (string, error) {
	if len(raw) == 0 {
		return "", errors.New("path component is empty")
	}
	if bytes.IndexByte(raw, 0) >= 0 {
		return "", errors.New("path component contains NUL")
	}
	value := string(raw)
	if value == "." || value == ".." || strings.ContainsAny(value, `/\\`) {
		return "", errors.New("path traversal or separator is not allowed")
	}
	return value, nil
}

func dictGet(value Value, key string) (Value, bool) {
	for _, entry := range value.Dict {
		if string(entry.Key) == key {
			return entry.Value, true
		}
	}
	return Value{}, false
}

func withDefaults(limits Limits) Limits {
	defaults := DefaultLimits()
	if limits.MaxDepth <= 0 {
		limits.MaxDepth = defaults.MaxDepth
	}
	if limits.MaxNodes <= 0 {
		limits.MaxNodes = defaults.MaxNodes
	}
	if limits.MaxStringLen <= 0 {
		limits.MaxStringLen = defaults.MaxStringLen
	}
	if limits.MaxFiles <= 0 {
		limits.MaxFiles = defaults.MaxFiles
	}
	if limits.MaxTotalSize <= 0 {
		limits.MaxTotalSize = defaults.MaxTotalSize
	}
	return limits
}

type parser struct {
	data   []byte
	pos    int
	limits Limits
	nodes  int
}

func (parser *parser) parseValue(depth int) (Value, error) {
	if depth > parser.limits.MaxDepth {
		return Value{}, errors.New("bencode nesting depth exceeds limit")
	}
	parser.nodes++
	if parser.nodes > parser.limits.MaxNodes {
		return Value{}, errors.New("bencode node count exceeds limit")
	}
	if parser.pos >= len(parser.data) {
		return Value{}, errors.New("unexpected end of bencode")
	}
	switch parser.data[parser.pos] {
	case 'i':
		return parser.parseInt()
	case 'l':
		return parser.parseList(depth)
	case 'd':
		return parser.parseDict(depth)
	default:
		return parser.parseBytes()
	}
}

func (parser *parser) parseInt() (Value, error) {
	parser.pos++
	start := parser.pos
	end := bytes.IndexByte(parser.data[start:], 'e')
	if end < 0 {
		return Value{}, errors.New("unterminated integer")
	}
	end += start
	raw := parser.data[start:end]
	if len(raw) == 0 || (len(raw) > 1 && raw[0] == '0') || (len(raw) > 2 && raw[0] == '-' && raw[1] == '0') {
		return Value{}, errors.New("invalid integer encoding")
	}
	var value int64
	for index, char := range raw {
		if (char < '0' || char > '9') && !(index == 0 && char == '-') {
			return Value{}, errors.New("invalid integer")
		}
		if index == 0 && char == '-' && len(raw) == 1 {
			return Value{}, errors.New("invalid integer")
		}
	}
	negative := raw[0] == '-'
	for index := 0; index < len(raw); index++ {
		if negative && index == 0 {
			continue
		}
		digit := int64(raw[index] - '0')
		if value > (1<<63-1-digit)/10 {
			return Value{}, errors.New("integer overflows int64")
		}
		value = value*10 + digit
	}
	if negative {
		value = -value
	}
	parser.pos = end + 1
	return Value{Kind: 'i', Int: value}, nil
}

func (parser *parser) parseBytes() (Value, error) {
	colon := bytes.IndexByte(parser.data[parser.pos:], ':')
	if colon <= 0 {
		return Value{}, errors.New("invalid byte string length")
	}
	colon += parser.pos
	var length int64
	for index, char := range parser.data[parser.pos:colon] {
		if char < '0' || char > '9' || (index == 0 && char == '0' && colon-parser.pos > 1) {
			return Value{}, errors.New("invalid byte string length")
		}
		digit := int64(char - '0')
		if length > (1<<63-1-digit)/10 {
			return Value{}, errors.New("byte string length overflows int64")
		}
		length = length*10 + digit
	}
	if length > parser.limits.MaxStringLen {
		return Value{}, errors.New("byte string exceeds limit")
	}
	start := colon + 1
	end := int64(start) + length
	if end < int64(start) || end > int64(len(parser.data)) {
		return Value{}, errors.New("byte string exceeds input")
	}
	parser.pos = int(end)
	return Value{Kind: 's', Bytes: append([]byte(nil), parser.data[start:parser.pos]...)}, nil
}

func (parser *parser) parseList(depth int) (Value, error) {
	parser.pos++
	result := Value{Kind: 'l'}
	for {
		if parser.pos >= len(parser.data) {
			return Value{}, errors.New("unterminated list")
		}
		if parser.data[parser.pos] == 'e' {
			parser.pos++
			return result, nil
		}
		value, err := parser.parseValue(depth + 1)
		if err != nil {
			return Value{}, err
		}
		result.List = append(result.List, value)
	}
}

func (parser *parser) parseDict(depth int) (Value, error) {
	parser.pos++
	result := Value{Kind: 'd'}
	var previous []byte
	for {
		if parser.pos >= len(parser.data) {
			return Value{}, errors.New("unterminated dictionary")
		}
		if parser.data[parser.pos] == 'e' {
			parser.pos++
			return result, nil
		}
		key, err := parser.parseBytes()
		if err != nil {
			return Value{}, fmt.Errorf("dictionary key: %w", err)
		}
		if previous != nil && bytes.Compare(key.Bytes, previous) <= 0 {
			return Value{}, errors.New("dictionary keys are not strictly sorted")
		}
		previous = key.Bytes
		value, err := parser.parseValue(depth + 1)
		if err != nil {
			return Value{}, err
		}
		result.Dict = append(result.Dict, DictEntry{Key: key.Bytes, Value: value})
	}
}
