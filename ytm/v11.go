package ytm

import (
	"github.com/godbus/dbus/v5"
)

// linuxV11Key 读取 Linux Secret Service（keyring）中 Chrome v11 加密密钥。
//
// Chromium 的 v11 格式把密码存在 Secret Service（schema
// chrome_libsecret_os_crypt_password_v1，item 属性 application =
// "<浏览器> Safe Storage"）；本机无 v11 cookie，此路径未实测，
// 任何失败均返回 nil（调用方降级 empty key）。
func linuxV11Key(browser string) []byte {
	bp, ok := browserDirMap[browser]
	if !ok {
		return nil
	}
	service := bp.keychain + " Safe Storage"
	conn, err := dbus.SessionBus()
	if err != nil {
		return nil
	}
	defer conn.Close()

	const secretService = "org.freedesktop.secrets"
	const secretPath = "/org/freedesktop/secrets"
	svc := conn.Object(secretService, secretPath)

	// 1. 打开明文会话（plain 算法，无加密握手）
	var session dbus.ObjectPath
	call := svc.Call("org.freedesktop.Secret.Service.OpenSession", 0, "plain", dbus.MakeVariant(""))
	if call.Err != nil {
		return nil
	}
	var output dbus.Variant
	if err := call.Store(&output, &session); err != nil || session == "" {
		return nil
	}

	// 2. 定位默认 collection 并搜索 Chrome 项
	var collection dbus.ObjectPath
	if call := svc.Call("org.freedesktop.Secret.Service.ReadAlias", 0, "default"); call.Err == nil {
		_ = call.Store(&collection)
	}
	var items, locked []dbus.ObjectPath
	attrs := map[string]dbus.Variant{
		"application": dbus.MakeVariant(service),
		"xdg:schema":  dbus.MakeVariant("chrome_libsecret_os_crypt_password_v1"),
	}
	call = svc.Call("org.freedesktop.Secret.Service.SearchItems", 0, attrs)
	if call.Err == nil {
		_ = call.Store(&items, &locked)
	}
	// 放宽条件再试一次（部分 keyring 后端不写 xdg:schema）
	if len(items) == 0 {
		attrs2 := map[string]dbus.Variant{"application": dbus.MakeVariant(service)}
		if call := svc.Call("org.freedesktop.Secret.Service.SearchItems", 0, attrs2); call.Err == nil {
			_ = call.Store(&items, &locked)
		}
	}

	// 3. 逐个取 secret
	for _, itemPath := range items {
		var secret struct {
			Session     dbus.ObjectPath
			Parameters  []byte
			Value       []byte
			ContentType string
		}
		item := conn.Object(secretService, itemPath)
		call := item.Call("org.freedesktop.Secret.Item.GetSecret", 0, session)
		if call.Err != nil {
			continue
		}
		if err := call.Store(&secret); err != nil {
			continue
		}
		if len(secret.Value) > 0 {
			return secret.Value
		}
	}
	return nil
}
