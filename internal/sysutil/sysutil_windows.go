package sysutil

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// adminSDDL: owned by Administrators, only LocalSystem and Administrators
// get in, nothing inherited from the parent.
const adminSDDL = "O:BAD:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"

func setSDDL(path, sddl string) error {
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return err
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		owner, nil, dacl, nil)
}

// SecureDir restricts a directory and what it inherits to LocalSystem and
// Administrators, so the configuration with its passwords is not readable
// by ordinary users (C:\ProgramData lets every user read by default).
func SecureDir(path string) error { return setSDDL(path, adminSDDL) }

// SecureFile restricts a file to LocalSystem, Administrators and, read
// only, the user with SID reader.
func SecureFile(path, reader string) error {
	if _, err := windows.StringToSid(reader); err != nil {
		return fmt.Errorf("bad SID %q: %w", reader, err)
	}
	return setSDDL(path, "O:BAD:P(A;;FA;;;SY)(A;;FA;;;BA)(A;;FR;;;"+reader+")")
}

// Elevated reports whether the process runs with administrator rights
// (elevated, or as LocalSystem).
func Elevated() bool { return windows.GetCurrentProcessToken().IsElevated() }

// SelfID is the current user's SID; LocalSystem has none.
func SelfID() (string, bool) {
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", false
	}
	sid := u.User.Sid.String()
	return sid, sid != SystemSID
}

var (
	wtsapi32                        = windows.NewLazySystemDLL("wtsapi32.dll")
	procWTSQuerySessionInformationW = wtsapi32.NewProc("WTSQuerySessionInformationW")
)

func sessionString(class uint32) (string, error) {
	var buf *uint16
	var n uint32
	const currentSession = 0xFFFFFFFF
	r, _, err := procWTSQuerySessionInformationW.Call(0, currentSession, uintptr(class), uintptr(unsafe.Pointer(&buf)), uintptr(unsafe.Pointer(&n)))
	if r == 0 {
		return "", err
	}
	defer windows.WTSFreeMemory(uintptr(unsafe.Pointer(buf)))
	return windows.UTF16PtrToString(buf), nil
}

// ConsoleUser returns DOMAIN\name of the user logged in to this session,
// also when the process was elevated with another administrator's
// password.
func ConsoleUser() (string, error) {
	const wtsUserName, wtsDomainName = 5, 7
	name, err := sessionString(wtsUserName)
	if err != nil {
		return "", err
	}
	if name == "" {
		return "", errors.New("nobody is logged in to this session")
	}
	if dom, err := sessionString(wtsDomainName); err == nil && dom != "" {
		return dom + `\` + name, nil
	}
	return name, nil
}

// ProcessAlive reports whether pid runs.
func ProcessAlive(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var code uint32
	const stillActive = 259
	return windows.GetExitCodeProcess(h, &code) == nil && code == stillActive
}

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`

// SetUserRun sets (or with an empty command removes) the value name in
// the Run key of the logged-in user with SID sid, so it starts at their
// login. The user's registry must be loaded, which it is while they are
// logged in.
func SetUserRun(sid, name, command string) error {
	k, _, err := registry.CreateKey(registry.USERS, sid+`\`+runKey, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("registry of %s: %w", sid, err)
	}
	defer k.Close()
	if command == "" {
		if err := k.DeleteValue(name); err != nil && !errors.Is(err, registry.ErrNotExist) {
			return err
		}
		return nil
	}
	return k.SetStringValue(name, command)
}

// RemoveRunEverywhere removes the Run value name from every loaded user
// registry where it contains match (case-insensitive) and returns the
// SIDs it was removed for.
func RemoveRunEverywhere(name, match string) []string {
	k, err := registry.OpenKey(registry.USERS, "", registry.ENUMERATE_SUB_KEYS)
	if err != nil {
		return nil
	}
	sids, _ := k.ReadSubKeyNames(-1)
	k.Close()
	var done []string
	for _, sid := range sids {
		rk, err := registry.OpenKey(registry.USERS, sid+`\`+runKey, registry.QUERY_VALUE|registry.SET_VALUE)
		if err != nil {
			continue
		}
		v, _, err := rk.GetStringValue(name)
		if err == nil && strings.Contains(strings.ToLower(v), strings.ToLower(match)) && rk.DeleteValue(name) == nil {
			done = append(done, sid)
		}
		rk.Close()
	}
	return done
}

// UserAppData returns the roaming application data folder of the user
// with SID sid (C:\Users\NAME\AppData\Roaming).
func UserAppData(sid string) (string, error) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows NT\CurrentVersion\ProfileList\`+sid, registry.QUERY_VALUE)
	if err != nil {
		return "", fmt.Errorf("no profile for %s: %w", sid, err)
	}
	defer k.Close()
	profile, _, err := k.GetStringValue("ProfileImagePath")
	if err != nil {
		return "", err
	}
	profile, _ = registry.ExpandString(profile)
	if uk, err := registry.OpenKey(registry.USERS, sid+`\Software\Microsoft\Windows\CurrentVersion\Explorer\User Shell Folders`, registry.QUERY_VALUE); err == nil {
		defer uk.Close()
		if v, _, err := uk.GetStringValue("AppData"); err == nil && v != "" {
			return strings.ReplaceAll(strings.ReplaceAll(v, "%USERPROFILE%", profile), "%userprofile%", profile), nil
		}
	}
	return filepath.Join(profile, "AppData", "Roaming"), nil
}

const envKey = `SYSTEM\CurrentControlSet\Control\Session Manager\Environment`

// SystemPath adds dir to (add) or removes it from the system-wide PATH
// and tells running programs about it.
func SystemPath(dir string, add bool) error {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, envKey, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	cur, typ, err := k.GetStringValue("Path")
	if err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	if typ == 0 {
		typ = registry.EXPAND_SZ
	}
	var parts []string
	found := false
	for _, p := range strings.Split(cur, ";") {
		if p == "" {
			continue
		}
		if strings.EqualFold(strings.TrimRight(p, `\`), strings.TrimRight(dir, `\`)) {
			found = true
			if !add {
				continue
			}
		}
		parts = append(parts, p)
	}
	if add == found {
		return nil
	}
	if add {
		parts = append(parts, dir)
	}
	v := strings.Join(parts, ";")
	if typ == registry.EXPAND_SZ {
		err = k.SetExpandStringValue("Path", v)
	} else {
		err = k.SetStringValue("Path", v)
	}
	if err != nil {
		return err
	}
	env, _ := syscall.UTF16PtrFromString("Environment")
	const hwndBroadcast, wmSettingChange, smtoAbortIfHung = 0xffff, 0x1A, 0x2
	user32 := windows.NewLazySystemDLL("user32.dll")
	_, _, _ = user32.NewProc("SendMessageTimeoutW").Call(hwndBroadcast, wmSettingChange, 0, uintptr(unsafe.Pointer(env)), smtoAbortIfHung, 2000, 0)
	return nil
}

// ProgramData is the machine-wide data folder (C:\ProgramData), the same
// for every account (the environment variable may differ for services).
func ProgramData() string {
	if pd, err := windows.KnownFolderPath(windows.FOLDERID_ProgramData, 0); err == nil && pd != "" {
		return pd
	}
	if pd := os.Getenv("ProgramData"); pd != "" {
		return pd
	}
	return `C:\ProgramData`
}
