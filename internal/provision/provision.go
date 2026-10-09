// Package provision deploys EasySB from a declarative spec instead of the
// panel. The TUI is the interactive surface, but a host driven by a script has
// no terminal: this package is what turns a single JSON document into the
// certificate, the node store, the account store, the rendered core config and
// the running services, using the same internal calls the panel makes.
//
// The spec is a desired state rather than a one-off script. Re-running it with
// the same document reuses the nodes it already finds (matched by name, protocol
// and port) and keeps each account's token and credentials, so a second run
// never rotates a subscription URL or invalidates a client that already
// imported one.
package provision

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/EasySBTeam/EasySB/internal/cert"
	"github.com/EasySBTeam/EasySB/internal/deploy"
	"github.com/EasySBTeam/EasySB/internal/firewall"
	"github.com/EasySBTeam/EasySB/internal/node"
	"github.com/EasySBTeam/EasySB/internal/service"
	"github.com/EasySBTeam/EasySB/internal/state"
	"github.com/EasySBTeam/EasySB/internal/subd"
	"github.com/EasySBTeam/EasySB/internal/subscribe"
	"github.com/EasySBTeam/EasySB/internal/sysinfo"
	"github.com/EasySBTeam/EasySB/internal/user"
)

// GiB is the byte count of one gibibyte, the unit a spec's quota is given in.
const GiB = 1 << 30

// NodeSpec is one protocol inbound to deploy. Name defaults to the protocol's
// label and Port to its default; SNI and HopRange only apply to the protocols
// that use them.
type NodeSpec struct {
	Protocol string `json:"protocol"`
	Name     string `json:"name,omitempty"`
	Port     int    `json:"port,omitempty"`
	SNI      string `json:"sni,omitempty"`
	HopRange string `json:"hop_range,omitempty"`
}

// AccountSpec is one account to create or bring to this state. Password applies
// to the credential fields a node's protocol authenticates with a password, and
// UUID to the ones that use a UUID; a node whose protocol uses neither ignores
// them. QuotaGB of 0 means unlimited and ExpireDays of 0 means no expiry. Nodes
// names the nodes the account may use, by protocol key or node name; empty
// selects every deployed node.
type AccountSpec struct {
	Name       string   `json:"name"`
	Password   string   `json:"password,omitempty"`
	UUID       string   `json:"uuid,omitempty"`
	QuotaGB    float64  `json:"quota_gb,omitempty"`
	ExpireDays int      `json:"expire_days,omitempty"`
	Nodes      []string `json:"nodes,omitempty"`
}

// Spec is the whole deployment document.
type Spec struct {
	Domain   string        `json:"domain,omitempty"`
	Email    string        `json:"email,omitempty"`
	ServerIP string        `json:"server_ip,omitempty"`
	SubPort  int           `json:"sub_port,omitempty"`
	Nodes    []NodeSpec    `json:"nodes,omitempty"`
	Accounts []AccountSpec `json:"accounts,omitempty"`
}

// Result describes what a run produced, for a summary.
type Result struct {
	Domain   string
	Nodes    []NodeStatus
	Accounts []AccountStatus
}

// NodeStatus is one deployed node.
type NodeStatus struct {
	Name     string
	Protocol string
	Port     int
}

// AccountStatus is one account and the subscription URL that reaches it.
type AccountStatus struct {
	Name  string
	Token string
	URL   string
}

// Parse decodes and validates a spec. Unknown JSON fields are rejected, so a
// mistyped key fails loudly instead of being silently ignored. An absent node
// list means every protocol at its default port; that default needs a domain,
// so a spec that only wants VLESS + Reality must name it explicitly.
func Parse(data []byte) (Spec, error) {
	var s Spec
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return Spec{}, fmt.Errorf("parse spec: %w", err)
	}
	if len(s.Nodes) == 0 {
		for _, key := range state.Keys {
			s.Nodes = append(s.Nodes, NodeSpec{Protocol: key})
		}
	}
	if err := s.validate(); err != nil {
		return Spec{}, err
	}
	return s, nil
}

// validate fills in the per-node defaults in place and reports the first reason
// the spec cannot be deployed.
func (s *Spec) validate() error {
	subPort := s.SubPort
	if subPort == 0 {
		subPort = state.DefaultSubServePort
	}
	names := map[string]bool{}
	ports := map[int]bool{}
	needsCert := false
	for i := range s.Nodes {
		n := &s.Nodes[i]
		if !state.Known(n.Protocol) {
			return fmt.Errorf("unknown protocol %q", n.Protocol)
		}
		if strings.TrimSpace(n.Name) == "" {
			n.Name = state.Labels[n.Protocol]
		}
		if n.Port == 0 {
			p, _ := strconv.Atoi(state.DefaultPorts[n.Protocol])
			n.Port = p
		}
		if n.Port < 1 || n.Port > 65535 {
			return fmt.Errorf("node %q port %d is outside 1-65535", n.Name, n.Port)
		}
		if n.Port == subPort {
			return fmt.Errorf("node %q uses the subscription service port %d", n.Name, subPort)
		}
		if names[n.Name] {
			return fmt.Errorf("duplicate node name %q", n.Name)
		}
		if ports[n.Port] {
			return fmt.Errorf("duplicate node port %d", n.Port)
		}
		names[n.Name] = true
		ports[n.Port] = true
		if n.Protocol != state.ProtoVLESSReality {
			needsCert = true
		}
	}
	if needsCert && strings.TrimSpace(s.Domain) == "" {
		return errors.New("a domain is required for every protocol except VLESS + Reality")
	}
	if strings.TrimSpace(s.Domain) != "" && strings.TrimSpace(s.Email) == "" {
		return errors.New("an ACME email is required to issue a certificate")
	}
	seen := map[string]bool{}
	for _, a := range s.Accounts {
		if strings.TrimSpace(a.Name) == "" {
			return errors.New("account name is required")
		}
		if seen[a.Name] {
			return fmt.Errorf("duplicate account %q", a.Name)
		}
		seen[a.Name] = true
	}
	return nil
}

// Options are the system seams a run reaches through. Default fills them with
// the real calls; a test supplies its own.
type Options struct {
	NodesPath    string
	AccountsPath string
	Now          func() time.Time
	Log          func(string)

	LoadConfig    func() state.Config
	SaveConfig    func(state.Config) error
	Preflight     func(context.Context, string) cert.Report
	EnsureAccount func(context.Context, string, func(string)) error
	Issue         func(context.Context, string, string, func(string)) error
	CheckPort80   func() error
	CertPaths     func(string) (string, string, bool)
	InstallTimer  func(context.Context, func(string)) error
	ServiceActive func(context.Context) bool
	ServiceDo     func(context.Context, string) error
	ApplyStore    func(context.Context, state.Config, string, string) error
	SubWriteUnit  func() error
	SubDo         func(context.Context, string) error
	SubURL        func(state.Config, string) string
	FWApply       func(context.Context, state.Config, []node.Node, func(string)) error
	FWWriteUnit   func([]node.Node) error
	FWUnitAction  func(context.Context, string) error
}

// Default returns the real Options.
func Default() Options {
	return Options{
		NodesPath:     sysinfo.NodesFile,
		AccountsPath:  sysinfo.UsersFile,
		Now:           time.Now,
		Log:           func(string) {},
		LoadConfig:    state.Load,
		SaveConfig:    func(c state.Config) error { return c.Save() },
		Preflight:     cert.Preflight,
		EnsureAccount: cert.EnsureAccount,
		Issue:         cert.Issue,
		CheckPort80:   cert.CheckPort80,
		CertPaths:     cert.Paths,
		InstallTimer:  cert.InstallTimer,
		ServiceActive: service.Active,
		ServiceDo:     service.Do,
		ApplyStore:    deploy.ApplyStore,
		SubWriteUnit:  subd.WriteUnit,
		SubDo:         subd.Do,
		SubURL:        subscribe.SubscriptionURL,
		FWApply:       firewall.Apply,
		FWWriteUnit:   firewall.WriteUnit,
		FWUnitAction:  firewall.UnitAction,
	}
}

func (o Options) withDefaults() Options {
	d := Default()
	if o.NodesPath == "" {
		o.NodesPath = d.NodesPath
	}
	if o.AccountsPath == "" {
		o.AccountsPath = d.AccountsPath
	}
	if o.Now == nil {
		o.Now = d.Now
	}
	if o.Log == nil {
		o.Log = d.Log
	}
	if o.LoadConfig == nil {
		o.LoadConfig = d.LoadConfig
	}
	if o.SaveConfig == nil {
		o.SaveConfig = d.SaveConfig
	}
	if o.Preflight == nil {
		o.Preflight = d.Preflight
	}
	if o.EnsureAccount == nil {
		o.EnsureAccount = d.EnsureAccount
	}
	if o.Issue == nil {
		o.Issue = d.Issue
	}
	if o.CheckPort80 == nil {
		o.CheckPort80 = d.CheckPort80
	}
	if o.CertPaths == nil {
		o.CertPaths = d.CertPaths
	}
	if o.InstallTimer == nil {
		o.InstallTimer = d.InstallTimer
	}
	if o.ServiceActive == nil {
		o.ServiceActive = d.ServiceActive
	}
	if o.ServiceDo == nil {
		o.ServiceDo = d.ServiceDo
	}
	if o.ApplyStore == nil {
		o.ApplyStore = d.ApplyStore
	}
	if o.SubWriteUnit == nil {
		o.SubWriteUnit = d.SubWriteUnit
	}
	if o.SubDo == nil {
		o.SubDo = d.SubDo
	}
	if o.SubURL == nil {
		o.SubURL = d.SubURL
	}
	if o.FWApply == nil {
		o.FWApply = d.FWApply
	}
	if o.FWWriteUnit == nil {
		o.FWWriteUnit = d.FWWriteUnit
	}
	if o.FWUnitAction == nil {
		o.FWUnitAction = d.FWUnitAction
	}
	return o
}

// Run deploys the spec and returns what it produced. It is idempotent: the same
// spec against the same host adds nothing twice and rotates no credential.
func Run(ctx context.Context, spec Spec, opt Options) (*Result, error) {
	if err := spec.validate(); err != nil {
		return nil, err
	}
	opt = opt.withDefaults()
	log := opt.Log
	now := opt.Now()

	cfg := opt.LoadConfig()
	first := !cfg.NodeDeployed
	if cfg.SubServePort == 0 {
		cfg.SubServePort = state.DefaultSubServePort
	}
	if spec.SubPort > 0 {
		cfg.SubServePort = spec.SubPort
	}
	if cfg.SubSyncSecs == 0 {
		cfg.SubSyncSecs = state.DefaultSubSyncSeconds
	}
	if spec.ServerIP != "" {
		cfg.ServerIP = spec.ServerIP
	}
	if spec.Email != "" {
		cfg.ACMEEmail = spec.Email
	}

	// The node store first: an account selects nodes, so they have to exist.
	nodeStore, err := node.Load(opt.NodesPath)
	if err != nil {
		return nil, err
	}
	nodes, err := applyNodes(spec, nodeStore, log)
	if err != nil {
		return nil, err
	}

	userStore, err := user.Load(opt.AccountsPath)
	if err != nil {
		return nil, err
	}
	if err := applyAccounts(spec, userStore, nodes, now, log); err != nil {
		return nil, err
	}

	// The certificate is issued before the core is (re)started, so the first
	// configuration the core serves already carries the pair.
	if strings.TrimSpace(spec.Domain) != "" {
		if err := issueCertificate(ctx, spec, opt, log); err != nil {
			return nil, err
		}
		cfg.Domain = spec.Domain
		cfg.CertDomain = spec.Domain
	}
	if err := opt.SaveConfig(cfg); err != nil {
		return nil, err
	}

	if err := opt.ApplyStore(ctx, cfg, opt.NodesPath, opt.AccountsPath); err != nil {
		return nil, err
	}
	cfg.NodeDeployed = true
	if err := opt.SaveConfig(cfg); err != nil {
		return nil, err
	}

	// The hop firewall and the subscription service change with the host rather
	// than with a single node, so they are set up on the first deploy only, the
	// same as the panel does. A firewall that cannot be applied is a warning: the
	// nodes still serve, only port hopping is missing.
	if first {
		if err := opt.FWApply(ctx, cfg, nodes, log); err != nil {
			log("firewall: " + err.Error())
		} else if err := opt.FWWriteUnit(nodes); err == nil {
			_ = opt.FWUnitAction(ctx, "enable")
		}
	}

	if err := opt.SubWriteUnit(); err != nil {
		return nil, err
	}
	if err := opt.SubDo(ctx, "enable"); err != nil {
		log("subscription service enable: " + err.Error())
	}
	if err := opt.SubDo(ctx, "restart"); err != nil {
		return nil, err
	}

	return buildResult(cfg, nodes, userStore, opt.SubURL), nil
}

// applyNodes creates the spec's nodes that are not already present. A node is
// the same node when its protocol and port match, or when its name does, so a
// re-run reuses the stored node and keeps its id and key material.
func applyNodes(spec Spec, store *node.Store, log func(string)) ([]node.Node, error) {
	for _, sn := range spec.Nodes {
		if existing, ok := findNode(store.Nodes(), sn); ok {
			log(fmt.Sprintf("node present: %s (%s) on %d", existing.Name, existing.Protocol, existing.Port))
			continue
		}
		params := map[string]string{}
		if sn.SNI != "" {
			params[node.ParamRealitySNI] = sn.SNI
		}
		if sn.HopRange != "" {
			params[node.ParamHopRange] = sn.HopRange
		}
		n := node.New(sn.Protocol, sn.Name, sn.Port, params)
		if err := store.Add(n); err != nil {
			return nil, err
		}
		log(fmt.Sprintf("node added: %s (%s) on %d", n.Name, n.Protocol, n.Port))
	}
	return store.Nodes(), nil
}

func findNode(nodes []node.Node, sn NodeSpec) (node.Node, bool) {
	for _, n := range nodes {
		if n.Name == sn.Name || (n.Protocol == sn.Protocol && n.Port == sn.Port) {
			return n, true
		}
	}
	return node.Node{}, false
}

// applyAccounts creates the spec's accounts, or brings an existing one to the
// spec's quota, expiry and node selection. A new account gets a fresh token;
// an existing one keeps it, so its subscription URL survives a re-run.
func applyAccounts(spec Spec, store *user.Store, nodes []node.Node, now time.Time, log func(string)) error {
	for _, sa := range spec.Accounts {
		selections, err := selectionsFor(sa, nodes)
		if err != nil {
			return err
		}
		if _, ok := store.Find(sa.Name); ok {
			err := store.Update(sa.Name, func(u *user.User) error {
				for _, sel := range selections {
					u.Select(sel.Node, sel.Protocol)
				}
				applyAccountSettings(u, sa, now)
				return nil
			})
			if err != nil {
				return err
			}
			log("account updated: " + sa.Name)
			continue
		}
		u := user.New(sa.Name, selections, now)
		applyAccountSettings(&u, sa, now)
		if err := store.Add(u); err != nil {
			return err
		}
		log("account added: " + sa.Name)
	}
	return nil
}

// selectionsFor resolves an account's node list to (id, protocol) pairs. An
// empty list selects every node; each entry matches a node by protocol key or
// by name.
func selectionsFor(sa AccountSpec, nodes []node.Node) ([]user.Selection, error) {
	if len(sa.Nodes) == 0 {
		out := make([]user.Selection, 0, len(nodes))
		for _, n := range nodes {
			out = append(out, user.Selection{Node: n.ID, Protocol: n.Protocol})
		}
		return out, nil
	}
	var out []user.Selection
	for _, want := range sa.Nodes {
		matched := false
		for _, n := range nodes {
			if n.Protocol == want || n.Name == want {
				out = append(out, user.Selection{Node: n.ID, Protocol: n.Protocol})
				matched = true
			}
		}
		if !matched {
			return nil, fmt.Errorf("account %q selects unknown node %q", sa.Name, want)
		}
	}
	return out, nil
}

// applyAccountSettings writes the quota, expiry and any caller-supplied
// credential into an account. A generated credential is only overwritten when
// the spec provides a value for a field the protocol actually uses, which is
// what keeps a password from landing on a UUID-only node.
func applyAccountSettings(u *user.User, sa AccountSpec, now time.Time) {
	if sa.QuotaGB > 0 {
		u.QuotaBytes = int64(sa.QuotaGB * float64(GiB))
	} else {
		u.QuotaBytes = 0
	}
	if sa.ExpireDays > 0 {
		u.ExpireAt = now.UTC().AddDate(0, 0, sa.ExpireDays)
	} else {
		u.ExpireAt = time.Time{}
	}
	for id, cred := range u.Credentials {
		if sa.Password != "" && cred.Password != "" {
			cred.Password = sa.Password
		}
		if sa.UUID != "" && cred.UUID != "" {
			cred.UUID = sa.UUID
		}
		u.Credentials[id] = cred
	}
}

// issueCertificate runs the same sequence the panel's domain screen does: check
// where the domain points, register the ACME account, free port 80 by stopping a
// running core, issue, then restart what was stopped.
func issueCertificate(ctx context.Context, spec Spec, opt Options, log func(string)) error {
	domain := spec.Domain
	report := opt.Preflight(ctx, domain)
	if report.Mismatch() {
		log("dns: " + domain + " does not point at this host: " + strings.Join(report.Resolved, ", "))
	}
	for _, other := range report.Others() {
		log("dns: extra address " + other)
	}

	if err := opt.EnsureAccount(ctx, spec.Email, log); err != nil {
		return err
	}

	stopped := opt.ServiceActive(ctx)
	if stopped {
		log("stopping " + sysinfo.ServiceName + " to free port 80")
		if err := opt.ServiceDo(ctx, "stop"); err != nil {
			log("stop " + sysinfo.ServiceName + ": " + err.Error())
		}
	}
	restart := func() {
		if stopped {
			if err := opt.ServiceDo(ctx, "start"); err != nil {
				log("start " + sysinfo.ServiceName + ": " + err.Error())
			}
		}
	}

	if err := opt.CheckPort80(); err != nil {
		restart()
		return fmt.Errorf("port 80 is busy: %w", err)
	}
	if err := opt.Issue(ctx, domain, spec.Email, log); err != nil {
		restart()
		return err
	}
	restart()

	if _, _, ok := opt.CertPaths(domain); !ok {
		return errors.New("no usable certificate produced for " + domain)
	}
	if err := opt.InstallTimer(ctx, log); err != nil {
		log("renewal timer: " + err.Error())
	}
	return nil
}

// buildResult assembles the summary, using the same URL builder the panel and
// the subscription service use, so the address printed here is the one a client
// can actually fetch.
func buildResult(cfg state.Config, nodes []node.Node, store *user.Store, subURL func(state.Config, string) string) *Result {
	res := &Result{Domain: cfg.Domain}
	for _, n := range nodes {
		res.Nodes = append(res.Nodes, NodeStatus{Name: n.Name, Protocol: n.Protocol, Port: n.Port})
	}
	for _, u := range store.Users() {
		res.Accounts = append(res.Accounts, AccountStatus{
			Name:  u.Name,
			Token: u.Token,
			URL:   subURL(cfg, u.Token),
		})
	}
	return res
}
