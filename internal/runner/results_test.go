package runner

import (
	"bufio"
	"os"
	"testing"

	"github.com/goccy/go-json"
)

func countLines(t *testing.T, p string) int {
	return len(readResults(t, p))
}

func readResults(t *testing.T, p string) []Result {
	t.Helper()
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }() // read-only: a close error cannot lose data
	var out []Result
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		var r Result
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}
