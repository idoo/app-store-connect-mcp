package websession

import (
	"crypto/sha1" //nolint:gosec // hashcash is SHA-1 by definition
	"strings"
	"testing"
	"time"
)

func TestMakeHashcash(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 34, 56, 0, time.UTC)
	for _, bits := range []int{0, 1, 8, 11, 16} {
		stamp := makeHashcash(bits, "4d74fb15eb23f465f1f6fcbf534e5877", now)
		if !strings.HasPrefix(stamp, "1:") || !strings.Contains(stamp, ":20261027123456:4d74fb15eb23f465f1f6fcbf534e5877::") {
			t.Fatalf("bad stamp format %q", stamp)
		}
		sum := sha1.Sum([]byte(stamp)) //nolint:gosec // see import
		if leadingZeroBits(sum[:]) < bits {
			t.Fatalf("stamp %q has fewer than %d leading zero bits", stamp, bits)
		}
	}
}

func TestLeadingZeroBits(t *testing.T) {
	cases := []struct {
		in   []byte
		want int
	}{
		{[]byte{0x80}, 0}, {[]byte{0x40}, 1}, {[]byte{0x01}, 7}, {[]byte{0, 0x10}, 11}, {[]byte{0, 0}, 16},
	}
	for _, c := range cases {
		if got := leadingZeroBits(c.in); got != c.want {
			t.Fatalf("leadingZeroBits(%x) = %d, want %d", c.in, got, c.want)
		}
	}
}
