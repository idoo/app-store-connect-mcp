package upload

import (
	"crypto/md5" //nolint:gosec // Apple's sourceFileChecksum is MD5; not used for security.
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
)

// FileMD5 streams r and returns its MD5 digest as lowercase hex, the format
// of Apple's sourceFileChecksum. Memory use is constant.
func FileMD5(r io.Reader) (string, error) {
	h := md5.New() //nolint:gosec // see import
	if _, err := io.Copy(h, r); err != nil {
		return "", fmt.Errorf("md5: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// fileDigests streams r once and returns its MD5 and SHA-256 as hex.
func fileDigests(r io.Reader) (md5Hex, sha256Hex string, err error) {
	m := md5.New() //nolint:gosec // see import
	s := sha256.New()
	if _, err := io.Copy(io.MultiWriter(m, s), r); err != nil {
		return "", "", fmt.Errorf("digest: %w", err)
	}
	return hex.EncodeToString(m.Sum(nil)), hex.EncodeToString(s.Sum(nil)), nil
}
