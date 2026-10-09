package cert

import (
	"context"
	"crypto"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-acme/lego/v5/acme"
	"github.com/go-acme/lego/v5/certcrypto"
	"github.com/go-acme/lego/v5/certificate"
	"github.com/go-acme/lego/v5/challenge"
	"github.com/go-acme/lego/v5/challenge/http01"
	"github.com/go-acme/lego/v5/lego"
	legolog "github.com/go-acme/lego/v5/log"
	"github.com/go-acme/lego/v5/registration"
)

// StagingEnv switches issuance to the Let's Encrypt staging endpoint: the
// certificates are untrusted but the rate limits are not, which is what makes it
// the right endpoint for trying a domain out.
const StagingEnv = "EASYSB_ACME_STAGING"

// UserAgent identifies the panel to the CA. Let's Encrypt asks clients to send
// something it can contact the author of; lego appends its own version to it.
const UserAgent = "EasySB"

// RenewBefore is how long before expiry a certificate is renewed. Let's Encrypt
// certificates are valid for 90 days and the CA asks for renewal at 30 days left;
// renewing earlier only spends rate limit, so the nightly timer stops here.
const RenewBefore = 30 * 24 * time.Hour

// Test seams. They are package variables rather than constants so a unit test can
// run the whole issuance against a throwaway ACME server and a listener on a spare
// port; production always takes the values below.
var (
	// directoryFor resolves the CA directory the client orders from.
	directoryFor = letsEncryptDirectory

	// challengeProvider builds the listener that answers the HTTP-01 challenge.
	challengeProvider = standaloneChallenge

	// httpClient is the client lego talks to the CA with, nil for lego's own
	// (proxy-aware, two minute timeout). A test sets it to trust its own server.
	httpClient = func() *http.Client { return nil }
)

// letsEncryptDirectory pins the CA rather than taking a default from a tool the
// panel no longer ships: acme.sh's default moved from Let's Encrypt to ZeroSSL, so
// "whatever the installed client prefers" would silently change who signs the
// panel's certificates and would no longer match the staging switch.
func letsEncryptDirectory(staging bool) string {
	if staging {
		return lego.DirectoryURLLetsEncryptStaging
	}
	return lego.DirectoryURLLetsEncrypt
}

// standaloneChallenge is the challenge listener the panel ships: the HTTP-01
// standalone method acme.sh used to do with socat, now the standard library. Port
// 80 is the only address a CA looks at.
func standaloneChallenge() challenge.Provider {
	return http01.NewProviderServer("", "80")
}

// silenceOnce guards the redirection of lego's logger, which is global state.
var silenceOnce sync.Once

// silenceLego sends lego's own progress log to nowhere.
//
// The panel owns the terminal (a full-screen bubbletea dashboard) and its own log
// channel, so lego's lines would otherwise be painted through the middle of the
// interface. Nothing is lost: every error lego returns is wrapped below and
// handed to the caller's log function, which is where the operator reads it.
func silenceLego() {
	silenceOnce.Do(func() {
		legolog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	})
}

// Staging reports whether issuance should use the Let's Encrypt staging endpoint.
func Staging() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(StagingEnv))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// account is the ACME account the panel registers once and then reuses: the key
// that signs its requests, the address it was registered with, and the account
// URL the CA handed back, which is how the CA identifies it from then on. It
// satisfies registration.User, which is the handle lego's client takes.
type account struct {
	email string
	key   crypto.Signer
	reg   *acme.ExtendedAccount
}

func (a *account) GetEmail() string { return a.email }

func (a *account) GetRegistration() *acme.ExtendedAccount { return a.reg }

func (a *account) GetPrivateKey() crypto.Signer { return a.key }

// storedRegistration is the part of the account that has to survive a restart.
// The key identifies the account to the CA and is stored beside it; the URL is
// what the panel would otherwise have to ask for again on every run.
type storedRegistration struct {
	Location string `json:"location"`
}

// errNotRegistered separates "no account yet" from a broken state file: the first
// is the normal state of a fresh panel and is fixed by registering, the second
// has to be reported rather than overwritten.
var errNotRegistered = errors.New("no ACME account has been registered yet")

func accountKeyPath() string { return filepath.Join(Dir(), accountKeyName) }

func registrationPath() string { return filepath.Join(Dir(), accountJSONName) }

// readAccount returns the account the panel already has. It writes nothing, which
// is what lets Registered() answer without a side effect.
func readAccount(email string) (*account, error) {
	keyPEM, err := os.ReadFile(accountKeyPath())
	if err != nil {
		return nil, err
	}
	key, err := certcrypto.ParsePEMPrivateKey(keyPEM)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", accountKeyPath(), err)
	}
	acct := &account{email: strings.TrimSpace(email), key: key}

	data, err := os.ReadFile(registrationPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errNotRegistered
		}
		return nil, err
	}
	var reg storedRegistration
	if err := json.Unmarshal(data, &reg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", registrationPath(), err)
	}
	if strings.TrimSpace(reg.Location) == "" {
		return nil, errNotRegistered
	}
	acct.reg = &acme.ExtendedAccount{Location: reg.Location}
	return acct, nil
}

// accountKey returns the account key, generating and storing one when the panel
// has never registered an account. The key outlives a lost registration: it is
// what an account is, and re-registering with the same key is how a panel that
// lost its state file keeps the account it already had.
func accountKey() (crypto.Signer, error) {
	if _, err := os.Stat(accountKeyPath()); err == nil {
		keyPEM, err := os.ReadFile(accountKeyPath())
		if err != nil {
			return nil, err
		}
		return certcrypto.ParsePEMPrivateKey(keyPEM)
	}

	key, err := certcrypto.GeneratePrivateKey(certcrypto.EC256)
	if err != nil {
		return nil, err
	}
	if err := writeFile(accountKeyPath(), certcrypto.PEMEncode(key), 0o600); err != nil {
		return nil, err
	}
	return key, nil
}

// writeRegistration stores the account URL, 0600: it is not a secret, but the
// directory it lives in holds the account key.
func writeRegistration(location string) error {
	data, err := json.Marshal(storedRegistration{Location: location})
	if err != nil {
		return err
	}
	return writeFile(registrationPath(), data, 0o600)
}

// Registered reports whether the panel has an ACME account it can order with. It
// reads the state directory and asks the CA nothing, because the renewal timer
// asks this question on a host that may have no route to Let's Encrypt at all.
func Registered() bool {
	_, err := readAccount("")
	return err == nil
}

// EnsureAccount makes sure the panel has an ACME account to order certificates
// with. It is idempotent and costs nothing once the account exists: registering
// an account is a once-per-host event, and re-registering on every issuance would
// spend a request against the CA's rate limit for no reason.
//
// email is the address the CA uses to warn about an expiring certificate. It is
// required only when there is no account yet.
func EnsureAccount(ctx context.Context, email string, log func(string)) error {
	_, err := ensureAccount(ctx, email, log)
	return err
}

// ensureAccount is EnsureAccount plus the account it produced, which is what the
// issuance paths need next.
func ensureAccount(ctx context.Context, email string, logf func(string)) (*account, error) {
	key, err := accountKey()
	if err != nil {
		return nil, err
	}
	acct := &account{email: strings.TrimSpace(email), key: key}

	if existing, err := readAccount(email); err == nil {
		return existing, nil
	} else if !errors.Is(err, errNotRegistered) {
		return nil, err
	}

	if acct.email == "" {
		return nil, errors.New("an email address is required to register an ACME account")
	}
	client, err := newClient(acct, Staging())
	if err != nil {
		return nil, err
	}
	logf("acme: registering the account " + acct.email + " at " + directoryFor(Staging()))
	// The terms were accepted by asking the panel to issue a certificate; the same
	// implicit consent acme.sh took. Let's Encrypt's subscriber agreement is the
	// only text this covers.
	acc, err := client.Registration.Register(ctx, registration.RegisterOptions{TermsOfServiceAgreed: true})
	if err != nil {
		return nil, fmt.Errorf("register the ACME account: %w", err)
	}
	if strings.TrimSpace(acc.Location) == "" {
		return nil, errors.New("the CA did not return an account URL")
	}
	if err := writeRegistration(acc.Location); err != nil {
		return nil, err
	}
	acct.reg = &acme.ExtendedAccount{Location: acc.Location}
	return acct, nil
}

// newClient builds a lego client for one account against one CA.
func newClient(acct *account, staging bool) (*lego.Client, error) {
	silenceLego()
	config := lego.NewConfig(acct)
	config.CADirURL = directoryFor(staging)
	config.UserAgent = UserAgent
	if hc := httpClient(); hc != nil {
		config.HTTPClient = hc
	}
	client, err := lego.NewClient(config)
	if err != nil {
		return nil, fmt.Errorf("acme %s: %w", config.CADirURL, err)
	}
	if err := client.Challenge.SetHTTP01Provider(challengeProvider()); err != nil {
		return nil, fmt.Errorf("acme http-01: %w", err)
	}
	return client, nil
}

// Issue obtains a certificate for domain over the HTTP-01 challenge and installs
// it as the pair Paths() resolves.
//
// A certificate that is still well inside its lifetime is left alone and reported
// as a success: it is the pair the caller asked for, acme.sh behaved the same way
// ("Domains not changed", exit non-zero, which is not a failure), and reissuing on
// a second click would spend one of the five duplicate certificates Let's Encrypt
// allows a week. Remove the certificate first to replace it early.
func Issue(ctx context.Context, domain, email string, log func(string)) error {
	domain, err := cleanDomain(domain)
	if err != nil {
		return err
	}
	// The cheap answer first: this is the only path that touches neither the CA nor
	// the account state. A pair that cannot be read at all (err != nil) is not a
	// reason to stop: issuing is exactly what repairs it, and the order below would
	// not be attempted for a certificate that is merely still valid.
	if ok, expiry, err := dueForRenewal(domain, time.Now()); err == nil && !ok {
		log("acme: " + domain + " is still valid until " + expiry.UTC().Format(time.RFC3339) + ", not reissuing")
		return nil
	}

	acct, err := ensureAccount(ctx, email, log)
	if err != nil {
		return err
	}
	client, err := newClient(acct, Staging())
	if err != nil {
		return err
	}
	log("acme: " + directoryFor(Staging()) + " (http-01, " + domain + ")")

	res, err := client.Certificate.Obtain(ctx, certificate.ObtainRequest{
		Domains: []string{domain},
		// The bundle is the fullchain sing-box and the subscription service load.
		Bundle: true,
		// acme.sh was asked for ec-256; the certificates the panel already handed
		// out use it, so a reissue keeps the same shape.
		KeyType: certcrypto.EC256,
	})
	if err != nil {
		return fmt.Errorf("issue %s: %w", domain, err)
	}
	if err := installPair(domain, res); err != nil {
		return err
	}
	if expiry, err := expiryOf(domain); err == nil {
		log("acme: issued " + domain + ", valid until " + expiry.UTC().Format(time.RFC3339))
	}
	return nil
}

// Renew renews every certificate that is close enough to expiry and reports which
// domains were renewed. It is what the systemd timer calls, and its answer is what
// decides whether the services have to be reloaded: the timer runs every night,
// and a renewal that changed nothing must not restart a running core.
//
// Certificates that are not due are skipped without a request to the CA, and a
// domain whose renewal fails is reported without stopping the others: one broken
// record must not cost the other domains their renewal.
func Renew(ctx context.Context, log func(string)) ([]string, error) {
	domains := Domains()
	if len(domains) == 0 {
		return nil, nil
	}
	acct, err := readAccount("")
	if err != nil {
		if errors.Is(err, errNotRegistered) {
			return nil, errors.New("no ACME account has been registered yet; issue a certificate first")
		}
		return nil, err
	}

	var renewed []string
	var failures []error
	for _, domain := range domains {
		due, expiry, err := dueForRenewal(domain, time.Now())
		if err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", domain, err))
			continue
		}
		if !due {
			log("acme: " + domain + " is valid until " + expiry.UTC().Format(time.RFC3339) + ", not due")
			continue
		}
		if err := renewOne(ctx, acct, domain); err != nil {
			failures = append(failures, err)
			continue
		}
		renewed = append(renewed, domain)
		log("acme: renewed " + domain)
	}
	return renewed, errors.Join(failures...)
}

// renewOne replaces the certificate of one domain, reusing its private key so the
// certificate that is served keeps the same key pair.
func renewOne(ctx context.Context, acct *account, domain string) error {
	res, err := loadResource(domain)
	if err != nil {
		return err
	}
	client, err := newClient(acct, Staging())
	if err != nil {
		return err
	}
	next, err := client.Certificate.Renew(ctx, res, &certificate.RenewOptions{
		Bundle:  true,
		KeyType: certcrypto.EC256,
	})
	if err != nil {
		return fmt.Errorf("renew %s: %w", domain, err)
	}
	return installPair(domain, next)
}

// Remove deletes the certificate pair of a domain. The account and every other
// domain are untouched; the certificate is not revoked at the CA, which is what
// acme.sh's --remove did too: a certificate that is no longer served is harmless,
// and Let's Encrypt revokes on request rather than on removal.
//
// ctx is in the signature for symmetry with the calls that do reach the CA;
// nothing here is remote, so it is not used.
func Remove(_ context.Context, domain string) error {
	domain, err := cleanDomain(domain)
	if err != nil {
		return err
	}
	dir, _ := domainDir(domain)
	return os.RemoveAll(dir)
}

// loadResource rebuilds the lego view of an installed certificate from the files
// on disk: the pair, plus the domains the certificate itself carries. Nothing
// else is stored, so a pair that was copied onto the host by hand can still be
// renewed by the panel.
func loadResource(domain string) (certificate.Resource, error) {
	fullchain, key, ok := Paths(domain)
	if !ok {
		return certificate.Resource{}, fmt.Errorf("no certificate for %s", domain)
	}
	certPEM, err := os.ReadFile(fullchain)
	if err != nil {
		return certificate.Resource{}, err
	}
	keyPEM, err := os.ReadFile(key)
	if err != nil {
		return certificate.Resource{}, err
	}
	certs, err := certcrypto.ParsePEMBundle(certPEM)
	if err != nil {
		return certificate.Resource{}, fmt.Errorf("parse %s: %w", fullchain, err)
	}
	return certificate.Resource{
		ID:          domain,
		Domains:     certcrypto.ExtractDomains(certs[0]),
		Certificate: certPEM,
		PrivateKey:  keyPEM,
		KeyType:     certcrypto.EC256,
	}, nil
}

// installPair writes an issued certificate pair, the key first and the
// certificate second, under the mode each deserves.
//
// Each write is atomic on its own, so neither file is ever truncated. The two
// files cannot be replaced as one, however, so an interruption between them still
// leaves a half pair, and what that half is depends on whether the domain already
// had a certificate:
//
//   - first issuance: the old fullchain.cer does not exist, so the interruption
//     leaves a key with no certificate. pairIn requires both files to exist, so
//     Paths reports no pair, ResolveActive falls back to the placeholder, and the
//     issuance is simply retried. This is the state docs/pitfalls.md describes.
//   - renewal: the previous fullchain.cer is still there, so the interruption
//     leaves the NEW private key beside the OLD certificate. pairIn tests
//     existence and non-empty size only, so that does report as a pair - a
//     mismatched one, which is the combination sing-box refuses to start with.
//
// The renewal case is a residual risk of a two-file replacement, not an oversight:
// two paths cannot be replaced as one, so some interruption between them is
// unavoidable. What this function does control is everything around that window:
//
//   - the pair the CA returned is verified to match BEFORE either file is written,
//     so a response whose key does not belong to its certificate is refused while
//     the working pair is still intact, rather than installed and then discovered
//     by the core;
//   - each file is written atomically, so neither is ever truncated;
//   - the key is written first, so the first issuance - the common path, and the one
//     that runs on a fresh host with nothing to fall back on - leaves a state
//     pairIn reports as no pair, and the issuance is simply retried.
//
// Closing the renewal window itself means making pairIn reject a mismatched pair,
// which changes what ResolveActive and dueForRenewal do with one; both of those were
// tried and reverted because each broke correct behaviour, so it stays tracked as F7
// in docs/optimization-audit.md rather than being forced here.
func installPair(domain string, res *certificate.Resource) error {
	if res == nil {
		return errors.New("the CA returned no certificate")
	}
	dir, ok := domainDir(domain)
	if !ok {
		return fmt.Errorf("invalid domain %q", domain)
	}
	if len(res.PrivateKey) == 0 || len(res.Certificate) == 0 {
		return fmt.Errorf("the CA returned an incomplete certificate for %s", domain)
	}
	// The pair is checked before anything is written. Without this, a CA response
	// whose key does not match its certificate would be installed over a working
	// pair, and the mismatch would surface as a node that cannot start rather than as
	// a failed renewal with the previous certificate still serving.
	if _, err := tls.X509KeyPair(res.Certificate, res.PrivateKey); err != nil {
		return fmt.Errorf("the CA returned a certificate and key that do not match for %s: %w", domain, err)
	}
	if err := writeFile(filepath.Join(dir, keyName), res.PrivateKey, 0o600); err != nil {
		return err
	}
	return writeFile(filepath.Join(dir, fullchainName), res.Certificate, 0o644)
}

// dueForRenewal reports whether the certificate installed for a domain has less
// than RenewBefore left, along with its expiry. A domain without a certificate at
// all is due, which is what makes the same call usable for a first issuance.
//
// A self-signed placeholder counts as due. Paths resolves the placeholder so the
// core always has something to serve, but it is not a certificate a client
// accepts: treating its ten-year NotAfter as a real lifetime is what used to make
// the first issuance a silent no-op, because Issue reads this answer and stops.
func dueForRenewal(domain string, now time.Time) (bool, time.Time, error) {
	fullchain, _, ok := Paths(domain)
	if !ok {
		return true, time.Time{}, nil
	}
	expiry, err := expiryOf(domain)
	if err != nil {
		return false, time.Time{}, fmt.Errorf("read %s: %w", fullchain, err)
	}
	if selfSigned(fullchain) {
		return true, time.Time{}, nil
	}
	return expiry.Before(now.Add(RenewBefore)), expiry, nil
}

// expiryOf reads the expiry of the leaf certificate installed for a domain. The
// bundle starts with the leaf (the CA follows it), which is the order a fullchain
// is written in.
func expiryOf(domain string) (time.Time, error) {
	fullchain, _, ok := Paths(domain)
	if !ok {
		return time.Time{}, fmt.Errorf("no certificate for %s", domain)
	}
	data, err := os.ReadFile(fullchain)
	if err != nil {
		return time.Time{}, err
	}
	certs, err := certcrypto.ParsePEMBundle(data)
	if err != nil {
		return time.Time{}, err
	}
	if len(certs) == 0 {
		return time.Time{}, errors.New("no certificate in the bundle")
	}
	return certs[0].NotAfter, nil
}
