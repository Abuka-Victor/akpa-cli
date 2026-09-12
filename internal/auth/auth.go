// Package auth implements the password gate that sits in front of the local
// file server.
//
// Everything here runs client-side on purpose. The relay never learns the
// password and holds no session state: it forwards bytes, and this package
// decides — inside the CLI process, against an in-memory secret — whether a
// request is allowed through.
//
// # Why the login is not a form POST
//
// Two constraints shape the design, and neither is negotiable from here:
//
//   - The relay only routes GET to /live/{id}. A POST never reaches this
//     process at all; it falls through to the relay's own landing page. So
//     the login exchange has to be GETs carrying custom headers, which the
//     relay does forward intact.
//
//   - The relay-to-CLI tunnel is plain TCP. Anything sent through it crosses
//     the internet unencrypted, so the password itself must never be in it.
//
// The exchange is therefore a challenge-response. The browser asks for a
// nonce, computes HMAC-SHA256(SHA-256(password), nonce) with WebCrypto, and
// sends only that proof. The password never leaves the visitor's machine, and
// a proof captured off the wire is useless: it is bound to a nonce this
// process signed, that expires, and that is retired the moment it succeeds.
//
// The cost is that the login page needs JavaScript. The alternative — a GET
// form — would put the password in the URL, in browser history, and in every
// access log between here and the visitor, which is worse than requiring a
// feature every browser opening a share link already has.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// How long a visitor stays signed in after entering the password.
	SessionTTL = 12 * time.Hour

	// How long a challenge stays usable. The browser requests one immediately
	// before using it, so this only has to outlast one round trip.
	nonceTTL = 5 * time.Minute

	// Request headers that drive the exchange. A request with none of them is
	// just someone asking for a file.
	headerChallenge = "X-Akpa-Challenge"
	headerNonce     = "X-Akpa-Nonce"
	headerProof     = "X-Akpa-Proof"

	// A wrong password costs the caller this much, multiplied by the number
	// of consecutive failures, capped below. It is not a real rate limiter —
	// it just makes an online guessing loop slow enough to be pointless
	// against any password that isn't in the top thousand.
	failDelay    = 400 * time.Millisecond
	maxFailDelay = 3 * time.Second

	// Retired nonces are pruned once there are more than this many. Only
	// successful logins retire one, so this is generous.
	pruneAt = 1024
)

// Gate wraps an http.Handler and requires a password before it is reached.
//
// The zero value is not usable; build one with New.
type Gate struct {
	hash   [32]byte // sha256 of the password — also the challenge-response key
	secret []byte   // HMAC key for cookies and nonces, random per process
	cookie string   // cookie name, unique per process

	mu    sync.Mutex
	fails int
	used  map[string]int64 // retired nonce -> its expiry, for replay protection
}

// New builds a Gate for the given password.
//
// The signing secret and the cookie name are both random and live only in
// memory. Stop the process and every session it issued becomes unverifiable,
// because the key that signed them is gone.
//
// The random cookie name matters more than it looks: every tunnel is served
// from the same relay hostname, so two akpa processes shared with one browser
// would otherwise fight over a single cookie.
func New(password string) (*Gate, error) {
	if password == "" {
		return nil, fmt.Errorf("password is empty")
	}

	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, fmt.Errorf("generating session key: %w", err)
	}
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		return nil, fmt.Errorf("generating session key: %w", err)
	}

	return &Gate{
		hash:   sha256.Sum256([]byte(password)),
		secret: secret,
		cookie: "akpa_" + hex.EncodeToString(suffix),
		used:   map[string]int64{},
	}, nil
}

// Wrap returns a handler that serves the login page until the visitor proves
// they know the password, and delegates to next once they have.
func (g *Gate) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if g.authorized(r) {
			next.ServeHTTP(w, r)
			return
		}

		switch {
		case r.Header.Get(headerChallenge) != "":
			g.serveChallenge(w)
		case r.Header.Get(headerProof) != "":
			g.serveProof(w, r)
		default:
			g.serveLogin(w)
		}
	})
}

// CookieName is the per-process cookie this gate issues. Exported for
// diagnostics; nothing outside the browser needs it.
func (g *Gate) CookieName() string { return g.cookie }

// ---------------------------------------------------------------------------
// the exchange
// ---------------------------------------------------------------------------

func (g *Gate) serveChallenge(w http.ResponseWriter) {
	gateHeaders(w)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(g.newNonce()))
}

func (g *Gate) serveProof(w http.ResponseWriter, r *http.Request) {
	nonce := r.Header.Get(headerNonce)
	if !g.nonceValid(nonce) {
		// Not a wrong password — a challenge that expired or was already
		// spent. The page just asks for a fresh one, so say which it is.
		g.refuse(w, "stale")
		return
	}

	proof, err := hex.DecodeString(r.Header.Get(headerProof))
	if err != nil || !hmac.Equal(proof, g.expectedProof(nonce)) {
		g.throttle()
		g.refuse(w, "wrong")
		return
	}

	g.retire(nonce)

	g.mu.Lock()
	g.fails = 0
	g.mu.Unlock()

	expiry := strconv.FormatInt(time.Now().Add(SessionTTL).Unix(), 10)
	http.SetCookie(w, &http.Cookie{
		Name:     g.cookie,
		Value:    expiry + "." + hex.EncodeToString(g.sign(expiry)),
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(SessionTTL.Seconds()),
		// Secure is deliberately not set. The public link is HTTPS, but the
		// same handler answers on http://127.0.0.1, and a Secure cookie would
		// make local access impossible to sign in to.
	})

	gateHeaders(w)
	w.WriteHeader(http.StatusNoContent)
}

// expectedProof is what a browser that knows the password will have computed:
// HMAC-SHA256 over the nonce, keyed by the hash of the password.
func (g *Gate) expectedProof(nonce string) []byte {
	mac := hmac.New(sha256.New, g.hash[:])
	mac.Write([]byte(nonce))
	return mac.Sum(nil)
}

func (g *Gate) refuse(w http.ResponseWriter, reason string) {
	gateHeaders(w)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(reason))
}

func (g *Gate) throttle() {
	g.mu.Lock()
	g.fails++
	delay := time.Duration(g.fails) * failDelay
	g.mu.Unlock()

	if delay > maxFailDelay {
		delay = maxFailDelay
	}
	time.Sleep(delay)
}

// ---------------------------------------------------------------------------
// nonces
// ---------------------------------------------------------------------------

// newNonce mints a signed, self-describing challenge. Signing it means this
// process can recognise its own nonces later without having stored them —
// only the ones that have been spent need remembering.
func (g *Gate) newNonce() string {
	raw := make([]byte, 16)
	_, _ = rand.Read(raw)

	body := strconv.FormatInt(time.Now().Add(nonceTTL).Unix(), 10) + "." + hex.EncodeToString(raw)
	return body + "." + hex.EncodeToString(g.sign(body))
}

func (g *Gate) nonceValid(nonce string) bool {
	i := strings.LastIndex(nonce, ".")
	if i < 0 {
		return false
	}
	body, sig := nonce[:i], nonce[i+1:]

	want, err := hex.DecodeString(sig)
	if err != nil || !hmac.Equal(want, g.sign(body)) {
		return false
	}

	expiry, _, ok := strings.Cut(body, ".")
	if !ok {
		return false
	}
	unix, err := strconv.ParseInt(expiry, 10, 64)
	if err != nil || time.Now().Unix() >= unix {
		return false
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	_, spent := g.used[nonce]
	return !spent
}

// retire marks a nonce as spent so a proof captured off the plaintext tunnel
// cannot be replayed. Only successful logins land here, so the map stays tiny.
func (g *Gate) retire(nonce string) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if len(g.used) > pruneAt {
		now := time.Now().Unix()
		for k, exp := range g.used {
			if now >= exp {
				delete(g.used, k)
			}
		}
	}
	g.used[nonce] = time.Now().Add(nonceTTL).Unix()
}

// ---------------------------------------------------------------------------
// sessions
// ---------------------------------------------------------------------------

// authorized reports whether the request carries a cookie this process signed
// and that has not expired.
func (g *Gate) authorized(r *http.Request) bool {
	c, err := r.Cookie(g.cookie)
	if err != nil {
		return false
	}

	expiry, sig, ok := strings.Cut(c.Value, ".")
	if !ok {
		return false
	}

	unix, err := strconv.ParseInt(expiry, 10, 64)
	if err != nil || time.Now().Unix() >= unix {
		return false
	}

	want, err := hex.DecodeString(sig)
	if err != nil {
		return false
	}
	// The expiry is inside the signed message, so a visitor cannot extend
	// their own session by editing the cookie.
	return hmac.Equal(want, g.sign(expiry))
}

func (g *Gate) sign(msg string) []byte {
	mac := hmac.New(sha256.New, g.secret)
	mac.Write([]byte(msg))
	return mac.Sum(nil)
}

// ---------------------------------------------------------------------------
// pages
// ---------------------------------------------------------------------------

func (g *Gate) serveLogin(w http.ResponseWriter) {
	gateHeaders(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(loginPage))
}

func gateHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	// Same URL, different answer depending on these. Nothing in the path
	// should be caching an unauthenticated 401 over a real response.
	h.Set("Vary", headerChallenge+", "+headerNonce+", "+headerProof+", Cookie")
	// The login page has no user content in it, but a share can, and a shared
	// folder is exactly the kind of place a stray HTML file turns up.
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
}

const loginPage = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex, nofollow">
<title>akpa · protected</title>
<style>
  :root {
    color-scheme: light dark;
    --bg: #fbfaf8; --card: #fff; --ink: #17150f; --muted: #6d675c;
    --line: #e5e0d6; --accent: #c76d06; --bad: #b3261e;
  }
  @media (prefers-color-scheme: dark) {
    :root {
      --bg: #131210; --card: #1c1a17; --ink: #f2efe9; --muted: #9b948a;
      --line: #302c27; --accent: #e89b3c; --bad: #f2857c;
    }
  }
  * { box-sizing: border-box; }
  body {
    margin: 0; min-height: 100vh; display: grid; place-items: center;
    padding: 24px; background: var(--bg); color: var(--ink);
    font: 15px/1.5 ui-sans-serif, -apple-system, "Segoe UI", Roboto, sans-serif;
  }
  .card {
    width: 100%; max-width: 340px; background: var(--card);
    border: 1px solid var(--line); border-radius: 14px; padding: 28px 26px 26px;
  }
  pre.art {
    margin: 0 0 20px; color: var(--accent);
    font: 700 11px/1.45 ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
    overflow-x: auto;
  }
  h1 { margin: 0 0 6px; font-size: 17px; letter-spacing: -0.01em; }
  p.sub { margin: 0 0 20px; color: var(--muted); font-size: 13.5px; }
  label { display: block; margin-bottom: 7px; font-size: 12px;
          text-transform: uppercase; letter-spacing: .08em; color: var(--muted); }
  input {
    width: 100%; padding: 11px 13px; font: inherit; color: var(--ink);
    background: var(--bg); border: 1px solid var(--line); border-radius: 9px;
  }
  input:focus { outline: 2px solid var(--accent); outline-offset: 1px; border-color: transparent; }
  button {
    width: 100%; margin-top: 14px; padding: 11px 13px; font: inherit; font-weight: 600;
    color: #fff; background: var(--accent); border: 0; border-radius: 9px; cursor: pointer;
  }
  button:hover:not(:disabled) { filter: brightness(1.07); }
  button:disabled { opacity: .65; cursor: default; }
  .err { margin: 14px 0 0; color: var(--bad); font-size: 13.5px; }
  .foot { margin: 20px 0 0; color: var(--muted); font-size: 12px; }
</style>
</head>
<body>
  <main class="card">
    <pre class="art">   ___   __ _____  ___
  / _ | / //_/ _ \/ _ |
 / __ |/ ,&lt; / ___/ __ |
/_/ |_/_/|_/_/  /_/ |_|</pre>
    <h1>This share is protected</h1>
    <p class="sub">Enter the password you were given to see these files.</p>
    <form id="f" autocomplete="off">
      <label for="pw">Password</label>
      <input id="pw" type="password" autofocus required
             autocomplete="current-password" spellcheck="false">
      <button id="go" type="submit">Unlock</button>
      <p class="err" id="err" hidden></p>
    </form>
    <noscript><p class="err">JavaScript is required to sign in, so that the
      password can be checked without ever being sent over the network.</p></noscript>
    <p class="foot">Checked on the sharer's machine. The password itself never
      leaves this browser.</p>
  </main>
<script>
(function () {
  var f = document.getElementById('f'), pw = document.getElementById('pw'),
      go = document.getElementById('go'), err = document.getElementById('err');

  function fail(msg) {
    err.textContent = msg; err.hidden = false;
    go.disabled = false; go.textContent = 'Unlock';
    pw.select();
  }

  function hex(buf) {
    return Array.prototype.map.call(new Uint8Array(buf), function (b) {
      return b.toString(16).padStart(2, '0');
    }).join('');
  }

  // The exchange rides entirely on headers: the relay forwards GET and
  // nothing else, and a proof in the URL would be logged everywhere it went.
  function ask(headers) {
    return fetch(location.href, { headers: headers, cache: 'no-store', credentials: 'same-origin' });
  }

  f.addEventListener('submit', function (e) {
    e.preventDefault();

    if (!window.crypto || !crypto.subtle) {
      fail('This browser cannot sign in securely here (WebCrypto unavailable).');
      return;
    }

    err.hidden = true;
    go.disabled = true;
    go.textContent = 'Checking…';

    var enc = new TextEncoder(), secret = pw.value;

    ask({ 'X-Akpa-Challenge': '1' })
      .then(function (r) {
        if (!r.ok) throw new Error('challenge');
        return r.text();
      })
      .then(function (nonce) {
        nonce = nonce.trim();
        // Prove knowledge of the password without transmitting it: the nonce
        // is signed with the password's own hash as the key.
        return crypto.subtle.digest('SHA-256', enc.encode(secret))
          .then(function (digest) {
            return crypto.subtle.importKey('raw', digest,
              { name: 'HMAC', hash: 'SHA-256' }, false, ['sign']);
          })
          .then(function (key) { return crypto.subtle.sign('HMAC', key, enc.encode(nonce)); })
          .then(function (sig) {
            return ask({ 'X-Akpa-Nonce': nonce, 'X-Akpa-Proof': hex(sig) });
          });
      })
      .then(function (r) {
        if (r.status === 204) { location.reload(); return; }
        return r.text().then(function (why) {
          fail(why === 'stale'
            ? 'That took too long. Try once more.'
            : 'That password is not right.');
        });
      })
      .catch(function () { fail('Could not reach the share. Try again.'); });
  });
})();
</script>
</body>
</html>
`
