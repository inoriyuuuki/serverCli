// Command servercli-release-sync mirrors a trusted GitHub release into OSS.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"servercli/internal/oss"
	"servercli/internal/releasecache"
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "servercli-release-sync:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if err := releasecache.ValidateCredentialFreeArgs(args); err != nil {
		return err
	}
	if len(args) == 0 {
		printUsage(stderr)
		return errors.New("a plan or apply subcommand is required")
	}
	switch args[0] {
	case "plan":
		return runPlan(ctx, args[1:], stdout, stderr)
	case "apply":
		return runApply(ctx, args[1:], stdout, stderr)
	case "help", "-h", "--help":
		printUsage(stdout)
		return nil
	default:
		printUsage(stderr)
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
}

func runPlan(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("plan", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var common commonFlags
	bindCommonFlags(fs, &common)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("plan: unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	client := newGitHubClient(os.Getenv("GITHUB_API_URL"), os.Getenv("GITHUB_TOKEN"), common.timeout)
	plan, err := releasecache.PlanSync(ctx, common.options(client))
	if err != nil {
		return err
	}
	return writeJSON(stdout, plan)
}

func runApply(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("apply", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var common commonFlags
	bindCommonFlags(fs, &common)
	var endpoint, internalEndpoint, bucket, region, credentialFile string
	var preferInternal, force bool
	fs.StringVar(&endpoint, "oss-endpoint", "", "OSS public endpoint")
	fs.StringVar(&internalEndpoint, "oss-internal-endpoint", "", "optional OSS internal endpoint")
	fs.StringVar(&bucket, "oss-bucket", "", "OSS bucket")
	fs.StringVar(&region, "oss-region", "", "OSS region")
	fs.StringVar(&credentialFile, "oss-ak-file", "", "0600 credential file (JSON or KEY=VALUE format)")
	fs.BoolVar(&preferInternal, "oss-prefer-internal", false, "prefer the internal OSS endpoint")
	fs.BoolVar(&force, "force", false, "replace an already verified release")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("apply: unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	if endpoint == "" || bucket == "" {
		return errors.New("apply: --oss-endpoint and --oss-bucket are required")
	}
	credentials, err := loadOSSCredentials(credentialFile)
	if err != nil {
		return err
	}
	cfg := oss.Config{
		Endpoint: endpoint, InternalEndpoint: internalEndpoint, Bucket: bucket, Region: region,
		AccessKeyID: credentials.AccessKeyID, AccessKeySecret: credentials.AccessKeySecret,
		PreferInternal: preferInternal, Timeout: common.timeout, UserAgent: "servercli-release-sync/1",
	}
	provider, err := newHTTPOSSProvider(cfg)
	if err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	client := newGitHubClient(os.Getenv("GITHUB_API_URL"), os.Getenv("GITHUB_TOKEN"), common.timeout)
	opts := common.options(client)
	opts.OSS = provider
	opts.Force = force
	opts.Log = log
	result, err := releasecache.ApplySync(ctx, opts)
	if err != nil {
		return err
	}
	return writeJSON(stdout, result)
}

type commonFlags struct {
	owner, repo, tag, version, ossBaseKey string
	modulesVersion, schemaMin, schemaMax  string
	osName, arch                          string
	timeout                               time.Duration
}

func bindCommonFlags(fs *flag.FlagSet, common *commonFlags) {
	fs.StringVar(&common.owner, "owner", "", "GitHub repository owner")
	fs.StringVar(&common.repo, "repo", "", "GitHub repository name")
	fs.StringVar(&common.tag, "tag", "", "GitHub release tag")
	fs.StringVar(&common.version, "version", "", "cache version (defaults to tag)")
	fs.StringVar(&common.ossBaseKey, "oss-base-key", "servercli/releases", "OSS release cache prefix")
	fs.StringVar(&common.modulesVersion, "modules-version", "", "modules version recorded in the cache manifest")
	fs.StringVar(&common.schemaMin, "schema-min", "", "minimum compatible schema version")
	fs.StringVar(&common.schemaMax, "schema-max", "", "maximum compatible schema version")
	fs.StringVar(&common.osName, "os", "", "artifact operating system")
	fs.StringVar(&common.arch, "arch", "", "artifact architecture")
	fs.DurationVar(&common.timeout, "timeout", 10*time.Minute, "overall operation timeout")
}

func (common commonFlags) options(client releasecache.GitHubReleaseClient) releasecache.SyncOptions {
	return releasecache.SyncOptions{
		Version: common.version, Owner: common.owner, Repo: common.repo, Tag: common.tag,
		GitHub: client, OSSBaseKey: common.ossBaseKey, ModulesVersion: common.modulesVersion,
		Schema:  releasecache.SchemaCompatInfo{Min: common.schemaMin, Max: common.schemaMax},
		Timeout: common.timeout, OS: common.osName, Arch: common.arch,
	}
}

func writeJSON(w io.Writer, value any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(value)
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, `Usage:
  servercli-release-sync plan  --owner X --repo Y --tag Z [--version V]
  servercli-release-sync apply --owner X --repo Y --tag Z --oss-endpoint URL --oss-bucket NAME [options]

Credentials are never accepted as command-line values:
  GitHub: GITHUB_TOKEN
  OSS:    OSS_ACCESS_KEY_ID and OSS_ACCESS_KEY_SECRET, or --oss-ak-file pointing to a 0600 file`)
}
