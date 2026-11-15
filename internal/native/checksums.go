package native

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Checksum is one line of SHA256SUMS: a file's SHA-256 and its package path.
type Checksum struct {
	SHA256 string
	Path   string
}

// FormatChecksums writes SHA256SUMS in the format `shasum -a 256 -c` and
// `sha256sum -c` read: "<hex>  <path>", sorted by path.
func FormatChecksums(list []Checksum) []byte {
	sorted := append([]Checksum(nil), list...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })
	var b bytes.Buffer
	for _, c := range sorted {
		fmt.Fprintf(&b, "%s  %s\n", c.SHA256, c.Path)
	}
	return b.Bytes()
}

// ParseChecksums reads SHA256SUMS (binary-mode "<hex> *<path>" lines too).
func ParseChecksums(data []byte) ([]Checksum, error) {
	var out []Checksum
	sc := bufio.NewScanner(bytes.NewReader(data))
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimRight(sc.Text(), "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		sum, rest, ok := strings.Cut(line, " ")
		if !ok || len(sum) != 64 {
			return nil, fmt.Errorf("%s line %d isn't \"<sha256>  <path>\"", ChecksumsName, n)
		}
		if _, err := hex.DecodeString(sum); err != nil {
			return nil, fmt.Errorf("%s line %d: bad SHA-256", ChecksumsName, n)
		}
		p := strings.TrimPrefix(strings.TrimPrefix(rest, " "), "*")
		if _, err := CleanRel(p); err != nil {
			return nil, fmt.Errorf("%s line %d: %v", ChecksumsName, n, err)
		}
		out = append(out, Checksum{SHA256: strings.ToLower(sum), Path: p})
	}
	return out, sc.Err()
}

// FileSHA256 hashes a file.
func FileSHA256(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// VerifyPackage checks every file SHA256SUMS lists under root. checking is
// told each path before it is hashed. It returns one problem per bad file.
func VerifyPackage(root string, checking func(path string)) ([]string, error) {
	data, err := os.ReadFile(filepath.Join(root, ChecksumsName))
	if err != nil {
		return nil, err
	}
	list, err := ParseChecksums(data)
	if err != nil {
		return nil, err
	}
	var problems []string
	for _, c := range list {
		if checking != nil {
			checking(c.Path)
		}
		p, err := Resolve(root, c.Path)
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		got, err := FileSHA256(p)
		switch {
		case os.IsNotExist(err):
			problems = append(problems, c.Path+" is missing")
		case err != nil:
			problems = append(problems, c.Path+": "+err.Error())
		case got != c.SHA256:
			problems = append(problems, c.Path+" has changed (its SHA-256 doesn't match)")
		}
	}
	return problems, nil
}
