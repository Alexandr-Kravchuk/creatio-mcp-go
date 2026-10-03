package knowledge

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unicode/utf8"
)

// The built-in curated library and its pinned signing key, copied from clio's
// BuiltInKnowledgeBundleTrustStore. A settings entry can never substitute a key for this library.
const (
	CuratedAlias          = "creatio-curated"
	CuratedLibraryID      = "com.creatio.clio"
	CuratedLocation       = "https://api.github.com/"
	CuratedOwner          = "Advance-Technologies-Foundation"
	CuratedRepository     = "clio-knowledge"
	CuratedAssetName      = "clio-knowledge-bundle.zip"
	CuratedLegacyAlias    = "creatio-poc"
	CuratedPriority       = 100
	curatedPrimaryKeyID   = "clio-knowledge-2026-08"
	curatedPrimaryKeyPEM  = "-----BEGIN PUBLIC KEY-----\nMFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAEC3j3ZzoiZ8ZwxCnsKt1+ep949n7P\n1rMyfF0ND3Amxam+UQIvtBbpfy7Q+c1Q+ZG2G4A0AkA3R3ttZ8sDMBI3Ng==\n-----END PUBLIC KEY-----\n"
	maxPublicKeyBytes     = 16 * 1024
	legacyTrustedKeyIDEnv = "CLIO_KNOWLEDGE_TRUSTED_KEY_ID"
	legacyTrustedPathEnv  = "CLIO_KNOWLEDGE_TRUSTED_PUBLIC_KEY_PATH"
)

var builtInKeys = map[string]string{curatedPrimaryKeyID: curatedPrimaryKeyPEM}

// configuredTrustStore composes clio's three stores: pinned trust for the built-in library first and
// never overridable, then the key file of the one configured source publishing the library, and, for
// the legacy contract only, the environment-variable key.
type configuredTrustStore struct {
	sources func() (map[string]SourceConfig, error)
}

func (s configuredTrustStore) PublicKey(libraryID, keyID string, multiSource bool) (*ecdsa.PublicKey, bool) {
	if !multiSource {
		return environmentTrustedKey(keyID)
	}
	if libraryID == CuratedLibraryID {
		pemText, ok := builtInKeys[keyID]
		if !ok {
			return nil, false
		}
		key, _, ok := ParseP256PublicKeyPEM(pemText)
		return key, ok
	}
	sources, err := s.sources()
	if err != nil {
		return nil, false
	}
	var match *SourceConfig
	for _, source := range sources {
		if source.LibraryID == libraryID {
			if match != nil {
				return nil, false // clio's SingleOrDefault throws, which its store reports as untrusted
			}
			copied := source
			match = &copied
		}
	}
	if match == nil || match.TrustedKeyID != keyID {
		return nil, false
	}
	key, _, ok := readPublicKeyFile(match.TrustedPublicKeyPath)
	return key, ok
}

func environmentTrustedKey(keyID string) (*ecdsa.PublicKey, bool) {
	trustedKeyID := os.Getenv(legacyTrustedKeyIDEnv)
	path := os.Getenv(legacyTrustedPathEnv)
	if keyID != trustedKeyID || strings.TrimSpace(path) == "" || !filepath.IsAbs(path) {
		return nil, false
	}
	key, _, ok := readPublicKeyFile(path)
	return key, ok
}

// readPublicKeyFile applies EnvironmentKnowledgeBundleTrustStore.TryReadPublicKeyFile: an absolute local
// regular file without links in its ancestry, at most 16 KiB of strict UTF-8 holding one P-256 key.
func readPublicKeyFile(path string) (*ecdsa.PublicKey, []byte, bool) {
	normalized, ok := normalizeLocalPublicKeyPath(path, true)
	if !ok {
		return nil, nil, false
	}
	info, err := os.Stat(normalized)
	if err != nil || info.Size() == 0 || info.Size() > maxPublicKeyBytes {
		return nil, nil, false
	}
	data, err := os.ReadFile(normalized)
	if err != nil || !utf8.Valid(data) || bytes.HasPrefix(data, []byte("\xEF\xBB\xBF")) {
		return nil, nil, false
	}
	return ParseP256PublicKeyPEM(string(data))
}

func normalizeLocalPublicKeyPath(path string, requireExisting bool) (string, bool) {
	candidate := strings.TrimSpace(path)
	if candidate == "" || !filepath.IsAbs(candidate) || (runtime.GOOS == "windows" && strings.HasPrefix(candidate, `\\`)) {
		return "", false
	}
	normalized := filepath.Clean(candidate)
	if hasLinkInAncestry(normalized) {
		return "", false
	}
	if !requireExisting {
		return normalized, true
	}
	info, err := os.Lstat(normalized)
	if err != nil || !info.Mode().IsRegular() {
		return "", false
	}
	return normalized, true
}

// TrustFingerprint is the SHA-256 of a key file's SubjectPublicKeyInfo, or "unavailable"; clio folds it
// into a generation's identity so replacing the key file forces re-verification.
func TrustFingerprint(path string) string {
	if _, spki, ok := readPublicKeyFile(path); ok {
		sum := sha256.Sum256(spki)
		return strings.ToUpper(hex.EncodeToString(sum[:]))
	}
	return "unavailable"
}

// hasLinkInAncestry reports a symbolic link (or, on Windows, a junction) in any existing component.
func hasLinkInAncestry(path string) bool {
	current := path
	for current != "" {
		if info, err := os.Lstat(current); err == nil && isReparsePoint(info) {
			return true
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	return false
}

func isReparsePoint(info os.FileInfo) bool {
	mode := info.Mode()
	return mode&os.ModeSymlink != 0 || (runtime.GOOS == "windows" && mode&os.ModeIrregular != 0)
}
