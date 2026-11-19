//go:build windows || (darwin && cgo)

package host

import "context"

func nativeRuntimeCredentialStore(context.Context) hostCredentialStore {
	return hostCredentialStore{
		backends: []string{"native"},
		read: func(_, service, account string) (*string, error) {
			return readSystemCredential(service, account)
		},
		preserve: preserveSystemCredential,
		replace:  replaceSystemCredential,
		remove:   removeSystemCredential,
	}
}
