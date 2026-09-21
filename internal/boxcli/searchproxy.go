// Copyright 2024 Jetify Inc. and contributors. All rights reserved.
// Use of this source code is governed by the license in the LICENSE file.

package boxcli

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/MakeNowJust/heredoc/v2"
	"github.com/spf13/cobra"

	"go.jetify.com/devbox/internal/boxcli/usererr"
	"go.jetify.com/devbox/internal/nxv"
	"go.jetify.com/devbox/internal/nxvops"
	"go.jetify.com/devbox/internal/searchproxy"
	"go.jetify.com/devbox/internal/ux"
)

type searchProxyFlags struct {
	listen      string
	index       string
	nxv         string
	installable string
	nxvDB       string
	systems     []string
	evalSystems bool
}

func searchProxyCmd() *cobra.Command {
	flags := searchProxyFlags{}
	searchProxyCommand := &cobra.Command{
		Use:   "search-proxy",
		Short: "Serve the Devbox search API from an nxv version index",
		Long: heredoc.Doc(`
			Serve the Devbox search API from a self-hosted nxv version index.

			'serve' answers the search API; point Devbox at it by setting
			DEVBOX_SEARCH_HOST. 'index' maintains the index it reads.
		`),
	}

	serveCommand := &cobra.Command{
		Use:   "serve",
		Short: "Answer the Devbox search API from an nxv version index",
		Long: heredoc.Doc(`
			Answer the Devbox search API from an nxv version index.

			Point Devbox at the address this serves on by setting DEVBOX_SEARCH_HOST,
			and keep the index it reads fresh with 'devbox search-proxy index refresh'.

			An nxv server is started and supervised for the lifetime of this command,
			built with nix, so nxv does not need to be installed. Pass --index to use
			an nxv that is already running instead.
		`),
		Example: "\n  devbox search-proxy serve --listen 127.0.0.1:8099",
		Args:    cobra.ExactArgs(0),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSearchProxy(cmd, flags)
		},
	}
	serveCommand.Flags().StringVar(&flags.listen, "listen", "127.0.0.1:8099",
		"address to serve the Devbox search API on")
	serveCommand.Flags().StringVar(&flags.index, "index", "",
		"base URL of an already-running nxv server; one is started when empty")
	serveCommand.Flags().StringVar(&flags.nxv, "nxv", "",
		"path to an nxv executable; built from --nxv-installable when empty")
	serveCommand.Flags().StringVar(&flags.installable, "nxv-installable",
		nxvops.DefaultInstallable, "nix installable to build nxv from")
	serveCommand.Flags().StringVar(&flags.nxvDB, "nxv-db", "",
		"index database for the started nxv; uses nxv's own default when empty")
	serveCommand.Flags().StringSliceVar(&flags.systems, "systems",
		searchproxy.DefaultSystems,
		"Nix systems to report; pass an empty list to report every system nixpkgs lists")
	serveCommand.Flags().BoolVar(&flags.evalSystems, "eval-systems", true,
		"resolve the version each system actually gets, evaluating nixpkgs for a "+
			"commit the index has not seen before")

	searchProxyCommand.AddCommand(serveCommand)
	searchProxyCommand.AddCommand(searchProxyIndexCmd())

	return searchProxyCommand
}

// runSearchProxy serves the Devbox search API, starting an nxv to read from
// unless one was named with --index.
func runSearchProxy(cmd *cobra.Command, flags searchProxyFlags) error {
	// The nxv child process outlives this command if a signal kills it before
	// the deferred Stop runs, so handle interrupts rather than dying on them.
	ctx, stopSignals := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	index := flags.index
	if index == "" {
		binary, err := nxvops.ResolveBinary(ctx, flags.nxv, flags.installable)
		if err != nil {
			return usererr.WithUserMessage(err, "Could not get an nxv to serve from")
		}
		ux.Finfof(cmd.ErrOrStderr(), "Starting nxv to read the index from\n")
		nxvServer, err := nxvops.StartServer(ctx, binary, flags.nxvDB, flags.evalSystems)
		if err != nil {
			// Usually a missing or unreadable index rather than a devbox bug, so
			// say so instead of asking for a bug report.
			return usererr.WithUserMessage(err,
				"Could not start nxv. Bootstrap an index with `nxv sync` first, "+
					"or pass --nxv-db to point at one.")
		}
		defer func() { _ = nxvServer.Stop() }()
		index = nxvServer.URL
	}

	server := &http.Server{
		Addr:              flags.listen,
		Handler:           searchproxy.NewHandler(nxv.NewClient(index), flags.systems),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	slog.Info("serving the devbox search API", "listen", flags.listen, "index", index)
	if err := server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

type searchProxyIndexFlags struct {
	nxv           string
	installable   string
	nxvDB         string
	report        string
	manifestURL   string
	publicKey     string
	output        string
	urlPrefix     string
	force         bool
	backfillEvals bool
	maxLag        time.Duration
}

// registerNxvFlags adds the flags every index subcommand needs to get hold of
// an nxv and an index.
func registerNxvFlags(command *cobra.Command, flags *searchProxyIndexFlags, dbUsage string) {
	command.Flags().StringVar(&flags.nxv, "nxv", "",
		"path to an nxv executable; built from --nxv-installable when empty")
	command.Flags().StringVar(&flags.installable, "nxv-installable",
		nxvops.DefaultInstallable, "nix installable to build nxv from")
	command.Flags().StringVar(&flags.nxvDB, "nxv-db", "", dbUsage)
}

func searchProxyIndexCmd() *cobra.Command {
	flags := searchProxyIndexFlags{}
	command := &cobra.Command{
		Use:   "index",
		Short: "Maintain the nxv version index the proxy reads",
		Long: heredoc.Doc(`
			Maintain the nxv version index that 'devbox search-proxy serve' reads.

			Bootstrap the index once with 'nxv sync', then keep it fresh with
			'refresh'. Never run a re-planning ingest against a bootstrapped index:
			its pre-2020 releases cannot settle, so the index stays unhealthy.
		`),
	}

	refreshCommand := &cobra.Command{
		Use:   "refresh [-- <nxv args>...]",
		Short: "Ingest new nixpkgs channel releases into the index",
		Long: heredoc.Doc(`
			Run an incremental nxv ingest over the configured channels.

			Everything after '--' is passed verbatim to nxv. Re-planning flags are
			refused: they re-queue every known release, and a bootstrapped index can
			never settle them.

			nxv itself does not need to be installed: without --nxv it is built from
			--nxv-installable with nix. That installable is the indexer variant,
			because nxv's own release binaries are built without the feature its
			index subcommand needs.
		`),
		Example: "\n  devbox search-proxy index refresh -- --database /var/lib/nxv/index.db " +
			"--report /var/lib/nxv/last-report.json",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			binary, err := nxvops.ResolveBinary(cmd.Context(), flags.nxv, flags.installable)
			if err != nil {
				return err
			}
			if err := nxvops.Refresh(cmd.Context(), binary, flags.nxvDB, args); err != nil {
				return usererr.WithUserMessage(err, "Refreshing the nxv index failed")
			}
			return nil
		},
	}

	healthCommand := &cobra.Command{
		Use:   "health --report <path>",
		Short: "Fail when the index is unhealthy or its head lag is too large",
		Long: heredoc.Doc(`
			Read the report 'refresh' wrote and fail when the index is unhealthy or its
			head lag is beyond --max-lag.

			It reads a report rather than producing one: 'nxv index' ingests, so a health
			check that ran it would mutate the index it is meant to observe. Have refresh
			write the report by passing '--report <path>' through to nxv.
		`),
		Example: "\n  devbox search-proxy index health --report /var/lib/nxv/last-report.json --max-lag 24h",
		Args:    cobra.ExactArgs(0),
		RunE: func(cmd *cobra.Command, args []string) error {
			return nxvops.CheckHealthFile(flags.report, flags.maxLag)
		},
	}
	healthCommand.Flags().StringVar(&flags.report, "report", "",
		"path of the report nxv index wrote")
	healthCommand.Flags().DurationVar(&flags.maxLag, "max-lag", 24*time.Hour,
		"head lag the health check fails beyond")
	_ = healthCommand.MarkFlagRequired("report")

	registerNxvFlags(refreshCommand, &flags,
		"index database to refresh; uses nxv's own default when empty")

	syncCommand := &cobra.Command{
		Use:   "sync [-- <nxv args>...]",
		Short: "Download a prebuilt index, replacing the local one",
		Long: heredoc.Doc(`
			Download a prebuilt index and write it over the local one.

			This is how an index is bootstrapped, and how one is started over:
			building the same coverage with 'refresh' takes hours, most of it
			re-ingesting channel releases that will never change again. Follow it with
			'refresh' to catch up on anything published since the artifact was built.
		`),
		Example: "\n  devbox search-proxy index sync --nxv-db /var/lib/nxv/index.db --force",
		Args:    cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			binary, err := nxvops.ResolveBinary(cmd.Context(), flags.nxv, flags.installable)
			if err != nil {
				return usererr.WithUserMessage(err, "Could not get an nxv to sync with")
			}
			if flags.manifestURL != "" {
				args = append([]string{"--manifest-url", flags.manifestURL}, args...)
			}
			if flags.publicKey != "" {
				args = append([]string{"--public-key", flags.publicKey}, args...)
			}
			if err := nxvops.Sync(cmd.Context(), binary, flags.nxvDB, flags.force, args); err != nil {
				return usererr.WithUserMessage(err, "Downloading the nxv index failed")
			}
			return nil
		},
	}
	registerNxvFlags(syncCommand, &flags,
		"index database to write; uses nxv's own default when empty")
	syncCommand.Flags().BoolVar(&flags.force, "force", false,
		"replace the index even when it is already current")
	syncCommand.Flags().StringVar(&flags.manifestURL, "manifest-url", "",
		"manifest to download from; uses nxv's published index when empty")
	syncCommand.Flags().StringVar(&flags.publicKey, "public-key", "",
		"minisign public key the manifest is verified against")

	buildCommand := &cobra.Command{
		Use:   "build --nxv-db <path> [-- <nxv args>...]",
		Short: "Ingest every channel release into a new index",
		Long: heredoc.Doc(`
			Build an index from nothing, ingesting every channel release.

			This takes hours: 'sync' downloads the same coverage in under a minute, and
			most of what this re-ingests are channel releases that will never change
			again. Build when the point is to owe nothing to a published artifact.

			It refuses to run against an existing index rather than deleting one --
			remove it yourself, or build somewhere else. --backfill-evals adds the
			pre-2020 era, which needs nix and takes hours more.
		`),
		Example: "\n  devbox search-proxy index build --nxv-db /var/lib/nxv/index.db",
		Args:    cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			binary, err := nxvops.ResolveBinary(cmd.Context(), flags.nxv, flags.installable)
			if err != nil {
				return usererr.WithUserMessage(err, "Could not get an nxv to build with")
			}
			if err := nxvops.Build(
				cmd.Context(), binary, flags.nxvDB, flags.backfillEvals, args,
			); err != nil {
				return usererr.WithUserMessage(err, "Building the nxv index failed")
			}
			return nil
		},
	}
	registerNxvFlags(buildCommand, &flags, "index database to create")
	buildCommand.Flags().BoolVar(&flags.backfillEvals, "backfill-evals", false,
		"also ingest the pre-2020 nix-env era (needs nix; hours more)")

	publishCommand := &cobra.Command{
		Use:   "publish --output <dir> [-- <nxv args>...]",
		Short: "Write the artifacts that sync downloads",
		Long: heredoc.Doc(`
			Write the compressed index, bloom filter and manifest that 'sync' downloads.

			Upload the output somewhere 'sync --manifest-url' can reach to keep a
			recovery copy of an index, so a replacement proxy is a sync away rather
			than a rebuild. Pass '-- --sign --secret-key <key>' to sign the manifest,
			and give 'sync' the matching --public-key.
		`),
		Example: "\n  devbox search-proxy index publish --output ./dist " +
			"--url-prefix https://example.s3.amazonaws.com/nxv",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			binary, err := nxvops.ResolveBinary(cmd.Context(), flags.nxv, flags.installable)
			if err != nil {
				return usererr.WithUserMessage(err, "Could not get an nxv to publish with")
			}
			if err := nxvops.Publish(cmd.Context(), binary, flags.nxvDB,
				flags.output, flags.urlPrefix, args); err != nil {
				return usererr.WithUserMessage(err, "Publishing the nxv index failed")
			}
			return nil
		},
	}
	registerNxvFlags(publishCommand, &flags, "index database to publish")
	publishCommand.Flags().StringVarP(&flags.output, "output", "o", "",
		"directory to write the artifacts into")
	publishCommand.Flags().StringVar(&flags.urlPrefix, "url-prefix", "",
		"base URL the artifacts will be served from")
	_ = publishCommand.MarkFlagRequired("output")

	command.AddCommand(buildCommand)
	command.AddCommand(publishCommand)
	command.AddCommand(syncCommand)
	command.AddCommand(refreshCommand)
	command.AddCommand(healthCommand)

	return command
}
