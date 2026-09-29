package cert

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/go-acme/lego/v5/challenge/http01"
	"github.com/go-acme/lego/v5/lego"
)

// tempDir points the package at a throwaway state directory. Every path the
// package resolves starts at Dir(), so this is what keeps the tests off
// /etc/sing-box, where the panel writes when it runs as root.
func tempDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "acme")
	t.Setenv(DirEnv, dir)
	return dir
}

// writePair plants a certificate pair for a domain, expiring at notAfter. The
// issued pair is written by the package under test, so the tests build their own
// to control the expiry: renewal is a decision about a date.
//
// The leaf is signed by a throwaway CA rather than by itself: a self-signed pair is
// the placeholder, and `Usable` rejects one, so a fixture for "the panel issued this"
// has to look like something a CA issued.
func writePair(t *testing.T, dir, domain string, notAfter time.Time) (fullchain, key string) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caSerial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 96))
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          caSerial,
		Subject:               pkix.Name{CommonName: "EasySB test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              notAfter.AddDate(1, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}

	keyPair, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 96))
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: domain},
		DNSNames:              []string{domain},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca, &keyPair.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(keyPair)
	if err != nil {
		t.Fatal(err)
	}
	fullchain = filepath.Join(dir, domain, fullchainName)
	key = filepath.Join(dir, domain, keyName)
	if err := os.MkdirAll(filepath.Dir(fullchain), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(key, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fullchain, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		t.Fatal(err)
	}
	return fullchain, key
}

// A host that was upgraded has its certificate where the previous version put it, and
// the panel has to keep serving it: clients must not notice the upgrade. The panel's
// own directory wins once it has issued one there.
func TestPathsReadsWhatAnEarlierVersionLeft(t *testing.T) {
	t.Run("acme.sh home of a 4.x host", func(t *testing.T) {
		tempDir(t)
		home := t.TempDir()
		t.Setenv("HOME", home)

		legacy := filepath.Join(home, ".acme.sh", "example.com_ecc")
		if err := os.MkdirAll(legacy, 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		writePair(t, filepath.Dir(legacy), filepath.Base(legacy), time.Now().AddDate(0, 3, 0))
		// acme.sh names the key after the domain, inside the *_ecc directory.
		if err := os.Rename(filepath.Join(legacy, keyName), filepath.Join(legacy, "example.com.key")); err != nil {
			t.Fatalf("rename key: %v", err)
		}

		fullchain, key, ok := Paths("example.com")
		if !ok {
			t.Fatal("a certificate the previous version issued should still resolve")
		}
		if !strings.HasPrefix(fullchain, legacy) || !strings.HasSuffix(key, "example.com.key") {
			t.Fatalf("resolved %s / %s, want the acme.sh pair", fullchain, key)
		}
	})

	t.Run("the panel's own pair wins", func(t *testing.T) {
		dir := tempDir(t)
		home := t.TempDir()
		t.Setenv("HOME", home)
		legacy := filepath.Join(home, ".acme.sh", "example.com_ecc")
		if err := os.MkdirAll(legacy, 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		writePair(t, filepath.Dir(legacy), filepath.Base(legacy), time.Now().AddDate(0, 3, 0))
		want, _ := writePair(t, dir, "example.com", time.Now().AddDate(0, 3, 0))

		got, _, ok := Paths("example.com")
		if !ok || got != want {
			t.Fatalf("Paths = %q (ok=%v), want the panel's own pair %q", got, ok, want)
		}
	})
}

// The self-signed placeholder keeps the core's configuration renderable, but it is not
// a certificate a client accepts: a URL must not promise https because of one.
func TestUsableRejectsTheSelfSignedPlaceholder(t *testing.T) {
	dir := tempDir(t)
	if err := os.MkdirAll(filepath.Join(dir, "example.com"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := GenerateSelfSigned(filepath.Join(dir, "example.com", fullchainName), filepath.Join(dir, "example.com", keyName), "example.com"); err != nil {
		t.Fatalf("GenerateSelfSigned: %v", err)
	}
	if _, _, ok := Paths("example.com"); !ok {
		t.Fatal("the placeholder pair should resolve: the core needs something to serve")
	}
	if Usable("example.com") {
		t.Fatal("a self-signed placeholder must not count as a usable certificate")
	}

	// The same path with a CA-issued pair is usable, so the check is about the
	// certificate and not about the file being there.
	writePair(t, dir, "example.com", time.Now().AddDate(0, 3, 0))
	if !Usable("example.com") {
		t.Fatal("an issued pair should count as usable")
	}
}

func TestPathsAndDomains(t *testing.T) {
	dir := tempDir(t)
	writePair(t, dir, "example.com", time.Now().AddDate(0, 0, 60))

	fullchain, key, ok := Paths("example.com")
	if !ok {
		t.Fatal("expected a certificate pair for example.com")
	}
	if filepath.Base(fullchain) != fullchainName || filepath.Base(key) != keyName {
		t.Fatalf("unexpected paths: %s %s", fullchain, key)
	}
	if !Usable("example.com") {
		t.Fatal("an installed pair is usable")
	}
	if _, _, ok := Paths("missing.com"); ok {
		t.Fatal("unexpected certificate for missing.com")
	}
	if Usable("missing.com") {
		t.Fatal("a domain without a pair is not usable")
	}

	domains := Domains()
	if len(domains) != 1 || domains[0] != "example.com" {
		t.Fatalf("domains = %v, want [example.com]", domains)
	}
}

// TestPathsStaysInsideTheStateDirectory guards the one path that is built from a
// value the operator (or an entry in the state directory) supplies: a domain that
// carries a separator or a parent reference must not resolve to a file outside
// Dir(), whichever end of the state directory it comes from.
func TestPathsStaysInsideTheStateDirectory(t *testing.T) {
	dir := tempDir(t)
	for _, domain := range []string{"", " ", ".", "..", "../../etc/passwd", `..\..\windows`, "a/b"} {
		if _, _, ok := Paths(domain); ok {
			t.Errorf("Paths(%q) resolved a pair", domain)
		}
		if _, ok := domainDir(domain); ok {
			t.Errorf("domainDir(%q) accepted the name", domain)
		}
	}
	// A name that merely contains dots is still a domain.
	if _, ok := domainDir("dev.example.com"); !ok {
		t.Error("a normal domain was refused")
	}
	if _, _, ok := Paths("dev.example.com"); ok {
		t.Error("no pair was written for dev.example.com")
	}
	_ = dir
}

// TestDomainsIgnoresWhatIsNotAPair covers the two ways a directory ends up in the
// state directory without being a certificate: the account files beside it, and
// the leftovers of an interrupted write.
func TestDomainsIgnoresWhatIsNotAPair(t *testing.T) {
	dir := tempDir(t)
	writePair(t, dir, "example.com", time.Now().AddDate(0, 0, 60))
	if err := os.WriteFile(filepath.Join(dir, accountKeyName), []byte("key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "half.com"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "half.com", fullchainName), []byte("only the cert"), 0o644); err != nil {
		t.Fatal(err)
	}

	domains := Domains()
	if len(domains) != 1 || domains[0] != "example.com" {
		t.Fatalf("domains = %v, want only example.com", domains)
	}
}

func TestGenerateSelfSigned(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "cert", fullchainName)
	keyPath := filepath.Join(dir, "cert", keyName)

	if err := GenerateSelfSigned(certPath, keyPath, "easysb.local"); err != nil {
		t.Fatalf("GenerateSelfSigned: %v", err)
	}

	// The placeholder is handed to sing-box and to a TLS listener, so the pair has
	// to load and the name has to be in a SAN: a client ignores the CN.
	pair, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		t.Fatalf("the generated pair does not load: %v", err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if leaf.Subject.CommonName != "easysb.local" {
		t.Errorf("common name = %q, want easysb.local", leaf.Subject.CommonName)
	}
	if len(leaf.DNSNames) != 1 || leaf.DNSNames[0] != "easysb.local" {
		t.Errorf("DNS names = %v, want [easysb.local]", leaf.DNSNames)
	}
	if leaf.IsCA {
		t.Error("the placeholder is a leaf, not a CA")
	}
	if leaf.NotAfter.Before(time.Now().AddDate(9, 0, 0)) {
		t.Errorf("the placeholder expires %s, too soon", leaf.NotAfter)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(keyPath)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("key mode = %v, want 0600", info.Mode().Perm())
		}
	}
}

func TestGenerateSelfSignedForAnAddress(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, fullchainName)
	keyPath := filepath.Join(dir, keyName)
	if err := GenerateSelfSigned(certPath, keyPath, "127.0.0.1"); err != nil {
		t.Fatalf("GenerateSelfSigned: %v", err)
	}
	pair, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(leaf.IPAddresses) != 1 || leaf.IPAddresses[0].String() != "127.0.0.1" {
		t.Errorf("IP SANs = %v, want [127.0.0.1]", leaf.IPAddresses)
	}
	if len(leaf.DNSNames) != 0 {
		t.Errorf("an address is not a DNS name, got %v", leaf.DNSNames)
	}
}

func TestResolveActiveUsesTheIssuedPair(t *testing.T) {
	dir := tempDir(t)
	fullchain, key := writePair(t, dir, "example.com", time.Now().AddDate(0, 0, 60))

	resolved, err := ResolveActive("example.com")
	if err != nil {
		t.Fatalf("ResolveActive: %v", err)
	}
	if resolved.Fullchain != fullchain || resolved.Key != key {
		t.Fatalf("ResolveActive = %+v, want the issued pair", resolved)
	}
	if resolved.SelfSigned {
		t.Error("an issued pair is not self-signed")
	}
}

func TestRemoveTakesOneDomainOnly(t *testing.T) {
	dir := tempDir(t)
	writePair(t, dir, "a.example.com", time.Now().AddDate(0, 0, 60))
	writePair(t, dir, "b.example.com", time.Now().AddDate(0, 0, 60))

	if err := Remove(context.Background(), "a.example.com"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, _, ok := Paths("a.example.com"); ok {
		t.Error("the removed pair is still there")
	}
	if _, _, ok := Paths("b.example.com"); !ok {
		t.Error("the other domain lost its pair")
	}
	// Removing what is not there is not an error: the menu can be used twice.
	if err := Remove(context.Background(), "a.example.com"); err != nil {
		t.Errorf("Remove of a missing domain: %v", err)
	}
	if err := Remove(context.Background(), "../etc"); err == nil {
		t.Error("Remove of a domain that escapes the state directory should fail")
	}
}

func TestDueForRenewal(t *testing.T) {
	dir := tempDir(t)
	now := time.Now()

	// Nothing installed yet: the first issuance takes this path.
	if due, _, err := dueForRenewal("example.com", now); err != nil || !due {
		t.Fatalf("dueForRenewal without a pair = %v, %v, want due", due, err)
	}

	writePair(t, dir, "fresh.example.com", now.AddDate(0, 0, 80))
	if due, expiry, err := dueForRenewal("fresh.example.com", now); err != nil || due || expiry.IsZero() {
		t.Fatalf("a certificate with 80 days left = %v, %v, %v, want not due", due, expiry, err)
	}

	writePair(t, dir, "old.example.com", now.AddDate(0, 0, 10))
	if due, _, err := dueForRenewal("old.example.com", now); err != nil || !due {
		t.Fatalf("a certificate with 10 days left = %v, %v, want due", due, err)
	}

	// The window is RenewBefore wide, which is the promise the daily timer relies
	// on: a certificate with more than that left is left alone.
	writePair(t, dir, "edge.example.com", now.Add(RenewBefore+time.Hour))
	if due, _, err := dueForRenewal("edge.example.com", now); err != nil || due {
		t.Fatalf("a certificate just outside the window = %v, %v, want not due", due, err)
	}

	// An unreadable pair is reported rather than silently replaced.
	if err := os.MkdirAll(filepath.Join(dir, "broken.example.com"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "broken.example.com", fullchainName), []byte("not a certificate"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "broken.example.com", keyName), []byte("not a key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := dueForRenewal("broken.example.com", now); err == nil {
		t.Fatal("a pair that cannot be parsed should be reported")
	}
}

// TestDueForRenewalTreatsThePlaceholderAsDue pins the first-issuance path. Paths
// resolves the self-signed placeholder so the core has something to serve, but its
// ten-year NotAfter is not an issued certificate: reading it as one made Issue log
// "still valid" and return success without ever contacting the CA.
func TestDueForRenewalTreatsThePlaceholderAsDue(t *testing.T) {
	dir := tempDir(t)
	if err := os.MkdirAll(filepath.Join(dir, "example.com"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := GenerateSelfSigned(filepath.Join(dir, "example.com", fullchainName), filepath.Join(dir, "example.com", keyName), "example.com"); err != nil {
		t.Fatalf("GenerateSelfSigned: %v", err)
	}
	due, _, err := dueForRenewal("example.com", time.Now())
	if err != nil {
		t.Fatalf("dueForRenewal: %v", err)
	}
	if !due {
		t.Fatal("a self-signed placeholder is not an issued certificate and must be renewed")
	}
}

func TestExpiryOfReadsTheLeaf(t *testing.T) {
	dir := tempDir(t)
	want := time.Now().AddDate(0, 0, 42).Truncate(time.Second)
	writePair(t, dir, "example.com", want)

	got, err := expiryOf("example.com")
	if err != nil {
		t.Fatalf("expiryOf: %v", err)
	}
	if !got.Equal(want) {
		t.Fatalf("expiryOf = %s, want %s", got, want)
	}
	if _, err := expiryOf("missing.com"); err == nil {
		t.Fatal("expiryOf without a pair should fail")
	}
}

// TestDirectoryPinsLetsEncrypt keeps the CA a decision of this package rather than
// of whatever tool is installed: the staging switch has to name the same CA as
// production, and the two must not be the same endpoint.
func TestDirectoryPinsLetsEncrypt(t *testing.T) {
	if got := letsEncryptDirectory(false); got != lego.DirectoryURLLetsEncrypt {
		t.Errorf("production directory = %q, want %q", got, lego.DirectoryURLLetsEncrypt)
	}
	staging := letsEncryptDirectory(true)
	if staging != lego.DirectoryURLLetsEncryptStaging {
		t.Errorf("staging directory = %q, want %q", staging, lego.DirectoryURLLetsEncryptStaging)
	}
	if staging == lego.DirectoryURLLetsEncrypt {
		t.Error("staging and production must not share an endpoint")
	}
}

// TestStandaloneChallengeListensOnPort80 is the contract the CA relies on: the
// HTTP-01 challenge is only ever fetched from http://<domain>/, so the listener
// has to be on 80, not on a port of our choosing.
func TestStandaloneChallengeListensOnPort80(t *testing.T) {
	provider, ok := standaloneChallenge().(*http01.ProviderServer)
	if !ok {
		t.Fatalf("the default provider is %T, want an HTTP-01 provider server", standaloneChallenge())
	}
	if got := provider.GetAddress(); got != ":80" {
		t.Errorf("challenge address = %q, want :80", got)
	}
}

func TestStaging(t *testing.T) {
	t.Setenv(StagingEnv, "")
	if Staging() {
		t.Fatal("staging should be off by default")
	}
	for _, on := range []string{"1", "true", "yes", "on", "TRUE"} {
		t.Setenv(StagingEnv, on)
		if !Staging() {
			t.Fatalf("%q should enable staging", on)
		}
	}
	for _, off := range []string{"0", "false", "no", "off", ""} {
		t.Setenv(StagingEnv, off)
		if Staging() {
			t.Fatalf("%q should not enable staging", off)
		}
	}
}

func TestReportMismatch(t *testing.T) {
	rep := Report{Resolved: []string{"1.2.3.4"}, PublicIP: "1.2.3.4"}
	if rep.Mismatch() {
		t.Fatal("an address that matches this host is not a mismatch")
	}
	rep.Resolved = []string{"5.6.7.8"}
	if !rep.Mismatch() {
		t.Fatal("a domain pointing only elsewhere is a mismatch")
	}
	rep.Resolved = []string{"5.6.7.8", "1.2.3.4"}
	if rep.Mismatch() {
		t.Fatal("one matching address is a match, CDNs round-robin")
	}
	// Without knowing this host's address there is nothing to compare.
	if (Report{Resolved: []string{"5.6.7.8"}}).Mismatch() {
		t.Fatal("an unknown public IP is not a mismatch")
	}
	if (Report{PublicIP: "1.2.3.4"}).Mismatch() {
		t.Fatal("an unresolvable domain is not a mismatch")
	}
}

// TestOthersAndStrayAddress cover the failure this panel hit in practice: a domain
// with one correct A record and one stale one. Let's Encrypt validates every
// address, so the stale record fails the order, and the error names that address.
func TestOthersAndStrayAddress(t *testing.T) {
	rep := Report{PublicIP: "154.201.92.132", Resolved: []string{"154.201.92.132", "103.185.248.26"}}
	if got := rep.Others(); len(got) != 1 || got[0] != "103.185.248.26" {
		t.Fatalf("Others() = %v, want [103.185.248.26]", got)
	}
	if rep.Mismatch() {
		t.Fatal("one matching address means this host is among them, not a mismatch")
	}
	if got := (Report{PublicIP: "1.2.3.4", Resolved: []string{"1.2.3.4"}}).Others(); len(got) != 0 {
		t.Fatalf("Others() = %v, want none", got)
	}
	// Without a known public address there is nothing to compare against.
	if got := (Report{Resolved: []string{"1.2.3.4"}}).Others(); len(got) != 0 {
		t.Fatalf("Others() without a public address = %v, want none", got)
	}

	// The CA's own wording, as it survives into the error lego returns.
	caError := errors.New(`issue: dev.example.com: Invalid status. Verification error details: During secondary validation: 103.185.248.26: Fetching http://dev.example.com/.well-known/acme-challenge/abc: Connection refused`)
	if got := StrayAddress(caError); got != "103.185.248.26" {
		t.Fatalf("StrayAddress = %q, want 103.185.248.26", got)
	}
	if got := StrayAddress(errors.New("issue: something else went wrong")); got != "" {
		t.Fatalf("StrayAddress of an unrelated error = %q, want empty", got)
	}
	if got := StrayAddress(nil); got != "" {
		t.Fatalf("StrayAddress(nil) = %q, want empty", got)
	}
	// An IPv6 address arrives in brackets.
	ipv6 := errors.New(`During secondary validation: [2001:db8::1]: Fetching http://dev.example.com/: Connection refused`)
	if got := StrayAddress(ipv6); got != "2001:db8::1" {
		t.Fatalf("StrayAddress = %q, want 2001:db8::1", got)
	}
}

// timerListed is real `systemctl list-timers easysb-acme.timer --no-pager` output
// taken from a Debian 13 host with the timer running, kept verbatim: the columns
// are fixed width and measured against the header, LAST and PASSED are dashes when
// empty, and NEXT is a five-field timestamp faster than the LEFT column is wide.
const timerListed = "NEXT                        LEFT LAST PASSED UNIT              ACTIVATES\n" +
	"Fri 2026-09-25 01:31:33 UTC  17h -         - easysb-acme.timer easysb-acme.service\n" +
	"\n1 timers listed.\n"

// timerNone is the same command with no timer installed.
const timerNone = "NEXT LEFT PASSED UNIT ACTIVATES\n\n0 timers listed.\n"

// TestRenewalUnitContract locks the unit text that renewal depends on. The panel
// never installs a crontab, so these files are the only thing that renews a
// certificate: a typo in a section or a missing --renew-certs would be silent
// until a certificate expires.
func TestRenewalUnitContract(t *testing.T) {
	timer := renewTimerUnit
	for _, want := range []string{"[Timer]", "OnCalendar=daily", "Persistent=true", "[Install]", "WantedBy=timers.target"} {
		if !strings.Contains(timer, want) {
			t.Errorf("timer unit is missing %q", want)
		}
	}

	svc := renewServiceUnit
	for _, want := range []string{"[Service]", "Type=oneshot", "--renew-certs"} {
		if !strings.Contains(svc, want) {
			t.Errorf("service unit is missing %q", want)
		}
	}
	// The command has to come from the format argument, not from a placeholder
	// that was never filled in.
	if !strings.Contains(svc, "ExecStart=%s") {
		t.Error("service unit does not take the executable path")
	}

	if !strings.Contains(renewOpenRC, "%s --renew-certs") {
		t.Error("OpenRC script does not run the renewal command")
	}
}

func TestParseTimerStatus(t *testing.T) {
	if got, want := parseTimerStatus(timerListed), "Fri 2026-09-25 01:31:33 UTC (17h)"; got != want {
		t.Fatalf("parseTimerStatus = %q, want %q", got, want)
	}
	if got := parseTimerStatus(timerNone); got != "" {
		t.Fatalf("parseTimerStatus of an empty list = %q, want empty", got)
	}
	if got := parseTimerStatus(""); got != "" {
		t.Fatalf("parseTimerStatus of no output = %q, want empty", got)
	}
	if got := parseTimerStatus("nonsense\n"); got != "" {
		t.Fatalf("parseTimerStatus of unparsable output = %q, want empty", got)
	}
}

// TestParseTimerStatusWithoutLeft covers a row whose LEFT column is empty: the
// next run is still worth showing.
func TestParseTimerStatusWithoutLeft(t *testing.T) {
	out := "NEXT                        LEFT LAST PASSED UNIT              ACTIVATES\n" +
		"Fri 2026-09-25 01:31:33 UTC     -         - easysb-acme.timer easysb-acme.service\n"
	if got, want := parseTimerStatus(out), "Fri 2026-09-25 01:31:33 UTC"; got != want {
		t.Fatalf("parseTimerStatus = %q, want %q", got, want)
	}
}
