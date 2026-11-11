package store

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/GhostFlying/tend/internal/blobstore"
)

const commandDisplayEncoding = "argv-display:"
const stringFieldEncoding = "json-string-field:"
const maxOriginalPresentation = 16 << 20

func historicalToolClip(raw []byte) []byte {
	if len(raw) <= 8192 {
		return nil
	}
	at := 8192
	for at > 0 && !utf8.RuneStart(raw[at]) {
		at--
	}
	out := append([]byte(nil), raw[:at]...)
	return append(out, fmt.Sprintf("\n[truncated: %d of %d bytes shown]", at, len(raw))...)
}

func decodeOriginalStringField(source []byte, encoding string) ([]byte, error) {
	bad := errors.New("store: invalid original JSON string field")
	encoded := strings.TrimPrefix(encoding, stringFieldEncoding)
	if len(source) > maxOriginalPresentation || len(encoded) > 8192 {
		return nil, bad
	}
	var outer string
	if err := json.Unmarshal(source, &outer); err != nil {
		return nil, bad
	}
	pathJSON, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, bad
	}
	var path []string
	if json.Unmarshal(pathJSON, &path) != nil || len(path) == 0 || len(path) > 16 {
		return nil, bad
	}
	value := json.RawMessage(outer)
	for _, key := range path {
		value = bytes.TrimSpace(value)
		if len(value) == 0 {
			return nil, bad
		}
		switch value[0] {
		case '{':
			var fields map[string]json.RawMessage
			if json.Unmarshal(value, &fields) != nil {
				return nil, bad
			}
			var ok bool
			value, ok = fields[key]
			if !ok {
				return nil, bad
			}
		case '[':
			var values []json.RawMessage
			i, err := strconv.Atoi(key)
			if err != nil || strconv.Itoa(i) != key || json.Unmarshal(value, &values) != nil || i < 0 || i >= len(values) {
				return nil, bad
			}
			value = values[i]
		default:
			return nil, bad
		}
	}
	var text string
	if len(value) == 0 || bytes.TrimSpace(value)[0] != '"' || json.Unmarshal(value, &text) != nil {
		return nil, bad
	}
	return []byte(text), nil
}

func originalStringFields(raw []byte, window blobstore.Window, needed map[string]bool) []preparedOriginal {
	var outer string
	if json.Unmarshal(raw, &outer) != nil || !json.Valid([]byte(outer)) {
		return nil
	}
	var result []preparedOriginal
	seen := map[string]bool{}
	var visit func(json.RawMessage, []string)
	visit = func(value json.RawMessage, path []string) {
		value = bytes.TrimSpace(value)
		if len(path) > 16 || len(value) < retainBodyThreshold {
			return
		}
		switch value[0] {
		case '"':
			var text string
			if json.Unmarshal(value, &text) != nil {
				return
			}
			hash := bodyHash([]byte(text))
			if !needed[hash] || seen[hash] || len(path) == 0 {
				return
			}
			pathJSON, _ := json.Marshal(path)
			encoding := stringFieldEncoding + base64.RawURLEncoding.EncodeToString(pathJSON)
			if len(encoding)-len(stringFieldEncoding) > 8192 {
				return
			}
			w := window
			result = append(result, preparedOriginal{Window: w, Characters: int64(utf8.RuneCountInString(text)), Reference: originalReference{Window: &w, Encoding: encoding, SHA256: hash}})
			seen[hash] = true
		case '{':
			var fields map[string]json.RawMessage
			if json.Unmarshal(value, &fields) != nil {
				return
			}
			keys := make([]string, 0, len(fields))
			for key := range fields {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				visit(fields[key], append(append([]string(nil), path...), key))
			}
		case '[':
			var items []json.RawMessage
			if json.Unmarshal(value, &items) != nil {
				return
			}
			for i, item := range items {
				visit(item, append(append([]string(nil), path...), strconv.Itoa(i)))
			}
		}
	}
	visit(json.RawMessage(outer), nil)
	return result
}

func commandSyntax(c byte) bool {
	return c == ' ' || c == '\t' || c == '\r' || c == '\n' || c == '\'' || c == '"' || c == '\\'
}

// commandDisplay separates the actual argv from presentation syntax. It never
// executes or expands shell input. Control contains only copy lengths and the
// seven quote/escape/whitespace bytes; argument contents are not copied into it.
func commandDisplay(raw []byte) ([]string, []byte, bool) {
	if len(raw) == 0 || len(raw) > maxOriginalPresentation {
		return nil, nil, false
	}
	var args []string
	var arg, control, syntax []byte
	copyBytes := uint64(0)
	flush := func() {
		control = binary.AppendUvarint(control, copyBytes)
		control = binary.AppendUvarint(control, uint64(len(syntax)))
		control = append(control, syntax...)
		copyBytes, syntax = 0, syntax[:0]
	}
	emit := func(c byte) {
		if len(syntax) != 0 {
			flush()
		}
		arg = append(arg, c)
		copyBytes++
	}
	state := byte(0)
	active := false
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		switch state {
		case 0:
			switch c {
			case ' ', '\t', '\r', '\n':
				syntax = append(syntax, c)
				if active {
					args = append(args, string(arg))
					arg, active = arg[:0], false
				}
			case '\'', '"':
				syntax = append(syntax, c)
				state, active = c, true
			case '\\':
				syntax = append(syntax, c)
				i++
				if i == len(raw) {
					return nil, nil, false
				}
				if raw[i] == '\n' {
					syntax = append(syntax, raw[i])
				} else {
					emit(raw[i])
					active = true
				}
			default:
				emit(c)
				active = true
			}
		case '\'':
			if c == '\'' {
				syntax = append(syntax, c)
				state = 0
			} else {
				emit(c)
			}
		case '"':
			if c == '"' {
				syntax = append(syntax, c)
				state = 0
			} else if c == '\\' && i+1 < len(raw) && strings.ContainsRune("$`\"\\\n", rune(raw[i+1])) {
				syntax = append(syntax, c)
				i++
				if raw[i] == '\n' {
					syntax = append(syntax, raw[i])
				} else {
					emit(raw[i])
				}
			} else {
				emit(c)
			}
		}
	}
	if state != 0 {
		return nil, nil, false
	}
	if active {
		args = append(args, string(arg))
	}
	flush()
	return args, control, len(args) != 0
}

func commandDisplayPresentation(raw, control []byte) (string, error) {
	var encoded bytes.Buffer
	w := gzip.NewWriter(&encoded)
	if _, err := w.Write(control); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	return commandDisplayEncoding + strconv.Itoa(len(raw)) + ":" + base64.RawURLEncoding.EncodeToString(encoded.Bytes()), nil
}

func decodeCommandDisplay(source []byte, encoding string) ([]byte, error) {
	bad := errors.New("store: invalid original argv presentation")
	length, compressed, ok := strings.Cut(strings.TrimPrefix(encoding, commandDisplayEncoding), ":")
	target, err := strconv.Atoi(length)
	if len(source) > maxOriginalPresentation || !ok || err != nil || target < 1 || target > maxOriginalPresentation || len(compressed) > 4*target+4096 {
		return nil, bad
	}
	var args []string
	if err = json.Unmarshal(source, &args); err != nil || args == nil {
		return nil, bad
	}
	var argv []byte
	for _, arg := range args {
		if len(arg) > target-len(argv) {
			return nil, bad
		}
		argv = append(argv, arg...)
	}
	packed, err := base64.RawURLEncoding.DecodeString(compressed)
	if err != nil {
		return nil, bad
	}
	reader := bytes.NewReader(packed)
	r, err := gzip.NewReader(reader)
	if err != nil {
		return nil, bad
	}
	r.Multistream(false)
	maxControl := 3*target + 32
	control, err := io.ReadAll(io.LimitReader(r, int64(maxControl)+1))
	err = errors.Join(err, r.Close())
	if err != nil || len(control) > maxControl || reader.Len() != 0 {
		return nil, bad
	}
	out := make([]byte, 0, target)
	at := 0
	for len(control) != 0 {
		n, count := binary.Uvarint(control)
		if count <= 0 || n > uint64(len(argv)-at) || n > uint64(target-len(out)) {
			return nil, bad
		}
		control = control[count:]
		out = append(out, argv[at:at+int(n)]...)
		at += int(n)
		n, count = binary.Uvarint(control)
		if count <= 0 {
			return nil, bad
		}
		control = control[count:]
		if n > uint64(len(control)) || n > uint64(target-len(out)) {
			return nil, bad
		}
		for _, c := range control[:int(n)] {
			if !commandSyntax(c) {
				return nil, bad
			}
		}
		out = append(out, control[:int(n)]...)
		control = control[int(n):]
	}
	if at != len(argv) || len(out) != target {
		return nil, bad
	}
	actual, _, ok := commandDisplay(out)
	if !ok || len(actual) != len(args) {
		return nil, bad
	}
	for i, arg := range args {
		if actual[i] != arg {
			return nil, bad
		}
	}
	return out, nil
}

func (s *Store) matchCommandDisplay(ctx context.Context, raw []byte) (*preparedOriginal, error) {
	args, control, ok := commandDisplay(raw)
	if !ok {
		return nil, nil
	}
	canonical, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	rows, err := s.read.QueryContext(ctx, canonicalOriginalLookupQuery, hashBytes(bodyHash(canonical)))
	if err != nil {
		return nil, err
	}
	var candidates []preparedOriginal
	for rows.Next() {
		var p preparedOriginal
		if err = rows.Scan(&p.Window.SHA256, &p.Window.Archive, &p.Window.Offset, &p.Window.Length, &p.Characters); err != nil {
			rows.Close()
			return nil, err
		}
		candidates = append(candidates, p)
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	for _, p := range candidates {
		source, e := s.files.ReadWindow(ctx, p.Window)
		if e != nil {
			return nil, e
		}
		value, e := canonicalOriginal(source)
		if e != nil {
			return nil, e
		}
		if !bytes.Equal(value, canonical) {
			continue
		}
		encoding, e := commandDisplayPresentation(raw, control)
		if e != nil {
			return nil, e
		}
		presented, e := decodeCommandDisplay(source, encoding)
		if e != nil || !bytes.Equal(presented, raw) {
			return nil, errors.New("store: original argv presentation differs")
		}
		w := p.Window
		p.Reference = originalReference{Window: &w, Encoding: encoding, SHA256: bodyHash(raw)}
		p.Characters = int64(utf8.RuneCount(raw))
		return &p, nil
	}
	return nil, nil
}
