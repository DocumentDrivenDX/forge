---
title: Demos
weight: 2
---

Terminal recordings showing forge in action with [LM Studio](https://lmstudio.ai/) and `qwen3-coder-next`.

## Read a file and explain it

Model reads `main.go` using the read tool and describes the program.

<script src="https://asciinema.org/a/demo.js" id="asciicast-read" async data-src="/forge/demos/file-read.cast" data-cols="100" data-rows="30"></script>
<noscript>

```
$ forge -p 'Read main.go and explain what this program does'

This program is a simple HTTP server that listens on port 8080 and responds
with "Hello from Forge!" to any request.
[success] tokens: 1861 in / 70 out
```

</noscript>

## Edit a config file

Model reads a config, edits the port number, and verifies the change.

<script src="https://asciinema.org/a/demo.js" id="asciicast-edit" async data-src="/forge/demos/file-edit.cast" data-cols="100" data-rows="30"></script>
<noscript>

```
$ forge -p 'Read config.yaml, change the server port from 8080 to 9090, then verify'

Done. The server port in config.yaml has been changed from 8080 to 9090.
[success] tokens: 4082 in / 127 out
```

</noscript>

## Explore project structure

Model uses bash to find all Go files and summarizes the package layout.

<script src="https://asciinema.org/a/demo.js" id="asciicast-bash" async data-src="/forge/demos/bash-explore.cast" data-cols="100" data-rows="30"></script>
<noscript>

```
$ forge -p 'List all Go files and summarize the package structure'

Package Structure:
├── cmd/server/main.go       (package main)
├── internal/api/handler.go  (package api)
├── internal/api/middleware.go (package api)
└── internal/db/postgres.go  (package db)
[success] tokens: 6388 in / 297 out
```

</noscript>

All demos run against [qwen3-coder-next](https://huggingface.co/Qwen) via LM Studio.
Demo scripts are in [`demos/scripts/`](https://github.com/DocumentDrivenDX/forge/tree/master/demos/scripts) and can be re-recorded with `demos/record.sh`.
