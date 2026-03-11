package version

// These variables are overridden at build time via -ldflags.
var (
	Version     = "dev"
	Commit      = "dev"
	ShortCommit = "dev"
	BuildDate   = ""
)
