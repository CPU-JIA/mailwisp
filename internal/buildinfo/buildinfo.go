// Package buildinfo exposes immutable identity embedded in a MailWisp binary.
package buildinfo

const name = "MailWisp"

var (
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"
)

// Info describes the source and release identity of a MailWisp binary.
type Info struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildDate string `json:"build_date"`
}

// Current returns the identity embedded by the release build.
func Current() Info {
	return Info{
		Name:      name,
		Version:   version,
		Commit:    commit,
		BuildDate: buildDate,
	}
}
