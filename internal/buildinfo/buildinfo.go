package buildinfo

import (
	"runtime/debug"
	"strings"
)

var (
	revision = ""
	modified = ""
	buildTime = ""
)

type Info struct {
	Revision  string
	Dirty     bool
	BuildTime string
}

func Current() Info {
	info := Info{
		Revision:  revision,
		Dirty:     parseModified(modified),
		BuildTime: buildTime,
	}

	if info.Revision != "" {
		return normalize(info)
	}

	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return normalize(info)
	}
	for _, setting := range bi.Settings {
		switch setting.Key {
		case "vcs.revision":
			if info.Revision == "" {
				info.Revision = setting.Value
			}
		case "vcs.modified":
			if modified == "" {
				info.Dirty = parseModified(setting.Value)
			}
		case "vcs.time":
			if info.BuildTime == "" {
				info.BuildTime = setting.Value
			}
		}
	}

	return normalize(info)
}

func (i Info) TreeState() string {
	if i.Dirty {
		return "dirty"
	}
	return "clean"
}

func parseModified(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "dirty":
		return true
	default:
		return false
	}
}

func normalize(info Info) Info {
	if info.Revision == "" {
		info.Revision = "unknown"
	}
	return info
}
