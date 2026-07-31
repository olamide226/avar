# avar

Run your current directory in Linux.

```bash
cd ~/code/my-project

avr                  # interactive Linux shell, same directory
avr npm test         # run one command in Linux
avr --arch amd64     # the same project on x86_64
avr --distro fedora  # the same project on Fedora
```

That is the whole mental model: **the current directory plus the operating
environment you pick.** No machine to name, no mounts to configure, no
`devcontainer.json`, no Docker flags, no SSH setup.

Inside Linux you get the same absolute path you were standing in, your files live
and writable in both directions, real passwordless `sudo`, packages that persist
between sessions, and any port you listen on reachable at `localhost` on macOS.

## Status

Early development, and the core loop works: `avr` and `avr <command>` open the
current directory in a real Linux VM, with the project mounted live at the same
path and the guest's exit code becoming avar's. Warm invocations measure ~205 ms.

Still to come before the MVP is complete: mount-change handling, `--arch`/`--distro`
wiring, snapshots and reset, project isolation, and `avr code`.

avar is built spec-first: see [`.kiro/specs/avar-cli/`](.kiro/specs/avar-cli/) for
the requirements, design, and phased plan, [`CLAUDE.md`](CLAUDE.md) for the working
agreement, and [`HANDOFF.md`](HANDOFF.md) for current state and open work.

## How it works

avar is a thin, opinionated layer over [Lima](https://lima-vm.io) (Apache-2.0, CNCF
incubating), which supplies the virtual machines, VirtioFS file sharing, and automatic
port forwarding. avar's contribution is the mental model: it maps your current
directory and a chosen environment onto a machine, mount, and working directory so
you never have to.

Requires macOS 13+ (Apple Silicon or Intel). Lima is installed automatically on
first run if it is missing.

## Development

```bash
make build   # compile ./bin/avr
make test    # unit and integration tests
make lint    # gofmt -s and go vet
make e2e     # real-Lima end-to-end tests (needs macOS + limactl)
```

## License

Apache-2.0. See [LICENSE](LICENSE).
