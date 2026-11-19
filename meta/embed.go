package meta

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"
)

const (
	beginMarker = "# pipemaster:meta:begin"
	endMarker   = "# pipemaster:meta:end"
	commentLead = "# "
	wrapWidth   = 76
)

// EmbedStack appends the stack block to the lexically first file and returns a new map. It writes no block
// for an empty stack. A first file without a trailing newline gains one, and the top layer's hash covers that form.
func EmbedStack(files map[string][]byte, s *Stack) map[string][]byte {
	out := make(map[string][]byte, len(files))
	for k, v := range files {
		out[k] = StripEmbedded(v)
	}
	if len(out) == 0 || len(s.Layers) == 0 {
		return out
	}
	first := sortedKeys(out)[0]
	if b := out[first]; len(b) > 0 && b[len(b)-1] != '\n' {
		out[first] = append(append([]byte{}, b...), '\n')
	}
	s.Top().Files = hashAll(out)

	enc := base64.StdEncoding.EncodeToString(marshalStack(s))
	var blk strings.Builder
	blk.WriteString(beginMarker + "\n")
	for len(enc) > 0 {
		n := min(wrapWidth, len(enc))
		blk.WriteString(commentLead + enc[:n] + "\n")
		enc = enc[n:]
	}
	blk.WriteString(endMarker + "\n")
	out[first] = append(append([]byte{}, out[first]...), blk.String()...)
	return out
}

// StripEmbedded returns b without its metadata block; it returns b unchanged when there is none.
func StripEmbedded(b []byte) []byte {
	start, end, ok := findBlock(b)
	if !ok {
		return b
	}
	return append(append([]byte{}, b[:start]...), b[end:]...)
}

// findBlock locates the block as byte offsets covering the begin line through the end line's newline.
func findBlock(b []byte) (start, end int, ok bool) {
	start = lineIndex(b, beginMarker, 0)
	if start < 0 {
		return 0, 0, false
	}
	e := lineIndex(b, endMarker, start)
	if e < 0 {
		return start, len(b), true
	}
	end = e + len(endMarker)
	if end < len(b) && b[end] == '\n' {
		end++
	}
	return start, end, true
}

// lineIndex returns the offset of the first line at or after from that equals marker, or -1.
func lineIndex(b []byte, marker string, from int) int {
	for i := from; i < len(b); {
		j := bytes.IndexByte(b[i:], '\n')
		line := b[i:]
		if j >= 0 {
			line = b[i : i+j]
		}
		if string(line) == marker {
			return i
		}
		if j < 0 {
			break
		}
		i += j + 1
	}
	return -1
}

func loadEmbedded(fsys fs.FS, parsed []string) (*Stack, error) {
	files := slices.Clone(parsed)
	slices.Sort(files)
	for _, path := range files {
		b, err := fs.ReadFile(fsys, path)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			continue
		case err != nil:
			return nil, fmt.Errorf("scan for embedded metadata: %w", err)
		}
		start, end, ok := findBlock(b)
		if !ok {
			continue
		}
		s, err := decodeBlock(b[start:end])
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if top := s.Top(); top != nil {
			if _, ok := top.Files[path]; ok {
				return s, nil
			}
		}
	}
	return &Stack{}, nil
}

func decodeBlock(blk []byte) (*Stack, error) {
	lines := strings.Split(strings.TrimSuffix(string(blk), "\n"), "\n")
	if len(lines) < 2 || lines[len(lines)-1] != endMarker {
		return nil, errors.New("embedded metadata block has no end marker")
	}
	var enc strings.Builder
	for _, l := range lines[1 : len(lines)-1] {
		body, ok := strings.CutPrefix(l, commentLead)
		if !ok {
			return nil, fmt.Errorf("embedded metadata line %q is not a comment", l)
		}
		enc.WriteString(body)
	}
	raw, err := base64.StdEncoding.DecodeString(enc.String())
	if err != nil {
		return nil, fmt.Errorf("decode embedded metadata: %w", err)
	}
	return parseStack(raw)
}
