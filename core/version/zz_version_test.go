package version

import (
	"runtime/debug"
	"testing"
)

// 发布标签不能覆盖源码身份；缺少 VCS 信息也不能显示成已知 clean。
// Cover linker/module precedence independently from revision and dirty evidence.
func TestSoftwareStampPreservesBuildProvenance(t *testing.T) {
	t.Parallel()
	info := &debug.BuildInfo{Main: debug.Module{Version: "v9.0.0"}, Settings: []debug.BuildSetting{
		{Key: "vcs.revision", Value: "actual-source-revision"}, {Key: "vcs.modified", Value: "true"},
	}}
	for _, stamp := range []string{"1.0.0", "1.0.0-rc.1"} {
		got := fromBuildInfo(stamp, info)
		if got.Version != stamp || got.Revision != "actual-source-revision" || got.Modified != "true" {
			t.Fatalf("software stamp erased provenance: %+v", got)
		}
	}
	if got := fromBuildInfo("devel", info); got.Version != "v9.0.0" || got.Revision != "actual-source-revision" {
		t.Fatalf("module build identity lost: %+v", got)
	}
	for _, mainVersion := range []string{"", "(devel)"} {
		got := fromBuildInfo("devel", &debug.BuildInfo{Main: debug.Module{Version: mainVersion}})
		if got.Version != "devel" || got.Revision != "" || got.Modified != "unknown" {
			t.Fatalf("source build claimed unsupported identity: %+v", got)
		}
	}
	if got := fromBuildInfo("", nil); got.Version != "devel" || got.Revision != "" || got.Modified != "unknown" {
		t.Fatalf("missing build info invented provenance: %+v", got)
	}
	if got := fromBuildInfo("1.0.0", &debug.BuildInfo{Settings: []debug.BuildSetting{{Key: "vcs.modified", Value: "invalid"}}}); got.Modified != "unknown" {
		t.Fatalf("invalid dirty setting treated as known: %+v", got)
	}
}
