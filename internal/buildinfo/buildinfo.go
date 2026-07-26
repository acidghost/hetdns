package buildinfo

// Info identifies a built hetdns binary.
type Info struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Date    string `json:"date"`
}

// New returns build information with useful development defaults.
func New(version, commit, date string) Info {
	if version == "" {
		version = "dev"
	}
	if commit == "" {
		commit = "unknown"
	}
	if date == "" {
		date = "unknown"
	}
	return Info{Version: version, Commit: commit, Date: date}
}
