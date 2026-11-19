//go:build windows || (darwin && cgo)

package host

func nativeHostCredentialStore() hostCredentialStore {
	return hostCredentialStore{
		backends: []string{"native"},
		read: func(_, service, account string) (*string, error) {
			return readSystemCredential(service, account)
		},
		preserve: preserveSystemCredential,
	}
}
