package sshd

// Public keys as authorized_keys holds them, read without x/crypto: a key's line is its type, its
// data in base64 and a comment, and the data starts with the type again, so a key pasted half-copied
// or with the wrong type in front is caught before sshd quietly ignores it.

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"unicode"
)

// Key is a public key: its type, its data, and the comment after them.
type Key struct {
	Type    string
	Blob    []byte
	Comment string
}

// keyFields is how many length-prefixed fields each type the dashboard takes has in its data, the
// type included: enough to catch a key cut short.
var keyFields = map[string]int{
	"ssh-ed25519":                        2, // type, key
	"ssh-rsa":                            3, // type, e, n
	"ecdsa-sha2-nistp256":                3, // type, curve, point
	"ecdsa-sha2-nistp384":                3,
	"ecdsa-sha2-nistp521":                3,
	"sk-ssh-ed25519@openssh.com":         3, // type, key, application
	"sk-ecdsa-sha2-nistp256@openssh.com": 4, // type, curve, point, application
}

// Fingerprint is the key's SHA256 fingerprint as ssh-keygen -lf shows it.
func (k Key) Fingerprint() string {
	sum := sha256.Sum256(k.Blob)
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
}

// Line is the key as one line of authorized_keys.
func (k Key) Line() string {
	line := k.Type + " " + base64.StdEncoding.EncodeToString(k.Blob)
	if k.Comment != "" {
		line += " " + k.Comment
	}
	return line
}

// ParseKey reads a public key pasted into the dashboard: "type base64 [comment]", of a type sshd
// takes. It turns down a private key, and anything else, in words that say what to paste instead.
func ParseKey(text string) (Key, error) {
	text = strings.TrimSpace(text)
	switch {
	case text == "":
		return Key{}, errors.New("paste a public key: the one line in the .pub file, starting with ssh-ed25519 or ssh-rsa")
	case strings.Contains(text, "PRIVATE KEY"):
		return Key{}, errors.New("that is a private key, which should never leave your computer; paste the public key instead: the one line in the .pub file next to it, starting with ssh-ed25519 or ssh-rsa")
	case strings.Contains(text, "BEGIN SSH2 PUBLIC KEY"):
		return Key{}, errors.New("that is a key in PuTTY's format; turn it into OpenSSH's with ssh-keygen -i -f key.pub (or PuTTYgen's \"Public key for pasting into OpenSSH\") and paste that line")
	case strings.ContainsAny(text, "\r\n"):
		return Key{}, errors.New("paste one key at a time, on one line")
	}
	fields := strings.Fields(text)
	if _, ok := keyFields[fields[0]]; !ok {
		return Key{}, fmt.Errorf("%q is not a key type this takes; paste a line starting with ssh-ed25519, ssh-rsa or ecdsa-sha2-nistp256", clip(fields[0], 40))
	}
	if len(fields) < 2 {
		return Key{}, errors.New("the key's data is missing after its type; copy the whole line from the .pub file")
	}
	k, err := decode(fields[0], fields[1])
	if err != nil {
		return Key{}, err
	}
	k.Comment = cleanComment(strings.Join(fields[2:], " "))
	return k, nil
}

// decode reads a key's base64 data and checks it is a key of the type in front of it.
func decode(typ, data string) (Key, error) {
	blob, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return Key{}, errors.New("the key's data is cut short or changed (it is not valid base64); copy the whole line from the .pub file again")
	}
	parts, ok := lengthPrefixed(blob)
	if !ok || len(parts) == 0 {
		return Key{}, errors.New("the key's data is cut short or changed; copy the whole line from the .pub file again")
	}
	if inner := string(parts[0]); inner != typ {
		return Key{}, fmt.Errorf("the line says %s but its data is a %s key; copy the whole line from the .pub file again", typ, clip(inner, 40))
	}
	if want, known := keyFields[typ]; known && len(parts) != want {
		return Key{}, errors.New("the key's data is cut short or changed; copy the whole line from the .pub file again")
	}
	return Key{Type: typ, Blob: blob}, nil
}

// lengthPrefixed splits a key's data into its fields, each a 4-byte big-endian length then that many
// bytes; false when the data does not split exactly.
func lengthPrefixed(b []byte) ([][]byte, bool) {
	var parts [][]byte
	for len(b) > 0 {
		if len(b) < 4 {
			return nil, false
		}
		n := binary.BigEndian.Uint32(b)
		b = b[4:]
		if uint64(n) > uint64(len(b)) {
			return nil, false
		}
		parts = append(parts, b[:n])
		b = b[n:]
	}
	return parts, true
}

// cleanComment keeps a comment to printable text on one line, short enough to read.
func cleanComment(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	return clip(strings.TrimSpace(s), 200)
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// keysIn are the keys an authorized_keys file lists, of any type and with or without options in
// front ("from=\"10.0.0.0/8\",no-pty ssh-ed25519 AAAA…"); lines that are not keys are skipped.
func keysIn(b []byte) []Key {
	var keys []Key
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		if k, ok := keyOnLine(sc.Text()); ok {
			keys = append(keys, k)
		}
	}
	return keys
}

// keyOnLine finds the key on one line of authorized_keys: the first word followed by data that
// decodes to a key of that type. Options may hold quoted spaces, so the words are split around quotes.
func keyOnLine(line string) (Key, bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return Key{}, false
	}
	words := splitQuoted(line)
	for i := 0; i+1 < len(words); i++ {
		if k, err := decode(words[i], words[i+1]); err == nil {
			k.Comment = strings.Join(words[i+2:], " ")
			return k, true
		}
	}
	return Key{}, false
}

// splitQuoted splits on spaces outside double quotes.
func splitQuoted(s string) []string {
	var words []string
	var cur strings.Builder
	quoted := false
	for _, r := range s {
		switch {
		case r == '"':
			quoted = !quoted
			cur.WriteRune(r)
		case (r == ' ' || r == '\t') && !quoted:
			if cur.Len() > 0 {
				words = append(words, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		words = append(words, cur.String())
	}
	return words
}
