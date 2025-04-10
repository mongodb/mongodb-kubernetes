// NOTE: if you edit this file, make sure to also edit the one under public/vault_policies

path "secret/data/mongodbenterprise/opsmanager/*" {
  capabilities = ["read", "list"]
}
path "secret/metadata/mongodbenterprise/opsmanager/*" {
  capabilities = ["list"]
}
