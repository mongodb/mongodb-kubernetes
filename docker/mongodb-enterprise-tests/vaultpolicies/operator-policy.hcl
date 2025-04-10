// NOTE: if you edit this file, make sure to also edit the one under public/vault_policies

path "secret/data/mongodbenterprise/*" {
  capabilities = ["create", "read", "update", "delete", "list"]
}
path "secret/metadata/mongodbenterprise/*" {
  capabilities = ["list", "read"]
}
