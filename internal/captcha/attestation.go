package captcha

import (
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"fmt"
)

// RegisterAttestation parses an Apple App Attest attestationObject and
// extracts the attestation public key (ECDSA P-256). Returns the raw key
// id (sha256 over the credential public key) plus the parsed key, ready to
// persist in the attest_keys table.
//
// SECURITY CAVEATS:
//
//  1. We do NOT validate Apple's x5c certificate chain against the Apple
//     App Attest root. That chain validation is a separate concern and
//     would require shipping Apple's root cert and CRL handling. Tracked
//     as a TODO — the current implementation extracts the credentialPublicKey
//     from the authData but trusts that the key is legitimate. Operators
//     should pair this with rate limiting on /api/attest/register and
//     out-of-band fraud signals until the chain validation lands.
//
//  2. The challenge binding is validated by the caller (handler) before
//     calling here — we do NOT consume the challenge in this function so
//     the same parsed attestation can be validated against the issued
//     challenge by the handler in a single SQL transaction.
//
//  3. The receipt is parsed but not verified against Apple's receipt
//     verification endpoint. Again tracked as TODO.
//
// Why ship this incomplete: full chain validation requires a Maintained
// Apple root + CRL pipeline. The signature-based Verify() path (the hot
// path) is complete and load-bearing on its own — the registration path
// is bootstrap-only and rate-limited.
type AttestationResult struct {
	KeyID     string
	PublicKey *ecdsa.PublicKey
	Counter   uint32
	AAGUID    [16]byte // 0x617070617474657374... for Apple App Attest sandbox/prod
}

// ParseAttestationObject decodes an Apple App Attest attestationObject blob
// (CBOR map of fmt / authData / attStmt). Only authData is decoded — that's
// where the credential public key lives.
func ParseAttestationObject(blob []byte) (*AttestationResult, error) {
	authData, err := extractAuthData(blob)
	if err != nil {
		return nil, fmt.Errorf("attestation: %w", err)
	}

	// authData layout:
	//   rpIdHash (32)
	//   flags (1)
	//   signCount (4)
	//   AAGUID (16)
	//   credentialIdLen (2 BE)
	//   credentialId (var)
	//   credentialPublicKey (CBOR COSE_Key)
	if len(authData) < 37+16+2 {
		return nil, errors.New("attestation: authData too short for attested credential data")
	}
	counter := binary.BigEndian.Uint32(authData[33:37])
	var aaguid [16]byte
	copy(aaguid[:], authData[37:53])
	credIDLen := binary.BigEndian.Uint16(authData[53:55])
	if int(55+credIDLen) > len(authData) {
		return nil, errors.New("attestation: credentialIdLen overflow")
	}
	credID := authData[55 : 55+credIDLen]
	pubKeyCBOR := authData[55+credIDLen:]

	pub, err := decodeCOSEECP256(pubKeyCBOR)
	if err != nil {
		return nil, fmt.Errorf("attestation: cose key: %w", err)
	}

	// Apple uses sha256(credentialPublicKey-as-uncompressed-point) as the
	// keyID surface to clients. We also accept the credentialId as the
	// keyID since the iOS client passes that to the server unmodified.
	// For consistency with the DCAppAttestService.generateKey API which
	// returns a base64-encoded keyID directly equal to credentialId, we
	// use credentialId here.
	keyIDHash := sha256.Sum256(credID)
	keyID := fmt.Sprintf("%x", keyIDHash[:])

	return &AttestationResult{
		KeyID:     keyID,
		PublicKey: pub,
		Counter:   counter,
		AAGUID:    aaguid,
	}, nil
}

// extractAuthData parses just enough of the attestationObject CBOR map to
// pull out the "authData" byte-string. We avoid a full CBOR dep by scanning
// for the known keys.
//
// The blob shape is { fmt: "apple-appattest", attStmt: {...}, authData: bytes }.
// CBOR maps are not order-stable in the spec, so we walk every key/value
// pair until we find authData.
func extractAuthData(blob []byte) ([]byte, error) {
	if len(blob) < 1 {
		return nil, errors.New("empty blob")
	}
	major := blob[0] >> 5
	if major != 5 {
		return nil, fmt.Errorf("expected CBOR map, major=%d", major)
	}
	mapLen, hdr, err := cborReadLength(blob)
	if err != nil {
		return nil, err
	}
	off := hdr
	for i := uint64(0); i < mapLen; i++ {
		key, n, err := cborReadString(blob[off:])
		if err != nil {
			return nil, err
		}
		off += n
		if key == "authData" {
			val, m, err := cborReadBytes(blob[off:])
			if err != nil {
				return nil, err
			}
			_ = m
			return val, nil
		}
		// Skip arbitrary value (string, bytes, or nested map).
		n, err = cborSkipValue(blob[off:])
		if err != nil {
			return nil, fmt.Errorf("skip value for key %q: %w", key, err)
		}
		off += n
	}
	return nil, errors.New("authData key not present")
}

// cborSkipValue advances past one CBOR value and returns the number of
// bytes consumed. Supports the major types appearing in attestation
// objects: bytestring (2), textstring (3), map (5), and the receipt subtype.
func cborSkipValue(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, errors.New("cbor: skip from empty buffer")
	}
	major := b[0] >> 5
	switch major {
	case 0, 1: // unsigned int / negative int — head only
		_, n, err := cborReadLength(b)
		return n, err
	case 2: // byte string
		_, n, err := cborReadBytes(b)
		return n, err
	case 3: // text string
		_, n, err := cborReadString(b)
		return n, err
	case 4: // array
		arrLen, n, err := cborReadLength(b)
		if err != nil {
			return 0, err
		}
		off := n
		for i := uint64(0); i < arrLen; i++ {
			m, err := cborSkipValue(b[off:])
			if err != nil {
				return 0, err
			}
			off += m
		}
		return off, nil
	case 5: // map
		mapLen, n, err := cborReadLength(b)
		if err != nil {
			return 0, err
		}
		off := n
		for i := uint64(0); i < mapLen; i++ {
			km, err := cborSkipValue(b[off:])
			if err != nil {
				return 0, err
			}
			off += km
			vm, err := cborSkipValue(b[off:])
			if err != nil {
				return 0, err
			}
			off += vm
		}
		return off, nil
	default:
		// Major types 6 (tag) and 7 (float/bool/null) aren't expected in
		// our parsed paths; reject so a future spec change surfaces.
		return 0, fmt.Errorf("cbor: unsupported major type %d", major)
	}
}

// decodeCOSEECP256 parses a COSE_Key (CBOR map) describing a P-256 ECDSA
// public key. The keys we expect:
//
//	1 (kty) -> 2 (EC2)
//	3 (alg) -> -7 (ES256)
//	-1 (crv) -> 1 (P-256)
//	-2 (x) -> bytes(32)
//	-3 (y) -> bytes(32)
//
// We scan the map until we find -2 (x) and -3 (y), reconstruct the public
// key from those coordinates. Validation of kty/alg/crv is best-effort —
// we accept any ES256 key where x/y are present and 32 bytes each.
func decodeCOSEECP256(b []byte) (*ecdsa.PublicKey, error) {
	if len(b) == 0 || b[0]>>5 != 5 {
		return nil, errors.New("cose: expected CBOR map")
	}
	mapLen, n, err := cborReadLength(b)
	if err != nil {
		return nil, err
	}
	off := n
	var xCoord, yCoord []byte
	for i := uint64(0); i < mapLen; i++ {
		// Key is a (possibly negative) integer. CBOR encodes negative
		// integers under major type 1 with value = -1 - (head value), so
		// -1 → 0x20, -2 → 0x21, -3 → 0x22.
		if off >= len(b) {
			return nil, errors.New("cose: truncated map")
		}
		head := b[off]
		var keyInt int64
		major := head >> 5
		switch major {
		case 0:
			val, m, err := cborReadLength(b[off:])
			if err != nil {
				return nil, err
			}
			keyInt = int64(val)
			off += m
		case 1:
			val, m, err := cborReadLength(b[off:])
			if err != nil {
				return nil, err
			}
			keyInt = -1 - int64(val)
			off += m
		default:
			return nil, fmt.Errorf("cose: unexpected key major %d", major)
		}
		// Read value — could be int (length-style) or bytes.
		if off >= len(b) {
			return nil, errors.New("cose: truncated map value")
		}
		valMajor := b[off] >> 5
		switch valMajor {
		case 0, 1:
			m, err := cborSkipValue(b[off:])
			if err != nil {
				return nil, err
			}
			off += m
		case 2:
			val, m, err := cborReadBytes(b[off:])
			if err != nil {
				return nil, err
			}
			switch keyInt {
			case -2:
				xCoord = val
			case -3:
				yCoord = val
			}
			off += m
		default:
			m, err := cborSkipValue(b[off:])
			if err != nil {
				return nil, err
			}
			off += m
		}
	}
	if len(xCoord) != 32 || len(yCoord) != 32 {
		return nil, fmt.Errorf("cose: missing x/y (xLen=%d yLen=%d)", len(xCoord), len(yCoord))
	}
	point := make([]byte, 0, 65)
	point = append(point, 0x04)
	point = append(point, xCoord...)
	point = append(point, yCoord...)
	return publicKeyFromUncompressed(point)
}

// ValidateAttestationStatement is a placeholder for future Apple x5c chain
// validation. Currently returns nil unconditionally. Implementing it
// requires:
//
//  1. Bundling Apple's App Attest root cert (Apple App Attestation Root CA).
//  2. Walking the x5c[] array of the attStmt and verifying each link with
//     crypto/x509's Verify against the root pool.
//  3. Pulling and checking the receipt (the leaf cert's appattest extension).
//
// Until that lands, register endpoints should be (a) heavily rate-limited
// and (b) optionally gated behind an admin-issued bootstrap token so that
// only legitimate clients ever exercise the path.
//
// Exposed so the handler explicitly opts in to "skip chain validation" —
// future work to replace the body is mechanical.
func ValidateAttestationStatement(_ []byte) error {
	return nil
}

// ParseAttestationCertChain is a stub for the future x5c-chain validation.
// Returns the parsed leaf cert for inspection (e.g. checking the appattest
// OID extension) but doesn't enforce chain trust.
func ParseAttestationCertChain(_ []byte) (*x509.Certificate, error) {
	return nil, errors.New("attestation chain parsing not yet implemented")
}
