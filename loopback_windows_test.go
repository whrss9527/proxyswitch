//go:build windows

package main

import (
	"errors"
	"strings"
	"syscall"
	"testing"
)

func TestListLoopbackApps(t *testing.T) {
	info, err := listLoopbackApps()
	if err != nil {
		t.Fatal(err)
	}
	named := 0
	for _, item := range info.Apps {
		if !strings.HasPrefix(strings.ToUpper(item.Sid), appContainerSidPrefix) || item.Package == "" {
			t.Errorf("应用的 SID 或包名不对：%+v", item)
		}
		if item.Name != "" && !strings.HasPrefix(item.Name, "@") {
			named++
		}
	}
	t.Logf("商店应用 %d 个，有名字的 %d 个", len(info.Apps), named)
	if _, err := loopbackExemptSids(); err != nil {
		t.Fatal(err)
	}
}

// 修改回环豁免要管理员权限：CI 以管理员身份运行时加一个应用再恢复。
func TestSetLoopbackExempt(t *testing.T) {
	requireRegistryTests(t)
	info, err := listLoopbackApps()
	if err != nil {
		t.Fatal(err)
	}
	current, err := loopbackExemptSids()
	if err != nil {
		t.Fatal(err)
	}
	var target string
	for _, item := range info.Apps {
		if !item.Exempt {
			target = item.Sid
			break
		}
	}
	if target == "" {
		t.Skip("没有可以用来测试的商店应用")
	}
	if err := setLoopbackExempt(append(append([]string{}, current...), target)); err != nil {
		if errors.Is(err, syscall.ERROR_ACCESS_DENIED) {
			t.Skip("需要管理员权限：", err)
		}
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := setLoopbackExempt(current); err != nil {
			t.Errorf("恢复回环豁免失败：%v", err)
		}
	})
	after, err := loopbackExemptSids()
	if err != nil {
		t.Fatal(err)
	}
	if !sameSids(after, append(append([]string{}, current...), target)) {
		t.Errorf("应加上 %s：%v", target, after)
	}
	if err := setLoopbackExempt([]string{"S-1-5-18"}); err == nil {
		t.Error("不是商店应用的 SID 应拒绝")
	}
}
