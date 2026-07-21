package version

var (
	version = "dev"
	commit  = "none"
)

// Version returns the build version.
func Version() string {
	return version
}

// Commit returns the source revision associated with the build.
func Commit() string {
	return commit
}
