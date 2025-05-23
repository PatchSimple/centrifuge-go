# agent

A dummy agent that runs forever, sending messages to a Centrifuge server. It
looks for a config file `agent_config.json` in the working directory on startup
to get its connection details.

It also looks for the an executable that can provide a token used to authorize
its connection with the centrifugo server. The path to this executable must be
specified in the `agent_config.json`.

Build the agent for your target system. Include the commit hash if that's
important to you.

```sh
cd examples/agent 
GOOS=linux GOARCH=amd64 go build -ldflags="-X main.Commit=$(git rev-parse HEAD)"
GOOS=windows GOARCH=amd64 go build -ldflags="-X main.Commit=$(git rev-parse HEAD)"
```

Example token executable (in this case `token.sh`). This could be any
executable. This is just here to explain the concept. On windows it would be a
.bat or .ps1 file, or some other compiled binary.

```sh
#!/bin/sh
# Get your token somehow...
# write the token to stdout.
echo "YOUR TOKEN HERE"
```

Example `agent_config.json`.

```json
{
    "tokenExePath": "/path/to/token.sh",
    "connUrl": "ws://localhost:8000/connection/websocket",
    "channel": "YOUR_CHANNEL_HERE",
    "message": "foo"
}
```

Then run the agent.

```sh
./agent
```
