package host

import "context"

func (m *Manager) unlockAccountRuntime(ctx context.Context, health HealthInfo) error {
	return m.unlockNativeRuntime(ctx, health, hostCredentialStore{
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
	})
}
