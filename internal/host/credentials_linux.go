package host

import (
	"context"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/godbus/dbus/v5"
	"golang.org/x/sys/unix"
)

const (
	secretService = "org.freedesktop.secrets"
	secretRoot    = "/org/freedesktop/secrets"
	secretAPI     = "org.freedesktop.Secret."
)

type linuxSecret struct {
	Session dbus.ObjectPath
	Params  []byte
	Value   []byte
	Type    string
}

func linuxKeyringRead(service, account string) (*string, error) {
	ring, err := unix.KeyctlGetKeyringID(unix.KEY_SPEC_SESSION_KEYRING, false)
	if err != nil {
		return nil, fmt.Errorf("Linux session keyring: %w", err)
	}
	// The historical keyring provider links the user's persistent keys to this session.
	_, linkErr := unix.KeyctlInt(unix.KEYCTL_GET_PERSISTENT, -1, ring, 0, 0)
	id, err := unix.KeyctlSearch(ring, "user", "keyring-rs:"+account+"@"+service, 0)
	if errors.Is(err, unix.ENOKEY) {
		if linkErr != nil && !errors.Is(linkErr, unix.ENOTSUP) && !errors.Is(linkErr, unix.ENOSYS) {
			return nil, fmt.Errorf("Linux persistent keyring: %w", linkErr)
		}
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("Search Linux session keyring: %w", err)
	}
	buf := make([]byte, 65536)
	defer clear(buf)
	n, err := unix.KeyctlBuffer(unix.KEYCTL_READ, id, buf, 0)
	if err != nil {
		return nil, fmt.Errorf("Read Linux session keyring: %w", err)
	}
	if n < 0 || n > len(buf) {
		return nil, errors.New("Linux keyring credential exceeds the Runtime protocol limit")
	}
	return linuxCredentialValue(buf[:n])
}

func linuxCredentialValue(value []byte) (*string, error) {
	if len(value) > 65536 {
		return nil, errors.New("Linux keyring credential exceeds the Runtime protocol limit")
	}
	if !utf8.Valid(value) {
		return nil, providerCredentialError("bad_encoding")
	}
	text := string(value)
	return &text, nil
}

func secretServiceCredential(ctx context.Context, service, account string, preserve *string) (*string, error) {
	return updateSecretServiceCredential(ctx, service, account, nil, preserve)
}

// Credential replacement is serialized by the installation takeover lock.
func updateSecretServiceCredential(ctx context.Context, service, account string, expected, replacement *string) (*string, error) {
	return changeSecretServiceCredential(ctx, service, account, expected, replacement, false)
}

func removeSecretServiceCredential(ctx context.Context, service, account, expected string) error {
	_, err := changeSecretServiceCredential(ctx, service, account, &expected, nil, true)
	return err
}

func changeSecretServiceCredential(ctx context.Context, service, account string, expected, replacement *string, remove bool) (*string, error) {
	bus, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, fmt.Errorf("Connect Linux Secret Service: %w", err)
	}
	defer bus.Close()
	root := bus.Object(secretService, secretRoot)
	attributes := map[string]string{"service": service, "username": account, "target": "default"}
	var unlocked, locked []dbus.ObjectPath
	err = root.CallWithContext(ctx, secretAPI+"Service.SearchItems", 0, attributes).Store(&unlocked, &locked)
	if err != nil {
		return nil, fmt.Errorf("Search Linux Secret Service: %w", err)
	}
	items := append(unlocked, locked...)
	var collection dbus.ObjectPath
	if len(items) == 0 {
		if remove {
			return nil, nil
		}
		err := root.CallWithContext(ctx, secretAPI+"Service.ReadAlias", 0, "default").Store(&collection)
		if err != nil {
			return nil, fmt.Errorf("Find Linux default keyring: %w", err)
		}
		if collection == "/" {
			return nil, errors.New("Linux Secret Service has no default keyring")
		}
		// Match the historical target-less entries only in the default collection.
		legacy := map[string]string{"service": service, "username": account}
		err = bus.Object(secretService, collection).CallWithContext(ctx, secretAPI+"Collection.SearchItems", 0, legacy).Store(&items)
		if err != nil {
			return nil, fmt.Errorf("Search historical Linux keyring: %w", err)
		}
	}
	if len(items) > 1 {
		return nil, providerCredentialError("ambiguous")
	}
	if len(items) == 0 {
		if expected != nil {
			return nil, errors.New("Linux Secret Service credential disappeared; original operation retained")
		}
		if replacement == nil {
			return nil, nil
		}
	}
	var available []dbus.ObjectPath
	var prompt dbus.ObjectPath
	paths := items
	if len(items) == 0 {
		paths = []dbus.ObjectPath{collection}
	}
	if err := root.CallWithContext(ctx, secretAPI+"Service.Unlock", 0, paths).Store(&available, &prompt); err != nil {
		return nil, fmt.Errorf("Unlock Linux Secret Service item: %w", err)
	}
	if prompt != "/" {
		return nil, fmt.Errorf("Unlock the Linux system keyring and retry: %w", providerCredentialError("access_denied"))
	}

	var output dbus.Variant
	var session dbus.ObjectPath
	if err := root.CallWithContext(ctx, secretAPI+"Service.OpenSession", 0, "plain", dbus.MakeVariant("")).Store(&output, &session); err != nil {
		return nil, fmt.Errorf("Open Linux Secret Service session: %w", err)
	}
	if session == "/" || !session.IsValid() {
		return nil, errors.New("Linux Secret Service did not open a session")
	}

	var item dbus.ObjectPath
	if len(items) == 1 {
		item = items[0]
	} else {
		attributes["application"] = "rust-keyring"
		properties := map[string]dbus.Variant{
			secretAPI + "Item.Attributes": dbus.MakeVariant(attributes),
			secretAPI + "Item.Label":      dbus.MakeVariant(account + "@" + service),
		}
		value := []byte(*replacement)
		defer clear(value)
		secret := linuxSecret{Session: session, Value: value, Type: "text/plain"}
		err := bus.Object(secretService, collection).CallWithContext(ctx, secretAPI+"Collection.CreateItem", 0, properties, secret, false).Store(&item, &prompt)
		if err != nil {
			return nil, fmt.Errorf("Save Linux Secret Service credential: %w", err)
		}
		if prompt != "/" || item == "/" {
			return nil, errors.New("Linux Secret Service did not save the credential without interaction")
		}
	}
	entry := bus.Object(secretService, item)
	read := func() (*string, error) {
		var secret linuxSecret
		if err := entry.CallWithContext(ctx, secretAPI+"Item.GetSecret", 0, session).Store(&secret); err != nil {
			return nil, fmt.Errorf("Read Linux Secret Service credential: %w", err)
		}
		defer clear(secret.Value)
		if secret.Session != session || len(secret.Params) != 0 {
			return nil, errors.New("Linux Secret Service returned an invalid secret session")
		}
		return linuxCredentialValue(secret.Value)
	}
	value, err := read()
	if remove && err == nil {
		if *value != *expected {
			return nil, errors.New("Linux Secret Service credential changed before cleanup")
		}
		var prompt dbus.ObjectPath
		if err := entry.CallWithContext(ctx, secretAPI+"Item.Delete", 0).Store(&prompt); err != nil {
			return nil, fmt.Errorf("Delete Linux Secret Service credential: %w", err)
		}
		if prompt != "/" {
			return nil, errors.New("Linux Secret Service did not remove the credential without interaction")
		}
		remaining, err := secretServiceCredential(ctx, service, account, nil)
		if err != nil {
			return nil, err
		}
		if remaining != nil {
			return nil, errors.New("Linux Secret Service credential cleanup could not be verified")
		}
		return nil, nil
	}
	if err != nil || replacement == nil || *replacement == *value {
		return value, err
	}
	if expected == nil || *expected != *value {
		return nil, errors.New("Linux Secret Service credential differs; original session retained")
	}

	payload := []byte(*replacement)
	defer clear(payload)
	if err := entry.CallWithContext(ctx, secretAPI+"Item.SetSecret", 0, linuxSecret{Session: session, Value: payload, Type: "text/plain"}).Err; err != nil {
		return nil, fmt.Errorf("Replace Linux Secret Service credential: %w", err)
	}
	written, err := read()
	if err != nil {
		return nil, err
	}
	if *written != *replacement {
		return nil, errors.New("Linux Secret Service credential readback differs")
	}
	return written, nil
}
