<div align="center">

<img src=".github/assets/logo.png" alt="FS-Entangle Logo" width="250"/>

<h1>FS-Entangle</h1>

[![Release Build](https://github.com/tanq16/fs-entangle/actions/workflows/release.yml/badge.svg)](https://github.com/tanq16/fs-entangle/actions/workflows/release.yml)
[![GitHub Release](https://img.shields.io/github/v/release/tanq16/fs-entangle)](https://github.com/Tanq16/fs-entangle/releases/latest)

All-in-one client+server file-system watcher that allows sync across multiple clients via a designated server for realtime updates.

</div>

---

## Features

- Multi-architecture and multi-OS self-contained binaries for seamless command-line operation
- All-in-one binary for both server and client operations
- Multi-client sync support with thread-safe write operations on server (source of truth)
- Realtime sync to server and simultaneous broadcast to all active clients
- Websocket-based network communication for data sync
- Available as an extremely lean Docker container to run in homelab settings

## Installation

Simply download the appropriate binary from [releases](https://github.com/Tanq16/fs-entangle/releases/latest) and execute the binary.

To clone, develop or build locally, do the following:

```bash
git clone https://github.com/Tanq16/fs-entangle --depth=1 && \
cd fs-entangle && \
go build .
```

Ensure you have Go v1.23+.

## Usage

Designate a directory as source of truth on the server machine and deploy as:

```bash
fs-entangle server -d mydir
```

For client machines, designate the directory to sync and connect to the server machine as follows:

```bash
fs-entangle client -d mydir -a "ws://SERVER_IP:8080/ws"
```

File/folder patterns can be ignored from client and/or server by using the `--ignore` flag. Example - `--ignore .git,.obsidian,*.log`.

> [!IMPORTANT]
> Server is always considered source of truth and is synced at first connect. Make sure you make changes after the initial sync (i.e., when the client connects to the server).

To run via Docker, mount your directory to `/data` and add any applicable ignore patterns like so:

```bash
docker run --rm -p 8080:8080 \
-v /home/tanq/knowledgebase:/data \
-it tanq16/fs-entangle:main \
--ignore ".obsidian,.DS_Store"
```
