package corpus

import (
	"bufio"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Large corpora are stored as gzipped shards so that every file stays under
// GitHub's size limit:
//
//	records-000.jsonl.gz, records-001.jsonl.gz, ...
//	oracle-000.jsonl.gz,  oracle-001.jsonl.gz,  ...
//
// SHA256SUMS lists the stored files, but a .gz file is hashed by its
// decompressed content, so the corpus hash is a property of the data and not
// of the gzip encoder. The G0 corpus keeps its single uncompressed
// records.jsonl and oracle.jsonl.

// ShardSize is the number of records per shard.
const ShardSize = 100_000

// WriteShards writes items as gzipped JSONL shards named <prefix>-NNN.jsonl.gz.
func WriteShards[T any](dir, prefix string, items []T) error {
	for s := 0; s*ShardSize < len(items); s++ {
		end := min((s+1)*ShardSize, len(items))
		path := filepath.Join(dir, fmt.Sprintf("%s-%03d.jsonl.gz", prefix, s))
		f, err := os.Create(path)
		if err != nil {
			return err
		}
		zw, err := gzip.NewWriterLevel(f, gzip.BestCompression)
		if err != nil {
			f.Close()
			return err
		}
		bw := bufio.NewWriterSize(zw, 1<<20)
		enc := json.NewEncoder(bw)
		enc.SetEscapeHTML(false)
		for i := s * ShardSize; i < end; i++ {
			if err := enc.Encode(items[i]); err != nil {
				f.Close()
				return fmt.Errorf("encode %s: %w", path, err)
			}
		}
		if err := bw.Flush(); err != nil {
			f.Close()
			return err
		}
		if err := zw.Close(); err != nil {
			f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
	}
	return nil
}

// files returns the stored files holding prefix ("records" or "oracle"),
// in order: the single G0 file, or the shards.
func files(dir, prefix string) ([]string, error) {
	single := filepath.Join(dir, prefix+".jsonl")
	if _, err := os.Stat(single); err == nil {
		return []string{single}, nil
	}
	shards, err := filepath.Glob(filepath.Join(dir, prefix+"-[0-9][0-9][0-9].jsonl.gz"))
	if err != nil {
		return nil, err
	}
	if len(shards) == 0 {
		return nil, fmt.Errorf("corpus %s has no %s files", dir, prefix)
	}
	sort.Strings(shards)
	return shards, nil
}

func open(path string) (io.ReadCloser, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	if !strings.HasSuffix(path, ".gz") {
		return f, nil
	}
	zr, err := gzip.NewReader(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	return struct {
		io.Reader
		io.Closer
	}{zr, closers{zr, f}}, nil
}

type closers []io.Closer

func (cs closers) Close() error {
	var first error
	for _, c := range cs {
		if err := c.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// Each streams every value stored under prefix, in corpus order.
func Each[T any](dir, prefix string, fn func(T) error) error {
	paths, err := files(dir, prefix)
	if err != nil {
		return err
	}
	for _, p := range paths {
		r, err := open(p)
		if err != nil {
			return err
		}
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 1<<20), 1<<24)
		for line := 1; sc.Scan(); line++ {
			var v T
			if err := json.Unmarshal(sc.Bytes(), &v); err != nil {
				r.Close()
				return fmt.Errorf("%s line %d: %w", p, line, err)
			}
			if err := fn(v); err != nil {
				r.Close()
				return err
			}
		}
		if err := sc.Err(); err != nil {
			r.Close()
			return err
		}
		r.Close()
	}
	return nil
}

// All reads every value stored under prefix.
func All[T any](dir, prefix string) ([]T, error) {
	var out []T
	err := Each(dir, prefix, func(v T) error { out = append(out, v); return nil })
	return out, err
}

// Records and Truths read a corpus in either layout.
func Records(dir string) ([]Record, error) { return All[Record](dir, "records") }

// Truths reads the oracle file(s).
func Truths(dir string) ([]Truth, error) { return All[Truth](dir, "oracle") }

// hashFile hashes a stored file's content, decompressing .gz files.
func hashFile(path string) (string, error) {
	r, err := open(path)
	if err != nil {
		return "", err
	}
	defer r.Close()
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// dataFiles lists the files a corpus hash covers: record and oracle files in
// either layout, and the policy pair.
func dataFiles(dir string) ([]string, error) {
	var names []string
	for _, prefix := range []string{"records", "oracle"} {
		ps, err := files(dir, prefix)
		if err != nil {
			return nil, err
		}
		for _, p := range ps {
			rel, _ := filepath.Rel(dir, p)
			names = append(names, rel)
		}
	}
	names = append(names, filepath.Join(PolicyDir, CedarFile), filepath.Join(PolicyDir, ProseFile))
	sort.Strings(names)
	return names, nil
}
