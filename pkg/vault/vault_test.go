package vault

import "testing"

func TestDatabaseSecretPathsUseDatabaseConfiguration(t *testing.T) {
	client := &VaultClient{VaultConfig: VaultConfiguration{
		OperatorSecretPath: "operator/custom",
		DatabaseSecretPath: "database/custom",
	}}

	if got, want := client.DatabaseSecretPath(), "/secret/data/database/custom"; got != want {
		t.Errorf("DatabaseSecretPath() = %q, want %q", got, want)
	}
	if got, want := client.DatabaseSecretMetadataPath(), "/secret/metadata/database/custom"; got != want {
		t.Errorf("DatabaseSecretMetadataPath() = %q, want %q", got, want)
	}
}
