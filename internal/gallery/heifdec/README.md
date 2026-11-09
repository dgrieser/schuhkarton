The gzipped static `heif-dec` that the Linux release builds embed, one per
architecture (`linux_<GOARCH>.gz`, see `heifdec_linux_*.go`). The files in the
repository are empty: the release workflow builds the real ones with
`deploy/heif-dec` before GoReleaser runs. A build with an empty file has no
bundled heif-dec and uses one on the PATH, if any.
