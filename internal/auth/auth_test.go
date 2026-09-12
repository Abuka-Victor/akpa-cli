package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

const pw = "open-sesame"

func secretHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("the files"))
	})
}

func newGate(t *testing.T) (*Gate, http.Handler) {
	t.Helper()
	g, err := New(pw)
	if err != nil {
		t.Fatal(err)
	}
	return g, g.Wrap(secretHandler())
}

// get issues a GET with the given headers, the way the login page's fetch does.
func get(h http.Handler, headers map[string]string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func challenge(t *testing.T, h http.Handler) string {
	t.Helper()
	w := get(h, map[string]string{headerChallenge: "1"})
	if w.Code != http.StatusOK {
		t.Fatalf("challenge status = %d, want 200", w.Code)
	}
	return strings.TrimSpace(w.Body.String())
}

// proveWith is the browser's half of the exchange, in Go: the nonce signed
// with the hash of the password as the key.
func proveWith(password, nonce string) string {
	sum := sha256.Sum256([]byte(password))
	mac := hmac.New(sha256.New, sum[:])
	mac.Write([]byte(nonce))
	return hex.EncodeToString(mac.Sum(nil))
}

func login(t *testing.T, h http.Handler, password string) *httptest.ResponseRecorder {
	t.Helper()
	nonce := challenge(t, h)
	return get(h, map[string]string{headerNonce: nonce, headerProof: proveWith(password, nonce)})
}

func TestGateBlocksAnonymous(t *testing.T) {
	_, h := newGate(t)

	w := get(h, nil)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
	if strings.Contains(w.Body.String(), "the files") {
		t.Error("content leaked to an unauthenticated request")
	}
	if !strings.Contains(w.Body.String(), "This share is protected") {
		t.Error("login page not served")
	}
}

func TestCorrectPasswordUnlocks(t *testing.T) {
	_, h := newGate(t)

	w := login(t, h, pw)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", w.Code)
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("got %d cookies, want 1", len(cookies))
	}
	if !cookies[0].HttpOnly {
		t.Error("session cookie is not HttpOnly")
	}

	w2 := get(h, nil, cookies[0])
	if w2.Code != http.StatusOK || w2.Body.String() != "the files" {
		t.Errorf("authenticated request got %d %q", w2.Code, w2.Body.String())
	}
}

func TestWrongPasswordIsRejected(t *testing.T) {
	_, h := newGate(t)

	w := login(t, h, "not-the-password")
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
	if w.Body.String() != "wrong" {
		t.Errorf("reason = %q, want %q", w.Body.String(), "wrong")
	}
	if len(w.Result().Cookies()) != 0 {
		t.Error("a session cookie was issued for a wrong password")
	}
}

// The proof is the only thing that crosses the tunnel, so replaying a captured
// one must not work a second time.
func TestProofCannotBeReplayed(t *testing.T) {
	_, h := newGate(t)

	nonce := challenge(t, h)
	proof := proveWith(pw, nonce)
	headers := map[string]string{headerNonce: nonce, headerProof: proof}

	if w := get(h, headers); w.Code != http.StatusNoContent {
		t.Fatalf("first use status = %d, want 204", w.Code)
	}

	w := get(h, headers)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("replayed proof was accepted (status %d)", w.Code)
	}
	if w.Body.String() != "stale" {
		t.Errorf("reason = %q, want %q", w.Body.String(), "stale")
	}
	if len(w.Result().Cookies()) != 0 {
		t.Error("replay issued a session cookie")
	}
}

func TestForgedAndExpiredNoncesAreRejected(t *testing.T) {
	g, h := newGate(t)

	// A nonce minted by a different process must not be usable here.
	other, _ := New(pw)
	foreign := other.newNonce()

	expiredBody := strconv.FormatInt(time.Now().Add(-time.Minute).Unix(), 10) + ".abcd"
	expired := expiredBody + "." + hex.EncodeToString(g.sign(expiredBody))

	// Correctly signed, but with the expiry edited afterwards.
	validBody := strconv.FormatInt(time.Now().Add(nonceTTL).Unix(), 10) + ".abcd"
	tampered := strconv.FormatInt(time.Now().Add(99*time.Hour).Unix(), 10) + ".abcd." +
		hex.EncodeToString(g.sign(validBody))

	for name, nonce := range map[string]string{
		"garbage":         "nonsense",
		"unsigned":        validBody,
		"bad signature":   validBody + ".00ff",
		"expired":         expired,
		"another process": foreign,
		"tampered expiry": tampered,
	} {
		w := get(h, map[string]string{headerNonce: nonce, headerProof: proveWith(pw, nonce)})
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s nonce was accepted (status %d)", name, w.Code)
		}
		if len(w.Result().Cookies()) != 0 {
			t.Errorf("%s nonce issued a session cookie", name)
		}
	}
}

func TestForgedAndExpiredCookiesAreRejected(t *testing.T) {
	g, h := newGate(t)

	future := strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)
	past := strconv.FormatInt(time.Now().Add(-time.Hour).Unix(), 10)

	other, _ := New(pw)
	stolen := future + "." + hex.EncodeToString(other.sign(future))

	for name, value := range map[string]string{
		"garbage":         "nonsense",
		"no signature":    future,
		"bad signature":   future + ".00ff",
		"expired":         past + "." + hex.EncodeToString(g.sign(past)),
		"another process": stolen,
		"tampered expiry": strconv.FormatInt(time.Now().Add(99*time.Hour).Unix(), 10) + "." + hex.EncodeToString(g.sign(future)),
	} {
		w := get(h, nil, &http.Cookie{Name: g.cookie, Value: value})
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s cookie was accepted (status %d)", name, w.Code)
		}
	}
}

// The password must never appear in anything the gate sends, since everything
// it sends crosses the plaintext relay tunnel.
func TestPasswordNeverAppearsInResponses(t *testing.T) {
	_, h := newGate(t)

	bodies := []string{
		get(h, nil).Body.String(),
		get(h, map[string]string{headerChallenge: "1"}).Body.String(),
		login(t, h, pw).Body.String(),
	}
	for _, b := range bodies {
		if strings.Contains(b, pw) {
			t.Error("a gate response contained the password")
		}
	}
}

func TestChallengesAreUnique(t *testing.T) {
	_, h := newGate(t)

	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		n := challenge(t, h)
		if seen[n] {
			t.Fatal("challenge repeated")
		}
		seen[n] = true
	}
}

func TestEmptyPasswordIsNotAGate(t *testing.T) {
	if _, err := New(""); err == nil {
		t.Error(`New("") should fail rather than build a gate that opens for nothing`)
	}
}
