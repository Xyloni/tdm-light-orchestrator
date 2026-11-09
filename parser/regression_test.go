package parser

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReviewDuplicateLoginsRejected(t *testing.T) {
	p := filepath.Join(t.TempDir(), "account.txt")
	os.WriteFile(p, []byte("e:same:p:token:1:dev\ne:same:p:token2:2:dev\n"), 0600)
	if _, err := ParseFile(p); err == nil {
		t.Fatal("duplicate logins accepted; manager overwrites account state")
	}
}
func TestReviewPathLoginRejected(t *testing.T) {
	p := filepath.Join(t.TempDir(), "account.txt")
	os.WriteFile(p, []byte("e:../outside:p:token:1:dev\n"), 0600)
	if _, err := ParseFile(p); err == nil {
		t.Fatal("path traversal accepted as login")
	}
}
func BenchmarkReviewParse10000(b *testing.B) {
	var s strings.Builder
	for i := 0; i < 10000; i++ {
		fmt.Fprintf(&s, "e:user%d:p:token:%d:device\n", i, i)
	}
	p := filepath.Join(b.TempDir(), "account.txt")
	os.WriteFile(p, []byte(s.String()), 0600)
	b.ReportAllocs()
	b.SetBytes(int64(s.Len()))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		g, err := ParseFile(p)
		if err != nil || len(g[0]) != 10000 {
			b.Fatal(err)
		}
	}
}
