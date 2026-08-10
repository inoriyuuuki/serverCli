// Command servercli is the database-free bootstrap/ops CLI. It runs on a fresh
// CentOS/RHEL server before PostgreSQL, Docker, Gitea or a Control Plane
// exist, driving the init wizard, bundle import, module provisioning, and the
// update/backup/restore compatibility entrypoints.
package main

import (
	"os"

	"servercli/internal/cli"
)

// version/build/commit are overridden at build time via -X main.version=...
var (
	version = "0.1.0"
	build   = "dev"
	commit  = "unknown"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr, cli.VersionInfo{
		Version: version,
		Build:   build,
		Commit:  commit,
	}))
}
