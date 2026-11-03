// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 John-Michael and crossdock contributors

// Command crossdock publishes package artifacts as signed apt, pacman, RPM
// and GooGet repositories.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jm2/crossdock/internal/archpkg"
	"github.com/jm2/crossdock/internal/config"
	"github.com/jm2/crossdock/internal/deb"
	"github.com/jm2/crossdock/internal/publish"
	"github.com/jm2/crossdock/internal/serve"
	"github.com/jm2/crossdock/internal/sign"
)

// version is stamped at build time with
// -ldflags "-X main.version=v1.2.3"; it must stay a variable.
var version = "dev"

const usage = `usage: crossdock <command> [flags]

commands:
  publish       build all configured repos into a new release and swap it in
  serve         serve the current release over HTTP (read-only)
  nginx-config  print an nginx server block matching serve's behaviour
  inspect       print the metadata crossdock reads from package files
  version       print the version
`

func main() {
	log.SetFlags(0)
	log.SetPrefix("crossdock: ")
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "publish":
		err = cmdPublish(os.Args[2:])
	case "inspect":
		err = cmdInspect(os.Args[2:])
	case "serve":
		err = cmdServe(os.Args[2:])
	case "nginx-config":
		err = cmdNginx(os.Args[2:])
	case "version", "--version", "-v":
		fmt.Println("crossdock", version, runtime.GOOS+"/"+runtime.GOARCH, runtime.Version())
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
	if err != nil {
		log.Fatal(err)
	}
}

func cmdPublish(args []string) error {
	fs := flag.NewFlagSet("publish", flag.ExitOnError)
	cfgPath := fs.String("c", "crossdock.json", "config file")
	allowUnsigned := fs.Bool("allow-unsigned", false, "publish without signatures when no signing key is configured")
	quiet := fs.Bool("q", false, "only print errors")
	fs.Parse(args)

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	logf := log.Printf
	if *quiet {
		logf = func(string, ...any) {}
	}

	now, err := publishTime()
	if err != nil {
		return err
	}
	env := &publish.Env{Now: now, Logf: logf}
	if cfg.Signing != nil {
		s, err := sign.New(cfg.Signing)
		if err != nil {
			return err
		}
		defer s.Close()
		env.Signer = s
		logf("signing with %s", s.Fingerprint())
	} else if !*allowUnsigned {
		return errors.New("no signing key configured; add \"signing\" or pass -allow-unsigned")
	}

	rel, err := publish.Begin(cfg.Output, now)
	if err != nil {
		return err
	}
	env.Release = rel
	if err := build(cfg, env); err != nil {
		rel.Abort()
		return err
	}
	if err := rel.Commit(); err != nil {
		rel.Abort()
		return err
	}
	logf("published release %s", rel.ID)
	removed, err := publish.Prune(cfg.Output, cfg.KeepReleases)
	for _, id := range removed {
		logf("pruned release %s", id)
	}
	return err
}

func build(cfg *config.Config, env *publish.Env) error {
	if env.Signer != nil {
		for _, armor := range []bool{true, false} {
			key, err := env.Signer.PublicKey(armor)
			if err != nil {
				return err
			}
			name := "crossdock.gpg"
			if armor {
				name = "crossdock.asc"
			}
			if _, err := env.Release.WriteFile(name, key); err != nil {
				return err
			}
		}
	}
	for _, r := range cfg.Repos {
		var err error
		switch r.Format {
		case config.FormatDeb:
			err = deb.Publish(r, env)
		case config.FormatArch:
			err = archpkg.Publish(r, env)
		default:
			err = fmt.Errorf("format %q is not implemented yet (see PLAN.md)", r.Format)
		}
		if err != nil {
			return fmt.Errorf("%s/%s: %w", r.Format, r.Name, err)
		}
	}
	return nil
}

// publishTime honours SOURCE_DATE_EPOCH for reproducible output.
func publishTime() (time.Time, error) {
	if s := os.Getenv("SOURCE_DATE_EPOCH"); s != "" {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return time.Time{}, fmt.Errorf("SOURCE_DATE_EPOCH: %w", err)
		}
		return time.Unix(n, 0).UTC(), nil
	}
	return time.Now().UTC(), nil
}

func cmdInspect(args []string) error {
	fs := flag.NewFlagSet("inspect", flag.ExitOnError)
	files := fs.Bool("files", false, "include the file list (pacman packages)")
	fs.Parse(args)
	if fs.NArg() == 0 {
		return errors.New("inspect: no package files given")
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	for _, p := range fs.Args() {
		var out any
		switch {
		case strings.HasSuffix(p, ".deb"):
			pkg, err := deb.ReadPackage(p)
			if err != nil {
				return err
			}
			ctrl := map[string]string{}
			for _, f := range pkg.Control {
				ctrl[f.Name] = f.Value
			}
			out = map[string]any{"format": "deb", "path": p, "name": pkg.Name,
				"version": pkg.Version, "arch": pkg.Arch, "source": pkg.Source, "control": ctrl}
		case isArch(p):
			pkg, err := archpkg.ReadPackage(p)
			if err != nil {
				return err
			}
			m := map[string]any{"format": "arch", "path": p, "name": pkg.Name(),
				"version": pkg.Version(), "arch": pkg.Arch(), "pkginfo": pkg.Info}
			if *files {
				m["files"] = pkg.Files
			}
			out = m
		default:
			return fmt.Errorf("%s: unrecognized package type", p)
		}
		if err := enc.Encode(out); err != nil {
			return err
		}
	}
	return nil
}

func isArch(p string) bool {
	for _, s := range archpkg.Suffixes {
		if strings.HasSuffix(p, s) {
			return true
		}
	}
	return false
}

// serveSettings resolves the output directory and serve settings from
// -root or the config file.
func serveSettings(root, cfgPath string) (string, config.Serve, error) {
	if root != "" {
		return root, config.Serve{Listen: config.DefaultListen}, nil
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return "", config.Serve{}, fmt.Errorf("%w (or pass -root)", err)
	}
	return cfg.Output, cfg.Serve, nil
}

func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	cfgPath := fs.String("c", "crossdock.json", "config file (for its output directory)")
	root := fs.String("root", "", "output directory to serve (overrides -c)")
	listen := fs.String("listen", "", "listen address (default: config serve.listen, else "+config.DefaultListen+")")
	index := fs.Bool("index", false, "enable HTML directory listings (also config serve.index)")
	quiet := fs.Bool("q", false, "disable the access log")
	fs.Parse(args)

	out, sc, err := serveSettings(*root, *cfgPath)
	if err != nil {
		return err
	}
	if *listen != "" {
		sc.Listen = *listen
	}
	if *index {
		sc.Index = true
	}
	if _, err := os.Stat(out); err != nil {
		return fmt.Errorf("output directory: %w", err)
	}
	var h http.Handler = &serve.Handler{Output: out, Index: sc.Index}
	if !*quiet {
		h = serve.AccessLog(h, log.New(os.Stdout, "", log.LstdFlags|log.LUTC))
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return serve.Run(ctx, sc.Listen, h, func(a net.Addr) {
		log.Printf("serving %s on http://%s", out, a)
	})
}

func cmdNginx(args []string) error {
	fs := flag.NewFlagSet("nginx-config", flag.ExitOnError)
	cfgPath := fs.String("c", "crossdock.json", "config file (for its output directory)")
	root := fs.String("root", "", "output directory (overrides -c)")
	listen := fs.String("listen", "80", "nginx listen directive value")
	name := fs.String("server-name", "_", "nginx server_name")
	index := fs.Bool("index", false, "enable autoindex")
	fs.Parse(args)

	out, _, err := serveSettings(*root, *cfgPath)
	if err != nil {
		return err
	}
	return serve.WriteNginx(os.Stdout, serve.NginxOptions{Output: out, Listen: *listen, ServerName: *name, Index: *index})
}
