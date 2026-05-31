// Package secrets provides age-based encryption-at-rest for ductile configuration
// and token files. It is deliberately small and stateless: pure functions over
// byte slices plus a Keyring value that holds resolved identities. The gateway
// decrypts in memory at config load; plaintext is never written back to disk.
//
// Design notes (Hickey/Armstrong): encryption is a data transformation, not a
// process concern — these functions take bytes and return bytes and own no
// state. The only state is the Keyring, an explicit value threaded by the
// caller (the config loader) rather than a package global.
package secrets

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	"filippo.io/age"
	"filippo.io/age/armor"
)

// binaryHeader is the first line of a binary age file.
const binaryHeader = "age-encryption.org/v1"

// armorHeader is the first line of an ASCII-armored age file.
const armorHeader = "-----BEGIN AGE ENCRYPTED FILE-----"

// IsEncrypted reports whether data is an age ciphertext (binary or armored).
// Detection is content-based, not extension-based, so an operator may name an
// encrypted file anything (e.g. tokens.yaml) and the loader still recognises it.
func IsEncrypted(data []byte) bool {
	trimmed := bytes.TrimLeft(data, " \t\r\n")
	return bytes.HasPrefix(trimmed, []byte(binaryHeader)) ||
		bytes.HasPrefix(trimmed, []byte(armorHeader))
}

// Encrypt encrypts plaintext to one or more recipients and returns ASCII-armored
// ciphertext (text-friendly for git and diffs). Multi-recipient is the intended
// use: one bundle, a separate key per host.
func Encrypt(plaintext []byte, recipients []age.Recipient) ([]byte, error) {
	if len(recipients) == 0 {
		return nil, fmt.Errorf("encrypt: no recipients")
	}
	var buf bytes.Buffer
	armorW := armor.NewWriter(&buf)
	w, err := age.Encrypt(armorW, recipients...)
	if err != nil {
		return nil, fmt.Errorf("encrypt: init: %w", err)
	}
	if _, err := w.Write(plaintext); err != nil {
		return nil, fmt.Errorf("encrypt: write: %w", err)
	}
	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("encrypt: finalize: %w", err)
	}
	if err := armorW.Close(); err != nil {
		return nil, fmt.Errorf("encrypt: armor: %w", err)
	}
	return buf.Bytes(), nil
}

// Decrypt decrypts age ciphertext (binary or armored) with the given identities.
// It fails hard on any error — a bad key, a wrong key, or corrupt ciphertext all
// return an error rather than silently yielding empty or partial plaintext.
func Decrypt(ciphertext []byte, identities []age.Identity) ([]byte, error) {
	if len(identities) == 0 {
		return nil, fmt.Errorf("decrypt: no identities available")
	}
	var src io.Reader = bytes.NewReader(ciphertext)
	if bytes.HasPrefix(bytes.TrimLeft(ciphertext, " \t\r\n"), []byte(armorHeader)) {
		src = armor.NewReader(src)
	}
	r, err := age.Decrypt(src, identities...)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}
	plaintext, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("decrypt: read: %w", err)
	}
	return plaintext, nil
}

// GenerateIdentity creates a fresh X25519 identity (private key) and its
// recipient (public key).
func GenerateIdentity() (*age.X25519Identity, error) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		return nil, fmt.Errorf("generate identity: %w", err)
	}
	return id, nil
}

// ParseIdentities parses one or more age identities from a key file's contents.
// It tolerates comments and blank lines (age's own format).
func ParseIdentities(data []byte) ([]age.Identity, error) {
	ids, err := age.ParseIdentities(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("parse identities: %w", err)
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("parse identities: no identities found")
	}
	return ids, nil
}

// ParseRecipients parses age recipients (public keys) from text, one per line,
// ignoring comments and blanks.
func ParseRecipients(data []byte) ([]age.Recipient, error) {
	var recipients []age.Recipient
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		r, err := age.ParseX25519Recipient(line)
		if err != nil {
			return nil, fmt.Errorf("parse recipient %q: %w", line, err)
		}
		recipients = append(recipients, r)
	}
	if len(recipients) == 0 {
		return nil, fmt.Errorf("parse recipients: none found")
	}
	return recipients, nil
}
