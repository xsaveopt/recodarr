package agent

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return p
}

func TestHashFileOfASmallFileCoversEveryByte(t *testing.T) {
	data := []byte(strings.Repeat("recodarr", 1000))
	p := writeFile(t, "small.mkv", data)

	got, err := HashFile(p)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}

	h := sha256.New()
	var sz [8]byte
	binary.LittleEndian.PutUint64(sz[:], uint64(len(data)))
	h.Write(sz[:])
	h.Write(data)
	if want := hex.EncodeToString(h.Sum(nil)); got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestHashFileIsStableAndSensitiveToContent(t *testing.T) {
	a := writeFile(t, "a.mkv", []byte("one"))
	b := writeFile(t, "b.mkv", []byte("two"))

	ha1, err := HashFile(a)
	if err != nil {
		t.Fatalf("hash a: %v", err)
	}
	ha2, _ := HashFile(a)
	hb, _ := HashFile(b)
	if ha1 != ha2 {
		t.Fatal("hashing the same file twice gave different results")
	}
	if ha1 == hb {
		t.Fatal("different content hashed the same")
	}
}

func largeSparseFile(t *testing.T, size int64, marks map[int64]byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "large.mkv")
	f, err := os.Create(p)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer f.Close()
	if err := f.Truncate(size); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	for off, b := range marks {
		if _, err := f.WriteAt([]byte{b}, off); err != nil {
			t.Fatalf("write at %d: %v", off, err)
		}
	}
	return p
}

func TestHashFileOfALargeFileSamplesOnlyThreeWindows(t *testing.T) {
	size := int64(4*hashSampleWindow + 1024)
	base, err := HashFile(largeSparseFile(t, size, nil))
	if err != nil {
		t.Fatalf("hash base: %v", err)
	}

	outside := int64(hashSampleWindow + 10)
	untouched, err := HashFile(largeSparseFile(t, size, map[int64]byte{outside: 'x'}))
	if err != nil {
		t.Fatalf("hash outside: %v", err)
	}
	if untouched != base {
		t.Fatal("a byte outside the sampled windows changed the hash")
	}

	for name, off := range map[string]int64{
		"head":   0,
		"middle": size / 2,
		"tail":   size - 1,
	} {
		got, err := HashFile(largeSparseFile(t, size, map[int64]byte{off: 'x'}))
		if err != nil {
			t.Fatalf("hash %s: %v", name, err)
		}
		if got == base {
			t.Fatalf("a byte in the %s window did not change the hash", name)
		}
	}
}

func TestHashFileMixesInTheSize(t *testing.T) {
	size := int64(4*hashSampleWindow + 1024)
	a, err := HashFile(largeSparseFile(t, size, nil))
	if err != nil {
		t.Fatalf("hash a: %v", err)
	}
	b, err := HashFile(largeSparseFile(t, size+1, nil))
	if err != nil {
		t.Fatalf("hash b: %v", err)
	}
	if a == b {
		t.Fatal("files of different sizes hashed the same")
	}
}

func TestHashFileReportsAMissingFile(t *testing.T) {
	if _, err := HashFile(filepath.Join(t.TempDir(), "nope.mkv")); err == nil {
		t.Fatal("hashing a missing file succeeded")
	}
}
