// Apple sign-in hashcash. Protocol details follow
// rudrankriyam/App-Store-Connect-CLI (MIT), internal/web/auth.go.

package websession

import (
	"crypto/sha1" //nolint:gosec // Apple's hashcash stamp is defined over SHA-1.
	"fmt"
	"time"
)

// hashcashMaxBits bounds the proof-of-work Apple may demand (it asks for ~10).
const hashcashMaxBits = 32

// makeHashcash returns the X-Apple-HC stamp "1:bits:date:challenge::counter"
// whose SHA-1 starts with at least bits zero bits.
func makeHashcash(bits int, challenge string, now time.Time) string {
	date := now.UTC().Format("20060102150405")
	for counter := 0; ; counter++ {
		candidate := fmt.Sprintf("1:%d:%s:%s::%d", bits, date, challenge, counter)
		sum := sha1.Sum([]byte(candidate)) //nolint:gosec // see import
		if leadingZeroBits(sum[:]) >= bits {
			return candidate
		}
	}
}

// leadingZeroBits counts the zero bits at the start of b.
func leadingZeroBits(b []byte) int {
	n := 0
	for _, c := range b {
		if c == 0 {
			n += 8
			continue
		}
		for mask := byte(0x80); mask != 0 && c&mask == 0; mask >>= 1 {
			n++
		}
		break
	}
	return n
}
