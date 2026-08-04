# WSL SSH deployment

Use Ubuntu 22.04 in WSL for all Shiden node SSH and SCP operations. The dedicated
private key and its persistent agent socket remain inside WSL; passwords and key
passphrases are entered only in the interactive WSL terminal.

From WSL:

```bash
cd /mnt/c/Users/sarah/Documents/01_MyApp/on_github/collator
bash scripts/setup-ssh-wsl.sh
```

The setup verifies the server Ed25519 fingerprint before changing `known_hosts`,
creates `~/.ssh/shiden_collator_deploy_ed25519`, starts a persistent agent, and
offers to register the restricted public key using the existing SSH password.

After `ssh -o BatchMode=yes shiden-collator hostname` succeeds:

```bash
bash scripts/deploy-wsl.sh
```

The deploy script performs the read-only preflight, locked local tests/builds,
checksum-protected release upload, remote extraction verification, and agent build.
It does not run the initial privileged bootstrap or restart `astar.service`.

On this host, `/var/run/docker.sock` is owned by `root:docker` with mode `0660` and
`tk` is intentionally not a member of the Docker group. Keep Compose root-managed:
run the reviewed bootstrap and release activation with interactive `sudo` from the
existing password-authenticated session. Do not grant passwordless Docker sudo or
add `tk` to the root-equivalent Docker group merely for deployment automation.
