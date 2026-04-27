package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/mongodb/mongodb-kubernetes/internal/ci/release"
)

const buildInfoPath = "build_info.json"

func newCopyReleaseDockerfilesCmd() *cobra.Command {
	var (
		version string
		dest    string
		dryRun  bool
	)
	cmd := &cobra.Command{
		Use:   "copy-release-dockerfiles",
		Short: "Copy release Dockerfiles into <dest>/<image>/<version>/ubi/",
		Long: `Reads build_info.json for each release image's source Dockerfile and copies
each into the public dir layout used for releases:

  <dest>/<image>/<version>/ubi/Dockerfile

By default <dest> is public/dockerfiles. The init-database Dockerfile is
copied to both mongodb-kubernetes-init-database and mongodb-kubernetes-init-appdb.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runCopyReleaseDockerfiles(cmd, version, dest, dryRun)
		},
	}
	cmd.Flags().StringVar(&version, "version", "", "release version (required), e.g. 1.8.1")
	cmd.Flags().StringVar(&dest, "dest", "public/dockerfiles", "target root directory")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print actions without writing files")
	_ = cmd.MarkFlagRequired("version")
	return cmd
}

func runCopyReleaseDockerfiles(cmd *cobra.Command, version, dest string, dryRun bool) error {
	bi, err := release.ReadBuildInfo(buildInfoPath)
	if err != nil {
		return err
	}
	plan, err := release.PlanDockerfileCopies(bi, version, dest)
	if err != nil {
		return err
	}

	out := cmd.ErrOrStderr()
	prefix := "→ copy"
	if dryRun {
		prefix = "[dry-run] would copy"
	}
	for _, p := range plan {
		fmt.Fprintf(out, "%s %s -> %s\n", prefix, p.Src, p.Dst)
	}

	if !dryRun {
		if err := release.CopyDockerfiles(plan); err != nil {
			return err
		}
	}

	verb := "copied"
	if dryRun {
		verb = "would copy"
	}
	fmt.Fprintf(out, "→ %s %d Dockerfile(s) for version %s\n", verb, len(plan), version)
	return nil
}
