// Package sbcore is the sing-box engine the panel carries inside itself.
//
// The core is not a file any more. github.com/sagernet/sing-box is a module
// requirement of this binary, so the version is a go.mod line and the node is this
// same executable started in node mode (`easysb core run -c /etc/sing-box/config.json`,
// which the service unit runs); there is nothing to download, install or switch on
// the host. Two consequences are worth stating next to the code, because both
// surprise:
//
//   - A capability is a build tag, not a probe. with_v2ray_api is the only way
//     sing-box counts bytes per account, which is what the traffic columns in
//     账号与流量 read; a tag cannot be discovered at run time, it is compiled in or
//     it is not. StatsCapable answers from the build (stats_on.go / stats_off.go),
//     and the deploy path asks before writing the experimental.v2ray_api block:
//     a build without the tag rejects a configuration that names the API at all.
//   - Check runs the real core. There is no sing-box process to fork: the same
//     library that serves the node builds the configuration and closes it again,
//     so what the panel validates is what the node accepts.
package sbcore

import (
	"context"
	"os"
	"runtime/debug"

	"github.com/sagernet/sing-box"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/experimental/deprecated"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/json"
	"github.com/sagernet/sing/service"
)

// modulePath is how the core appears in this module's requirements; it is also
// where Version reads the release number from when the build did not stamp one.
const modulePath = "github.com/sagernet/sing-box"

// engineContext prepares the context the core needs: the registries every
// protocol, DNS transport and service is looked up in, plus the manager that
// reports deprecated options. It is the same setup the upstream command line
// builds, so a configuration the panel accepts is a configuration sing-box
// itself accepts. The registries are attached to the caller's context rather
// than to a fresh one, so cancelling a deploy cancels the engine with it.
func engineContext(parent context.Context) context.Context {
	ctx := service.ContextWith(parent, deprecated.NewStderrManager(log.StdLogger()))
	return include.Context(ctx)
}

// Parse decodes a configuration file the way the core does, including the
// extended JSON the schema uses (comments, merged fields).
func Parse(path string) (option.Options, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return option.Options{}, E.Cause(err, "read config at ", path)
	}
	var options option.Options
	options, err = json.UnmarshalExtendedContext[option.Options](engineContext(context.Background()), content)
	if err != nil {
		return option.Options{}, E.Cause(err, "decode config at ", path)
	}
	return options, nil
}

// Check builds the configuration and throws it away again. It is the acceptance
// test the deploy path runs before restarting the node: a core that refuses the
// document refuses it here, in the same process, instead of leaving a service
// that cannot start. Building and closing the engine is the slowest step of a
// deploy, so it runs on the caller's context and can be given up on.
func Check(ctx context.Context, path string) error {
	options, err := Parse(path)
	if err != nil {
		return err
	}
	checkCtx, cancel := context.WithCancel(service.ExtendContext(engineContext(ctx)))
	defer cancel()
	instance, err := box.New(box.Options{Context: checkCtx, Options: options})
	if err != nil {
		return err
	}
	return instance.Close()
}

// Run starts the node described by the configuration file and blocks until ctx is
// cancelled, which is what the node service unit does: the process is the node,
// and stopping the unit ends it. The engine is built on ctx, so the cancellation
// the process receives reaches its listeners rather than only this function.
func Run(ctx context.Context, path string) error {
	options, err := Parse(path)
	if err != nil {
		return err
	}
	runCtx, cancel := context.WithCancel(service.ExtendContext(engineContext(ctx)))
	defer cancel()
	instance, err := box.New(box.Options{Context: runCtx, Options: options})
	if err != nil {
		return E.Cause(err, "create the service")
	}
	if err := instance.Start(); err != nil {
		_ = instance.Close()
		return E.Cause(err, "start the service")
	}
	<-ctx.Done()
	return instance.Close()
}

// Version is the sing-box release compiled into this binary. A release build
// stamps it into the module constant; a developer build only knows the version of
// the module requirement, which is the same number.
func Version() string {
	if C.Version != "" && C.Version != "unknown" {
		return C.Version
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, dep := range info.Deps {
			if dep.Path == modulePath {
				return dep.Version
			}
		}
	}
	return "unknown"
}
