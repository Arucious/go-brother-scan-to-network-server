package brother

import (
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
)

// digestChallenge is a parsed WWW-Authenticate: Digest header.
type digestChallenge struct {
	realm, nonce, opaque, algorithm, qop string
}

func parseDigestChallenge(header string) (digestChallenge, error) {
	scheme, rest, _ := strings.Cut(strings.TrimSpace(header), " ")
	if !strings.EqualFold(scheme, "Digest") {
		return digestChallenge{}, fmt.Errorf("unsupported auth scheme %q", scheme)
	}
	var c digestChallenge
	for _, part := range splitAuthParams(rest) {
		k, v, _ := strings.Cut(part, "=")
		v = strings.Trim(strings.TrimSpace(v), `"`)
		switch strings.ToLower(strings.TrimSpace(k)) {
		case "realm":
			c.realm = v
		case "nonce":
			c.nonce = v
		case "opaque":
			c.opaque = v
		case "algorithm":
			c.algorithm = v
		case "qop":
			c.qop = v
		}
	}
	if c.nonce == "" {
		return c, fmt.Errorf("digest challenge without nonce")
	}
	if c.algorithm != "" && !strings.EqualFold(c.algorithm, "MD5") {
		return c, fmt.Errorf("unsupported digest algorithm %q", c.algorithm)
	}
	return c, nil
}

// splitAuthParams splits comma separated auth params, honouring quotes.
func splitAuthParams(s string) []string {
	var out []string
	var cur strings.Builder
	quoted := false
	for _, r := range s {
		switch {
		case r == '"':
			quoted = !quoted
			cur.WriteRune(r)
		case r == ',' && !quoted:
			out = append(out, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

func md5hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

// authorization builds the Authorization header for one request.
func (c digestChallenge) authorization(method, uri, user, password string) string {
	ha1 := md5hex(user + ":" + c.realm + ":" + password)
	ha2 := md5hex(method + ":" + uri)

	parts := []string{
		fmt.Sprintf(`username="%s"`, user),
		fmt.Sprintf(`realm="%s"`, c.realm),
		fmt.Sprintf(`nonce="%s"`, c.nonce),
		fmt.Sprintf(`uri="%s"`, uri),
		`algorithm=MD5`,
	}

	var response string
	if hasQopAuth(c.qop) {
		cnonceBytes := make([]byte, 8)
		_, _ = rand.Read(cnonceBytes)
		cnonce := hex.EncodeToString(cnonceBytes)
		nc := "00000001"
		response = md5hex(strings.Join([]string{ha1, c.nonce, nc, cnonce, "auth", ha2}, ":"))
		parts = append(parts, `qop=auth`, "nc="+nc, fmt.Sprintf(`cnonce="%s"`, cnonce))
	} else {
		response = md5hex(ha1 + ":" + c.nonce + ":" + ha2)
	}
	parts = append(parts, fmt.Sprintf(`response="%s"`, response))
	if c.opaque != "" {
		parts = append(parts, fmt.Sprintf(`opaque="%s"`, c.opaque))
	}
	return "Digest " + strings.Join(parts, ", ")
}

func hasQopAuth(qop string) bool {
	for _, q := range strings.Split(qop, ",") {
		if strings.TrimSpace(q) == "auth" {
			return true
		}
	}
	return false
}
