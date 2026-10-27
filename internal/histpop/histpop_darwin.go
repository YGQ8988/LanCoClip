//go:build darwin && cgo

// Package histpop pops up a native context menu listing recent lancoclip
// entries (for the history hotkey), without opening a browser.
package histpop

/*
#cgo LDFLAGS: -framework Cocoa
#include <stdlib.h>

// Implemented in histpop_darwin.m (compiled by cgo as Objective-C); picks
// call back into Go through the //export-ed histpopPicked below.
// 注意：这里只放 C 原型，不能 include Cocoa 头（cgo 按 C 编译会报错）。
// labels 是所有条目按组拼接后的扁平数组，offsets 是每组在数组中的起止下标
// （长度 groupCount+1）。每组渲染成顶层菜单项 + 子菜单。
int showHistoryMenu(const char **groupTitles, const char **labels,
                    const int *offsets, int groupCount);
*/
import "C"

import (
	"sync"
	"unsafe"
)

// Group is one top-level row of the popup plus the entries revealed when the
// cursor hovers it. Items are numbered by the caller ("1. …").
type Group struct {
	Title string
	Items []string
}

var (
	mu     sync.Mutex
	pickFn func(group, item int)
)

// 每组条目 id 编码为 group*itemIDStep + item + 1，避免嵌套传递。
const itemIDStep = 1000

// histpopPicked is called from the Obj-C menu action (main thread) while the
// popup menu is tracking. The tag carries the encoded group/item id.
//
//export histpopPicked
func histpopPicked(tag C.int) {
	id := int(tag)
	if id <= 0 {
		return
	}
	g := (id - 1) / itemIDStep
	i := (id - 1) % itemIDStep
	mu.Lock()
	fn := pickFn
	mu.Unlock()
	if fn != nil {
		fn(g, i)
	}
}

// Show pops a native cascading menu at the current mouse position: one row per
// group, each opening a submenu with that group's items. onPick receives the
// chosen (group, item) indexes; it runs on the main thread, so it must be
// quick. Show blocks until the menu is dismissed and reports success.
func Show(groups []Group, onPick func(group, item int)) bool {
	if len(groups) == 0 {
		return false
	}
	mu.Lock()
	pickFn = onPick
	mu.Unlock()

	titles := make([]*C.char, len(groups))
	for i, g := range groups {
		cs := C.CString(g.Title)
		defer C.free(unsafe.Pointer(cs))
		titles[i] = cs
	}

	var flat []string
	offsets := make([]C.int, len(groups)+1)
	for i, g := range groups {
		offsets[i] = C.int(len(flat))
		flat = append(flat, g.Items...)
	}
	offsets[len(groups)] = C.int(len(flat))

	labels := make([]*C.char, 0, len(flat))
	for _, s := range flat {
		cs := C.CString(s)
		defer C.free(unsafe.Pointer(cs))
		labels = append(labels, cs)
	}
	// C 侧不接受空指针：没有条目时给一个占位指针（不会被读取）
	if len(labels) == 0 {
		labels = append(labels, C.CString(""))
	}

	status := C.showHistoryMenu(&titles[0], &labels[0], &offsets[0], C.int(len(groups)))
	return status == 0
}
