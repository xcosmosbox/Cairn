// Package version 统一软件版本展示；数据格式和 MCP 协议版本各自独立。
// Software identity does not replace VCS provenance, Bundle format or protocol versions.
package version

import (
	"fmt"
	"runtime/debug"
)

// Version 可由 -ldflags '-X github.com/xcosmosbox/cairn/core/version.Version=1.0.0' 注入。
// Unstamped source builds remain development/module builds, never an assumed release.
var Version = "devel"

// Build 同时保留软件标签与实际源码身份；未知 dirty 状态不伪装成 clean。
// Modified is true, false or unknown, independently of the software version stamp.
type Build struct {
	Version  string
	Revision string
	Modified string
}

// Info 返回当前二进制的编译身份，不读取配置、数据库或环境变量。
// Info reads immutable binary build metadata without external side effects.
func Info() Build {
	info, _ := debug.ReadBuildInfo()
	return fromBuildInfo(Version, info)
}

func fromBuildInfo(stamp string, info *debug.BuildInfo) Build {
	build := Build{Version: "devel", Modified: "unknown"}
	if stamp != "" && stamp != "devel" {
		build.Version = stamp
	} else if info != nil && info.Main.Version != "" && info.Main.Version != "(devel)" {
		build.Version = info.Main.Version
	}
	if info != nil {
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				build.Revision = setting.Value
			case "vcs.modified":
				if setting.Value == "true" || setting.Value == "false" {
					build.Modified = setting.Value
				}
			}
		}
	}
	return build
}

// Current 是软件版本；不能用于推断 schema、协议或 reader 兼容性。
func Current() string { return Info().Version }

// Summary 给无数据库的 --version 输出保留 revision/dirty 证据。
func Summary(program string) string {
	build := Info()
	revision := build.Revision
	if revision == "" {
		revision = "unknown"
	}
	return fmt.Sprintf("%s %s (revision=%s, modified=%s)", program, build.Version, revision, build.Modified)
}
