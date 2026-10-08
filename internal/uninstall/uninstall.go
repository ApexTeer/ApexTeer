// Package uninstall removes the EasySB deployment, keeping the issued
// certificates like the legacy shell implementation.
package uninstall

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/EasySBTeam/EasySB/internal/cert"
	"github.com/EasySBTeam/EasySB/internal/firewall"
	"github.com/EasySBTeam/EasySB/internal/node"
	"github.com/EasySBTeam/EasySB/internal/service"
	"github.com/EasySBTeam/EasySB/internal/subd"
	"github.com/EasySBTeam/EasySB/internal/sysinfo"
)

// BackupDir is where the pre-uninstall archive is written.
const BackupDir = "/root"

// shortcuts lists the launcher paths removed on uninstall. They are the same paths the
// installer uses, so the list lives with the rest of the install paths.
var shortcuts = sysinfo.PanelPaths

// Backup archives /etc/sing-box into /root and returns the archive path.
func Backup(log func(string)) (string, error) {
	if _, err := os.Stat(sysinfo.WorkDir); err != nil {
		return "", nil
	}
	stamp := time.Now().Format("20060102-150405")
	archive := fmt.Sprintf("%s/easysb-backup-%s.tar.gz", BackupDir, stamp)
	if _, err := exec.LookPath("tar"); err != nil {
		return "", errors.New("tar not available")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "tar", "-czf", archive, "-C", "/etc", "sing-box")
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", errors.New("backup: " + strings.TrimSpace(string(out)))
	}
	log("backup: " + archive)
	return archive, nil
}

// Run performs the full uninstall sequence.
func Run(ctx context.Context, log func(string)) error {
	if _, err := Backup(log); err != nil {
		log("backup failed: " + err.Error())
	}
	// The certificate state directory sits under the work directory, which is
	// removed below, so it is moved aside first: a reinstall that had to issue
	// certificates again would spend a Let's Encrypt rate limit to get back what
	// was already there. A failure here is not fatal, the backup above holds a copy.
	// A state directory that is not under the work directory needs no such care.
	certDir := cert.Dir()
	stashed := ""
	if within(sysinfo.WorkDir, certDir) {
		var err error
		if stashed, err = stashCerts(certDir); err != nil {
			log("certificates: " + err.Error())
		}
	}

	log("$ service stop/disable " + sysinfo.ServiceName)
	if err := service.Do(ctx, "stop"); err != nil {
		log("stop: " + err.Error())
	}
	if err := service.Do(ctx, "disable"); err != nil {
		log("disable: " + err.Error())
	}

	// The subscription service serves the accounts to the outside world, so it
	// goes before the files it reads do.
	log("$ service stop/disable " + sysinfo.SubServiceName)
	if err := subd.Do(ctx, "stop"); err != nil {
		log("stop subscription: " + err.Error())
	}
	if err := subd.Do(ctx, "disable"); err != nil {
		log("disable subscription: " + err.Error())
	}

	nodes, err := node.Load(sysinfo.NodesFile)
	if err != nil {
		log("nodes: " + err.Error())
		nodes = &node.Store{}
	}
	if err := firewall.Remove(ctx, nodes.Nodes()); err != nil {
		log("firewall remove: " + err.Error())
	}
	if err := firewall.UnitAction(ctx, "disable"); err != nil {
		log("firewall disable: " + err.Error())
	}
	if err := firewall.RemoveUnit(); err != nil {
		log("firewall unit: " + err.Error())
	}

	if err := service.RemoveUnit(); err != nil {
		log("service unit: " + err.Error())
	}
	if err := subd.RemoveUnit(); err != nil {
		log("subscription unit: " + err.Error())
	}

	if err := os.RemoveAll(sysinfo.WorkDir); err != nil {
		log("remove " + sysinfo.WorkDir + ": " + err.Error())
	}
	for _, path := range shortcuts {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			log("remove " + path + ": " + err.Error())
		}
	}

	log("done")
	if restored := restoreCerts(stashed, certDir); restored != "" {
		log("certificates: " + restored)
	}
	if len(cert.Domains()) > 0 {
		log("certificates kept in " + certDir)
	}
	return nil
}

// stashCerts moves dir out of the way, into the system temporary directory, and
// returns where it was put. The ACME account and the issued certificates are what
// a reinstall would otherwise have to obtain again, and a Let's Encrypt rate limit
// is the price of getting them.
func stashCerts(dir string) (string, error) {
	tmp, err := os.MkdirTemp("", "easysb-certs")
	if err != nil {
		return "", err
	}
	target := filepath.Join(tmp, filepath.Base(dir))
	if err := os.Rename(dir, target); err != nil {
		os.RemoveAll(tmp)
		return "", err
	}
	return target, nil
}

// restoreCerts moves a directory stashed by stashCerts back to dir, and returns
// the reason it could not be put back, if it could not. It reports nothing (an
// empty string) when there was nothing to restore.
func restoreCerts(stashed, dir string) string {
	if stashed == "" {
		return ""
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err.Error() + "; they are in " + stashed
	}
	if err := os.Rename(stashed, dir); err != nil {
		return err.Error() + "; they are in " + stashed
	}
	if err := os.RemoveAll(filepath.Dir(stashed)); err != nil {
		return err.Error()
	}
	return ""
}

// within reports whether path is the directory dir itself or a file or directory
// below it.
func within(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
