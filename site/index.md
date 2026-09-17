---
title: Home
nav_order: 1
description: Run your current directory in Linux, from macOS or Windows.
permalink: /
---

# avar

Run your current directory in Linux, from macOS or Windows.

```bash
cd ~/code/my-project
avr                  # interactive Linux shell, same project
avr npm test         # run one command in Linux
```

The mental model is **the current directory plus the operating environment
you pick**. There is no machine to name, no mounts to configure and no SSH to
set up. avar runs [Lima](https://lima-vm.io) virtual machines on macOS and
WSL 2 distributions on Windows, and the commands are the same on both.

## Start with the README

Installing avar and getting to a first Linux shell take one page, and that
page is the README:

- [Install](https://github.com/olamide226/avar/blob/main/README.md#install)
- [Sixty seconds to a Linux shell](https://github.com/olamide226/avar/blob/main/README.md#sixty-seconds-to-a-linux-shell)
- [Limitations](https://github.com/olamide226/avar/blob/main/README.md#limitations)

## Reference

This site holds what does not fit on that page.

| Page | What it covers |
| --- | --- |
| [Command reference]({% link commands.md %}) | Every command's help text, generated from `avr help` |
| [Configuration]({% link configuration.md %}) | How the environment is chosen, `.avr.toml` and its approval model, and `config.toml` |
| [Environments]({% link environments.md %}) | The distributions, releases and architectures avar runs, on each host |
| [Platform notes]({% link platforms.md %}) | What Linux can see, the Windows filesystem boundary, `--native-fs` and `avr sync` |
| [Troubleshooting]({% link troubleshooting.md %}) | Common errors, what they mean, and what to do |

## Contributing

avar is developed spec-first, and every change arrives as a pull request.
[CONTRIBUTING.md](https://github.com/olamide226/avar/blob/main/CONTRIBUTING.md)
explains how to propose one, and
[docs/lessons.md](https://github.com/olamide226/avar/blob/main/docs/lessons.md)
records the mistakes that changed how the project is worked on.
