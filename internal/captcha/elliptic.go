package captcha

import (
	"crypto/elliptic"
)

// ellipticP256 returns the NIST P-256 curve. Wrapped in a helper so the
// rest of the package can avoid importing crypto/elliptic everywhere and
// so we can swap to a different P-256 implementation (e.g. stdlib's
// crypto/ecdh once it gains the affordances we need) without touching
// every call site.
func ellipticP256() elliptic.Curve {
	return elliptic.P256()
}
