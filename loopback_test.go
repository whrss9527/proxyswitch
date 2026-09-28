package main

import (
	"reflect"
	"testing"
)

func TestMergeLoopback(t *testing.T) {
	installed := []LoopbackApp{{Sid: "S-1-15-2-1"}, {Sid: "S-1-15-2-2"}, {Sid: "S-1-15-2-3"}}
	// 已安装的按勾选决定；没安装的（其他工具为别的应用加的）保留；SID 不分大小写。
	current := []string{"S-1-15-2-1", "s-1-15-2-9"}
	got := mergeLoopback(current, installed, []string{"S-1-15-2-2", "S-1-15-2-2", "S-1-15-2-7"})
	if want := []string{"S-1-15-2-2", "s-1-15-2-9"}; !reflect.DeepEqual(got, want) {
		t.Errorf("合并结果不对：%v", got)
	}
	if !sameSids([]string{"s-1-15-2-9", "S-1-15-2-2"}, got) || sameSids(got, current) || sameSids([]string{"S-1-15-2-1", "S-1-15-2-1"}, []string{"S-1-15-2-1", "S-1-15-2-2"}) {
		t.Error("比较两组 SID 时不分大小写和顺序")
	}
	if got := mergeLoopback(nil, installed, nil); len(got) != 0 {
		t.Errorf("都不勾选时应没有豁免：%v", got)
	}

	apps := []LoopbackApp{{Name: "xbox", Package: "b"}, {Name: "Netflix", Package: "a"}, {Name: "Xbox", Package: "a"}}
	sortLoopbackApps(apps)
	if apps[0].Name != "Netflix" || apps[1].Package != "a" || apps[2].Package != "b" {
		t.Errorf("应按名字（不分大小写）再按包名排序：%+v", apps)
	}
}
