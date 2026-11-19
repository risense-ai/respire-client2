package host

import "context"

func (m *Manager) unlockAccountRuntime(ctx context.Context, health HealthInfo) error {
	return m.unlockNativeRuntime(ctx, health, nativeRuntimeCredentialStore(ctx))
}

func nativeRuntimeCredentialStore(ctx context.Context) hostCredentialStore {
	return hostCredentialStore{
		backends: []string{"secret_service", "kernel"},
		read: func(backend, service, account string) (*string, error) {
			if backend == "kernel" {
				return linuxKeyringRead(service, account)
			}
			return secretServiceCredential(ctx, service, account, nil)
		},
		preserve: func(service, account, value string) error {
			_, err := secretServiceCredential(ctx, service, account, &value)
			return err
		},
		replace: func(service, account string, expected *string, value string) error {
			_, err := updateSecretServiceCredential(ctx, service, account, expected, &value)
			return err
		},
		remove: func(service, account, expected string) error {
			return removeSecretServiceCredential(ctx, service, account, expected)
		},
	}
}
