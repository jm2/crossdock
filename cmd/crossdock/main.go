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
	"github.com/jm2/crossdock/internal/goo"
	"github.com/jm2/crossdock/internal/publish"
	"github.com/jm2/crossdock/internal/rpm"
	"github.com/jm2/crossdock/internal/serve"
	"github.com/jm2/crossdock/internal/sign"
	"github.com/jm2/crossdock/internal/unitconf"
)

// version is stamped at build time with
// -ldflags "-X main.version=v1.2.3"; it must stay a variable.
var version = "dev"

const usage = `usage: crossdock <command> [flags]

commands:
  publish       build all configured repos into a new release and swap it in
  serve         serve the current release over HTTP (read-only)
  nginx-config  print an nginx server block matching serve's behaviour
  systemd-config  write systemd drop-ins that match the config's paths
  inspect       print the metadata crossdock reads from package files
  keygen        create a signing key (Ed25519) for publish
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
	case "systemd-config":
		err = cmdSystemd(os.Args[2:])
	case "keygen":
		err = cmdKeygen(os.Args[2:])
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
	cfgPath := fs.String("c", "crossdock.yaml", "config file")
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

	if err := publish.Lock(cfg.Output, logf); err != nil {
		return err
	}
	rel, err := publish.Begin(cfg.Output, now)
	if err != nil {
		return err
	}
	env.Release = rel
	rel.Logf = log.Printf // warnings still show with -q
	if cfg.NoSymlinks {
		rel.NoSymlinks()
	}
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

// checkNotEmpty refuses to empty a repo that has packages. An empty source
// is usually a storage problem, such as an unmounted disk, and publishing it
// would take the packages away from every client. A repo that has never had
// packages may stay empty, and allow_empty lets one be emptied on purpose.
func checkNotEmpty(r config.Repo, env *publish.Env) error {
	var suffixes []string
	switch r.Format {
	case config.FormatDeb:
		suffixes = []string{".deb"}
	case config.FormatArch:
		suffixes = archpkg.Suffixes
	case config.FormatRPM:
		suffixes = rpm.Suffixes
	case config.FormatGoo:
		suffixes = goo.Suffixes
	}
	files, err := publish.FindFiles(r.Source, suffixes...)
	if err != nil {
		return err
	}
	if len(files) > 0 {
		return nil
	}
	key := string(r.Format) + "/" + r.Name
	prev, err := env.Release.PreviousPackages(key, suffixes...)
	if err != nil {
		return err
	}
	if prev > 0 && !r.AllowEmpty {
		return fmt.Errorf("%s is empty but the published repo has packages; is the disk mounted? (set allow_empty: true on this repo to empty it)", r.Source)
	}
	if prev > 0 {
		env.Logf("warning: %s: emptying the repo (allow_empty)", key)
	}
	return nil
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
	// Check every repo first: the publish after this can take hours.
	for _, r := range cfg.Repos {
		if err := checkNotEmpty(r, env); err != nil {
			return fmt.Errorf("%s/%s: %w", r.Format, r.Name, err)
		}
	}
	for _, r := range cfg.Repos {
		// Again, as earlier repos can take hours and disks can go away.
		if err := checkNotEmpty(r, env); err != nil {
			return fmt.Errorf("%s/%s: %w", r.Format, r.Name, err)
		}
		var err error
		switch r.Format {
		case config.FormatDeb:
			err = deb.Publish(r, env)
		case config.FormatArch:
			err = archpkg.Publish(r, env)
		case config.FormatRPM:
			err = rpm.Publish(r, env)
		case config.FormatGoo:
			err = goo.Publish(r, env)
		default:
			err = fmt.Errorf("unknown format %q", r.Format)
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
	files := fs.Bool("files", false, "include the file list (pacman and RPM packages)")
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
		case strings.HasSuffix(p, ".rpm"):
			pkg, err := rpm.ReadPackage(p)
			if err != nil {
				return err
			}
			m := map[string]any{"format": "rpm", "path": p, "name": pkg.Name, "nevra": pkg.NEVRA(),
				"epoch": pkg.Epoch, "version": pkg.Version, "release": pkg.Release, "arch": pkg.Arch,
				"summary": pkg.Summary, "license": pkg.License, "url": pkg.URL, "sourcerpm": pkg.SourceRPM,
				"provides": pkg.Provides, "requires": pkg.Requires}
			if *files {
				m["files"] = pkg.Files
			}
			out = m
		case strings.HasSuffix(p, ".goo"):
			pkg, err := goo.ReadPackage(p)
			if err != nil {
				return err
			}
			out = map[string]any{"format": "goo", "path": p, "name": pkg.Name, "version": pkg.Version,
				"arch": pkg.Arch, "pkgspec": pkg.Spec}
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
	cfgPath := fs.String("c", "crossdock.yaml", "config file (for its output directory)")
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
	cfgPath := fs.String("c", "crossdock.yaml", "config file (for its output directory)")
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

func cmdSystemd(args []string) error {
	fs := flag.NewFlagSet("systemd-config", flag.ExitOnError)
	cfgPath := fs.String("c", "/etc/crossdock/crossdock.yaml", "config file")
	root := fs.String("root", "", "write the files under this directory, e.g. / (default: print them)")
	fs.Parse(args)

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	files, err := unitconf.Generate(cfg)
	if err != nil {
		return err
	}
	if *root == "" {
		for _, f := range files {
			fmt.Printf("### %s\n%s\n", f.Path, f.Content)
		}
		return nil
	}
	return unitconf.Write(*root, files)
}

func cmdKeygen(args []string) error {
	fs := flag.NewFlagSet("keygen", flag.ExitOnError)
	uid := fs.String("uid", "", `key user ID, e.g. "jm2 packages <packages@example.lan>" (required)`)
	out := fs.String("out", "signing.asc", "where to write the armored secret key (mode 0600)")
	pub := fs.String("pub", "", "also write the armored public key here")
	passFile := fs.String("passphrase-file", "", "protect the key with the passphrase in this file (default: unprotected)")
	force := fs.Bool("force", false, "overwrite existing files")
	fs.Parse(args)
	if *uid == "" {
		return errors.New("keygen: -uid is required")
	}
	var pass string
	if *passFile != "" {
		b, err := os.ReadFile(*passFile)
		if err != nil {
			return err
		}
		pass = strings.TrimRight(string(b), "\r\n")
	}
	flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
	if *force {
		flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	}
	// Refuse to clobber before spending time generating.
	for _, p := range []string{*out, *pub} {
		if p == "" || *force {
			continue
		}
		if _, err := os.Stat(p); err == nil {
			return fmt.Errorf("keygen: %s exists (use -force to overwrite)", p)
		}
	}
	k, err := sign.Generate(*uid, pass)
	if err != nil {
		return err
	}
	write := func(p string, data []byte, mode os.FileMode) error {
		f, err := os.OpenFile(p, flags, mode)
		if err != nil {
			return err
		}
		if _, err := f.Write(data); err != nil {
			f.Close()
			return err
		}
		return f.Close()
	}
	if err := write(*out, k.Secret, 0o600); err != nil {
		return err
	}
	if *pub != "" {
		if err := write(*pub, k.Public, 0o644); err != nil {
			return err
		}
	}
	fmt.Printf("fingerprint %s\nsecret key  %s\n", k.Fingerprint, *out)
	if *pub != "" {
		fmt.Printf("public key  %s\n", *pub)
	}
	return nil
}
