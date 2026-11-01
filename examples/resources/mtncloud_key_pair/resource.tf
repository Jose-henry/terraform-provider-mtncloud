# Only the public key is needed for VMs to be created with it.
resource "mtncloud_key_pair" "deploy" {
  name       = "deploy-key"
  public_key = file("~/.ssh/id_ed25519.pub")
}
