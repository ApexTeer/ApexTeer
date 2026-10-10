// Package cert resolves the TLS certificate of the sing-box server: a Let's
// Encrypt certificate the panel issues itself over the HTTP-01 challenge, or a
// self-signed placeholder for as long as none has been issued.
//
// Issuance used to be a shell dependency: acme.sh, downloaded and installed at
// runtime, with socat or python to answer the challenge. Both are gone. The panel
// is one static binary and now speaks ACME itself through
// github.com/go-acme/lego/v5, so a minimal image needs neither a downloaded
// script nor a second interpreter, and there is no version of either that can
// drift from what this package reads. Everything the panel used to parse out of
// acme.sh's log output ("Domains not changed", "Next renewal time is", "Renew
// success") is now a value this package knows first hand: an account resource, a
// certificate resource and the expiry of the leaf that is on disk.
//
// The layout under Dir() is the panel's own:
//
//	<dir>/account.key      the ACME account key, 0600
//	<dir>/account.json     the account URL the CA handed back, 0600
//	<dir>/<domain>/fullchain.cer   the issued leaf plus its chain, 0644
//	<dir>/<domain>/private.key     its private key, 0600
package cert

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/EasySBTeam/EasySB/internal/atomicfile"
	"github.com/EasySBTeam/EasySB/internal/sysinfo"
)

// Filenames inside the state directory. They are the names the rest of the tree
// and the operators' own notes already use for the self-signed pair.
const (
	fullchainName   = "fullchain.cer"
	keyName         = "private.key"
	accountKeyName  = "account.key"
	accountJSONName = "account.json"
)

// DirEnv points the panel at a non-default certificate state directory. The tests
// use it to stay off /etc/sing-box, which is where the panel writes when it runs
// as root.
const DirEnv = "EASYSB_ACME_DIR"

// DefaultDir holds the ACME account and every certificate the panel issued.
const DefaultDir = sysinfo.WorkDir + "/acme"

// Resolved points at the certificate pair currently in use.
type Resolved struct {
	Fullchain  string
	Key        string
	SelfSigned bool
}

// Dir returns the directory that holds the ACME account and the issued
// certificates. Unlike the acme.sh home this replaces, it is a fixed location:
// the panel writes it as root and reads it as root, from the install script, from
// sudo and from the renewal unit alike, so there is no per-invocation HOME to
// probe for. DirEnv is the only override.
func Dir() string {
	if dir := strings.TrimSpace(os.Getenv(DirEnv)); dir != "" {
		return dir
	}
	return DefaultDir
}

// Paths resolves the certificate pair issued for a domain.
//
// The panel's own layout under Dir() wins. A host that was upgraded keeps serving
// what the previous version left behind, which is what makes the upgrade invisible
// to clients, so two legacy layouts are read as a fallback:
//
//   - `<home>/.acme.sh/<domain>_ecc/{fullchain.cer,<domain>.key}`, where acme.sh
//     put an issued pair on a 4.x host;
//   - `<workdir>/cert/{fullchain.cer,private.key}`, the flat pair older builds of
//     this panel kept for the active domain.
//
// Both are read-only: the panel never writes there, and its next issuance lands in
// its own directory, which then takes precedence.
func Paths(domain string) (string, string, bool) {
	if dir, ok := domainDir(domain); ok {
		pair := pairIn(dir, fullchainName, keyName)
		if pair.ok() {
			return pair.fullchain, pair.key, true
		}
	}
	for _, dir := range legacyDirs(domain) {
		for _, names := range legacyPairNames(domain) {
			pair := pairIn(dir, names[0], names[1])
			if pair.ok() {
				return pair.fullchain, pair.key, true
			}
		}
	}
	return "", "", false
}

// pair is a certificate pair on disk; every field is empty when either file is
// missing, which is what makes a half-written pair unusable rather than half-usable.
type pair struct {
	fullchain string
	key       string
}

func (p pair) ok() bool { return p.fullchain != "" && p.key != "" }

// pairIn resolves one fullchain/key pair inside a directory.
//
// Existence and non-empty size are the test here, not a key match. A match check
// was tried in this position and rejected: ResolveActive would treat a mismatched
// pair as "no pair" and quietly serve a self-signed placeholder instead of the
// certificate the operator installed, and dueForRenewal would read an unparsable
// pair as "no certificate" and silently re-order one. Both are worse than the
// problem they were meant to catch. The pair is instead verified where it is
// written, which is the point the wrong combination can first appear - see
// writeFile and installPair.
func pairIn(dir, fname, kname string) pair {
	fullchain := filepath.Join(dir, fname)
	key := filepath.Join(dir, kname)
	if !exists(fullchain) || !exists(key) {
		return pair{}
	}
	return pair{fullchain: fullchain, key: key}
}

// legacyDirs are the directories a previous EasySB or acme.sh kept certificates in.
//
// The domain is validated through domainDir first, so this fallback cannot be
// steered by a name the primary lookup would have refused. These are read-only
// paths joined from a value that arrives from the state file and from directory
// entries, and a name carrying a separator or ".." would otherwise select a
// directory outside the certificate tree: a domain is checked in one place or it
// is not checked at all.
func legacyDirs(domain string) []string {
	if _, ok := domainDir(domain); !ok {
		return nil
	}
	dirs := []string{
		filepath.Join(sysinfo.CertDir, domain),
		sysinfo.CertDir,
	}
	for _, home := range []string{os.Getenv("HOME"), "/root"} {
		if strings.TrimSpace(home) == "" {
			continue
		}
		dirs = append(dirs, filepath.Join(home, ".acme.sh", domain+"_ecc"))
	}
	return dirs
}

// legacyPairNames are the file names those layouts used. acme.sh names the key after
// the domain; the panel's own older layout used private.key.
func legacyPairNames(domain string) [][2]string {
	return [][2]string{
		{fullchainName, domain + ".key"},
		{fullchainName, keyName},
	}
}

// domainDir is the per-domain directory, and the gate that keeps a domain name
// from ever pointing outside Dir(). The value arrives from the state file the
// operator edits and from the directory entries the panel lists back, so a name
// carrying a separator, a drive letter or a parent reference is refused here
// rather than joined onto a path.
func domainDir(domain string) (string, bool) {
	domain = strings.TrimSpace(domain)
	switch {
	case domain == "",
		domain == ".",
		domain == "..",
		strings.ContainsAny(domain, `/\`),
		strings.Contains(domain, ".."):
		return "", false
	}
	return filepath.Join(Dir(), domain), true
}

// Usable reports whether a real, publicly trusted certificate is installed for
// the domain. The subscription URL the panel prints and the listener the
// subscription service opens must answer this question the same way, or clients
// are handed an address that speaks the other protocol, so both ask it here.
//
// The pair is inspected rather than assumed: a self-signed certificate — the
// placeholder this package writes, or one an earlier build left behind — is not a
// certificate a client accepts, so it does not count.
func Usable(domain string) bool {
	fullchain, _, ok := Paths(domain)
	if !ok {
		return false
	}
	return !selfSigned(fullchain)
}

// selfSigned reports whether the first certificate in a PEM file signs itself. The
// signature is checked with the certificate's own key rather than through
// CheckSignatureFrom, which refuses a self-signed leaf that is not a CA — which is
// exactly the shape of the placeholder this package writes. An unreadable file counts
// as self-signed, which is the safe direction: the panel then prints an http:// URL
// instead of promising a TLS endpoint a client would refuse.
func selfSigned(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return true
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return true
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return true
	}
	return leaf.CheckSignature(leaf.SignatureAlgorithm, leaf.RawTBSCertificate, leaf.Signature) == nil
}

// Domains lists the domains that have an issued certificate, in directory order,
// which is the order the switch and remove menus show them in.
func Domains() []string {
	entries, err := os.ReadDir(Dir())
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, _, ok := Paths(e.Name()); ok {
			out = append(out, e.Name())
		}
	}
	return out
}

// ResolveActive returns the active certificate pair. It prefers the certificate
// issued for the configured domain and otherwise makes sure a self-signed
// placeholder exists under sysinfo.CertDir, which is what keeps the core's
// configuration renderable before the first certificate is issued.
func ResolveActive(domain string) (Resolved, error) {
	if fullchain, key, ok := Paths(domain); ok {
		return Resolved{Fullchain: fullchain, Key: key}, nil
	}

	cn := strings.TrimSpace(domain)
	if cn == "" {
		cn = "easysb.local"
	}
	if !exists(sysinfo.SelfSignedCert) || !exists(sysinfo.SelfSignedKey) {
		if err := GenerateSelfSigned(sysinfo.SelfSignedCert, sysinfo.SelfSignedKey, cn); err != nil {
			return Resolved{}, err
		}
	}
	return Resolved{
		Fullchain:  sysinfo.SelfSignedCert,
		Key:        sysinfo.SelfSignedKey,
		SelfSigned: true,
	}, nil
}

// GenerateSelfSigned writes a self-signed EC certificate for cn. It is a
// placeholder: it keeps the core's configuration valid and lets the panel come up
// before a domain is pointed at the host, and it is never served over TLS by the
// subscription service, because clients reject it.
//
// The pair is generated in process rather than by openssl, which a minimal image
// does not necessarily carry and which the panel no longer needs for anything.
func GenerateSelfSigned(certPath, keyPath, cn string) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return err
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	// A name is only trustworthy to a client through a SAN, so the placeholder
	// carries one; an address goes in the IP field, where a certificate expects it.
	if ip := net.ParseIP(cn); ip != nil {
		template.IPAddresses = []net.IP{ip}
	} else {
		template.DNSNames = []string{cn}
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	if err := writeFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return err
	}
	return writeFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600)
}

// writeFile writes a file under Dir() (or sysinfo.CertDir for the placeholder),
// creating the directory it belongs to. The mode is set explicitly because the
// panel runs as root with whatever umask the invoking shell had, and a private
// key that ends up world readable is a leak that outlives the run that caused it.
//
// The write is atomic, so a reader never opens a half-written key or certificate.
// A truncated private key is a deployment that cannot start, and the placeholder
// pair is rewritten on these paths whenever a certificate is missing.
//
// What this deliberately does not do is check the file being written against its
// counterpart. A renewal writes a NEW certificate over an OLD key before it writes
// the new key, so the two legitimately disagree mid-renewal: refusing that would
// break every renewal rather than protect it. What is guaranteed instead is
// per-file atomicity - each of the pair is always a whole file, either its
// previous content or its new one - which is the strongest thing two separate
// paths can offer, and the reason installPair's write order matters.
func writeFile(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return atomicfile.Write(path, data, mode)
}

func exists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Size() > 0
}

// cleanDomain validates a domain the caller asked for, so a value from the
// operator or from a menu cannot become half a path or half a URL.
func cleanDomain(domain string) (string, error) {
	domain = strings.TrimSpace(domain)
	if domain == "" {
		return "", fmt.Errorf("domain is required")
	}
	if _, ok := domainDir(domain); !ok {
		return "", fmt.Errorf("invalid domain %q", domain)
	}
	return domain, nil
}
