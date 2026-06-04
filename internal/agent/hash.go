package agent

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"io"
	"os"
)

const hashSampleWindow = 4 << 20

func HashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	size := info.Size()

	h := sha256.New()
	var sz [8]byte
	binary.LittleEndian.PutUint64(sz[:], uint64(size))
	_, _ = h.Write(sz[:])

	if size <= 3*hashSampleWindow {
		if _, err := io.Copy(h, f); err != nil {
			return "", err
		}
		return hex.EncodeToString(h.Sum(nil)), nil
	}

	offsets := []int64{0, size/2 - hashSampleWindow/2, size - hashSampleWindow}
	for _, off := range offsets {
		if _, err := f.Seek(off, io.SeekStart); err != nil {
			return "", err
		}
		if _, err := io.CopyN(h, f, hashSampleWindow); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
