//go:build linux && (amd64 || arm64)

package main

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"unsafe"

	"github.com/ebitengine/purego"
)

func init() { runtime.LockOSThread() }

// run opens the window and returns when it is closed. GTK and WebKit are
// loaded at run time (purego), so the program builds without cgo.
func run(url, title, icon string, w, h int) error {
	gtk, err := open("libgtk-3.so.0")
	if err != nil {
		return err
	}
	wk, err := open("libwebkit2gtk-4.1.so.0", "libwebkit2gtk-4.0.so.37")
	if err != nil {
		return err
	}
	gobj, err := open("libgobject-2.0.so.0")
	if err != nil {
		return err
	}
	var (
		gtkInitCheck      func(argc, argv uintptr) bool
		gtkWindowNew      func(kind int32) uintptr
		gtkWindowSetTitle func(win uintptr, title string)
		gtkWindowSetSize  func(win uintptr, w, h int32)
		gtkWindowSetIcon  func(win uintptr, name string)
		gtkContainerAdd   func(c, child uintptr)
		gtkWidgetShowAll  func(w uintptr)
		gtkMain           func()
		webViewGetContext func(v uintptr) uintptr
		webViewNew        func() uintptr
		webViewLoadURI    func(v uintptr, uri string)
		signalConnectData func(inst uintptr, signal string, handler, data, destroy uintptr, flags int32) uint64
	)
	purego.RegisterLibFunc(&gtkInitCheck, gtk, "gtk_init_check")
	purego.RegisterLibFunc(&gtkWindowNew, gtk, "gtk_window_new")
	purego.RegisterLibFunc(&gtkWindowSetTitle, gtk, "gtk_window_set_title")
	purego.RegisterLibFunc(&gtkWindowSetSize, gtk, "gtk_window_set_default_size")
	purego.RegisterLibFunc(&gtkWindowSetIcon, gtk, "gtk_window_set_icon_name")
	purego.RegisterLibFunc(&gtkContainerAdd, gtk, "gtk_container_add")
	purego.RegisterLibFunc(&gtkWidgetShowAll, gtk, "gtk_widget_show_all")
	purego.RegisterLibFunc(&gtkMain, gtk, "gtk_main")
	purego.RegisterLibFunc(&webViewNew, wk, "webkit_web_view_new")
	purego.RegisterLibFunc(&webViewLoadURI, wk, "webkit_web_view_load_uri")
	purego.RegisterLibFunc(&signalConnectData, gobj, "g_signal_connect_data")
	purego.RegisterLibFunc(&webViewGetContext, wk, "webkit_web_view_get_context")
	mainQuit, err := purego.Dlsym(gtk, "gtk_main_quit")
	if err != nil {
		return err
	}
	if !gtkInitCheck(0, 0) {
		return errors.New("no display")
	}
	win := gtkWindowNew(0)
	gtkWindowSetTitle(win, title)
	gtkWindowSetSize(win, int32(w), int32(h))
	gtkWindowSetIcon(win, icon)
	view := webViewNew()
	gtkContainerAdd(win, view)
	// gtk_main_quit ignores the arguments a "destroy" handler gets.
	signalConnectData(win, "destroy", mainQuit, 0, 0, 0)
	if err := saveDialogs(gtk, wk, win, webViewGetContext(view), signalConnectData); err != nil {
		fmt.Fprintln(os.Stderr, "herakles-window: downloads go to the downloads folder:", err)
	}
	webViewLoadURI(view, url)
	gtkWidgetShowAll(win)
	gtkMain()
	return nil
}

// saveDialogs asks where to save each download, like a browser does.
func saveDialogs(gtk, wk, win, ctx uintptr, connect func(uintptr, string, uintptr, uintptr, uintptr, int32) uint64) error {
	var (
		chooserNew      func(title string, parent uintptr, action int32, accept, cancel string) uintptr
		setCurrentName  func(chooser uintptr, name string)
		setOverwrite    func(chooser uintptr, on bool)
		dialogRun       func(dialog uintptr) int32
		chooserGetURI   func(chooser uintptr) uintptr
		objectUnref     func(obj uintptr)
		downloadSetDest func(d uintptr, uri uintptr)
		downloadCancel  func(d uintptr)
		gFree           func(p uintptr)
		setFolder       func(chooser uintptr, dir string) bool
		specialDir      func(kind int32) *byte
	)
	glib, err := open("libglib-2.0.so.0")
	if err != nil {
		return err
	}
	gobj, err := open("libgobject-2.0.so.0")
	if err != nil {
		return err
	}
	purego.RegisterLibFunc(&chooserNew, gtk, "gtk_file_chooser_native_new")
	purego.RegisterLibFunc(&setCurrentName, gtk, "gtk_file_chooser_set_current_name")
	purego.RegisterLibFunc(&setOverwrite, gtk, "gtk_file_chooser_set_do_overwrite_confirmation")
	purego.RegisterLibFunc(&dialogRun, gtk, "gtk_native_dialog_run")
	purego.RegisterLibFunc(&chooserGetURI, gtk, "gtk_file_chooser_get_uri")
	purego.RegisterLibFunc(&objectUnref, gobj, "g_object_unref")
	purego.RegisterLibFunc(&downloadSetDest, wk, "webkit_download_set_destination")
	purego.RegisterLibFunc(&downloadCancel, wk, "webkit_download_cancel")
	purego.RegisterLibFunc(&gFree, glib, "g_free")
	purego.RegisterLibFunc(&setFolder, gtk, "gtk_file_chooser_set_current_folder")
	purego.RegisterLibFunc(&specialDir, glib, "g_get_user_special_dir")

	// decide-destination(download, suggested_filename, data) -> gboolean
	decide := purego.NewCallback(func(d uintptr, suggested *byte, _ uintptr) uintptr {
		const actionSave, responseAccept = 1, -3
		ch := chooserNew("Save download", win, actionSave, "_Save", "_Cancel")
		const downloadDir = 2 // G_USER_DIRECTORY_DOWNLOAD
		dir := goString(specialDir(downloadDir))
		if dir == "" {
			dir, _ = os.UserHomeDir()
		}
		if dir != "" {
			setFolder(ch, dir)
		}
		setCurrentName(ch, goString(suggested))
		setOverwrite(ch, true)
		if dialogRun(ch) == responseAccept {
			if uri := chooserGetURI(ch); uri != 0 {
				downloadSetDest(d, uri)
				gFree(uri)
			}
		} else {
			downloadCancel(d)
		}
		objectUnref(ch)
		return 1
	})
	// download-started(context, download, data)
	started := purego.NewCallback(func(_, d, _ uintptr) uintptr {
		connect(d, "decide-destination", decide, 0, 0, 0)
		return 0
	})
	connect(ctx, "download-started", started, 0, 0, 0)
	return nil
}

func goString(p *byte) string {
	if p == nil {
		return ""
	}
	var b []byte
	for q := unsafe.Pointer(p); *(*byte)(q) != 0; q = unsafe.Add(q, 1) {
		b = append(b, *(*byte)(q))
	}
	return string(b)
}

func open(names ...string) (uintptr, error) {
	var last error
	for _, n := range names {
		h, err := purego.Dlopen(n, purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err == nil {
			return h, nil
		}
		last = err
	}
	return 0, fmt.Errorf("%s not installed (%v)", names[0], last)
}
